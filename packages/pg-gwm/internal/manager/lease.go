package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// LeaseRequest is what a requester asks of lease. BaseDir is the --repo path
// when given, else the current directory; its HEAD is the default commit-ish.
type LeaseRequest struct {
	Name, Owner string
	Metadata    map[string]string
	TTL         *config.Duration
	Branch      string
	Create      bool // -b Branch
	Reset       bool // -B Branch
	Detach      bool
	CommitIsh   string
	OrRenew     bool
	Wait        time.Duration
	BaseDir     string
}

const maxNameLen = 100

// plan is the outcome of step 2: either a finished record (row 12, or a
// renewal), or a worktree recorded as Preparing that step 3 provisions.
type plan struct {
	done       *Record
	w          state.Worktree
	spec       provision.BranchSpec
	switchOnly bool // an available worktree is switched, not created
	opHold     func()
}

// Lease follows the lease algorithm: validate, select or create under the
// lock, provision outside it, record the outcome, and on a cancel mid-create
// record the worktree as broken.
func (m *Manager) Lease(ctx context.Context, r LeaseRequest) (Record, error) {
	if err := validate(&r); err != nil {
		return Record{}, err
	}
	p, err := m.reserve(ctx, r)
	if err != nil {
		return Record{}, err
	}
	if p.done != nil {
		return *p.done, nil
	}
	defer p.opHold()
	return m.provisionAndRecord(ctx, r, p)
}

// validate is step 1: the checks that need no repository.
func validate(r *LeaseRequest) error {
	flags := 0
	for _, f := range []bool{r.Create, r.Reset, r.Detach} {
		if f {
			flags++
		}
	}
	switch {
	case flags > 1:
		return ErrUsage("-b, -B and --detach are mutually exclusive", "pass at most one", nil)
	case (r.Create || r.Reset) && r.Branch == "":
		return ErrUsage("-b and -B need a branch name", "", nil)
	case !r.Create && !r.Reset && r.Branch != "":
		return ErrUsage("a branch name is only given with -b or -B", "", map[string]any{"branch": r.Branch})
	case r.OrRenew && r.Name == "":
		return ErrUsage("--or-renew needs --name", "pass --name", nil)
	case strings.HasPrefix(r.CommitIsh, "-"):
		return ErrUsage(fmt.Sprintf("%q is not a commit-ish", r.CommitIsh), "", map[string]any{"commit_ish": r.CommitIsh})
	case r.Name == "" && r.Branch == "" && !r.Detach && r.CommitIsh == "": // row 9
		return ErrUsage("nothing says what to check out", "pass --name, -b or --detach", nil)
	}
	if r.TTL != nil && !r.TTL.None && r.TTL.D <= 0 {
		return ErrUsage("--ttl must be positive or none", "e.g. --ttl 2h", map[string]any{"flag": "--ttl"})
	}
	if err := validName(r.Name); err != nil {
		return err
	}
	for k := range r.Metadata {
		if k == "" {
			return ErrUsage("a metadata key is empty", "use --metadata key=value", map[string]any{"flag": "--metadata"})
		}
	}
	if (r.Create || r.Reset) && strings.HasPrefix(r.Branch, preservedPrefix) { // row 13
		return ErrUsage(fmt.Sprintf("branch %q is in the %s namespace, which holds only preserved work", r.Branch, preservedPrefix),
			"choose a branch name outside "+preservedPrefix, map[string]any{"branch": r.Branch})
	}
	return nil
}

func validName(name string) error {
	if name == "" {
		return nil
	}
	bad := utf8.RuneCountInString(name) > maxNameLen || !utf8.ValidString(name)
	for _, c := range name {
		if !unicode.IsPrint(c) {
			bad = true
		}
	}
	if bad {
		return ErrUsage(fmt.Sprintf("the name must be one printable line of at most %d characters", maxNameLen),
			"shorten it and remove control characters and newlines", map[string]any{"flag": "--name"})
	}
	return nil
}

// reserve is step 2, under the repository's lock.
func (m *Manager) reserve(ctx context.Context, r LeaseRequest) (*plan, error) {
	lk, err := m.Store.Lock(ctx, span(m.Cfg.LockWait, 0))
	if errors.Is(err, state.ErrBusy) {
		return nil, ErrBusy("")
	}
	if err != nil {
		return nil, err
	}
	defer lk.Unlock()
	now := m.now()
	st := lk.State()

	if !m.Cfg.TombstoneRetention.None && m.Cfg.TombstoneRetention.D > 0 {
		lk.PruneTombstones(m.Cfg.TombstoneRetention.D, now)
	}

	if holder, _ := lk.FindName(r.Name, now); holder != nil {
		if r.OrRenew {
			rec, err := m.orRenewLive(ctx, lk, holder, r)
			if err != nil {
				return nil, err
			}
			return &plan{done: &rec}, nil
		}
		return nil, ErrNameHeld(r.Name, holder.Lease.ID, holder.Effective(now))
	}

	env, err := m.loadBranchEnv(ctx, r, st, now)
	if err != nil {
		return nil, err
	}
	spec, target, err := planBranch(env)
	if err != nil {
		return nil, err
	}

	eff := m.Cfg.DefaultTTL
	if r.TTL != nil {
		eff = *r.TTL
	}
	lease := &state.Lease{
		ID: m.newLeaseID(st), Name: r.Name, Owner: r.Owner, TTL: eff,
		Metadata: copyMeta(r.Metadata), Acquired: now,
	}

	if target != nil { // row 12: the released worktree that holds the branch
		target.Lease = lease
		m.markLeased(target, lease, now)
		if err := lk.Commit(); err != nil {
			return nil, err
		}
		m.event(state.Event{
			Kind: "lease", WorktreeID: target.ID, LeaseID: lease.ID, Name: r.Name, Owner: r.Owner,
			Outcome: "ok", Details: map[string]any{"branch": target.Branch, "reused": "released"},
		})
		rec := m.finalRecord(ctx, *target, r)
		return &plan{done: &rec}, nil
	}

	p := &plan{spec: spec}
	for _, c := range m.candidates(st) {
		rel, err := m.Store.HoldOp(c.ID)
		if err != nil {
			continue // another command is working on it
		}
		c.Lease, c.Status, c.StatusSince = lease, state.Preparing, now
		p.w, p.switchOnly, p.opHold = *c, true, rel
		break
	}
	if p.opHold == nil {
		w, err := m.newWorktree(ctx, st, lease, r, now)
		if err != nil {
			return nil, err
		}
		rel, err := m.Store.HoldOp(w.ID)
		if err != nil {
			return nil, err
		}
		st.Worktrees = append(st.Worktrees, w)
		p.w, p.opHold = w, rel
	}
	if err := lk.Commit(); err != nil {
		p.opHold()
		return nil, err
	}
	return p, nil
}

// orRenewLive is the seam for lease --or-renew meeting a live name: the
// renew path is not part of this change.
func (m *Manager) orRenewLive(_ context.Context, _ *state.Locked, w *state.Worktree, _ LeaseRequest) (Record, error) {
	return Record{}, ErrInternal(fmt.Errorf("--or-renew of the live lease %s is not available yet", w.Lease.ID))
}

func copyMeta(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (m *Manager) newLeaseID(st *state.State) string {
	for {
		id := state.NewLeaseID()
		taken := false
		for _, w := range st.Worktrees {
			taken = taken || (w.Lease != nil && w.Lease.ID == id)
		}
		if !taken {
			return id
		}
	}
}

func (m *Manager) newWorktreeID(st *state.State) string {
	for {
		id := state.NewWorktreeID()
		taken := false
		for _, w := range st.Worktrees {
			taken = taken || w.ID == id
		}
		if !taken {
			return id
		}
	}
}

// newWorktree is the create path of step 2: at max with no candidate it
// refuses; otherwise it picks the slot and directory and checks the floor.
func (m *Manager) newWorktree(ctx context.Context, st *state.State, lease *state.Lease, r LeaseRequest, now time.Time) (state.Worktree, error) {
	if m.Cfg.Max != nil && len(st.Worktrees) >= *m.Cfg.Max {
		counts, broken := m.counts(st)
		return state.Worktree{}, ErrMaxReached(*m.Cfg.Max, counts, broken)
	}
	root := m.Cfg.WorktreeRoot
	in := config.TemplateInput{
		Repo: m.Repo.Name, Name: lease.Name, LeaseID: lease.ID, Owner: lease.Owner,
		Metadata: lease.Metadata, Count: len(st.Worktrees) + 1,
	}
	if m.Cfg.Min != nil {
		in.Min = *m.Cfg.Min
	}
	if m.Cfg.Max != nil {
		in.Max = *m.Cfg.Max
	}
	var dirName string
	slot, err := freeSlot(st, func(i int) (bool, error) {
		in.Index = i
		d, err := config.RenderDir(m.Cfg.WorktreeDirTemplate, in)
		if err != nil {
			return false, ErrUsage(err.Error(), "fix worktree-dir-template", map[string]any{"flag": "worktree-dir-template"})
		}
		dirName = d
		_, serr := os.Lstat(filepath.Join(root, d))
		if serr == nil {
			return true, nil
		}
		if errors.Is(serr, os.ErrNotExist) {
			return false, nil
		}
		return false, serr
	})
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return state.Worktree{}, err
		}
		return state.Worktree{}, ErrInternal(err)
	}
	if fl := m.Cfg.FreeSpaceFloor; fl != nil && *fl > 0 {
		free, err := m.freeBytes(existingAncestor(root))
		if err != nil {
			return state.Worktree{}, ErrInternal(err)
		}
		if free < *fl {
			return state.Worktree{}, ErrLowDisk(free, *fl)
		}
	}
	id := m.newWorktreeID(st)
	return state.Worktree{
		ID: id, Slot: slot, Path: filepath.Join(root, dirName), Status: state.Preparing,
		Lease: lease, CreatedAt: now, StatusSince: now,
	}, nil
}

// markLeased makes w leased under lease, with its expiry counted from now.
func (m *Manager) markLeased(w *state.Worktree, lease *state.Lease, now time.Time) {
	w.Lease, w.Status, w.StatusSince, w.Reason = lease, state.Leased, now, ""
	lease.Expiry = nil
	if !lease.TTL.None && lease.TTL.D > 0 {
		exp := now.Add(lease.TTL.D)
		lease.Expiry = &exp
	}
}

// provisionAndRecord is steps 3 to 5.
func (m *Manager) provisionAndRecord(ctx context.Context, r LeaseRequest, p *plan) (Record, error) {
	if err := os.MkdirAll(m.Cfg.WorktreeRoot, 0o755); err != nil {
		return m.fail(ctx, p, ErrInternal(err))
	}
	if p.switchOnly {
		t0 := time.Now()
		runHook := m.Cfg.ReuseHookPolicy != "skip"
		if err := m.Prov.Switch(ctx, p.w.Path, p.spec, runHook); err != nil {
			return m.fail(ctx, p, err)
		}
		m.phase("switch", t0)
	} else {
		t0 := time.Now()
		rel, err := m.Store.HoldCreate(ctx, span(m.Cfg.CreateTimeout, 24*time.Hour))
		m.phase("wait", t0)
		if err != nil {
			if errors.Is(err, state.ErrBusy) {
				return m.fail(ctx, p, ErrBusy("another worktree creation"))
			}
			return m.fail(ctx, p, err)
		}
		t1 := time.Now()
		err = m.Prov.Create(ctx, p.w.Path, p.spec, "pg-gwm "+p.w.ID)
		rel()
		if err != nil {
			return m.fail(ctx, p, err)
		}
		m.phase("create", t1)
	}

	lk, cancel, err := m.lockDetached(ctx)
	if err != nil {
		return Record{}, err
	}
	defer cancel()
	defer lk.Unlock()
	var w *state.Worktree
	for i := range lk.State().Worktrees {
		if lk.State().Worktrees[i].ID == p.w.ID {
			w = &lk.State().Worktrees[i]
		}
	}
	if w == nil {
		return Record{}, ErrInternal(fmt.Errorf("worktree %s vanished from the state while it was being prepared", p.w.ID))
	}
	w.Branch = p.spec.Branch
	m.markLeased(w, w.Lease, m.now())
	if err := lk.Commit(); err != nil {
		return Record{}, err
	}
	m.event(state.Event{
		Kind: "lease", WorktreeID: w.ID, LeaseID: w.Lease.ID, Name: w.Lease.Name, Owner: w.Lease.Owner,
		Outcome: "ok", Details: map[string]any{"branch": w.Branch, "path": w.Path, "slot": w.Slot},
	})
	return m.finalRecord(ctx, *w, r), nil
}

// finalRecord renders w with the head its directory reports, and registers
// the repository for the reaper.
func (m *Manager) finalRecord(ctx context.Context, w state.Worktree, r LeaseRequest) Record {
	head := ""
	if out, err := m.git(context.WithoutCancel(ctx), w.Path, "rev-parse", "HEAD"); err == nil {
		head = strings.TrimSpace(out)
	}
	if m.Registry.Path != "" {
		_ = m.Registry.Add(m.Repo.Main)
	}
	rec := m.record(w, head)
	if r.OrRenew {
		f := false
		rec.Renewed = &f
	}
	return rec
}

// lockDetached takes the repository lock for recording an outcome, which must
// succeed even after the request was cancelled.
func (m *Manager) lockDetached(ctx context.Context) (*state.Locked, context.CancelFunc, error) {
	bound := span(m.Cfg.LockWait, 5*time.Second)
	if bound < 5*time.Second {
		bound = 5 * time.Second
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bound)
	lk, err := m.Store.Lock(dctx, bound)
	if err != nil {
		cancel()
		if errors.Is(err, state.ErrBusy) {
			return nil, nil, ErrBusy("")
		}
		return nil, nil, err
	}
	return lk, cancel, nil
}

// fail records the prepared worktree as broken (its name is free again) and
// returns the error to report: interrupted after a cancel, the cause's own
// catalog error when it has one, else broken.
func (m *Manager) fail(ctx context.Context, p *plan, cause error) (Record, error) {
	interrupted := ctx.Err() != nil
	reason := cause.Error()
	if interrupted {
		reason = "interrupted while the worktree was being prepared"
	}
	if lk, cancel, err := m.lockDetached(ctx); err == nil {
		for i := range lk.State().Worktrees {
			w := &lk.State().Worktrees[i]
			if w.ID == p.w.ID {
				w.Status, w.Reason, w.Lease, w.StatusSince = state.Broken, reason, nil, m.now()
			}
		}
		_ = lk.Commit()
		lk.Unlock()
		cancel()
	}
	m.event(state.Event{
		Kind: "broken", WorktreeID: p.w.ID, LeaseID: p.w.Lease.ID, Name: p.w.Lease.Name,
		Owner: p.w.Lease.Owner, Outcome: "error", Details: map[string]any{"reason": reason},
	})

	var ce *Error
	var ge *gitrun.GitError
	switch {
	case interrupted:
		return Record{}, ErrInterrupted("interrupted while the worktree was being prepared; it is marked broken")
	case errors.As(cause, &ce):
		return Record{}, ce
	case errors.As(cause, &ge) && strings.Contains(ge.Stderr, "cannot lock ref"):
		return Record{}, ErrUsage("git cannot create the branch: "+strings.TrimSpace(ge.Stderr),
			"pass -b with another name, or --detach", map[string]any{"branch": p.spec.Branch, "git": strings.TrimSpace(ge.Stderr)})
	}
	return Record{}, ErrBroken(p.w.ID, reason)
}
