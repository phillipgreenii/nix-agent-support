package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// write puts content at rel inside dir.
func (e *env) write(dir, rel, content string) {
	e.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// commitFile commits rel with content in the main checkout.
func (e *env) commitFile(rel, content string) {
	e.t.Helper()
	if e.fake != nil {
		e.fake.commitFile(rel, content)
		return
	}
	e.write(e.repo.Dir, rel, content)
	e.git(e.repo.Dir, "add", rel)
	e.git(e.repo.Dir, "commit", "-m", "add "+rel)
}

// worktreeOf is the state record of the worktree with id.
func (e *env) worktreeOf(id string) (state.Worktree, bool) {
	for _, w := range e.state().Worktrees {
		if w.ID == id {
			return w, true
		}
	}
	return state.Worktree{}, false
}

func TestPreservedNameAndMessage(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 0, 5, 0, time.UTC)
	cases := []struct{ branch, name, want string }{
		{"feature/x", "potato", "pg-gwm/dirty-reaped-20261114T100005Z-feature-x-potato"},
		{"", "l-abc", "pg-gwm/dirty-reaped-20261114T100005Z-detached-l-abc"},
		{"a b~c", "n:a?me", "pg-gwm/dirty-reaped-20261114T100005Z-a-b-c-n-a-me"},
		{"x..y", "z.lock", "pg-gwm/dirty-reaped-20261114T100005Z-x.-y-z.lock-"},
	}
	for _, c := range cases {
		if got := preservedBranchName(at, c.branch, c.name); got != c.want {
			t.Errorf("preservedBranchName(%q, %q) = %q, want %q", c.branch, c.name, got, c.want)
		}
	}
	msg := preserveMessage("potato", "l-abc", "feature/x", "pg-gwm/dirty-reaped-X")
	for _, want := range []string{"potato", "l-abc", "feature/x", "hooks were skipped", "pg-gwm lease -b <your-branch> pg-gwm/dirty-reaped-X", "git reset --soft HEAD~1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

// blockerProv reports a fixed blocker for every worktree's unsaved work.
type blockerProv struct {
	provision.Provisioner
	blocker string
}

func (b blockerProv) Unsaved(ctx context.Context, dir string) (provision.Unsaved, error) {
	u, err := b.Provisioner.Unsaved(ctx, dir)
	u.Blocker = b.blocker
	return u, err
}

func TestPreservation(t *testing.T) {
	t.Run("branch, commit, untracked in, ignored out, lease branch unmoved", func(t *testing.T) {
		e := newEnv(t)
		e.commitFile(".gitignore", "*.log\n")
		e.commitFile("tracked.txt", "one\n")
		rec := e.mustLease(LeaseRequest{Name: "potato", Branch: "feature/x", Create: true})
		leaseTip := e.git(rec.WorktreePath, "rev-parse", "feature/x")
		e.write(rec.WorktreePath, "tracked.txt", "two\n")
		e.write(rec.WorktreePath, "new.txt", "new\n")
		e.write(rec.WorktreePath, "build.log", "noise\n")

		w, _ := e.worktreeOf(rec.ID)
		pb, err := e.m.preserveIfNeeded(t.Context(), w)
		if err != nil {
			t.Fatal(err)
		}
		if want := "pg-gwm/dirty-reaped-20261114T100000Z-feature-x-potato"; pb != want {
			t.Fatalf("branch = %q, want %q", pb, want)
		}
		if got := e.git(e.repo.Dir, "log", "-1", "--format=%s", pb); got != "adding dirty WIP" {
			t.Errorf("subject = %q", got)
		}
		body := e.git(e.repo.Dir, "log", "-1", "--format=%b", pb)
		for _, want := range []string{"pg-gwm lease -b <your-branch> " + pb, "git reset --soft HEAD~1", "hooks were skipped", "potato"} {
			if !strings.Contains(body, want) {
				t.Errorf("body lacks %q:\n%s", want, body)
			}
		}
		files := e.git(e.repo.Dir, "ls-tree", "-r", "--name-only", pb)
		if !strings.Contains(files, "new.txt") || strings.Contains(files, "build.log") {
			t.Errorf("preserved tree:\n%s", files)
		}
		if got := e.git(e.repo.Dir, "show", pb+":tracked.txt"); got != "two" {
			t.Errorf("tracked.txt = %q", got)
		}
		if got := e.git(e.repo.Dir, "rev-parse", "feature/x"); got != leaseTip {
			t.Errorf("lease branch moved: %s -> %s", leaseTip, got)
		}
		st := e.state()
		if len(st.Preserved) != 1 || st.Preserved[0].Branch != pb || st.Preserved[0].Name != "potato" || st.Preserved[0].LeaseID != str(rec.LeaseID) {
			t.Errorf("preserved entries = %+v", st.Preserved)
		}
		if !strings.Contains(e.events(), `"kind":"preserve"`) {
			t.Errorf("no preserve event:\n%s", e.events())
		}
	})

	t.Run("nothing unsaved preserves nothing", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "n", Detach: true})
		w, _ := e.worktreeOf(rec.ID)
		pb, err := e.m.preserveIfNeeded(t.Context(), w)
		if err != nil || pb != "" || len(e.state().Preserved) != 0 {
			t.Errorf("pb = %q, err = %v, entries = %+v", pb, err, e.state().Preserved)
		}
	})

	t.Run("detached head stands in for the branch", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "n", Detach: true})
		e.write(rec.WorktreePath, "f.txt", "x\n")
		w, _ := e.worktreeOf(rec.ID)
		pb, err := e.m.preserveIfNeeded(t.Context(), w)
		if err != nil {
			t.Fatal(err)
		}
		if want := "pg-gwm/dirty-reaped-20261114T100000Z-detached-n"; pb != want {
			t.Errorf("branch = %q, want %q", pb, want)
		}
	})

	t.Run("merge in progress blocks and the hand-out marks the worktree broken", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "n", Detach: true})
		gd := e.git(rec.WorktreePath, "rev-parse", "--absolute-git-dir")
		e.write(gd, "MERGE_HEAD", rec.Head+"\n")
		w, _ := e.worktreeOf(rec.ID)
		if _, err := e.m.preserveIfNeeded(t.Context(), w); !errors.Is(err, provision.ErrPreserveBlocked) {
			t.Fatalf("err = %v, want ErrPreserveBlocked", err)
		}
		if got := e.git(e.repo.Dir, "branch", "--list", "pg-gwm/*"); got != "" {
			t.Errorf("a preserved branch was made: %q", got)
		}

		// Make it available, then lease: the hand-out finds the blocker.
		e.seed(func(st *state.State) {
			for i := range st.Worktrees {
				st.Worktrees[i].Status, st.Worktrees[i].Lease, st.Worktrees[i].Branch = state.Available, nil, ""
			}
		})
		_, err := e.lease(LeaseRequest{Name: "next", Detach: true})
		wantErr(t, err, "broken", 28)
		if w, ok := e.worktreeOf(rec.ID); !ok || w.Status != state.Broken {
			t.Errorf("worktree after = %+v (present %v), want broken", w, ok)
		}
		if _, err := os.Stat(rec.WorktreePath); err != nil {
			t.Errorf("worktree removed: %v", err)
		}
	})

	t.Run("submodule change blocks", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "n", Detach: true})
		e.m.Prov = blockerProv{Provisioner: e.m.Prov, blocker: "submodule change in sub"}
		e.write(rec.WorktreePath, "f.txt", "x\n")
		w, _ := e.worktreeOf(rec.ID)
		_, err := e.m.preserveIfNeeded(t.Context(), w)
		if !errors.Is(err, provision.ErrPreserveBlocked) || !strings.Contains(err.Error(), "submodule") {
			t.Errorf("err = %v", err)
		}
	})
}

// reclaimNow reclaims one worktree as a lease at max would and returns it.
func (e *env) reclaimNow() *state.Worktree {
	e.t.Helper()
	lk, err := e.m.Store.Lock(e.t.Context(), time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	defer lk.Unlock()
	w, err := e.m.reclaimOne(e.t.Context(), lk)
	if err != nil {
		e.t.Fatal(err)
	}
	return w
}

func TestLeasePreservedBranch(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Max = ptr(1) })
	rec := e.mustLease(LeaseRequest{Name: "potato", Branch: "feature/x", Create: true, TTL: ptr(dur(t, "1h"))})
	e.write(rec.WorktreePath, "wip.txt", "work\n")
	e.now = e.now.Add(2 * time.Hour)

	// A lease at max reclaims the expired one, preserving its work.
	other := e.mustLease(LeaseRequest{Name: "other", Detach: true})
	list, err := e.m.PreservedList(t.Context())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	pb := list[0].Branch
	if !strings.HasPrefix(pb, "pg-gwm/dirty-reaped-20261114T120000Z-feature-x-potato") || list[0].Name != "potato" || list[0].LeaseID != str(rec.LeaseID) {
		t.Errorf("entry = %+v", list[0])
	}
	if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(other.LeaseID)}); err != nil {
		t.Fatal(err)
	}

	// Journey step 4: lease -b mine <preserved branch>, then undo the commit.
	mine := e.mustLease(LeaseRequest{Name: "m", Branch: "mine", Create: true, CommitIsh: pb})
	if str(mine.Branch) != "mine" {
		t.Fatalf("branch = %v", mine.Branch)
	}
	e.git(mine.WorktreePath, "reset", "--soft", "HEAD~1")
	if got := e.git(mine.WorktreePath, "status", "--porcelain"); got != "A  wip.txt" {
		t.Errorf("status after reset = %q, want the work staged", got)
	}

	// The preserved namespace itself stays closed to -b.
	_, err = e.lease(LeaseRequest{Name: "z", Branch: pb + "-2", Create: true})
	wantErr(t, err, "usage", 2)
}

func TestHandOutPreservesWrites(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Reuse = true })
	rec := e.mustLease(LeaseRequest{Name: "first", Branch: "feat", Create: true, TTL: ptr(dur(t, "1h"))})
	e.now = e.now.Add(2 * time.Hour)
	got := e.reclaimNow()
	if got == nil || got.ID != rec.ID || got.Status != state.Available || got.Branch != "" || got.Lease != nil {
		t.Fatalf("reclaimed = %+v", got)
	}
	// Written after the reclaim, before the next lease's switch.
	e.write(rec.WorktreePath, "late.txt", "late\n")

	next := e.mustLease(LeaseRequest{Name: "next", Branch: "next-b", Create: true})
	if next.ID != rec.ID {
		t.Fatalf("lease took %s, want the reclaimed %s", next.ID, rec.ID)
	}
	list, _ := e.m.PreservedList(t.Context())
	if len(list) != 1 {
		t.Fatalf("preserved = %+v", list)
	}
	if got := e.git(e.repo.Dir, "show", list[0].Branch+":late.txt"); got != "late" {
		t.Errorf("late.txt on the preserved branch = %q", got)
	}
	if _, err := os.Stat(filepath.Join(next.WorktreePath, "late.txt")); !os.IsNotExist(err) {
		t.Errorf("late.txt still in the handed-out worktree: %v", err)
	}
	if e.git(next.WorktreePath, "status", "--porcelain") != "" {
		t.Error("handed-out worktree is not clean")
	}
}
