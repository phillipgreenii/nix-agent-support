package gitfixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain scrubs GIT_DIR/GIT_WORK_TREE/etc from this process's own
// environment before any test runs (pg2-12795). Without this, running this
// package's own tests from inside a git hook's subprocess environment (this
// repo's `run-unit-tests` pre-commit hook is exactly such a hook) leaks
// those vars in ambiently -- and initDecoyCanonical below deliberately
// leaves its OWN cmd.Env unset (see its doc comment), so without this scrub
// its `-C <decoy>` git commands inherit the leaked GIT_DIR/GIT_INDEX_FILE
// and land on the REAL invoking repository instead of the decoy. Confirmed
// live: exactly this happened once, landing a stray `init` commit on this
// worktree's real branch before this TestMain existed.
func TestMain(m *testing.M) {
	ScrubProcessGitEnv()
	os.Exit(m.Run())
}

// initDecoyCanonical creates a real git repo at dir with one commit, using a
// deliberately UNSCRUBBED subprocess environment -- standing in for a real
// canonical clone that must never be touched by this package's fixtures.
// ("Unscrubbed" here means it does not go through this package's own
// allowlisted Env/CommitEnv -- TestMain above still keeps the AMBIENT
// process environment free of git-location vars, which this decoy's own
// setup relies on just as much as the code under test does.)
//
// `-c core.hooksPath=/dev/null` on every call is a SEPARATE concern from the
// GIT_DIR/GIT_WORK_TREE scrubbing above; it exists because this machine
// wires a global git hooks path that fires this very module's own
// `run-unit-tests` pre-commit hook on ANY `git commit` ANYWHERE, including a
// throwaway decoy under t.TempDir(). Without it, this helper's own
// `git commit` recursively re-invokes the whole test suite (this test
// included) from inside itself.
func initDecoyCanonical(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	run("-c", "user.email=real@example.com", "-c", "user.name=Real User",
		"commit", "-q", "--allow-empty", "-m", "init")
}

// TestMustRun_IgnoresAmbientGitDirLeak is the regression pin for pg2-12795:
// a git-fixture test that shells out via this package's MustRun/Env must
// stay confined to the directory it was given even when the AMBIENT
// environment (simulating a git-hook-invoked `go test` process, or a
// sibling fixture's leftover env) carries GIT_DIR/GIT_WORK_TREE pointing at
// a different, real repository. Before the fix (plain os.Environ()
// forwarded to the subprocess), this exact shape corrupted a canonical
// clone's own .git/config with `core.worktree=<worktree path>` and
// `user.email`/`user.name` set to this package's own test identity --
// reproduced against a disposable decoy here, never against a real
// checkout.
func TestMustRun_IgnoresAmbientGitDirLeak(t *testing.T) {
	decoy := t.TempDir()
	target := t.TempDir()
	otherWorktree := filepath.Join(decoy, ".worktrees", "some-other-bead")
	if err := os.MkdirAll(otherWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	initDecoyCanonical(t, decoy)

	before, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
	if err != nil {
		t.Fatalf("read decoy config: %v", err)
	}

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment that launched this test process (e.g. by the invoking
	// git hook), pointing at the decoy "canonical" clone and one of its
	// worktrees -- exactly what pg2-12795 found in the real corruption.
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", otherWorktree)

	MustRun(t, target, "init", "-b", "main")
	MustRun(t, target, "config", "user.name", "test")
	MustRun(t, target, "config", "user.email", "test@example.com")

	after, err := os.ReadFile(filepath.Join(decoy, ".git", "config"))
	if err != nil {
		t.Fatalf("read decoy config after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("decoy canonical's .git/config was mutated by a fixture call meant for %s:\nbefore:\n%s\nafter:\n%s",
			target, before, after)
	}

	// And the target dir must have actually received its OWN, independent
	// repo -- the fixture must not silently no-op just because it avoided
	// the decoy.
	if _, err := os.Stat(filepath.Join(target, ".git", "config")); err != nil {
		t.Fatalf("target dir %s has no .git/config of its own: %v", target, err)
	}
	targetCfg, err := os.ReadFile(filepath.Join(target, ".git", "config"))
	if err != nil {
		t.Fatalf("read target config: %v", err)
	}
	if got := string(targetCfg); !strings.Contains(got, "test@example.com") {
		t.Fatalf("target repo's own config missing the configured user.email; got:\n%s", got)
	}
}
