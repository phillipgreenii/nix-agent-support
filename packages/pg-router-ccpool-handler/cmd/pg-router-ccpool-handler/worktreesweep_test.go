package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
)

// Worktree-keyed sweep (bead pg2-ganjb, INV-CCH-19), against REAL git: a temp
// repository, real linked worktrees under a temp WorktreeDir, and the real
// gitclient opener (HOME and git config isolated from this machine).

type sweepHarness struct {
	t        *testing.T
	repo     string
	wtDir    string
	lockDir  string
	cc       *orphanCC
	deps     executor.Deps
	env      orphanEnv
	logPath  string
	role     roles.Role
	opener   worktree.Opener
	gitEnv   []string
	homeDir  string
	nowFixed time.Time
}

func newSweepHarness(t *testing.T) *sweepHarness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	h := &sweepHarness{t: t, homeDir: t.TempDir(), lockDir: t.TempDir()}
	// Drop every inherited GIT_* variable: under a git hook (the commit's own
	// pre-commit run) GIT_DIR/GIT_INDEX_FILE/... point at the OUTER repository and
	// would redirect these fixture commands into it.
	h.gitEnv = append(
		envWithoutGit(os.Environ()),
		"HOME="+h.homeDir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.repo = repo
	h.git(repo, "init", "-q", "-b", "main")
	h.git(repo, "commit", "-q", "--allow-empty", "-m", "init")
	wtDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.wtDir = wtDir
	h.opener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		return gitclient.New(
			ctx, dir,
			gitclient.WithHome(h.homeDir),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		)
	}

	h.cc = &orphanCC{}
	h.logPath = filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(h.logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lw.Close() })
	cfg := config.Default()
	cfg.RepoRoot = repo
	cfg.WorktreeDir = wtDir
	cfg.SessionPrefix = "pg-router-"
	h.nowFixed = time.Now()
	h.deps = executor.Deps{CC: h.cc, BD: &orphanBD{}, Cfg: cfg, Log: lw, Now: func() time.Time { return h.nowFixed }}
	h.env = orphanEnv{open: h.opener, lockDir: h.lockDir}
	h.role = orphanRole(roles.CloseOnly, 0)
	return h
}

func envWithoutGit(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

func (h *sweepHarness) git(dir string, args ...string) string {
	h.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = h.gitEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}

// leak creates the per-bead worktree exactly as dispatch does (worktree.Ensure:
// pg-router/<bead> off HEAD) and ages it past the grace window.
func (h *sweepHarness) leak(bead string) string {
	h.t.Helper()
	path, err := worktree.Ensure(context.Background(), h.opener, h.wtDir, h.repo, bead)
	if err != nil {
		h.t.Fatalf("worktree.Ensure(%s): %v", bead, err)
	}
	old := h.nowFixed.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(path, ".git"), old, old); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *sweepHarness) sweep() int {
	return sweepLeakedWorktrees(context.Background(), h.role, h.deps, h.env)
}

func (h *sweepHarness) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (h *sweepHarness) branchExists(bead string) bool {
	cmd := exec.Command("git", "-C", h.repo, "rev-parse", "--verify", "--quiet", "refs/heads/pg-router/"+bead)
	cmd.Env = h.gitEnv
	return cmd.Run() == nil
}

func (h *sweepHarness) reclaimEvents() []map[string]any {
	h.t.Helper()
	f, err := os.Open(h.logPath)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			h.t.Fatal(err)
		}
		if m["kind"] == worktreeEventReclaimed {
			out = append(out, m)
		}
	}
	return out
}

func (h *sweepHarness) wantKept(path, bead string) {
	h.t.Helper()
	if !h.exists(path) || !h.branchExists(bead) {
		h.t.Errorf("worktree %s and branch pg-router/%s must be kept", path, bead)
	}
}

// The core case: a handler died between worktree creation and session creation,
// so no row exists. The next dispatch reclaims worktree and branch.
func TestSweep_leakedWorktreeWithNoRow_isReclaimedWithBranch(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	if !h.branchExists("zr-k") {
		t.Fatal("fixture: branch should exist")
	}
	if got := h.sweep(); got != 1 {
		t.Fatalf("sweep removed %d, want 1", got)
	}
	if h.exists(path) || h.branchExists("zr-k") {
		t.Errorf("worktree and anchor branch must both be gone; worktree exists=%v branch exists=%v", h.exists(path), h.branchExists("zr-k"))
	}
	evs := h.reclaimEvents()
	if len(evs) != 1 || evs[0]["bead"] != "zr-k" || evs[0]["worktree"] != path || evs[0]["role"] != "worker" {
		t.Errorf("want one worktree_reclaimed event for zr-k, got %v", evs)
	}
}

// pg2-w3usi's residual: the row was closed (idle_ttl) before anyone reclaimed
// the worktree. A closed, not-live row does not protect it.
func TestSweep_worktreeOfClosedNotLiveRow_isReclaimed(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.cc.sessions = []ccpool.Session{{
		ExternalID: "pg-router-worker-zr-k-1", State: ccpool.StateIdle, Live: false, CloseReason: "idle_ttl", CWD: path,
		Meta: map[string]string{ccpool.MetaKeyBead: "zr-k"},
	}}
	if got := h.sweep(); got != 1 || h.exists(path) {
		t.Fatalf("removed=%d exists=%v, want the closed row's worktree reclaimed", got, h.exists(path))
	}
}

func TestSweep_keptWhileASessionRowNamesIt(t *testing.T) {
	cases := []struct {
		name string
		row  func(path string) ccpool.Session
	}{
		{"open row by working directory", func(p string) ccpool.Session {
			return ccpool.Session{ExternalID: "pg-router-worker-x-1", State: ccpool.StateWorking, Live: true, CWD: p}
		}},
		{"open row by bead meta", func(string) ccpool.Session {
			return ccpool.Session{
				ExternalID: "pg-router-review-zr-k-1", State: ccpool.StateIdle, Live: true,
				Meta: map[string]string{ccpool.MetaKeyBead: "zr-k"},
			}
		}},
		{"closed but still live row", func(p string) ccpool.Session {
			return ccpool.Session{ExternalID: "pg-router-worker-x-1", State: ccpool.StateIdle, Live: true, CloseReason: "handler", CWD: p}
		}},
		{"starting row (a sibling still launching)", func(p string) ccpool.Session {
			return ccpool.Session{ExternalID: "pg-router-worker-x-1", State: ccpool.StateStarting, Live: true, CWD: p}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSweepHarness(t)
			path := h.leak("zr-k")
			h.cc.sessions = []ccpool.Session{tc.row(path)}
			if got := h.sweep(); got != 0 {
				t.Errorf("removed %d, want 0", got)
			}
			h.wantKept(path, "zr-k")
		})
	}
}

func TestSweep_keptWhenYoungerThanGrace(t *testing.T) {
	h := newSweepHarness(t)
	path, err := worktree.Ensure(context.Background(), h.opener, h.wtDir, h.repo, "zr-k") // fresh mtime
	if err != nil {
		t.Fatal(err)
	}
	h.nowFixed = time.Now()
	if got := h.sweep(); got != 0 {
		t.Errorf("a worktree younger than the grace window (launch wait + lease TTL) must be kept; removed %d", got)
	}
	h.wantKept(path, "zr-k")
}

func TestSweep_keptWhenDirty(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.sweep(); got != 0 {
		t.Errorf("a dirty worktree must be kept; removed %d", got)
	}
	h.wantKept(path, "zr-k")
}

func TestSweep_keptWhenBranchHasUniqueCommits(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.git(path, "commit", "-q", "--allow-empty", "-m", "worker commit")
	if got := h.sweep(); got != 0 {
		t.Errorf("a branch with a commit HEAD lacks must be kept; removed %d", got)
	}
	h.wantKept(path, "zr-k")
}

// The same commit once it is reachable from the repo's HEAD (merged) is no
// longer unique, so the worktree is reclaimable.
func TestSweep_mergedCommitsAreNotUnique(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.git(path, "commit", "-q", "--allow-empty", "-m", "worker commit")
	h.git(h.repo, "merge", "-q", "--ff-only", "pg-router/zr-k")
	if got := h.sweep(); got != 1 || h.exists(path) {
		t.Errorf("removed=%d exists=%v, want the merged worktree reclaimed", got, h.exists(path))
	}
}

func TestSweep_keptWhenGitLocked(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.git(h.repo, "worktree", "lock", path)
	if got := h.sweep(); got != 0 {
		t.Errorf("a git-locked worktree must be kept; removed %d", got)
	}
	h.wantKept(path, "zr-k")
}

func TestSweep_keptWhenAnotherBranchIsCheckedOut(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.git(path, "switch", "-q", "-c", "operator/side-branch")
	if got := h.sweep(); got != 0 {
		t.Errorf("a worktree on a non-anchor branch must be kept; removed %d", got)
	}
	if !h.exists(path) {
		t.Error("worktree must be kept")
	}
}

func TestSweep_ignoresDirectoriesThatAreNotLinkedWorktrees(t *testing.T) {
	h := newSweepHarness(t)
	plain := filepath.Join(h.wtDir, "just-a-dir")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	old := h.nowFixed.Add(-time.Hour)
	_ = os.Chtimes(plain, old, old)
	if err := os.WriteFile(filepath.Join(h.wtDir, "stray-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.sweep(); got != 0 || !h.exists(plain) {
		t.Errorf("removed=%d plainExists=%v: only linked worktrees are swept", got, h.exists(plain))
	}
}

func TestSweep_listErrorMeansNoAction(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	h.cc.listErr = errors.New("ccpool list: boom")
	if got := h.sweep(); got != 0 {
		t.Errorf("removed %d on a list error, want 0", got)
	}
	h.wantKept(path, "zr-k")
}

func TestSweep_disabledConfigurations(t *testing.T) {
	t.Run("role without ccpool block", func(t *testing.T) {
		h := newSweepHarness(t)
		path := h.leak("zr-k")
		h.role = roles.Role{Name: "cmd", Type: "command"}
		if got := h.sweep(); got != 0 {
			t.Errorf("removed %d", got)
		}
		h.wantKept(path, "zr-k")
	})
	t.Run("non-worktree isolation", func(t *testing.T) {
		h := newSweepHarness(t)
		path := h.leak("zr-k")
		h.role.CCPool.Isolation = roles.IsolationConfig{Type: "none"}
		if got := h.sweep(); got != 0 {
			t.Errorf("removed %d", got)
		}
		h.wantKept(path, "zr-k")
	})
	t.Run("no lock directory (acting without exclusion is forbidden)", func(t *testing.T) {
		h := newSweepHarness(t)
		path := h.leak("zr-k")
		h.env.lockDir = ""
		if got := h.sweep(); got != 0 {
			t.Errorf("removed %d", got)
		}
		h.wantKept(path, "zr-k")
	})
	t.Run("missing worktree directory", func(t *testing.T) {
		h := newSweepHarness(t)
		h.deps.Cfg.WorktreeDir = filepath.Join(h.wtDir, "does-not-exist")
		if got := h.sweep(); got != 0 {
			t.Errorf("removed %d", got)
		}
	})
}

func TestSweep_boundedPerPass(t *testing.T) {
	h := newSweepHarness(t)
	for _, b := range []string{"zr-1", "zr-2", "zr-3", "zr-4", "zr-5", "zr-6", "zr-7"} {
		h.leak(b)
	}
	if got := h.sweep(); got != worktreeSweepMaxPerPass {
		t.Fatalf("first pass removed %d, want %d", got, worktreeSweepMaxPerPass)
	}
	if got := h.sweep(); got != 2 {
		t.Fatalf("second pass removed %d, want the remaining 2", got)
	}
}

// A live dispatch holds the per-bead worktree lock shared, so the sweep leaves
// its worktree alone even though no session row exists yet.
func TestSweep_keptWhileADispatchHoldsTheWorktreeLock(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	l, err := sessionlock.TryRLock(h.lockDir, sessionlock.WorktreeKey("zr-k"))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.sweep(); got != 0 {
		t.Errorf("removed %d while a dispatch held the lock", got)
	}
	h.wantKept(path, "zr-k")
	l.Unlock()
	if got := h.sweep(); got != 1 || h.exists(path) {
		t.Errorf("after release: removed=%d exists=%v, want reclaimed", got, h.exists(path))
	}
}

// The bead's acceptance test: kill the handler between isolation.Ensure and
// CC.Ensure, then assert the next dispatch reclaims. The "handler" is a real
// child process that holds the per-bead worktree lock as run() does; SIGKILL
// drops it with the process.
func TestSweep_killedHandlerBetweenWorktreeAndSession_reclaimedOnNextDispatch(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldWorktreeLock$")
	cmd.Env = append(envWithoutGit(os.Environ()), "PGR_HELPER_HOLD_LOCK=1", "PGR_HELPER_LOCK_DIR="+h.lockDir, "PGR_HELPER_BEAD=zr-k")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "locked") {
		t.Fatalf("helper did not report holding the lock: %q %v", line, err)
	}

	if got := h.sweep(); got != 0 {
		t.Fatalf("the live handler's worktree must not be swept; removed %d", got)
	}
	h.wantKept(path, "zr-k")

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	if got := h.sweep(); got != 1 {
		t.Fatalf("after the handler was killed the sweep removed %d, want 1", got)
	}
	if h.exists(path) || h.branchExists("zr-k") {
		t.Errorf("worktree and branch must be reclaimed; worktree exists=%v branch exists=%v", h.exists(path), h.branchExists("zr-k"))
	}
}

// TestHelperHoldWorktreeLock is not a test: it is the child process of the kill
// test above, a stand-in for a handler parked between isolation.Ensure and
// CC.Ensure. It exits immediately when run as a normal test.
func TestHelperHoldWorktreeLock(t *testing.T) {
	if os.Getenv("PGR_HELPER_HOLD_LOCK") != "1" {
		t.Skip("helper process only")
	}
	l, err := sessionlock.TryRLock(os.Getenv("PGR_HELPER_LOCK_DIR"), sessionlock.WorktreeKey(os.Getenv("PGR_HELPER_BEAD")))
	if err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(2)
	}
	_ = l
	os.Stdout.WriteString("locked\n")
	select {} // until killed
}
