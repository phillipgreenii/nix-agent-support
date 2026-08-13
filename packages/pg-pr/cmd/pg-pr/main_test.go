package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
)

// TestMain points browser.BinEnvVar at a path that cannot exist before any test
// in this package runs, so no test can ever launch the operator's real browser.
//
// The `open` tests stub browser.OpenWindow outright (see runOpenCmd), which is
// the real isolation. This is the fail-safe underneath it: a future test that
// drives the command without stubbing would otherwise exec the actual Chrome
// and pop windows onto someone's desktop mid-`go test`. Here it gets a
// missing-binary error instead.
func TestMain(m *testing.M) {
	if err := os.Setenv(browser.BinEnvVar, filepath.Join(os.TempDir(), "pg-pr-test-must-never-be-a-real-browser")); err != nil {
		panic("guard browser.BinEnvVar: " + err.Error())
	}
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
