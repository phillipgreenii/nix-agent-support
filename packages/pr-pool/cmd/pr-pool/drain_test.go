package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestParseSelfLogin(t *testing.T) {
	got, err := parseSelfLogin([]byte(`{"self_login":"phillipg","worktree_root":"/x"}`))
	if err != nil || got != "phillipg" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParseSelfLogin_empty(t *testing.T) {
	if _, err := parseSelfLogin([]byte(`{"self_login":""}`)); err == nil {
		t.Error("empty self_login should error")
	}
}

// fakeBR is a fake beads.Runner: it returns canned output keyed by joined args,
// or a single error for every call.
type fakeBR struct {
	out map[string]string
	err error
}

func (f fakeBR) Run(_ context.Context, args ...string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out[strings.Join(args, " ")], nil
}

func TestReadBeadsPrefix_fromBd(t *testing.T) {
	br := fakeBR{out: map[string]string{"config get issue_prefix": "zr\n"}}
	got, err := readBeadsPrefix(context.Background(), br)
	if err != nil {
		t.Fatalf("readBeadsPrefix error: %v", err)
	}
	if got != "zr" {
		t.Errorf("readBeadsPrefix = %q, want zr", got)
	}
}

func TestReadBeadsPrefix_bdError(t *testing.T) {
	if _, err := readBeadsPrefix(context.Background(), fakeBR{err: errors.New("bd boom")}); err == nil {
		t.Error("bd error should propagate")
	}
}

func TestReadBeadsPrefix_emptyOutput(t *testing.T) {
	if _, err := readBeadsPrefix(context.Background(), fakeBR{out: map[string]string{}}); err == nil {
		t.Error("empty prefix should error")
	}
}

func TestPrecheckPrefix_matchAndMismatch(t *testing.T) {
	br := fakeBR{out: map[string]string{"config get issue_prefix": "zr"}}
	if err := precheckPrefix(context.Background(), br, "zr"); err != nil {
		t.Errorf("matching prefix should pass, got %v", err)
	}
	if err := precheckPrefix(context.Background(), br, "wrong"); err == nil {
		t.Error("prefix mismatch should fail")
	}
}

// Regression for pg2-hc67: precheck must pass from a monorepo worktree/slot that
// has NO local .beads dir, as long as bd resolves the store there.
func TestPrecheck_passesWithoutLocalBeadsDir(t *testing.T) {
	br := fakeBR{out: map[string]string{
		"list --limit 1 --json":   "[]",
		"config get issue_prefix": "zr",
	}}
	cfg := config.Config{RepoRoot: "/Volumes/acme/slot-a", BeadsPrefix: "zr"}
	if err := precheck(context.Background(), cfg, br); err != nil {
		t.Errorf("precheck should pass without a local .beads dir; got %v", err)
	}
}

func TestPrecheck_bdUnreachable(t *testing.T) {
	cfg := config.Config{RepoRoot: "/x", BeadsPrefix: "zr"}
	if err := precheck(context.Background(), cfg, fakeBR{err: errors.New("bd down")}); err == nil {
		t.Error("unreachable bd should fail precheck")
	}
}

// fixtureGitEnv is a minimal, explicit environment for the git commands this
// test file uses to BUILD fixtures: PATH plus a fixed test identity, and
// nothing else from the ambient environment.
//
// HOME is pointed at its own fresh t.TempDir() rather than forwarded, unlike
// production gitenv.Environ (which must forward the real HOME to keep
// working against the operator's actual repos): this machine's real HOME
// wires a global git hook that refuses a commit whose author identity looks
// like a placeholder ("t"/"example.com"/...) -- exactly the identity these
// fixtures use. GIT_CONFIG_NOSYSTEM/_GLOBAL/_SYSTEM back the isolation
// belt-and-suspenders. This function is also deliberately the ONLY place in
// this file GIT_-prefixed variables are set, and it never forwards ambient
// ones -- including GIT_DIR/GIT_WORK_TREE this test itself sets via
// t.Setenv to simulate the leak below -- so fixture setup/inspection cannot
// be disturbed by the exact leak warnTrackedConfig is being checked against.
// Mirrors ccpool's gitfacet_test.go testGitEnv (pg2-aqpvr).
func fixtureGitEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureGitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

// TestWarnTrackedConfig_ignoresLeakedGitDir is the regression pin for
// pg2-bh09g: git's repository discovery consults GIT_DIR/GIT_WORK_TREE before
// -C, so a `git commit` from a linked worktree that exports them into the
// process environment (mechanism write-up: pg2-67h4y) must not make
// warnTrackedConfig answer about the LEAKED repository instead of
// cfg.RepoRoot. target's copy of .pr-pool/config.toml is untracked (no
// warning expected); leaked's copy is committed (tracked). Verified to FAIL
// against the pre-fix code (a bare exec.CommandContext with no cmd.Env,
// which inherits os.Environ() wholesale): it then warns about target even
// though target's own file is untracked, because the leaked GIT_DIR silently
// redirected the check at leaked's tracked copy.
func TestWarnTrackedConfig_ignoresLeakedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	target := t.TempDir()
	target, _ = filepath.EvalSymlinks(target)
	runFixtureGit(t, target, "init", "-q")
	if err := os.MkdirAll(filepath.Join(target, ".pr-pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".pr-pool", "config.toml"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}

	leaked := t.TempDir()
	leaked, _ = filepath.EvalSymlinks(leaked)
	runFixtureGit(t, leaked, "init", "-q")
	if err := os.MkdirAll(filepath.Join(leaked, ".pr-pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaked, ".pr-pool", "config.toml"), []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, leaked, "add", ".pr-pool/config.toml")
	runFixtureGit(t, leaked, "commit", "-q", "-m", "add tracked config")

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment pointing at a DIFFERENT repository than cfg.RepoRoot.
	t.Setenv("GIT_DIR", filepath.Join(leaked, ".git"))
	t.Setenv("GIT_WORK_TREE", leaked)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	warnTrackedConfig(context.Background(), config.Config{RepoRoot: target})

	if strings.Contains(buf.String(), "tracked by git") {
		t.Fatalf("warnTrackedConfig warned that target's config.toml is tracked, but target's own copy is untracked -- a leaked GIT_DIR/GIT_WORK_TREE answered about the LEAKED repo instead of %s:\n%s", target, buf.String())
	}
}
