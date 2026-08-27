package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// TestMain points browser.BinEnvVar at a path that cannot exist before any test
// in this package runs, so no test can ever launch the operator's real browser.
//
// The `open` tests stub browser.OpenWindow outright (see runOpenCmd), which is
// the real isolation. This is the fail-safe underneath it: a future test that
// drives the command without stubbing would otherwise exec the actual Chrome
// and pop windows onto someone's desktop mid-`go test`. Here it gets a
// missing-binary error instead.
//
// It also disables SQLite durability for every store this package's tests
// open (seedListStore, and the direct store.Open calls in feedback_test.go,
// migrate_cmd_test.go, and pr_test.go): each store creation costs ~17 fsyncs,
// and fsync latency on a loaded/slow-fsync builder is enough to blow `go
// test`'s 10-minute default timeout even though CPU time is trivial. See
// store.synchronousPragma for the full write-up; mirrors ceta commit
// `1138b8a1`.
func TestMain(m *testing.M) {
	// Unset every git-location env var BEFORE any test runs. A git hook
	// (pre-commit/prek, invoking `go test` for this very package as its
	// run-unit-tests hook) exports GIT_DIR/GIT_WORK_TREE for the commit in
	// progress, and this test binary inherits that. `-C <dir>` and even an
	// explicit repo path argument do NOT override these — git's own repo
	// discovery consults them FIRST — so every test here that builds an
	// isolated fixture under t.TempDir() and then drives this package's real
	// git-invoking code (branch detect, worktree add, RepoFromRemote, ...)
	// would otherwise silently operate on the AMBIENT repo (this checkout)
	// instead of the fixture: confirmed 2026-08-27 (pg2-5ek6b/pg2-12795) —
	// TestBranchDetectHuman and friends reported this checkout's own
	// repo/branch instead of the fixture's "owner/repo"/"main" under a real
	// commit-time hook run, though a plain `go test` from an uncontaminated
	// shell never reproduces it. Unsetting these here, once, for the whole
	// process is sufficient: nothing in this package's tests or the code
	// under test re-sets them.
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
		"GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
	} {
		_ = os.Unsetenv(k)
	}

	if err := os.Setenv(browser.BinEnvVar, filepath.Join(os.TempDir(), "pg-pr-test-must-never-be-a-real-browser")); err != nil {
		panic("guard browser.BinEnvVar: " + err.Error())
	}
	store.SetSynchronousForTests("OFF")

	// Point mergeRequestLock (pr_write.go) at an isolated lock dir for the
	// whole test binary instead of its production default
	// ($XDG_RUNTIME_DIR/pg-pr/locks). Without this, ANY test that runs
	// `pr create` would take a REAL flock under that path — contending with
	// (or worse, silently synchronizing against) an actual pg-pr process
	// running on the same machine. See prlock.Options.LockDir's own doc
	// comment: "Tests MUST inject a t.TempDir() value here instead of
	// relying on the default." TestMain runs outside any *testing.T, so it
	// uses os.MkdirTemp directly rather than t.TempDir().
	lockDir, err := os.MkdirTemp("", "pg-pr-cmd-prlock-test-*")
	if err != nil {
		panic("create test lock dir: " + err.Error())
	}
	mergeRequestLock = prlock.New(prlock.Options{LockDir: lockDir})

	code := m.Run()
	_ = os.RemoveAll(lockDir)
	os.Exit(code)
}

// TestExitCodeFor pins the error->exit-code mapping (bead pg2-4dz88.6.3): a
// give-up on the cross-process merge-request lock (internal/prlock.ErrTimeout,
// wrapped or bare) maps to exitBusy; every other error — including nil-vs-any
// unexpected error — stays on the generic path (1), which per this
// workspace's exit-code convention MUST NOT be given a specific meaning.
func TestExitCodeFor(t *testing.T) {
	if got := exitCodeFor(errors.New("boom")); got != 1 {
		t.Errorf("generic error exit code = %d, want 1", got)
	}
	wrapped := fmt.Errorf("pr create: await merge-request lock for o/r#1: %w", prlock.ErrTimeout)
	if got := exitCodeFor(wrapped); got != exitBusy {
		t.Errorf("wrapped prlock.ErrTimeout exit code = %d, want exitBusy (%d)", got, exitBusy)
	}
	if got := exitCodeFor(prlock.ErrTimeout); got != exitBusy {
		t.Errorf("bare prlock.ErrTimeout exit code = %d, want exitBusy (%d)", got, exitBusy)
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := stdout.String()
	want := "pg-pr dev\n"
	if got != want {
		t.Fatalf("version output: got %q, want %q", got, want)
	}
}
