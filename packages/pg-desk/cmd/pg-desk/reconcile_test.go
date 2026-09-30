package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestReconcileCmdRequiresNoArgs(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"reconcile"})
	if err != nil {
		t.Fatalf("rootCmd has no reconcile subcommand: %v", err)
	}
	if err := cmd.Args(cmd, []string{"x"}); err == nil {
		t.Fatal("Args([x]): expected an error")
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Fatalf("Args(nil): %v", err)
	}
}

// TestReconcileCmdHasRetryAllFlag: the operator's manual repair flag
// (bead pg2-xb6fs) exists and defaults off, so scheduled runs honor the
// automatic-retry policy.
func TestReconcileCmdHasRetryAllFlag(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"reconcile"})
	if err != nil {
		t.Fatalf("rootCmd has no reconcile subcommand: %v", err)
	}
	f := cmd.Flags().Lookup("retry-all")
	if f == nil {
		t.Fatal("reconcile has no --retry-all flag")
	}
	if f.DefValue != "false" {
		t.Fatalf("--retry-all default = %q, want false", f.DefValue)
	}
}

// An empty store has nothing to reconcile and never gathers (no
// pg-connector on this test's $PATH), so it exits cleanly.
func TestReconcileCmdEmptyStoreSucceeds(t *testing.T) {
	origConfigLoad, origStoreOpen := runConfigLoad, runStoreOpen
	t.Cleanup(func() { runConfigLoad, runStoreOpen = origConfigLoad, origStoreOpen })
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return sweepTestCfg(), nil }
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }

	cmd, _, err := rootCmd.Find([]string{"reconcile"})
	if err != nil {
		t.Fatalf("no reconcile subcommand: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("reconcile on empty store: %v", err)
	}
}
