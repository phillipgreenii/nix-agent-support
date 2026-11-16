package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/gwm"
	"github.com/phillipgreenii/pg-router-handlers/internal/report"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// ErrWorktreeLost is the cause of a dispatch whose pg-gwm lease was lost or
// released while its session was running.
var ErrWorktreeLost = errors.New("ccpool: worktree lease lost mid-session")

// gwmState is the per-dispatch pg-gwm state of a ccpoolRun. The zero value is a
// dispatch that does not use pg-gwm.
type gwmState struct {
	on bool
	// lease is the lease the dispatch holds; startHead is its starting commit
	// (empty when the lease was a renewal of an earlier one).
	lease     gwm.Lease
	startHead string
	// prefetchHead is the commit prefetch detached the worktree to.
	prefetchHead string
	ttl, parkTTL time.Duration

	mu        sync.Mutex // guards renewedAt, which the supervision tick advances
	renewedAt time.Time
	lost      atomic.Bool
}

func (r *ccpoolRun) gwmOn() bool { return r.gwm.on }

// useGWM reports whether this dispatch leases its worktree from pg-gwm.
func (r *ccpoolRun) useGWM(cc *roles.CCPoolConfig) bool {
	return r.deps.GWM != nil && usesWorktreeIsolation(cc.Isolation)
}

func (r *ccpoolRun) emitGWM(level, kind, msg string, fields map[string]any) {
	if r.deps.Log != nil {
		_ = r.deps.Log.Emit(level, kind, msg, fields)
	}
}

// leaseWorktree leases the dispatch's worktree and maps a refusal to the
// dispatch's outcome. On error the returned Result is the failure action
// already applied to the bead (empty for a decline).
func (r *ccpoolRun) leaseWorktree(ctx context.Context, d DispatchContext, cc *roles.CCPoolConfig) (string, report.Result, error) {
	iso := gwmIsolation{
		client: r.deps.GWM, git: r.deps.gitRun(), repoRoot: r.deps.Cfg.RepoRoot,
		ttl: cc.Isolation.LeaseTTL(), readOnly: cc.Isolation.ReadOnly(),
	}
	l, err := iso.lease(ctx, d.Role.Name, d.Item.ID, r.deps.ExternalID)
	if err != nil {
		res, mapped := r.mapGWMRefusal(ctx, d, cc, err)
		return "", res, mapped
	}
	r.gwm.on = true
	r.gwm.lease = l
	r.gwm.ttl, r.gwm.parkTTL = iso.ttl, cc.Isolation.ParkTTL()
	r.gwm.renewedAt = r.deps.clock()
	if !l.Renewed {
		r.gwm.startHead = l.Head
	} else if want := iso.expectedBranch(d.Item.ID); l.Branch != want {
		r.emitGWM("warn", "gwm_unexpected_branch", "a renewed worktree lease is not on the expected branch",
			map[string]any{"role": d.Role.Name, "bead": d.Item.ID, "lease": l.LeaseID, "branch": l.Branch, "expected": want})
		slog.Warn("renewed worktree lease is on an unexpected branch", "bead", d.Item.ID, "branch", l.Branch, "expected", want)
	}
	return l.Path, report.Result{}, nil
}

// mapGWMRefusal turns a failed pg-gwm lease into the dispatch outcome and
// writes the events row (pg-gwm counts its own refusals; the handler adds no
// counter).
func (r *ccpoolRun) mapGWMRefusal(ctx context.Context, d DispatchContext, cc *roles.CCPoolConfig, err error) (report.Result, error) {
	fields := map[string]any{"role": d.Role.Name, "bead": d.Item.ID, "error": err.Error()}
	var ge *gwm.Error
	if errors.As(err, &ge) {
		fields["error"], fields["hint"], fields["holder"] = ge.Name, ge.Hint, ge.Holder()
		if ge.Name == "" {
			fields["error"] = ge.Sentinel.Error()
		}
	}
	level, wrapped := "warn", error(nil)
	switch {
	case errors.Is(err, gwm.ErrMaxReached), errors.Is(err, gwm.ErrBusy), errors.Is(err, gwm.ErrBranchInUse):
		wrapped = fmt.Errorf("%w: %v", ErrPoolAtCapacity, err)
	case errors.Is(err, gwm.ErrLowDisk):
		wrapped = fmt.Errorf("isolation %s: %w: %v", d.Item.ID, ErrLowDisk, err)
	case errors.Is(err, gwm.ErrNameHeld):
		level, wrapped = "error", fmt.Errorf("internal: worktree lease name %s/%s held: %w", d.Role.Name, d.Item.ID, err)
	}
	r.emitGWM(level, "gwm_refusal", "pg-gwm refused the worktree lease", fields)
	if wrapped != nil {
		return report.Result{}, wrapped
	}
	// ref-unresolvable, broken and anything else: a dispatch failure, with the
	// role's on_dispatch_fail action.
	var res report.Result
	if cc.OnDispatchFail == roles.DispatchUnclaim && r.releaseAfterFailedSend(ctx, d.Item.ID) {
		res = failureAction(report.Unclaimed, d.Item.ID)
	}
	return res, fmt.Errorf("isolation %s: %w", d.Item.ID, err)
}

// gwmMeta adds the lease's session metadata keys to meta.
func (r *ccpoolRun) gwmMeta(meta map[string]string) {
	if !r.gwm.on {
		return
	}
	meta[ccpool.MetaKeyGWMLease] = r.gwm.lease.LeaseID
	if r.gwm.startHead != "" {
		meta[ccpool.MetaKeyGWMStartHead] = r.gwm.startHead
	}
	if r.gwm.prefetchHead != "" {
		meta[ccpool.MetaKeyGWMPrefetchHead] = r.gwm.prefetchHead
	}
}

// notePrefetchHead records the commit prefetch left HEAD at.
func (r *ccpoolRun) notePrefetchHead(ctx context.Context, wt string) {
	if !r.gwm.on {
		return
	}
	if head, ok := r.gitHead(ctx, wt); ok {
		r.gwm.prefetchHead = head
	}
}

func (r *ccpoolRun) gitHead(ctx context.Context, wt string) (string, bool) {
	var out bytes.Buffer
	if err := r.deps.gitRun()(ctx, wt, &out, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return "", false
	}
	h := strings.TrimSpace(out.String())
	return h, h != ""
}

func leaseOver(err error) bool {
	return errors.Is(err, gwm.ErrNotFound) || errors.Is(err, gwm.ErrLeaseLost) || errors.Is(err, gwm.ErrLeaseReleased)
}

// renewWorktreeLease rides the supervision tick: once less than half of the
// lease TTL remains since the last lease or renew, it renews. A lost or
// released lease flags the session and ends the dispatch as a failure once the
// session settles.
func (r *ccpoolRun) renewWorktreeLease(ctx context.Context, session string) {
	g := &r.gwm
	if !g.on || g.lost.Load() || g.ttl <= 0 {
		return
	}
	now := r.deps.clock()
	g.mu.Lock()
	last := g.renewedAt
	g.mu.Unlock()
	if g.ttl-now.Sub(last) >= g.ttl/2 {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, leaseWriteTimeout)
	defer cancel()
	err := r.deps.GWM.Renew(wctx, g.lease.LeaseID, g.ttl)
	switch {
	case err == nil:
		g.mu.Lock()
		g.renewedAt = now
		g.mu.Unlock()
	case errors.Is(err, gwm.ErrLeaseLost) || errors.Is(err, gwm.ErrLeaseReleased):
		g.lost.Store(true)
		r.emitGWM("error", "worktree_lost", "the pg-gwm worktree lease was lost while the session was running",
			map[string]any{"session": session, "lease": g.lease.LeaseID, "error": err.Error()})
		if serr := r.deps.CC.SetMeta(wctx, session, ccpool.MetaKeyWorktreeLost, "1"); serr != nil {
			slog.Warn("could not flag the session row worktree_lost", "session", session, "err", serr)
		}
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
	default:
		slog.Warn("worktree lease renew failed", "session", session, "lease", g.lease.LeaseID, "err", err)
	}
}

// gwmSessionEnd is the dispatch-path session end for a pg-gwm lease. release
// says the session settled and its worktree may go: a prefetch checkout that
// is untouched is first detached back to the lease's starting commit, then the
// lease is released (expired when the tree is dirty). Otherwise the session is
// still live: the lease is renewed for the parked TTL.
func (r *ccpoolRun) gwmSessionEnd(ctx context.Context, cc *roles.CCPoolConfig, name string, release bool) {
	g := &r.gwm
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeCloseTimeout)
	defer cancel()
	if !release {
		if err := r.deps.GWM.Renew(cctx, g.lease.LeaseID, g.parkTTL); err != nil && !leaseOver(err) {
			slog.Warn("could not renew the parked worktree lease", "session", name, "lease", g.lease.LeaseID, "err", err)
		}
		return
	}
	if cc != nil && cc.Isolation.Prefetch != nil && g.prefetchHead != "" && g.startHead != "" {
		r.detachToStart(cctx, name)
	}
	key := gwm.Key{LeaseID: g.lease.LeaseID, Name: g.lease.Name}
	err := r.deps.GWM.Release(cctx, key)
	if errors.Is(err, gwm.ErrDirty) {
		err = r.deps.GWM.Expire(cctx, key)
	}
	if err != nil && !leaseOver(err) {
		slog.Warn("could not release the worktree lease", "session", name, "lease", g.lease.LeaseID, "err", err)
	}
}

// detachToStart puts a clean worktree that still sits on the prefetched commit
// back on the lease's starting commit.
func (r *ccpoolRun) detachToStart(ctx context.Context, name string) {
	g := &r.gwm
	wt := g.lease.Path
	head, ok := r.gitHead(ctx, wt)
	if !ok || head != g.prefetchHead {
		return
	}
	var st bytes.Buffer
	if err := r.deps.gitRun()(ctx, wt, &st, "status", "--porcelain"); err != nil || strings.TrimSpace(st.String()) != "" {
		return
	}
	if err := r.deps.gitRun()(ctx, wt, io.Discard, "checkout", "--detach", "--quiet", g.startHead); err != nil {
		slog.Warn("could not detach the prefetch worktree back to its start commit", "session", name, "worktree", wt, "err", err)
	}
}
