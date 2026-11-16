package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// InfoKey says what info looks up. Raw is resolved in the order of the
// behavior set: an existing path (or a path inside one), a worktree id, a
// lease id, then a name. Path, ID, LeaseID and Name force one kind. Exactly
// one field is set.
type InfoKey struct{ Raw, Path, ID, LeaseID, Name string }

func (k InfoKey) set() int {
	n := 0
	for _, s := range []string{k.Raw, k.Path, k.ID, k.LeaseID, k.Name} {
		if s != "" {
			n++
		}
	}
	return n
}

// Info reports the record of one worktree, read without the lock.
func (m *Manager) Info(ctx context.Context, k InfoKey) (Record, error) {
	if k.set() != 1 {
		return Record{}, ErrUsage("name exactly one thing to look up", "pg-gwm info <key>, or one of --path --id --lease-id --name", nil)
	}
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		return Record{}, err
	}
	key := k.Raw + k.Path + k.ID + k.LeaseID + k.Name

	if k.Raw != "" || k.Path != "" {
		p := k.Raw + k.Path
		if _, serr := os.Stat(p); serr == nil {
			abs, aerr := filepath.Abs(p)
			if aerr != nil {
				return Record{}, ErrUsage(aerr.Error(), "", nil)
			}
			abs = realPath(abs)
			if w := m.managedContaining(&st, abs); w != nil {
				return m.record(*w, m.headOf(ctx, w.Path)), nil
			}
			if rec, ok, err := m.unmanagedContaining(ctx, &st, abs); err != nil {
				return Record{}, err
			} else if ok {
				return rec, nil
			}
		}
		if k.Path != "" {
			return Record{}, ErrNotFound(key)
		}
	}

	now := m.now()
	find := func(match func(w *state.Worktree) bool) *state.Worktree {
		for i := range st.Worktrees {
			if match(&st.Worktrees[i]) {
				return &st.Worktrees[i]
			}
		}
		return nil
	}
	var w *state.Worktree
	if k.Raw != "" || k.ID != "" {
		w = find(func(w *state.Worktree) bool { return w.ID == key })
	}
	if w == nil && (k.Raw != "" || k.LeaseID != "") {
		w = find(func(w *state.Worktree) bool { return w.Lease != nil && w.Lease.ID == key })
	}
	if w == nil && (k.Raw != "" || k.Name != "") {
		w = find(func(w *state.Worktree) bool {
			return w.Lease != nil && strings.EqualFold(w.Lease.Name, key) && heldStatus(w.Effective(now))
		})
	}
	if w == nil {
		return Record{}, ErrNotFound(key)
	}
	return m.record(*w, m.headOf(ctx, w.Path)), nil
}

func heldStatus(s state.Status) bool {
	return s == state.Preparing || s == state.Leased || s == state.LeaseExpired
}

// managedContaining is the managed worktree whose directory is p or holds p.
func (m *Manager) managedContaining(st *state.State, p string) *state.Worktree {
	var best *state.Worktree
	for i := range st.Worktrees {
		w := &st.Worktrees[i]
		if inside(realPath(w.Path), p) && (best == nil || len(w.Path) > len(best.Path)) {
			best = w
		}
	}
	return best
}

func inside(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// unmanagedContaining finds the git worktree (the main checkout included)
// that holds p and the manager does not manage.
func (m *Manager) unmanagedContaining(ctx context.Context, st *state.State, p string) (Record, bool, error) {
	list, err := m.gitWorktrees(ctx)
	if err != nil {
		return Record{}, false, err
	}
	var best *gitWT
	for i := range list {
		if inside(realPath(list[i].Path), p) && (best == nil || len(list[i].Path) > len(best.Path)) {
			best = &list[i]
		}
	}
	if best == nil {
		return Record{}, false, nil
	}
	rec := m.unmanagedRecord(ctx, *best)
	rec.Warnings = []string{fmt.Sprintf("%s is not managed by pg-gwm", best.Path)}
	return rec, true, nil
}

func (m *Manager) gitWorktrees(ctx context.Context) ([]gitWT, error) {
	out, err := m.git(ctx, m.Repo.Main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, ErrInternal(err)
	}
	return parseWorktreeList(out), nil
}

func (m *Manager) unmanagedRecord(ctx context.Context, g gitWT) Record {
	head := g.Head
	if h := m.headOf(ctx, g.Path); h != "" {
		head = h
	}
	return Record{
		WorktreePath: g.Path, Repo: m.Repo.Label, Branch: strPtr(g.Branch), Head: head,
		Status: state.Unmanaged, Metadata: map[string]string{}, Managed: false,
	}
}

// List reports every managed worktree, oldest first; includeUnmanaged adds
// the repository's other worktrees (the main checkout among them).
func (m *Manager) List(ctx context.Context, includeUnmanaged bool) ([]Record, error) {
	st, err := m.Store.ReadUnlocked()
	if err != nil {
		return nil, err
	}
	wts := append([]state.Worktree(nil), st.Worktrees...)
	sort.SliceStable(wts, func(i, j int) bool {
		if !wts[i].CreatedAt.Equal(wts[j].CreatedAt) {
			return wts[i].CreatedAt.Before(wts[j].CreatedAt)
		}
		return wts[i].ID < wts[j].ID
	})
	recs := make([]Record, 0, len(wts))
	managed := map[string]bool{}
	for _, w := range wts {
		managed[realPath(w.Path)] = true
		recs = append(recs, m.record(w, m.headOf(ctx, w.Path)))
	}
	if includeUnmanaged {
		list, err := m.gitWorktrees(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range list {
			if !managed[realPath(g.Path)] {
				recs = append(recs, m.unmanagedRecord(ctx, g))
			}
		}
	}
	return recs, nil
}
