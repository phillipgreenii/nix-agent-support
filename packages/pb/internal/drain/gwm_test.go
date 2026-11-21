package drain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// These tests drive the REAL pg-gwm binary against a throwaway git repository
// and a temp configuration whose worktree-root lies under t.TempDir(). They are
// single-process and fast (a unit test). They skip, with a message, when the
// pg-gwm binary is not on PATH. Run: nice -n 10 go test -p 4 ./internal/drain
// with pg-gwm installed (it is part of the per-user profile).

// recRunner records every command, and can rewrite arguments (to shorten the
// 10 minute --wait) and intercept bd. It delegates the rest to the real CLI.
type recRunner struct {
	mu      sync.Mutex
	calls   []string
	rewrite func(name string, args []string) []string
}

func (r *recRunner) Run(ctx context.Context, name string, args []string, o run.Options) (run.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	r.mu.Unlock()
	if name == "bd" {
		return run.Result{}, nil
	}
	if r.rewrite != nil {
		args = r.rewrite(name, args)
	}
	return run.CLIRunner{}.Run(ctx, name, args, o)
}

// forbidden returns the recorded calls pb must not make on the pg-gwm path.
func (r *recRunner) forbidden() []string {
	var bad []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "wtdone") || strings.Contains(c, "worktree remove") || strings.Contains(c, "worktree prune") {
			bad = append(bad, c)
		}
	}
	return bad
}

// gwmFixture returns a pg-gwm-managed repo (max slots) and its worktree root.
func gwmFixture(t *testing.T, max int) (repo, root string) {
	t.Helper()
	if _, err := exec.LookPath("pg-gwm"); err != nil {
		t.Skip("pg-gwm binary not on PATH: skipping the real-pg-gwm tests (install pg-gwm to run them)")
	}
	repo = newRepo(t)
	// pg-gwm's reaper commits unsaved work, so the fixture repo needs an identity.
	gitTest(t, repo, "config", "user.name", "pb test")
	gitTest(t, repo, "config", "user.email", "pb-test@example.invalid")
	root = filepath.Join(t.TempDir(), "wt")
	cfg := t.TempDir()
	conf := fmt.Sprintf("[repos.%q]\nworktree-root = %q\nmin = 0\nmax = %d\n", repo, root, max)
	if err := os.MkdirAll(filepath.Join(cfg, "pg-gwm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "pg-gwm", "config.toml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return repo, root
}

func gwmList(t *testing.T, repo string) []map[string]any {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "pg-gwm", "list", "--repo", repo, "--format=json").Output()
	if err != nil {
		t.Fatalf("pg-gwm list: %v", err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(out, &recs); err != nil {
		t.Fatalf("pg-gwm list json: %v: %s", err, out)
	}
	var live []map[string]any
	for _, r := range recs {
		if s, _ := r["status"].(string); s == "leased" || s == "lease-expired" {
			live = append(live, r)
		}
	}
	return live
}

func gwmBranchPresent(t *testing.T, repo, b string) bool {
	t.Helper()
	return strings.TrimSpace(gitTest(t, repo, "branch", "--list", b)) != ""
}

func isolateGwm1(t *testing.T, rr run.Runner, repo, bead string) Result {
	t.Helper()
	out, err := Isolate(context.Background(), rr, Params{RepoPath: repo, BeadID: bead, Actor: "sess-1"})
	if err != nil {
		t.Fatalf("Isolate(%s): %v", bead, err)
	}
	return out
}

// Acceptance 1 and 4 (ff-merge): lease, --or-renew on redispatch, release after
// clean work leaves no lease and no wtdone/git worktree remove call.
func TestGwm_leaseRenewReleaseAfterFFMerge(t *testing.T) {
	repo, root := gwmFixture(t, 3)
	rr := &recRunner{}
	first := isolateGwm1(t, rr, repo, "pg2-g1")
	if first.Isolation != IsolationGwm || first.LeaseName != "drain/pg2-g1" || first.LeaseID == "" || first.Branch != "drain/pg2-g1" {
		t.Fatalf("first = %+v", first)
	}
	if !strings.HasPrefix(first.Worktree, root+string(filepath.Separator)) && !strings.HasPrefix(first.Worktree, mustReal(t, root)+string(filepath.Separator)) {
		t.Errorf("worktree %s is not under the configured root %s", first.Worktree, root)
	}
	if first.Reused != "none" {
		t.Errorf("first Reused = %q, want none", first.Reused)
	}
	again := isolateGwm1(t, rr, repo, "pg2-g1")
	if again.LeaseID != first.LeaseID || again.Worktree != first.Worktree || again.Reused != "worktree" {
		t.Errorf("redispatch = %+v, want the same lease renewed (first %+v)", again, first)
	}
	if n := len(gwmList(t, repo)); n != 1 {
		t.Fatalf("%d live leases, want 1", n)
	}
	rel, err := Release(context.Background(), rr, ReleaseParams{RepoPath: repo, BeadID: "pg2-g1", Landing: LandedFFMerge})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if rel.Outcome != "released" {
		t.Errorf("release = %+v", rel)
	}
	if n := len(gwmList(t, repo)); n != 0 {
		t.Errorf("%d live leases after an ff-merge landing, want 0", n)
	}
	if !gwmBranchPresent(t, repo, "drain/pg2-g1") {
		t.Error("branch deleted: pb must leave it (the released slot still has it checked out)")
	}
	if bad := rr.forbidden(); len(bad) > 0 {
		t.Errorf("pg-gwm path ran forbidden commands: %v", bad)
	}
}

// Acceptance 4 (pull-request): the recorded decision is release-with-branch-kept.
func TestGwm_pullRequestLandingReleasesAndKeepsBranch(t *testing.T) {
	repo, _ := gwmFixture(t, 3)
	rr := &recRunner{}
	first := isolateGwm1(t, rr, repo, "pg2-g2")
	if err := os.WriteFile(filepath.Join(first.Worktree, "work.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "-C", first.Worktree, "add", "work.txt")
	gitTest(t, repo, "-C", first.Worktree, "commit", "-m", "work")
	rel, err := Release(context.Background(), rr, ReleaseParams{RepoPath: repo, BeadID: "pg2-g2", Landing: LandedPullRequest})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if rel.Outcome != "released" {
		t.Errorf("release = %+v, want released with the branch kept", rel)
	}
	if n := len(gwmList(t, repo)); n != 0 {
		t.Errorf("%d live leases after a pull-request landing, want 0 (lease released)", n)
	}
	if !gwmBranchPresent(t, repo, "drain/pg2-g2") {
		t.Fatal("branch deleted after a pull-request landing")
	}
	// A review-fix session re-leases the kept branch.
	again := isolateGwm1(t, rr, repo, "pg2-g2")
	if again.Reused != "branch" {
		t.Errorf("re-isolate Reused = %q, want branch", again.Reused)
	}
	if _, err := os.Stat(filepath.Join(again.Worktree, "work.txt")); err != nil {
		t.Errorf("kept branch's work missing after re-lease: %v", err)
	}
	if bad := rr.forbidden(); len(bad) > 0 {
		t.Errorf("forbidden commands: %v", bad)
	}
}

// Acceptance 1 and 2: unsaved work is expired (never released) and a reap
// yields a pg-gwm/dirty-reaped-* branch; wtdone and git worktree remove are
// never run.
func TestGwm_reclaimDirtyExpiresAndReapPreserves(t *testing.T) {
	repo, _ := gwmFixture(t, 3)
	rr := &recRunner{}
	first := isolateGwm1(t, rr, repo, "pg2-g3")
	if err := os.WriteFile(filepath.Join(first.Worktree, "unsaved.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Reclaim(context.Background(), rr, ReclaimParams{RepoPath: repo, BeadID: "pg2-g3", Actor: "sess-1"})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if out.Outcome != OutcomeKeptDirty || out.Isolation != IsolationGwm || out.Lease != "expired" {
		t.Fatalf("reclaim = %+v", out)
	}
	for _, c := range rr.calls {
		if strings.HasPrefix(c, "pg-gwm release") {
			t.Errorf("release called on a dirty worktree: %s", c)
		}
	}
	if o, err := exec.CommandContext(t.Context(), "pg-gwm", "reap", "--repo", repo, "--format=json").CombinedOutput(); err != nil {
		t.Fatalf("pg-gwm reap: %v: %s", err, o)
	}
	if got := gitTest(t, repo, "branch", "--list", "pg-gwm/dirty-reaped-*"); strings.TrimSpace(got) == "" {
		t.Errorf("no pg-gwm/dirty-reaped-* branch after the reap; branches:\n%s", gitTest(t, repo, "branch"))
	}
	if bad := rr.forbidden(); len(bad) > 0 {
		t.Errorf("forbidden commands: %v", bad)
	}
}

// A clean zero-commit worktree is released and noted; nothing else runs.
func TestGwm_reclaimCleanReleasesWithoutWtdone(t *testing.T) {
	repo, _ := gwmFixture(t, 3)
	rr := &recRunner{}
	isolateGwm1(t, rr, repo, "pg2-g4")
	out, err := Reclaim(context.Background(), rr, ReclaimParams{RepoPath: repo, BeadID: "pg2-g4", Actor: "sess-1"})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if out.Outcome != OutcomeRemoved || out.Lease != "released" || out.Isolation != IsolationGwm {
		t.Fatalf("reclaim = %+v", out)
	}
	if n := len(gwmList(t, repo)); n != 0 {
		t.Errorf("%d live leases, want 0", n)
	}
	if bad := rr.forbidden(); len(bad) > 0 {
		t.Errorf("forbidden commands: %v", bad)
	}
	noted := false
	for _, c := range rr.calls {
		noted = noted || strings.HasPrefix(c, "bd comment pg2-g4")
	}
	if !noted {
		t.Error("bead not noted")
	}
}

// max-reached: one retry with --wait, then ErrGwmMaxReached (the wait is
// shortened here so the test does not sit out 10 minutes).
func TestGwm_maxReachedRetriesOnceThenTransient(t *testing.T) {
	repo, _ := gwmFixture(t, 1)
	isolateGwm1(t, &recRunner{}, repo, "pg2-g5")
	rr := &recRunner{rewrite: func(name string, args []string) []string {
		out := append([]string{}, args...)
		for i := range out {
			if out[i] == "10m" {
				out[i] = "1s"
			}
		}
		return out
	}}
	_, err := Isolate(context.Background(), rr, Params{RepoPath: repo, BeadID: "pg2-g6", Actor: "sess-1"})
	if !errors.Is(err, ErrGwmMaxReached) {
		t.Fatalf("err = %v, want ErrGwmMaxReached", err)
	}
	waits := 0
	for _, c := range rr.calls {
		if strings.HasPrefix(c, "pg-gwm lease") {
			if strings.Contains(c, "--wait 10m") {
				waits++
			}
		}
	}
	if waits != 1 {
		t.Errorf("%d lease calls with --wait, want exactly 1 retry", waits)
	}
}

// Acceptance 3: a repo with no pg-gwm config keeps the legacy behavior, and a
// configured repo can still be forced onto it.

func TestGwm_forcedLegacyOnConfiguredRepo(t *testing.T) {
	repo, _ := gwmFixture(t, 3)
	out, err := Isolate(context.Background(), &recRunner{}, Params{RepoPath: repo, BeadID: "pg2-l2", Isolation: IsolationLegacy})
	if err != nil {
		t.Fatal(err)
	}
	if out.Isolation != IsolationLegacy || out.Worktree != filepath.Join(repo, ".worktrees", "pg2-l2") {
		t.Errorf("out = %+v", out)
	}
	if n := len(gwmList(t, repo)); n != 0 {
		t.Errorf("legacy isolation took %d leases", n)
	}
}

// A repo with no pg-gwm config keeps the legacy worktree (acceptance 3), and
// that legacy worktree reclaimed with pg-gwm present is still removed by wtdone.
func TestGwm_reclaimOfLegacyWorktreeIsUnchanged(t *testing.T) {
	repo := newRepo(t) // TestMain points XDG_* at an empty config
	rr := &recRunner{}
	iso, err := Isolate(context.Background(), rr, Params{RepoPath: repo, BeadID: "pg2-l3", Actor: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if iso.Isolation != IsolationLegacy || iso.Worktree != filepath.Join(repo, ".worktrees", "pg2-l3") || iso.LeaseID != "" {
		t.Errorf("out = %+v, want legacy .worktrees/<bead>", iso)
	}
	out, _ := Reclaim(context.Background(), rr, ReclaimParams{RepoPath: repo, BeadID: "pg2-l3", Actor: "a"})
	if out.Isolation != IsolationLegacy {
		t.Errorf("reclaim = %+v, want legacy", out)
	}
	called := false
	for _, c := range rr.calls {
		called = called || strings.HasPrefix(c, "wtdone")
	}
	if !called {
		t.Error("legacy reclaim no longer goes through wtdone")
	}
}

func TestGwmRepoRoot_parsesTableAndDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "c.toml")
	body := "[repos.\"/a/one\"]\nworktree-root = \"/w/one\"\nmax = 2\n\n[repos.'/a/two']\nmax = 1\n\n[repos.\"/a/three\"]\n# worktree-root = \"/no\"\nworktree-root='/w/three'\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for repo, want := range map[string]string{"/a/one": "/w/one", "/a/two": "/a/two.worktrees", "/a/three": "/w/three"} {
		got, found, err := gwmRepoRoot(cfg, repo)
		if err != nil || !found || got != want {
			t.Errorf("%s: got %q found=%v err=%v, want %q", repo, got, found, err, want)
		}
	}
	if _, found, _ := gwmRepoRoot(cfg, "/a/none"); found {
		t.Error("unlisted repo reported as configured")
	}
	if _, found, err := gwmRepoRoot(filepath.Join(t.TempDir(), "absent.toml"), "/a/one"); found || err != nil {
		t.Errorf("missing config: found=%v err=%v", found, err)
	}
}

func TestWritableAncestor(t *testing.T) {
	d := t.TempDir()
	if !writableAncestor(filepath.Join(d, "not", "yet", "there")) {
		t.Error("nonexistent root under a writable dir reads unwritable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	ro := filepath.Join(d, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ro, 0o700) //nolint:errcheck
	if writableAncestor(filepath.Join(ro, "wt")) {
		t.Error("root under a read-only dir reads writable")
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
