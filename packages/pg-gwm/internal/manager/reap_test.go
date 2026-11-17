package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
)

func (e *env) mustReap(o ReapOptions) ReapResult {
	e.t.Helper()
	res, err := e.m.Reap(e.t.Context(), o)
	if err != nil {
		e.t.Fatalf("reap: %v", err)
	}
	return res
}

func (e *env) count(s state.Status) int {
	n := 0
	for _, w := range e.state().Worktrees {
		if w.Status == s {
			n++
		}
	}
	return n
}

func (e *env) rawState() string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.m.Store.Dir, "state.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) branchExists(name string) bool {
	e.t.Helper()
	return e.git(e.repo.Dir, "branch", "--list", name) != ""
}

// four leases: three released and one expired, each on its own branch.
func (e *env) threeReleasedOneExpired() []Record {
	e.t.Helper()
	var recs []Record
	for _, n := range []string{"a", "b", "c"} {
		r := e.mustLease(LeaseRequest{Name: n, Branch: "br-" + n, Create: true})
		recs = append(recs, r)
	}
	recs = append(recs, e.mustLease(LeaseRequest{Name: "x", Branch: "br-x", Create: true, TTL: ptr(dur(e.t, "1h"))}))
	for _, r := range recs[:3] {
		if _, err := e.m.Release(e.t.Context(), LeaseKey{LeaseID: str(r.LeaseID)}); err != nil {
			e.t.Fatal(err)
		}
	}
	e.now = e.now.Add(2 * time.Hour)
	return recs
}

func TestReapMinTwo(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(2) })
	e.threeReleasedOneExpired()
	before := e.rawState()

	dry := e.mustReap(ReapOptions{DryRun: true})
	if len(dry.Kept) != 2 || len(dry.Removed) != 2 || dry.Outcome != ReapCompleted {
		t.Errorf("dry run = %+v", dry)
	}
	if e.rawState() != before {
		t.Error("the dry run changed the state")
	}
	if strings.Contains(e.events(), `"kind":"reap"`) {
		t.Error("the dry run wrote a reap event")
	}

	res := e.mustReap(ReapOptions{})
	if len(res.Kept) != 2 || len(res.Removed) != 2 || len(res.Created) != 0 {
		t.Fatalf("reap = %+v", res)
	}
	if e.count(state.Available) != 2 || len(e.state().Worktrees) != 2 {
		t.Errorf("state = %+v", e.state().Worktrees)
	}
	for _, b := range []string{"br-a", "br-b", "br-c", "br-x"} {
		if !e.branchExists(b) {
			t.Errorf("branch %s was deleted", b)
		}
	}
	if !strings.Contains(e.events(), `"kind":"reap"`) {
		t.Error("no reap event")
	}
	rec, err := state.ReadReaperRecord(state.ReaperPath())
	if err != nil || rec.Repos[e.m.Repo.Label].Removed != 2 || rec.Repos[e.m.Repo.Label].Kept != 2 {
		t.Errorf("reaper.json = %+v, %v", rec, err)
	}
	if !rec.Started.IsZero() || rec.LastSuccess != nil {
		t.Errorf("a single-repository reap wrote the run fields: %+v", rec)
	}
	snap, _ := state.Counters{Path: state.CountersPath()}.Read()
	if c := snap.Repos[e.m.Repo.Label]; c == nil || c.ReapRemoved != 2 || c.ReapKept != 2 {
		t.Errorf("counters = %+v", c)
	}
}

func TestReapReuseOff(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Min = ptr(1) })
	e.threeReleasedOneExpired()
	res := e.mustReap(ReapOptions{})
	if len(res.Removed) != 4 || len(res.Kept) != 0 || len(res.Created) != 1 {
		t.Fatalf("reap = %+v", res)
	}
	// Every reclaimed worktree is removed; only the fresh standby remains.
	if e.count(state.Available) != 1 || len(e.state().Worktrees) != 1 {
		t.Errorf("state = %+v", e.state().Worktrees)
	}
}

func TestReapShrinkAboveMax(t *testing.T) {
	e := newEnv(t)
	var recs []Record
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		recs = append(recs, e.mustLease(LeaseRequest{Name: n, Detach: true}))
	}
	for _, r := range recs {
		e.makeAvailable(r.ID)
	}
	e.m = e.manager(func(c *config.RepoConfig) { c.Max = ptr(3) })
	res := e.mustReap(ReapOptions{})
	if len(res.Removed) != 2 || e.count(state.Available) != 3 {
		t.Errorf("reap = %+v, state = %+v", res, e.state().Worktrees)
	}
}

// serialProv fails the test if two creations overlap.
type serialProv struct {
	provision.Provisioner
	t       *testing.T
	inside  atomic.Int32
	created atomic.Int32
}

func (s *serialProv) Create(ctx context.Context, dir string, b provision.BranchSpec, reason string) error {
	if s.inside.Add(1) != 1 {
		s.t.Error("two creations ran at once")
	}
	defer s.inside.Add(-1)
	s.created.Add(1)
	return s.Provisioner.Create(ctx, dir, b, reason)
}

func TestReapCreatesStandbys(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Min = ptr(2) })
	sp := &serialProv{Provisioner: e.m.Prov, t: t}
	e.m.Prov = sp
	res := e.mustReap(ReapOptions{})
	if len(res.Created) != 2 || sp.created.Load() != 2 || e.count(state.Available) != 2 {
		t.Fatalf("reap = %+v", res)
	}
	for _, w := range e.state().Worktrees {
		if !exists(w.Path) || w.Lease != nil {
			t.Errorf("standby %+v", w)
		}
	}

	t.Run("the floor stops creation", func(t *testing.T) {
		e := newEnv(t, func(c *config.RepoConfig) { c.Min, c.FreeSpaceFloor = ptr(2), ptr(uint64(1)<<62) })
		res := e.mustReap(ReapOptions{})
		if len(res.Created) != 0 || len(res.Warnings) == 0 {
			t.Errorf("reap = %+v", res)
		}
		if !strings.Contains(e.events(), `"kind":"low-disk"`) {
			t.Error("no low-disk event")
		}
	})
}

// failRemove makes Remove fail while on.
type failRemove struct {
	provision.Provisioner
	on atomic.Bool
}

func (f *failRemove) Remove(ctx context.Context, dir string) error {
	if f.on.Load() {
		return errors.New("remove refused")
	}
	return f.Provisioner.Remove(ctx, dir)
}

func TestReapBroken(t *testing.T) {
	e := newEnv(t)
	fr := &failRemove{Provisioner: e.m.Prov}
	e.m.Prov = fr
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	// A command died while preparing: the record says preparing, nothing holds it.
	e.seed(func(st *state.State) { st.Worktrees[0].Status = state.Preparing })

	fr.on.Store(true)
	res := e.mustReap(ReapOptions{})
	w, ok := e.worktreeOf(rec.ID)
	if !ok || w.Status != state.Broken || !exists(w.Path) || len(res.Broken) != 1 || len(res.Removed) != 0 {
		t.Fatalf("failed retry: %+v present=%v res=%+v", w, ok, res)
	}
	if !strings.Contains(e.events(), "remove refused") {
		t.Error("the failed retry left no event")
	}

	fr.on.Store(false)
	res = e.mustReap(ReapOptions{})
	if _, ok := e.worktreeOf(rec.ID); ok || exists(w.Path) || len(res.Removed) != 1 {
		t.Errorf("second reap: res=%+v present=%v", res, ok)
	}
}

// TestRenewRacesReap keeps the unit run short: each iteration spawns real
// worktree git, so a few iterations of the renew-versus-reap race stay in the
// unit gate and the 50-iteration soak (TestRenewRacesReapSoak) sits behind the
// integration tag.
func TestRenewRacesReap(t *testing.T) { renewRacesReap(t, renewRacesReapUnitIterations) }

const renewRacesReapUnitIterations = 2

func renewRacesReap(t *testing.T, iterations int) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) })
	for i := 0; i < iterations; i++ {
		rec := e.mustLease(LeaseRequest{Name: "n", Detach: true, TTL: ptr(dur(t, "1h"))})
		e.now = e.now.Add(2 * time.Hour) // the lease is now expired
		renewer := e.manager(func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) })
		var wg sync.WaitGroup
		var renewErr, reapErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, renewErr = renewer.Renew(context.Background(), LeaseKey{LeaseID: str(rec.LeaseID)}, nil, nil)
		}()
		go func() {
			defer wg.Done()
			_, reapErr = e.m.Reap(context.Background(), ReapOptions{})
		}()
		wg.Wait()
		if reapErr != nil {
			t.Fatalf("iteration %d: reap: %v", i, reapErr)
		}
		w, _ := e.worktreeOf(rec.ID)
		if renewErr == nil {
			if w.Status != state.Leased || w.Lease == nil || w.Lease.ID != str(rec.LeaseID) {
				t.Fatalf("iteration %d: renewed but the worktree is %+v", i, w)
			}
			if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(rec.LeaseID)}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		wantErr(t, renewErr, "lease-lost", 25)
		if w.Lease != nil || w.Status != state.Available {
			t.Fatalf("iteration %d: lease lost but the worktree is %+v", i, w)
		}
	}
}

func TestReapMissingDir(t *testing.T) {
	t.Run("a deleted worktree directory", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "a", Branch: "br-a", Create: true})
		if err := os.RemoveAll(rec.WorktreePath); err != nil {
			t.Fatal(err)
		}
		res := e.mustReap(ReapOptions{})
		if _, ok := e.worktreeOf(rec.ID); ok || len(res.Removed) != 1 {
			t.Errorf("record kept or not reported: %+v", res)
		}
		if got := e.git(e.repo.Dir, "worktree", "list", "--porcelain"); strings.Contains(got, rec.WorktreePath) {
			t.Errorf("git still registers it:\n%s", got)
		}
		if !e.branchExists("br-a") {
			t.Error("the branch was deleted")
		}
	})

	t.Run("a missing worktree-root", func(t *testing.T) {
		e := newEnv(t)
		rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
		before := e.rawState()
		if err := os.RemoveAll(e.m.Cfg.WorktreeRoot); err != nil {
			t.Fatal(err)
		}
		res := e.mustReap(ReapOptions{})
		if res.Outcome != ReapUnreachable || len(res.Warnings) != 1 {
			t.Errorf("reap = %+v", res)
		}
		if e.rawState() != before {
			t.Errorf("the state changed for %s", rec.ID)
		}
	})
}

// recordingRunner logs every program a call starts.
type recordingRunner struct {
	mu    sync.Mutex
	names []string
	next  cmdrun.Runner
}

func (r *recordingRunner) run(ctx context.Context, c cmdrun.Call) cmdrun.Result {
	r.mu.Lock()
	r.names = append(r.names, c.Name)
	r.mu.Unlock()
	return r.next(ctx, c)
}

func TestReapNoProcessProbe(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) })
	rr := &recordingRunner{next: cmdrun.Exec}
	g := e.g
	g.Run = rr.run
	e.g = g
	e.m = e.manager(func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) })
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(rec.LeaseID)}); err != nil {
		t.Fatal(err)
	}
	if res := e.mustReap(ReapOptions{}); len(res.Kept) != 1 {
		t.Fatalf("reap = %+v", res)
	}
	if len(rr.names) == 0 {
		t.Fatal("the recorder saw no call")
	}
	for _, n := range rr.names {
		if n != "git" {
			t.Errorf("a reclaim started %q", n)
		}
	}
}

func TestReapSingleFlight(t *testing.T) {
	e := newEnv(t)
	rel, err := e.m.Store.HoldReap()
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	res := e.mustReap(ReapOptions{})
	if res.Outcome != ReapSkipped {
		t.Errorf("outcome = %s", res.Outcome)
	}
	if strings.Contains(e.events(), `"kind":"reap"`) {
		t.Error("a skipped reap wrote an event")
	}
	if rec, _ := state.ReadReaperRecord(state.ReaperPath()); len(rec.Repos) != 0 {
		t.Errorf("a skipped reap wrote an entry: %+v", rec)
	}
}

func TestReapBusyRepoLock(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.LockWait = dur(t, "1s") })
	lk, err := e.m.Store.Lock(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	res, err := e.m.Reap(t.Context(), ReapOptions{})
	wantErr(t, err, "busy", 9)
	if res.Outcome != ReapSkipped {
		t.Errorf("outcome = %s", res.Outcome)
	}
}

func TestReapDeadline(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Min = ptr(2) })
	res := e.mustReap(ReapOptions{Deadline: e.now.Add(-time.Second)})
	if res.Outcome != ReapDeadline || len(res.Created) != 0 {
		t.Errorf("reap = %+v", res)
	}
}

// reapAllEnv wires a second repository next to e's and the manager factory
// ReapAll needs.
func reapAllEnv(t *testing.T) (e *env, second *gitfixture.Repo, f config.File, mk func(repo.Repo) (*Manager, error)) {
	t.Helper()
	e = newEnv(t)
	second = newBaseRepo(t)
	f = config.File{Repos: map[string]config.RepoConfig{e.repo.Dir: {}}}
	mk = func(rp repo.Repo) (*Manager, error) {
		cfg := f.For(rp)
		return &Manager{
			Repo: rp, Cfg: cfg, Store: state.Open(rp.CommonDir),
			Prov: provision.NewWorktree(rp.Main, e.g, time.Minute, time.Minute), Git: e.g,
			Registry: state.Registry{Path: state.RegistryPath()},
			Counters: state.Counters{Path: state.CountersPath()},
			Now:      func() time.Time { return e.now },
		}, nil
	}
	return
}

func TestReapAll(t *testing.T) {
	e, second, f, mk := reapAllEnv(t)
	rp2, err := repo.Resolve(t.Context(), gitruntest.New(t, second), second.Dir)
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := mk(rp2)
	// One released worktree in each repository; the second is registered by
	// its lease, the first is configured.
	for _, m := range []*Manager{e.m, m2} {
		rec, err := m.Lease(t.Context(), LeaseRequest{Name: "a", Detach: true, BaseDir: m.Repo.Main})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Release(t.Context(), LeaseKey{LeaseID: str(rec.LeaseID)}); err != nil {
			t.Fatal(err)
		}
	}
	reg := state.Registry{Path: state.RegistryPath()}
	gone := filepath.Join(t.TempDir(), "vanished")
	if err := reg.Add(gone); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	reapAllNow = func() time.Time { return clock }
	t.Cleanup(func() { reapAllNow = time.Now })

	results := ReapAll(t.Context(), f, reg, mk, ReapOptions{})
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	outcomes := map[string]string{}
	for _, r := range results {
		outcomes[filepath.Base(r.Repo)] = r.Outcome
	}
	if outcomes["vanished"] != ReapUnreachable || outcomes[filepath.Base(e.m.Repo.Main)] != ReapCompleted || outcomes[filepath.Base(m2.Repo.Main)] != ReapCompleted {
		t.Errorf("outcomes = %v", outcomes)
	}
	if len(e.state().Worktrees) != 0 {
		t.Errorf("the configured repository kept %+v", e.state().Worktrees)
	}
	rec, err := state.ReadReaperRecord(state.ReaperPath())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != ReapUnreachable || len(rec.Repos) != 3 || rec.LastSuccess == nil || !rec.Started.Equal(clock) {
		t.Errorf("reaper.json = %+v", rec)
	}
	if _, ok := rec.Repos[e.m.Repo.Label]; !ok {
		t.Errorf("no entry for %s in %+v", e.m.Repo.Label, rec.Repos)
	}
	if _, ok := rec.Repos[repo.PathLabel(gone)]; !ok {
		t.Errorf("no path-labelled entry for the vanished repository: %+v", rec.Repos)
	}
	entries, _ := reg.List()
	var missing bool
	for _, en := range entries {
		missing = missing || (en.Main == gone && en.MissingSince != nil)
	}
	if !missing {
		t.Errorf("the vanished path was not marked missing: %+v", entries)
	}

	clock = clock.Add(8 * 24 * time.Hour)
	ReapAll(t.Context(), f, reg, mk, ReapOptions{})
	entries, _ = reg.List()
	for _, en := range entries {
		if en.Main == gone {
			t.Errorf("the vanished path survived retention: %+v", entries)
		}
	}
}

func TestReapAllNothingAndFailures(t *testing.T) {
	newEnv(t) // only for the XDG settings
	reg := state.Registry{Path: state.RegistryPath()}
	mk := func(repo.Repo) (*Manager, error) { return nil, errors.New("no manager") }
	// The persisted RFC 3339 stamps have second resolution, so the clock is
	// injected and advanced instead of sleeping.
	clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	reapAllNow = func() time.Time { return clock }
	t.Cleanup(func() { reapAllNow = time.Now })

	if res := ReapAll(t.Context(), config.File{}, reg, mk, ReapOptions{}); res == nil || len(res) != 0 {
		t.Fatalf("empty run = %#v", res)
	}
	rec, _ := state.ReadReaperRecord(state.ReaperPath())
	if rec.Outcome != ReapCompleted || rec.LastSuccess == nil {
		t.Fatalf("an empty run must be a success: %+v", rec)
	}
	first := *rec.LastSuccess

	e2 := newBaseRepo(t)
	f := config.File{Repos: map[string]config.RepoConfig{e2.Dir: {}}}
	clock = clock.Add(2 * time.Second)
	res := ReapAll(t.Context(), f, reg, mk, ReapOptions{})
	if len(res) != 1 || res[0].Outcome != ReapError {
		t.Fatalf("results = %+v", res)
	}
	rec, _ = state.ReadReaperRecord(state.ReaperPath())
	if rec.Outcome != ReapError || rec.LastSuccess == nil || !rec.LastSuccess.Equal(first) {
		t.Errorf("a failed run must carry the last success forward: %+v (first %v)", rec, first)
	}
	if _, ok := rec.Repos[repo.PathLabel(realPath(e2.Dir))]; !ok {
		t.Errorf("entries = %+v", rec.Repos)
	}
}

// A broken worktree git no longer knows (no .git link, no admin directory)
// has nothing git can preserve: one reap removes its directory and record.
func TestReapBrokenUnregisteredWorktreeIsRemoved(t *testing.T) {
	e := newEnv(t)
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	w, _ := e.worktreeOf(rec.ID)
	e.git(e.repo.Dir, "worktree", "unlock", w.Path)
	if err := os.WriteFile(filepath.Join(w.Path, "leftover.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(e.repo.Dir, ".git", "worktrees", filepath.Base(w.Path))); err != nil {
		t.Fatal(err)
	}
	e.seed(func(st *state.State) { st.Worktrees[0].Status = state.Broken })

	res := e.mustReap(ReapOptions{})
	if _, ok := e.worktreeOf(rec.ID); ok || exists(w.Path) || len(res.Removed) != 1 || e.count(state.Broken) != 0 {
		t.Fatalf("not removed: present=%v dir=%v res=%+v", ok, exists(w.Path), res)
	}
	if !strings.Contains(e.events(), `"kind":"remove"`) {
		t.Errorf("no remove event: %s", e.events())
	}
}

// A worktree reap created and no lease ever wrote into has no work to
// protect, so a tree left half-deleted by an interrupted removal is not
// preserved as a branch of deletions.
func TestReapBrokenNeverLeasedIsNotPreserved(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) })
	e.commitFile("tracked.txt", "one\n")
	e.mustReap(ReapOptions{})
	if len(e.state().Worktrees) != 1 {
		t.Fatalf("standby not created: %+v", e.state().Worktrees)
	}
	w := e.state().Worktrees[0]
	if err := os.Remove(filepath.Join(w.Path, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	e.seed(func(st *state.State) { st.Worktrees[0].Status = state.Broken })

	e.mustReap(ReapOptions{})
	if got := e.git(e.repo.Dir, "branch", "--list", "pg-gwm/dirty-reaped-*"); got != "" {
		t.Errorf("a preserved branch was written: %q", got)
	}
	if len(e.state().Preserved) != 0 {
		t.Errorf("preserved recorded: %+v", e.state().Preserved)
	}
}

// A worktree a lease has held keeps its protection after it breaks.
func TestReapBrokenLeasedStillPreserved(t *testing.T) {
	e := newEnv(t)
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	w, _ := e.worktreeOf(rec.ID)
	e.write(w.Path, "work.txt", "mine\n")
	e.seed(func(st *state.State) { st.Worktrees[0].Status, st.Worktrees[0].Lease = state.Broken, nil })
	e.mustReap(ReapOptions{})
	if got := e.git(e.repo.Dir, "branch", "--list", "pg-gwm/dirty-reaped-*"); got == "" {
		t.Error("a once-leased worktree's work was not preserved")
	}
}

// timeoutStatus makes every git status call of e time out, as a loaded host
// does, and returns the number of such calls.
func (e *env) timeoutStatus(tune ...func(*config.RepoConfig)) *atomic.Int32 {
	var n atomic.Int32
	run := e.g.Run
	e.g.Run = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		for _, a := range c.Args {
			if a == "status" {
				n.Add(1)
				return cmdrun.Result{Code: -1, Signal: "terminated", TimedOut: true, CtxErr: context.DeadlineExceeded}
			}
		}
		return run(ctx, c)
	}
	e.m = e.manager(tune...)
	return &n
}

// A git status that times out under host load shows only that git was slow:
// a once-leased available worktree stays available, no broken event is
// written, and the reap says it will check again.
func TestReapStatusTimeoutKeepsAvailableWorktree(t *testing.T) {
	tune := func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) }
	e := newEnv(t, tune)
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(rec.LeaseID)}); err != nil {
		t.Fatal(err)
	}
	e.mustReap(ReapOptions{})
	if e.count(state.Available) != 1 {
		t.Fatalf("setup: %+v", e.state().Worktrees)
	}
	calls := e.timeoutStatus(tune)

	res := e.mustReap(ReapOptions{})
	if calls.Load() == 0 {
		t.Fatal("the unsaved-work check never ran")
	}
	if e.count(state.Available) != 1 || e.count(state.Broken) != 0 || len(res.Broken) != 0 {
		t.Fatalf("worktree not kept available: %+v res=%+v", e.state().Worktrees, res)
	}
	if strings.Contains(e.events(), `"kind":"broken"`) {
		t.Errorf("a broken event was written: %s", e.events())
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "later") || res.Outcome != ReapCompleted {
		t.Errorf("res = %+v", res)
	}
}

// A worktree no lease ever took has no unsaved work to protect, so the reap
// does not run the unsaved-work check on it.
func TestReapNeverLeasedAvailableSkipsUnsavedCheck(t *testing.T) {
	tune := func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) }
	e := newEnv(t, tune)
	e.mustReap(ReapOptions{})
	if e.count(state.Available) != 1 || !e.state().Worktrees[0].NeverLeased {
		t.Fatalf("setup: %+v", e.state().Worktrees)
	}
	calls := e.timeoutStatus(tune)
	e.mustReap(ReapOptions{})
	if calls.Load() != 0 {
		t.Errorf("git status ran %d times for a never-leased worktree", calls.Load())
	}
	if e.count(state.Available) != 1 {
		t.Errorf("worktrees = %+v", e.state().Worktrees)
	}
}

// A record an older build wrote lacks never_leased: the event log, which
// reaches back to its creation and holds no lease event for it, shows it was
// never leased. A lease event for it, or an event log that starts after its
// creation, keeps it protected.
func TestReapLegacyRecordNeverLeasedFromEventLog(t *testing.T) {
	tune := func(c *config.RepoConfig) { c.Reuse, c.Min = true, ptr(1) }
	legacy := func(e *env) {
		// The store stamps events with the wall clock, not the Manager clock.
		e.seed(func(st *state.State) {
			st.Worktrees[0].NeverLeased, st.Worktrees[0].CreatedAt = false, time.Now().UTC()
		})
	}
	t.Run("no lease event", func(t *testing.T) {
		e := newEnv(t, tune)
		e.mustReap(ReapOptions{})
		legacy(e)
		calls := e.timeoutStatus(tune)
		e.mustReap(ReapOptions{})
		if calls.Load() != 0 {
			t.Errorf("git status ran %d times", calls.Load())
		}
	})
	t.Run("lease event", func(t *testing.T) {
		e := newEnv(t, tune)
		rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(rec.LeaseID)}); err != nil {
			t.Fatal(err)
		}
		e.mustReap(ReapOptions{})
		legacy(e)
		calls := e.timeoutStatus(tune)
		e.mustReap(ReapOptions{})
		if calls.Load() == 0 {
			t.Error("a once-leased worktree skipped the unsaved-work check")
		}
	})
	t.Run("log starts after creation", func(t *testing.T) {
		e := newEnv(t, tune)
		e.mustReap(ReapOptions{})
		legacy(e)
		e.seed(func(st *state.State) { st.Worktrees[0].CreatedAt = time.Now().UTC().AddDate(0, 0, -3) })
		calls := e.timeoutStatus(tune)
		e.mustReap(ReapOptions{})
		if calls.Load() == 0 {
			t.Error("a worktree older than the event log skipped the unsaved-work check")
		}
	})
}

// A git status that times out in the broken retry leaves the record broken
// with its directory, to be retried; the next reap with a healthy git removes it.
func TestReapBrokenRetryStatusTimeoutStaysRetryable(t *testing.T) {
	e := newEnv(t)
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	w, _ := e.worktreeOf(rec.ID)
	e.write(w.Path, "work.txt", "mine\n")
	e.seed(func(st *state.State) { st.Worktrees[0].Status, st.Worktrees[0].Lease = state.Broken, nil })
	before := e.state().Worktrees[0]
	e.timeoutStatus()

	res := e.mustReap(ReapOptions{})
	got, ok := e.worktreeOf(rec.ID)
	if !ok || got.Status != state.Broken || !exists(w.Path) || got.Reason != before.Reason {
		t.Fatalf("record changed: ok=%v %+v", ok, got)
	}
	if len(res.Warnings) == 0 || len(res.Removed) != 0 {
		t.Errorf("res = %+v", res)
	}

	e.g = gitruntest.New(t, e.repo)
	e.m = e.manager()
	e.mustReap(ReapOptions{})
	if _, ok := e.worktreeOf(rec.ID); ok || exists(w.Path) {
		t.Errorf("not removed by the next reap")
	}
}
