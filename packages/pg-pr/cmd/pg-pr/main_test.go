package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
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
	if err := os.Setenv(browser.BinEnvVar, filepath.Join(os.TempDir(), "pg-pr-test-must-never-be-a-real-browser")); err != nil {
		panic("guard browser.BinEnvVar: " + err.Error())
	}
	store.SetSynchronousForTests("OFF")
	os.Exit(m.Run())
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
