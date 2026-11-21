package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// cleanCounter counts Clean calls.
type cleanCounter struct {
	provision.Provisioner
	n atomic.Int32
}

func (c *cleanCounter) Clean(ctx context.Context, dir string, keep []string) error {
	c.n.Add(1)
	return c.Provisioner.Clean(ctx, dir, keep)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestCleanOnOff(t *testing.T) {
	setup := func(t *testing.T, clean bool) (*env, *cleanCounter, Record) {
		e := newFakeEnv(t, func(c *config.RepoConfig) {
			c.Reuse, c.Clean, c.KeepList = true, clean, []string{".keepme"}
		})
		cc := &cleanCounter{Provisioner: e.m.Prov}
		e.m.Prov = cc
		e.fake.ignore("build.log", ".keepme")
		e.commitFile("tracked.txt", "one\n")
		first := e.mustLease(LeaseRequest{Name: "first", Detach: true})
		if cc.n.Load() != 0 {
			t.Fatal("a fresh worktree was cleaned")
		}
		e.write(first.WorktreePath, "build.log", "ignored\n")
		e.write(first.WorktreePath, ".keepme", "keep\n")
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(first.LeaseID)}); err != nil {
			t.Fatal(err)
		}
		// Written after the release: unsaved work, preserved before any clean.
		e.write(first.WorktreePath, "tracked.txt", "two\n")
		e.write(first.WorktreePath, "untracked.txt", "u\n")
		return e, cc, first
	}

	t.Run("on", func(t *testing.T) {
		e, cc, first := setup(t, true)
		second := e.mustLease(LeaseRequest{Name: "second", Detach: true})
		if second.ID != first.ID || cc.n.Load() != 1 {
			t.Fatalf("reused %v, clean calls %d", second.ID == first.ID, cc.n.Load())
		}
		p := second.WorktreePath
		if exists(filepath.Join(p, "build.log")) || exists(filepath.Join(p, "untracked.txt")) {
			t.Error("ignored or untracked files survived the clean")
		}
		if b, _ := os.ReadFile(filepath.Join(p, "tracked.txt")); string(b) != "one\n" {
			t.Errorf("tracked change survived: %q", b)
		}
		if !exists(filepath.Join(p, ".keepme")) {
			t.Error("the keep list was cleaned")
		}
		list, _ := e.m.PreservedList(t.Context())
		if len(list) != 1 {
			t.Fatalf("preserved = %+v", list)
		}
		files := strings.Join(e.fake.preserved[list[0].Branch], " ")
		if !strings.Contains(files, "untracked.txt") || !strings.Contains(files, "tracked.txt") {
			t.Errorf("work not preserved: %s", files)
		}
	})

	t.Run("off", func(t *testing.T) {
		e, cc, first := setup(t, false)
		second := e.mustLease(LeaseRequest{Name: "second", Detach: true})
		if second.ID != first.ID || cc.n.Load() != 0 {
			t.Fatalf("reused %v, clean calls %d", second.ID == first.ID, cc.n.Load())
		}
		if !exists(filepath.Join(second.WorktreePath, "build.log")) {
			t.Error("an ignored file did not carry over")
		}
	})
}

// A reuse switches the worktree with the hook enabled unless the policy is
// skip. That the skip is one command's override and never written to
// configuration, and that the hook then really does not run, is the
// provisioner's contract (provisiontest "switch hook policy").
func TestHookPolicy(t *testing.T) {
	run := func(t *testing.T, policy string) []bool {
		e := newFakeEnv(t, func(c *config.RepoConfig) { c.Reuse, c.ReuseHookPolicy = true, policy })
		first := e.mustLease(LeaseRequest{Name: "first", Detach: true})
		if len(e.fake.switches) != 0 {
			t.Fatalf("creation switched %v", e.fake.switches)
		}
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(first.LeaseID)}); err != nil {
			t.Fatal(err)
		}
		second := e.mustLease(LeaseRequest{Name: "second", Branch: "b2", Create: true})
		if second.ID != first.ID {
			t.Fatal("worktree not reused")
		}
		return e.fake.switches
	}
	if got := run(t, "skip"); len(got) != 1 || got[0] {
		t.Errorf("skip switched with runHook %v, want [false]", got)
	}
	if got := run(t, "run"); len(got) != 1 || !got[0] {
		t.Errorf("run switched with runHook %v, want [true]", got)
	}
}

// setStatus turns the worktree with id into an available one.
func (e *env) makeAvailable(id string) {
	e.t.Helper()
	e.seed(func(st *state.State) {
		for i := range st.Worktrees {
			if st.Worktrees[i].ID == id {
				w := &st.Worktrees[i]
				w.Status, w.Lease, w.Branch = state.Available, nil, ""
			}
		}
	})
}

func TestSelectionOrder(t *testing.T) {
	// four worktrees: available A, released R, lease-expired E, leased L.
	four := func(t *testing.T, tune ...func(*config.RepoConfig)) (e *env, a, r, x, l Record) {
		e = newFakeEnv(t, tune...)
		a = e.mustLease(LeaseRequest{Name: "a", Detach: true})
		r = e.mustLease(LeaseRequest{Name: "r", Branch: "keep-r", Create: true})
		x = e.mustLease(LeaseRequest{Name: "x", Detach: true, TTL: ptr(dur(t, "1h"))})
		l = e.mustLease(LeaseRequest{Name: "l", Detach: true})
		e.makeAvailable(a.ID)
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(r.LeaseID)}); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(2 * time.Hour)
		return
	}

	t.Run("reuse on: available, released, expired, then create", func(t *testing.T) {
		e, a, r, x, l := four(t, func(c *config.RepoConfig) { c.Reuse = true })
		for i, want := range []string{a.ID, r.ID, x.ID} {
			got := e.mustLease(LeaseRequest{Name: "n" + string(rune('1'+i)), Detach: true})
			if got.ID != want {
				t.Fatalf("lease %d took %s, want %s", i+1, got.ID, want)
			}
		}
		fresh := e.mustLease(LeaseRequest{Name: "n4", Detach: true})
		if fresh.ID == a.ID || fresh.ID == r.ID || fresh.ID == x.ID || fresh.ID == l.ID {
			t.Errorf("lease 4 took an existing worktree %s", fresh.ID)
		}
	})

	t.Run("reuse off: available, then create", func(t *testing.T) {
		e, a, r, x, _ := four(t)
		if got := e.mustLease(LeaseRequest{Name: "n1", Detach: true}); got.ID != a.ID {
			t.Fatalf("lease 1 took %s, want available %s", got.ID, a.ID)
		}
		fresh := e.mustLease(LeaseRequest{Name: "n2", Detach: true})
		if fresh.ID == r.ID || fresh.ID == x.ID {
			t.Errorf("lease 2 reused %s with reuse off", fresh.ID)
		}
		if w, _ := e.worktreeOf(r.ID); w.Status != state.Released {
			t.Errorf("released worktree changed: %+v", w)
		}
	})

	t.Run("a released holder of the branch comes before an available one", func(t *testing.T) {
		e, _, r, _, _ := four(t)
		e.makeAvailable(func() string { w := e.state().Worktrees; return w[3].ID }())
		got := e.mustLease(LeaseRequest{Name: "n1", CommitIsh: "keep-r"})
		if got.ID != r.ID {
			t.Errorf("took %s, want the released holder %s", got.ID, r.ID)
		}
	})
}

func TestMaxReclaim(t *testing.T) {
	t.Run("released before expired, exactly one, no top-up", func(t *testing.T) {
		e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max, c.Min = ptr(2), ptr(2) })
		rel := e.mustLease(LeaseRequest{Name: "rel", Detach: true})
		exp := e.mustLease(LeaseRequest{Name: "exp", Detach: true, TTL: ptr(dur(t, "1h"))})
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(rel.LeaseID)}); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(2 * time.Hour)
		e.mustLease(LeaseRequest{Name: "new", Detach: true})
		if _, ok := e.worktreeOf(rel.ID); ok {
			t.Error("the released worktree was not reclaimed (removed with reuse off)")
		}
		w, ok := e.worktreeOf(exp.ID)
		if !ok || w.Effective(e.now) != state.LeaseExpired {
			t.Errorf("expired worktree touched: %+v present=%v", w, ok)
		}
		if n := len(e.state().Worktrees); n != 2 {
			t.Errorf("%d worktrees, want 2 (no standby top-up)", n)
		}
	})

	t.Run("reuse on keeps the reclaimed worktree", func(t *testing.T) {
		e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max, c.Reuse = ptr(1), true })
		exp := e.mustLease(LeaseRequest{Name: "exp", Detach: true, TTL: ptr(dur(t, "1h"))})
		e.now = e.now.Add(2 * time.Hour)
		got := e.mustLease(LeaseRequest{Name: "new", Detach: true})
		if got.ID != exp.ID || len(e.state().Worktrees) != 1 {
			t.Errorf("took %s of %d worktrees, want the reclaimed %s", got.ID, len(e.state().Worktrees), exp.ID)
		}
		// The expired lease ended with a reclaimed tombstone.
		_, err := e.m.Renew(t.Context(), LeaseKey{LeaseID: str(exp.LeaseID)}, nil, nil)
		_ = wantErr(t, err, "lease-lost", 25)
	})

	t.Run("only leased worktrees: max-reached with the counts", func(t *testing.T) {
		e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max = ptr(2) })
		e.mustLease(LeaseRequest{Name: "a", Detach: true})
		e.mustLease(LeaseRequest{Name: "b", Detach: true})
		_, err := e.lease(LeaseRequest{Name: "c", Detach: true})
		ce := wantErr(t, err, "max-reached", 20)
		if ce.Details["max"] != 2 {
			t.Errorf("details = %+v", ce.Details)
		}
	})

	t.Run("--wait succeeds when a release happens", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max = ptr(1) })
			a := e.mustLease(LeaseRequest{Name: "a", Detach: true})
			other := e.manager(func(c *config.RepoConfig) { c.Max = ptr(1) })
			go func() {
				time.Sleep(500 * time.Millisecond)
				_, _ = other.Release(context.Background(), LeaseKey{LeaseID: str(a.LeaseID)})
			}()
			start := time.Now()
			rec, err := e.lease(LeaseRequest{Name: "b", Detach: true, Wait: 2 * time.Second})
			if err != nil {
				t.Fatalf("lease --wait: %v", err)
			}
			// Lower bound only: proves the wait happened. An upper bound is
			// wall-clock sensitive under host load.
			if d := time.Since(start); d < 400*time.Millisecond {
				t.Errorf("took %v, want at least 400ms", d)
			}
			if str(rec.Name) != "b" {
				t.Errorf("record = %+v", rec)
			}
		})
	})

	t.Run("--wait gives up with max-reached", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max = ptr(1) })
			e.mustLease(LeaseRequest{Name: "a", Detach: true})
			start := time.Now()
			_, err := e.lease(LeaseRequest{Name: "b", Detach: true, Wait: 1200 * time.Millisecond})
			_ = wantErr(t, err, "max-reached", 20)
			if d := time.Since(start); d < 1200*time.Millisecond {
				t.Errorf("gave up after %v, before the 1.2s wait", d)
			}
		})
	})
}

func TestPreservedListPrune(t *testing.T) {
	e := newFakeEnv(t)
	f := e.fake
	const (
		old    = "pg-gwm/dirty-reaped-20261001T000000Z-old-x"
		merged = "pg-gwm/dirty-reaped-20261114T090000Z-merged-x"
		young  = "pg-gwm/dirty-reaped-20261114T090000Z-young-x"
		out    = "pg-gwm/dirty-reaped-20261001T000000Z-out-x"
	)
	// A commit of its own is one no other branch reaches; merged is at main's.
	f.addBranchAt(old)
	f.addBranch(merged)
	f.addBranchAt(young)
	f.addBranchAt(out)
	f.addBranchAt("plain-old-branch")
	if err := f.addWorktree(filepath.Join(t.TempDir(), "co"), out); err != nil {
		t.Fatal(err)
	}

	// A real preservation, then a hand deletion: it must not be listed.
	rec := e.mustLease(LeaseRequest{Name: "gone", Detach: true})
	e.write(rec.WorktreePath, "f.txt", "x\n")
	w, _ := e.worktreeOf(rec.ID)
	gone, err := e.m.preserveIfNeeded(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.heads, gone)

	list, err := e.m.PreservedList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list {
		names = append(names, p.Branch)
		if p.Branch == gone {
			t.Errorf("hand-deleted branch %s listed", gone)
		}
		if p.At.IsZero() {
			t.Errorf("%s has no time", p.Branch)
		}
		if p.Name != "" || p.LeaseID != "" {
			t.Errorf("unknown branch %s has lease fields %+v", p.Branch, p)
		}
	}
	if len(list) != 4 {
		t.Fatalf("list = %v, want the four hand-made branches", names)
	}

	day := 24 * time.Hour
	dry, err := e.m.PreservedPrune(t.Context(), &day, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(dry, old, merged) {
		t.Errorf("dry run = %v, want %s and %s", dry, old, merged)
	}
	if got := f.branchesWithPrefix("pg-gwm/"); len(got) != 4 {
		t.Errorf("dry run deleted something: %v", got)
	}

	done, err := e.m.PreservedPrune(t.Context(), &day, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(done, old, merged) {
		t.Errorf("deleted = %v", done)
	}
	left := strings.Join(f.branchesWithPrefix(""), "\n")
	for _, keep := range []string{young, out, "plain-old-branch"} {
		if !strings.Contains(left, keep) {
			t.Errorf("%s was deleted:\n%s", keep, left)
		}
	}
	for _, gone := range []string{old, merged} {
		if strings.Contains(left, gone) {
			t.Errorf("%s survived", gone)
		}
	}
	// Without an age only a contained branch goes, so a second run finds none.
	again, err := e.m.PreservedPrune(t.Context(), nil, false)
	if err != nil || len(again) != 0 {
		t.Errorf("second prune = %v, %v", again, err)
	}
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}
