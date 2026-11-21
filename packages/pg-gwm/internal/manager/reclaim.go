package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// errHeld says another command holds the worktree's operation hold, so this
// one skips it.
var errHeld = errors.New("worktree is held by another command")

// reclaimCandidates lists what a lease may reclaim: released worktrees (the
// longest released first) before lease-expired ones (the oldest expiry
// first).
func (m *Manager) reclaimCandidates(st *state.State) []*state.Worktree {
	now := m.now()
	var released, expired []*state.Worktree
	for i := range st.Worktrees {
		w := &st.Worktrees[i]
		switch w.Effective(now) {
		case state.Released:
			released = append(released, w)
		case state.LeaseExpired:
			expired = append(expired, w)
		}
	}
	sort.SliceStable(released, func(i, j int) bool {
		if !released[i].StatusSince.Equal(released[j].StatusSince) {
			return released[i].StatusSince.Before(released[j].StatusSince)
		}
		return released[i].Slot < released[j].Slot
	})
	sort.SliceStable(expired, func(i, j int) bool {
		a, b := expired[i].Lease.Expiry, expired[j].Lease.Expiry
		if !a.Equal(*b) {
			return a.Before(*b)
		}
		return expired[i].Slot < expired[j].Slot
	})
	return append(released, expired...)
}

// hasExpired reports whether any worktree's lease has expired.
func (m *Manager) hasExpired(st *state.State) bool {
	now := m.now()
	for _, w := range st.Worktrees {
		if w.Effective(now) == state.LeaseExpired {
			return true
		}
	}
	return false
}

// reclaimOne reclaims exactly one worktree for a lease (INV-GWM-19): the
// first candidate of reclaimCandidates whose operation hold is free. With
// reuse on the worktree is kept, detached, and becomes available; with reuse
// off it is removed. It returns (nil, nil) with the lock still held when no
// candidate exists. Otherwise l is unlocked on return, and the worktree it
// reports is a copy of the record as it stood after the reclaim.
func (m *Manager) reclaimOne(ctx context.Context, l *state.Locked) (*state.Worktree, error) {
	for _, c := range m.reclaimCandidates(l.State()) {
		w, err := m.reclaimWorktree(ctx, l, c.ID, m.Cfg.Reuse)
		if errors.Is(err, errHeld) {
			continue
		}
		return w, err
	}
	return nil, nil
}

// reclaimWorktree is the per-worktree reclaim step of lease and reap
// (INV-GWM-23). l is locked and id names a released or lease-expired
// worktree (or, with keep false, an available one that reap retires). It ends the lease (status reclaiming, with a reclaimed tombstone
// for an expired one), takes the operation hold, releases l, preserves unsaved
// work, then either keeps the worktree detached and available (keep) or
// unlocks and removes it, and records the outcome under a fresh lock. A
// worktree another command holds returns errHeld with l still locked and
// nothing changed; any other return leaves l unlocked. A worktree whose work
// cannot be preserved, or whose git step fails, is marked broken and reported
// as the broken error.
func (m *Manager) reclaimWorktree(ctx context.Context, l *state.Locked, id string, keep bool) (*state.Worktree, error) {
	var w *state.Worktree
	for i := range l.State().Worktrees {
		if l.State().Worktrees[i].ID == id {
			w = &l.State().Worktrees[i]
		}
	}
	now := m.now()
	// Reap also retires an available worktree above max through here, with
	// keep false.
	if w == nil || (w.Effective(now) != state.Released && w.Effective(now) != state.LeaseExpired &&
		(w.Status != state.Available || w.Lease != nil || keep)) {
		return nil, ErrInternal(errors.New("worktree " + id + " is not reclaimable"))
	}
	rel, err := m.Store.HoldOp(ctx, id)
	if err != nil {
		return nil, errHeld
	}
	defer rel()

	snap := *w // keeps the lease, which names the preserved branch
	if snap.Lease != nil {
		le := snap.Lease
		l.State().Tombstones = append(l.State().Tombstones, state.Tombstone{
			Kind: "reclaimed", LeaseID: le.ID, Name: le.Name, Owner: le.Owner, At: now,
		})
	}
	w.Status, w.StatusSince = state.Reclaiming, now
	if err := l.Commit(); err != nil {
		return nil, err
	}
	l.Unlock()

	pb, gitErr := m.preserveIfNeeded(ctx, snap)
	if gitErr == nil {
		if _, err := m.Prov.RemoveStaleIndexLock(snap.Path, m.Cfg.IndexLockStaleAfter.D); err != nil {
			gitErr = err
		}
	}
	if gitErr == nil {
		if keep {
			gitErr = m.Prov.Switch(ctx, snap.Path, provision.BranchSpec{Detach: true}, m.Cfg.ReuseHookPolicy != "skip")
		} else {
			gitErr = m.removeWorktree(ctx, snap.Path)
		}
	}

	lk, cancel, err := m.lockDetached(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer lk.Unlock()
	st := lk.State()
	idx := -1
	for i := range st.Worktrees {
		if st.Worktrees[i].ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return nil, ErrInternal(errors.New("worktree " + id + " vanished from the state while it was being reclaimed"))
	}
	cur := &st.Worktrees[idx]
	if snap.Lease != nil && pb != "" {
		for i := range st.Tombstones {
			if st.Tombstones[i].LeaseID == snap.Lease.ID {
				st.Tombstones[i].PreservedBranch = pb
			}
		}
	}
	leaseID, name, owner := "", "", ""
	if snap.Lease != nil {
		leaseID, name, owner = snap.Lease.ID, snap.Lease.Name, snap.Lease.Owner
	}
	if gitErr != nil {
		reason := gitErr.Error()
		cur.Status, cur.Reason, cur.Lease, cur.StatusSince = state.Broken, reason, nil, m.now()
		_ = lk.Commit()
		m.event(state.Event{
			Kind: "broken", WorktreeID: id, LeaseID: leaseID, Name: name, Owner: owner,
			Outcome: "error", Details: map[string]any{"reason": reason},
		})
		return nil, ErrBroken(id, reason)
	}
	action := "removed"
	if keep {
		cur.Status, cur.Lease, cur.Branch, cur.Reason, cur.StatusSince = state.Available, nil, "", "", m.now()
		action = "kept"
	}
	out := *cur
	if !keep {
		st.Worktrees = append(st.Worktrees[:idx], st.Worktrees[idx+1:]...)
	}
	if err := lk.Commit(); err != nil {
		return nil, err
	}
	m.event(state.Event{
		Kind: "reclaim", WorktreeID: id, LeaseID: leaseID, Name: name, Owner: owner, Outcome: "ok",
		Details: map[string]any{"action": action, "preserved_branch": pb},
	})
	return &out, nil
}

// removeWorktree unlocks, stops the file-system monitor of, and removes the
// worktree at path.
func (m *Manager) removeWorktree(ctx context.Context, path string) error {
	// A retry after a failed removal meets a worktree already unlocked.
	var ge *gitrun.GitError
	if err := m.Prov.Unlock(ctx, path); err != nil && (!errors.As(err, &ge) || !strings.Contains(ge.Stderr, "not locked")) {
		return err
	}
	if err := m.Prov.StopFSMonitor(ctx, path); err != nil {
		return err
	}
	return m.Prov.Remove(ctx, path)
}

// registered reports whether path is a worktree git still knows: it holds a
// .git link and the repository lists it. A listing failure counts as
// registered, so a broken git never causes a directory to be deleted.
func (m *Manager) registered(ctx context.Context, path string) bool {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
		return false
	}
	out, err := m.git(ctx, m.Repo.Main, "worktree", "list", "--porcelain")
	if err != nil {
		return true
	}
	want := realPath(path)
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && realPath(p) == want {
			return true
		}
	}
	return false
}
