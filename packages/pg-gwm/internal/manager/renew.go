package manager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// LeaseKey names a lease by its id or its name; exactly one is set.
type LeaseKey struct{ LeaseID, Name string }

func (k LeaseKey) String() string {
	if k.LeaseID != "" {
		return k.LeaseID
	}
	return k.Name
}

func (k LeaseKey) check() error {
	if (k.LeaseID == "") == (k.Name == "") {
		return ErrUsage("name exactly one lease: a lease id or --name", "e.g. pg-gwm renew l-abc... or pg-gwm renew --name N", nil)
	}
	return nil
}

// lockRepo takes the repository lock for a command, mapping a timeout to busy.
func (m *Manager) lockRepo(ctx context.Context) (*state.Locked, error) {
	lk, err := m.Store.Lock(ctx, span(m.Cfg.LockWait, 0))
	if errors.Is(err, state.ErrBusy) {
		return nil, ErrBusy("")
	}
	return lk, err
}

// resolveLease finds the worktree whose lease k names, or answers with the
// catalog error for a lease that ended: lease-released, lease-lost, broken or
// not-found. The worktree returned is Preparing or Leased (possibly expired).
func (m *Manager) resolveLease(lk *state.Locked, k LeaseKey) (*state.Worktree, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	now := m.now()
	var (
		w *state.Worktree
		t *state.Tombstone
	)
	if k.LeaseID != "" {
		w, t = lk.FindLease(k.LeaseID)
	} else {
		w, t = lk.FindName(k.Name, now)
	}
	if w != nil {
		leaseID := ""
		if w.Lease != nil {
			leaseID = w.Lease.ID
		}
		switch w.Status {
		case state.Preparing, state.Leased:
			return w, nil
		case state.Reclaiming:
			return nil, ErrLeaseLost(leaseID, preservedFor(lk.State(), leaseID))
		case state.Broken:
			return nil, ErrBroken(w.ID, w.Reason)
		default:
			return nil, ErrLeaseReleased(leaseID)
		}
	}
	if t != nil {
		if t.Kind == "reclaimed" {
			return nil, ErrLeaseLost(t.LeaseID, t.PreservedBranch)
		}
		return nil, ErrLeaseReleased(t.LeaseID)
	}
	return nil, m.notFound(lk.State(), k)
}

func preservedFor(st *state.State, leaseID string) string {
	for _, p := range st.Preserved {
		if p.LeaseID == leaseID {
			return p.Branch
		}
	}
	return ""
}

// notFound is not-found for k; for a name the hint lists up to ten names that
// are held.
func (m *Manager) notFound(st *state.State, k LeaseKey) *Error {
	e := ErrNotFound(k.String())
	if k.Name == "" {
		return e
	}
	var held []string
	for _, w := range st.Worktrees {
		if w.Lease == nil {
			continue
		}
		switch w.Effective(m.now()) {
		case state.Preparing, state.Leased, state.LeaseExpired:
			if w.Lease.Name != "" {
				held = append(held, w.Lease.Name)
			}
		}
	}
	sort.Strings(held)
	if len(held) > 10 {
		held = held[:10]
	}
	if len(held) > 0 {
		e.Hint = "held names: " + strings.Join(held, ", ")
		e.Details["held_names"] = held
	}
	return e
}

// Renew moves a lease's expiry to now plus ttl, or plus its recorded TTL when
// ttl is nil. Non-nil metadata replaces the lease's metadata.
func (m *Manager) Renew(ctx context.Context, k LeaseKey, ttl *config.Duration, metadata map[string]string) (Record, error) {
	if err := k.check(); err != nil {
		return Record{}, err
	}
	if err := checkRenewTTL(ttl); err != nil {
		return Record{}, err
	}
	lk, err := m.lockRepo(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lk.Unlock()
	w, err := m.resolveLease(lk, k)
	if err != nil {
		return Record{}, err
	}
	if err := m.renewLocked(lk, w, ttl, metadata); err != nil {
		return Record{}, err
	}
	return m.record(*w, m.headOf(ctx, w.Path)), nil
}

func checkRenewTTL(ttl *config.Duration) error {
	switch {
	case ttl == nil:
	case ttl.None:
		return ErrUsage("renew cannot remove an expiry", "release and lease again with --ttl none", map[string]any{"flag": "--ttl"})
	case ttl.D <= 0:
		return ErrUsage("--ttl must be positive", "e.g. --ttl 2h", map[string]any{"flag": "--ttl"})
	}
	return nil
}

// renewLocked renews w's lease under the lock l and commits. The expiry is
// now plus ttl (which becomes the lease's recorded TTL), or now plus the
// recorded TTL when ttl is nil; a lease without a TTL stays without an expiry.
func (m *Manager) renewLocked(l *state.Locked, w *state.Worktree, ttl *config.Duration, metadata map[string]string) error {
	if err := checkRenewTTL(ttl); err != nil {
		return err
	}
	if w.Lease == nil || (w.Status != state.Preparing && w.Status != state.Leased) {
		return ErrInternal(fmt.Errorf("worktree %s holds no renewable lease", w.ID))
	}
	now := m.now()
	le := w.Lease
	if ttl != nil {
		le.TTL = *ttl
	}
	le.Expiry = nil
	if !le.TTL.None && le.TTL.D > 0 {
		exp := now.Add(le.TTL.D)
		le.Expiry = &exp
	}
	if metadata != nil {
		le.Metadata = copyMeta(metadata)
	}
	if err := l.Commit(); err != nil {
		return err
	}
	m.event(state.Event{
		Kind: "renew", WorktreeID: w.ID, LeaseID: le.ID, Name: le.Name, Owner: le.Owner, Outcome: "ok",
	})
	return nil
}

// Release ends a lease whose worktree holds no unsaved work.
func (m *Manager) Release(ctx context.Context, k LeaseKey) (Record, error) {
	if err := k.check(); err != nil {
		return Record{}, err
	}
	lk, err := m.lockRepo(ctx)
	if err != nil {
		return Record{}, err
	}
	w, err := m.resolveLease(lk, k)
	if err != nil {
		lk.Unlock()
		return Record{}, err
	}
	if w.Status == state.Preparing {
		lk.Unlock()
		e := ErrBusy("")
		e.Message = fmt.Sprintf("lease %s is still being prepared", w.Lease.ID)
		e.Hint = "retry when the lease call has returned"
		e.Details["lease_id"] = w.Lease.ID
		return Record{}, e
	}
	id, leaseID, path := w.ID, w.Lease.ID, w.Path
	lk.Unlock()

	rel, err := m.Store.HoldOp(ctx, id)
	if err != nil {
		return Record{}, ErrBusy("")
	}
	defer rel()
	un, err := m.Prov.Unsaved(ctx, path)
	if err != nil {
		return Record{}, ErrInternal(err)
	}
	if un.Any() {
		return Record{}, dirtyError(path, un.Files, un.Unreachable, un.Blocker)
	}

	lk, err = m.lockRepo(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lk.Unlock()
	// The lease may have ended or been replaced while the lock was let go.
	w, err = m.resolveLease(lk, LeaseKey{LeaseID: leaseID})
	if err != nil {
		return Record{}, err
	}
	if w.ID != id || w.Status != state.Leased {
		return Record{}, ErrBusy("")
	}
	now := m.now()
	le := w.Lease
	w.Status, w.StatusSince, w.Lease = state.Released, now, nil
	lk.State().Tombstones = append(lk.State().Tombstones, state.Tombstone{
		Kind: "released", LeaseID: le.ID, Name: le.Name, Owner: le.Owner, At: now,
	})
	if err := lk.Commit(); err != nil {
		return Record{}, err
	}
	m.event(state.Event{
		Kind: "release", WorktreeID: w.ID, LeaseID: le.ID, Name: le.Name, Owner: le.Owner, Outcome: "ok",
	})
	rec := m.record(*w, m.headOf(ctx, w.Path))
	// The record of a released worktree no longer carries the lease; the
	// output still names who held it.
	rec.Owner, rec.LeaseID, rec.Name = strPtr(le.Owner), strPtr(le.ID), strPtr(le.Name)
	rec.Status = state.Released
	return rec, nil
}

// dirtyError is the dirty refusal with the facts and the commands that resolve
// it.
func dirtyError(path string, files, commits []string, blocker string) *Error {
	e := ErrDirty(files, commits)
	e.Details["commands"] = []string{
		fmt.Sprintf("git -C %s add -A && git -C %s commit -m <message>", path, path),
		fmt.Sprintf("git -C %s stash push -u", path),
		fmt.Sprintf("git -C %s branch <name> HEAD", path),
	}
	if blocker != "" {
		e.Details["blocker"] = blocker
		e.Message += " (" + blocker + ")"
	}
	return e
}

// Expire sets a lease's expiry to now and changes nothing else.
func (m *Manager) Expire(ctx context.Context, k LeaseKey) (Record, error) {
	if err := k.check(); err != nil {
		return Record{}, err
	}
	lk, err := m.lockRepo(ctx)
	if err != nil {
		return Record{}, err
	}
	defer lk.Unlock()
	w, err := m.resolveLease(lk, k)
	if err != nil {
		return Record{}, err
	}
	if w.Status == state.Preparing {
		e := ErrBusy("")
		e.Message = fmt.Sprintf("lease %s is still being prepared", w.Lease.ID)
		e.Hint = "retry when the lease call has returned"
		e.Details["lease_id"] = w.Lease.ID
		return Record{}, e
	}
	now := m.now()
	w.Lease.Expiry = &now
	if err := lk.Commit(); err != nil {
		return Record{}, err
	}
	m.event(state.Event{
		Kind: "expire", WorktreeID: w.ID, LeaseID: w.Lease.ID, Name: w.Lease.Name, Owner: w.Lease.Owner, Outcome: "ok",
	})
	return m.record(*w, m.headOf(ctx, w.Path)), nil
}

// headOf is the full HEAD commit of the worktree at path, or "" when it
// cannot be read.
func (m *Manager) headOf(ctx context.Context, path string) string {
	out, err := m.git(context.WithoutCancel(ctx), path, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var _ = time.Second
