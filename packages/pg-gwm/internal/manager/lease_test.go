package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/cmdrun"
)

func ptr[T any](v T) *T { return &v }

func TestLeaseRecordFields(t *testing.T) {
	e := newEnv(t)
	rec := e.mustLease(LeaseRequest{Branch: "feat", Create: true})
	if !strings.HasPrefix(rec.WorktreePath, e.m.Cfg.WorktreeRoot+string(filepath.Separator)) {
		t.Errorf("path %q not under worktree-root %q", rec.WorktreePath, e.m.Cfg.WorktreeRoot)
	}
	if !strings.HasPrefix(rec.ID, "w-") || len(rec.ID) != 12 || rec.LeaseID == nil || !strings.HasPrefix(*rec.LeaseID, "l-") || len(*rec.LeaseID) != 12 {
		t.Errorf("ids %q %v", rec.ID, rec.LeaseID)
	}
	if rec.Metadata == nil || len(rec.Metadata) != 0 {
		t.Errorf("metadata = %#v, want empty object", rec.Metadata)
	}
	if rec.LeaseExpiration != nil || rec.Name != nil || rec.Owner != nil {
		t.Errorf("absent values not nil: %v %v %v", rec.LeaseExpiration, rec.Name, rec.Owner)
	}
	if !strings.HasSuffix(rec.CreatedAt, "Z") || rec.CreatedAt != "2026-11-14T10:00:00Z" {
		t.Errorf("created_at = %q", rec.CreatedAt)
	}
	if rec.Status != state.Leased || !rec.Managed || rec.Branch == nil || *rec.Branch != "feat" {
		t.Errorf("record = %+v", rec)
	}
	if want := e.git(e.repo.Dir, "rev-parse", "HEAD"); rec.Head != want {
		t.Errorf("head = %q, want the full commit %q", rec.Head, want)
	}
	if rec.Repo != e.m.Repo.Label {
		t.Errorf("repo = %q", rec.Repo)
	}
	if _, err := os.Stat(rec.WorktreePath); err != nil {
		t.Error(err)
	}
	// The lock reason names the worktree id on every managed worktree.
	if out := e.git(e.repo.Dir, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked pg-gwm "+rec.ID) {
		t.Errorf("worktree not locked with the pg-gwm reason:\n%s", out)
	}
}

func TestLeaseDefaultTTLGivesExpiry(t *testing.T) {
	d, _ := config.ParseDuration("1h")
	e := newEnv(t, func(c *config.RepoConfig) { c.DefaultTTL = d })
	rec := e.mustLease(LeaseRequest{Name: "n", Detach: true})
	if rec.LeaseExpiration == nil || *rec.LeaseExpiration != "2026-11-14T11:00:00Z" {
		t.Errorf("expiration = %v", rec.LeaseExpiration)
	}
	if rec.LeaseTTL == nil || *rec.LeaseTTL != "1h" {
		t.Errorf("ttl = %v", rec.LeaseTTL)
	}
	none := config.NoDuration
	rec = e.mustLease(LeaseRequest{Name: "m", Detach: true, TTL: &none})
	if rec.LeaseExpiration != nil || rec.LeaseTTL == nil || *rec.LeaseTTL != "none" {
		t.Errorf("--ttl none: %v %v", rec.LeaseExpiration, rec.LeaseTTL)
	}
}

func TestLeaseTTLMustBePositive(t *testing.T) {
	e := newEnv(t)
	_, err := e.lease(LeaseRequest{Name: "n", Detach: true, TTL: &config.Duration{}})
	_ = wantErr(t, err, "usage", 2)
}

func TestBranchTable(t *testing.T) {
	t.Run("1 existing branch", func(t *testing.T) {
		e := newFakeEnv(t)
		e.fake.addBranch("feat")
		rec := e.mustLease(LeaseRequest{CommitIsh: "feat"})
		if rec.Branch == nil || *rec.Branch != "feat" {
			t.Errorf("branch = %v", rec.Branch)
		}
	})
	t.Run("2 remote-tracking branch becomes a tracking local branch", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.repo.AddBareRemote(t.Context(), "origin"); err != nil {
			t.Fatal(err)
		}
		e.git(e.repo.Dir, "branch", "topic")
		e.git(e.repo.Dir, "push", "origin", "topic")
		e.git(e.repo.Dir, "branch", "-D", "topic")
		rec := e.mustLease(LeaseRequest{CommitIsh: "topic"})
		if rec.Branch == nil || *rec.Branch != "topic" {
			t.Fatalf("branch = %v", rec.Branch)
		}
		if up := e.git(e.repo.Dir, "rev-parse", "--abbrev-ref", "topic@{upstream}"); up != "origin/topic" {
			t.Errorf("upstream = %q", up)
		}
	})
	t.Run("3 commit-ish that is not a branch is detached", func(t *testing.T) {
		e := newEnv(t)
		first := e.git(e.repo.Dir, "rev-parse", "HEAD")
		e.git(e.repo.Dir, "tag", "v1")
		e.git(e.repo.Dir, "commit", "--allow-empty", "-m", "second")
		for _, ci := range []string{first, "v1"} {
			rec := e.mustLease(LeaseRequest{CommitIsh: ci})
			if rec.Branch != nil || rec.Head != first {
				t.Errorf("%s: branch %v head %q, want detached at %q", ci, rec.Branch, rec.Head, first)
			}
		}
	})
	t.Run("4 -b creates the branch at the commit-ish", func(t *testing.T) {
		e := newEnv(t)
		first := e.git(e.repo.Dir, "rev-parse", "HEAD")
		e.git(e.repo.Dir, "commit", "--allow-empty", "-m", "second")
		rec := e.mustLease(LeaseRequest{Branch: "b4", Create: true, CommitIsh: first})
		if rec.Head != first || e.git(e.repo.Dir, "rev-parse", "b4") != first {
			t.Errorf("b4 not at %s: head %s", first, rec.Head)
		}
	})
	t.Run("5 -b of an existing branch is usage", func(t *testing.T) {
		e := newFakeEnv(t)
		e.fake.addBranch("b5")
		_, err := e.lease(LeaseRequest{Branch: "b5", Create: true})
		_ = wantErr(t, err, "usage", 2)
	})
	t.Run("6 -B creates or resets", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.repo.AddBareRemote(t.Context(), "origin"); err != nil {
			t.Fatal(err)
		}
		first := e.git(e.repo.Dir, "rev-parse", "HEAD")
		e.git(e.repo.Dir, "branch", "b6", first)
		e.git(e.repo.Dir, "commit", "--allow-empty", "-m", "second")
		second := e.git(e.repo.Dir, "rev-parse", "HEAD")
		e.git(e.repo.Dir, "push", "origin", "HEAD:refs/heads/up")
		e.git(e.repo.Dir, "fetch", "origin")
		rec := e.mustLease(LeaseRequest{Branch: "b6", Reset: true, CommitIsh: "origin/up"})
		if rec.Head != second || e.git(e.repo.Dir, "rev-parse", "b6") != second {
			t.Errorf("b6 not reset to origin/up: head %s", rec.Head)
		}
		rec = e.mustLease(LeaseRequest{Branch: "b6new", Reset: true, CommitIsh: first})
		if rec.Head != first {
			t.Errorf("created -B at %s", rec.Head)
		}
		// -B never resets a branch a worktree holds.
		_, err := e.lease(LeaseRequest{Branch: "b6", Reset: true, CommitIsh: first})
		_ = wantErr(t, err, "branch-in-use", 22)
	})
	t.Run("7 --detach", func(t *testing.T) {
		e := newFakeEnv(t)
		rec := e.mustLease(LeaseRequest{Detach: true})
		if rec.Branch != nil || rec.Head != e.fake.heads[fakeMainBranch] {
			t.Errorf("record = %+v", rec)
		}
	})
	t.Run("8 name only", func(t *testing.T) {
		e := newFakeEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "fresh"})
		if rec.Branch == nil || *rec.Branch != "fresh" {
			t.Errorf("created branch = %v", rec.Branch)
		}
		e.fake.addBranch("old")
		rec = e.mustLease(LeaseRequest{Name: "old"})
		if rec.Branch == nil || *rec.Branch != "old" {
			t.Errorf("existing branch = %v", rec.Branch)
		}
		_, err := e.lease(LeaseRequest{Name: "bad name"})
		_ = wantErr(t, err, "usage", 2)
	})
	t.Run("9 nothing", func(t *testing.T) {
		e := newFakeEnv(t)
		_, err := e.lease(LeaseRequest{})
		ee := wantErr(t, err, "usage", 2)
		if !strings.Contains(ee.Hint, "--name") || !strings.Contains(ee.Hint, "-b") || !strings.Contains(ee.Hint, "--detach") {
			t.Errorf("hint = %q", ee.Hint)
		}
	})
	t.Run("10 branch held outside pg-gwm", func(t *testing.T) {
		e := newFakeEnv(t)
		_, err := e.lease(LeaseRequest{CommitIsh: fakeMainBranch})
		_ = wantErr(t, err, "branch-in-use", 22)
		other := filepath.Join(filepath.Dir(e.mainDir), "unmanaged")
		if err := e.fake.Create(t.Context(), other, provision.BranchSpec{Branch: "outside", Create: true}, ""); err != nil {
			t.Fatal(err)
		}
		ee := func() *Error {
			_, err := e.lease(LeaseRequest{CommitIsh: "outside"})
			return wantErr(t, err, "branch-in-use", 22)
		}()
		if got, _ := ee.Details["path"].(string); realPath(got) != realPath(other) {
			t.Errorf("details = %v", ee.Details)
		}
	})
	t.Run("11 branch held by a managed lease", func(t *testing.T) {
		e := newFakeEnv(t)
		first := e.mustLease(LeaseRequest{Name: "holder", Branch: "b11", Create: true})
		_, err := e.lease(LeaseRequest{CommitIsh: "b11"})
		ee := wantErr(t, err, "branch-in-use", 22)
		if ee.Details["lease_id"] != *first.LeaseID || ee.Details["name"] != "holder" {
			t.Errorf("details = %v", ee.Details)
		}
	})
	t.Run("12 branch held by a released worktree", func(t *testing.T) {
		e := newFakeEnv(t)
		first := e.mustLease(LeaseRequest{Name: "old", Branch: "b12", Create: true})
		lk, err := e.m.Store.Lock(t.Context(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		w := &lk.State().Worktrees[0]
		w.Status, w.Lease = state.Released, nil
		if err := lk.Commit(); err != nil {
			t.Fatal(err)
		}
		lk.Unlock()
		rec := e.mustLease(LeaseRequest{Name: "new", CommitIsh: "b12"})
		if rec.ID != first.ID || rec.WorktreePath != first.WorktreePath || rec.Status != state.Leased {
			t.Errorf("not the released worktree: %+v", rec)
		}
		if rec.LeaseID == nil || *rec.LeaseID == *first.LeaseID || rec.Name == nil || *rec.Name != "new" {
			t.Errorf("lease = %v name %v", rec.LeaseID, rec.Name)
		}
		if n := len(e.state().Worktrees); n != 1 {
			t.Errorf("%d worktrees, want 1", n)
		}
	})
	t.Run("13 new branch under pg-gwm/", func(t *testing.T) {
		e := newFakeEnv(t)
		for _, r := range []LeaseRequest{
			{Branch: "pg-gwm/x", Create: true},
			{Branch: "pg-gwm/x", Reset: true},
			{Name: "pg-gwm/x"},
		} {
			_, err := e.lease(r)
			_ = wantErr(t, err, "usage", 2)
		}
	})
}

func TestNoFetch(t *testing.T) {
	e := newEnv(t)
	var calls []string
	run := e.g.Run
	e.g.Run = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		calls = append(calls, strings.Join(c.Args, " "))
		return run(ctx, c)
	}
	e.m = e.manager()
	e.mustLease(LeaseRequest{Name: "ok", Detach: true})
	before := e.state()
	calls = nil
	_, err := e.lease(LeaseRequest{Branch: "nb", Create: true, CommitIsh: "no-such-ref"})
	ee := wantErr(t, err, "ref-unresolvable", 26)
	if ee.Details["reference"] != "no-such-ref" || ee.Details["git"] == "" {
		t.Errorf("details = %v", ee.Details)
	}
	for _, c := range calls {
		if strings.Contains(c, "fetch") || strings.Contains(c, "worktree add") {
			t.Errorf("unexpected git call %q", c)
		}
	}
	if after := e.state(); len(after.Worktrees) != len(before.Worktrees) {
		t.Errorf("state changed")
	}
}

func TestNameRules(t *testing.T) {
	e := newFakeEnv(t)
	e.mustLease(LeaseRequest{Name: "worker", Detach: true})
	e.mustLease(LeaseRequest{Name: "worker/bd-1", Detach: true})
	for _, name := range []string{strings.Repeat("x", 101), "two\nlines"} {
		_, err := e.lease(LeaseRequest{Name: name, Detach: true})
		_ = wantErr(t, err, "usage", 2)
	}
	e.mustLease(LeaseRequest{Name: strings.Repeat("x", 100), Detach: true})
	e.mustLease(LeaseRequest{Name: "Potato", Detach: true})
	_, err := e.lease(LeaseRequest{Name: "potato", Detach: true})
	_ = wantErr(t, err, "name-held", 21)
}

func TestNameBranchConflict(t *testing.T) {
	e := newEnv(t)
	e.git(e.repo.Dir, "branch", "worker/bd-1")
	_, err := e.lease(LeaseRequest{Name: "worker"})
	ee := wantErr(t, err, "usage", 2)
	if msg, _ := ee.Details["git"].(string); !strings.Contains(msg, "refs/heads/worker/bd-1") {
		t.Errorf("details = %v", ee.Details)
	}
}

func TestNameHeldWhilePreparing(t *testing.T) {
	e := newFakeEnv(t)
	bp := newBlocking(e.m.Prov)
	e.m.Prov = bp
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := e.m.Lease(ctx, LeaseRequest{Name: "slow", Detach: true, BaseDir: e.mainDir})
		done <- err
	}()
	<-bp.started
	if got := e.state().Worktrees; len(got) != 1 || got[0].Status != state.Preparing {
		t.Fatalf("state = %+v", got)
	}
	_, err := e.lease(LeaseRequest{Name: "SLOW", Detach: true})
	ee := wantErr(t, err, "name-held", 21)
	if ee.Details["status"] != "preparing" {
		t.Errorf("details = %v", ee.Details)
	}
	cancel()
	_ = wantErr(t, <-done, "interrupted", 130)
}

func TestSameNameRace(t *testing.T) {
	e := newFakeEnv(t)
	ms := []*Manager{e.manager(), e.manager()}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range ms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = ms[i].Lease(t.Context(), LeaseRequest{Name: "potato", Detach: true, BaseDir: e.mainDir})
		}()
	}
	close(start)
	wg.Wait()
	ok, held := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case CodeOf(err) == 21:
			held++
		default:
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 1 || held != 1 {
		t.Fatalf("successes %d, name-held %d, want 1 and 1", ok, held)
	}
}

func TestSlotIndex(t *testing.T) {
	e := newFakeEnv(t)
	var recs []Record
	for _, n := range []string{"a", "b", "c"} {
		recs = append(recs, e.mustLease(LeaseRequest{Name: n, Detach: true}))
	}
	// Free slot 1: remove its worktree and forget it.
	mid := recs[1]
	if err := e.m.Prov.Unlock(t.Context(), mid.WorktreePath); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Prov.Remove(t.Context(), mid.WorktreePath); err != nil {
		t.Fatal(err)
	}
	lk, err := e.m.Store.Lock(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	st := lk.State()
	for i, w := range st.Worktrees {
		if w.ID == mid.ID {
			st.Worktrees = append(st.Worktrees[:i], st.Worktrees[i+1:]...)
			break
		}
	}
	if err := lk.Commit(); err != nil {
		t.Fatal(err)
	}
	lk.Unlock()

	taken := filepath.Join(e.m.Cfg.WorktreeRoot, e.m.Repo.Name+"-wt-1")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := e.mustLease(LeaseRequest{Name: "d", Detach: true})
	if want := filepath.Join(e.m.Cfg.WorktreeRoot, e.m.Repo.Name+"-wt-3"); rec.WorktreePath != want {
		t.Errorf("occupied index 1: path %q, want %q", rec.WorktreePath, want)
	}
	if err := os.Remove(taken); err != nil {
		t.Fatal(err)
	}
	rec = e.mustLease(LeaseRequest{Name: "e", Detach: true})
	if rec.WorktreePath != taken {
		t.Errorf("free index 1: path %q, want %q", rec.WorktreePath, taken)
	}
}

func TestLowDisk(t *testing.T) {
	e := newFakeEnv(t, func(c *config.RepoConfig) { c.FreeSpaceFloor = ptr(uint64(100)) })
	e.m.FreeBytes = func(string) (uint64, error) { return 10, nil }
	_, err := e.lease(LeaseRequest{Name: "n", Detach: true})
	ee := wantErr(t, err, "low-disk", 29)
	if ee.Details["free_bytes"] != uint64(10) || ee.Details["floor"] != uint64(100) {
		t.Errorf("details = %v", ee.Details)
	}
	if n := len(e.state().Worktrees); n != 0 {
		t.Errorf("%d worktrees recorded", n)
	}
	if _, err := os.Stat(e.m.Cfg.WorktreeRoot); err == nil {
		t.Error("worktree-root was created")
	}
	e.m.FreeBytes = func(string) (uint64, error) { return 100, nil }
	e.mustLease(LeaseRequest{Name: "n", Detach: true})
}

func TestFirstLeaseCreatesRoot(t *testing.T) {
	e := newFakeEnv(t)
	if _, err := os.Stat(e.m.Cfg.WorktreeRoot); err == nil {
		t.Fatal("worktree-root exists before the first lease")
	}
	rec := e.mustLease(LeaseRequest{Name: "first", Detach: true})
	if fi, err := os.Stat(e.m.Cfg.WorktreeRoot); err != nil || !fi.IsDir() {
		t.Fatalf("worktree-root not created: %v", err)
	}
	if filepath.Dir(rec.WorktreePath) != e.m.Cfg.WorktreeRoot {
		t.Errorf("path = %q", rec.WorktreePath)
	}
}

func TestCancelMidCreate(t *testing.T) {
	e := newFakeEnv(t)
	bp := newBlocking(e.m.Prov)
	e.m.Prov = bp
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := e.m.Lease(ctx, LeaseRequest{Name: "doomed", Detach: true, BaseDir: e.mainDir})
		done <- err
	}()
	<-bp.started
	cancel()
	err := <-done
	_ = wantErr(t, err, "interrupted", 130)
	got := e.state().Worktrees
	if len(got) != 1 || got[0].Status != state.Broken || got[0].Lease != nil {
		t.Fatalf("state = %+v", got)
	}
	if !strings.Contains(e.events(), `"kind":"broken"`) {
		t.Errorf("no broken event in:\n%s", e.events())
	}
	// The name is free again.
	e.m.Prov = bp.Provisioner
	e.mustLease(LeaseRequest{Name: "doomed", Detach: true})
}
