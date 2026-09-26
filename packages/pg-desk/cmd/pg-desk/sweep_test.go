package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestSweepCmdRequiresNoArgs proves the CLI wiring: `pg-desk sweep` takes
// no positional arguments (it operates on every entity the store already
// knows about, never a single id) — mirrors run_test.go's own
// TestRunCmdRequiresTwoArgs, one arg count over.
func TestSweepCmdRequiresNoArgs(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"sweep"})
	if err != nil {
		t.Fatalf("rootCmd has no sweep subcommand: %v", err)
	}
	if cmd.Args == nil {
		t.Fatal("sweep subcommand has no Args validator")
	}
	if err := cmd.Args(cmd, []string{"acme/widgets#1"}); err == nil {
		t.Fatal("Args([acme/widgets#1]): expected an error, sweep takes no positional args")
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Fatalf("Args(nil): unexpected error: %v", err)
	}
}

// TestSweepCmdRequiresConfigAndStore mirrors run_test.go's own
// TestRunCmdRequiresConfigAndStore: a config-load failure surfaces as a
// RunE error and never reaches runStoreOpen, so this test never touches
// the real $XDG_STATE_HOME/pg-desk/store.db.
func TestSweepCmdRequiresConfigAndStore(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
	})

	wantErr := errors.New("no config file found")
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return nil, wantErr }
	storeOpenCalled := false
	runStoreOpen = func() (*store.Store, error) {
		storeOpenCalled = true
		return nil, nil
	}

	cmd, _, err := rootCmd.Find([]string{"sweep"})
	if err != nil {
		t.Fatalf("rootCmd has no sweep subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, nil)
	if runErr == nil {
		t.Fatal("sweep: expected an error when config loading fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "no config file found") {
		t.Fatalf("sweep: error = %q, want it to wrap the config-load failure", runErr.Error())
	}
	if storeOpenCalled {
		t.Fatal("sweep: runStoreOpen was called despite a config-load failure")
	}
}

// TestSweepCmdStoreOpenFailureSurfaces proves a store-open failure (config
// resolved fine) surfaces as a clear RunE error rather than a panic.
func TestSweepCmdStoreOpenFailureSurfaces(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
	})

	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return sweepTestCfg(), nil }
	wantErr := errors.New("open store.db: permission denied")
	runStoreOpen = func() (*store.Store, error) { return nil, wantErr }

	cmd, _, err := rootCmd.Find([]string{"sweep"})
	if err != nil {
		t.Fatalf("rootCmd has no sweep subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, nil)
	if runErr == nil {
		t.Fatal("sweep: expected an error when opening the store fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "permission denied") {
		t.Fatalf("sweep: error = %q, want it to wrap the store-open failure", runErr.Error())
	}
}

// sweepTestCfg is a minimal single-repo Config, matching Phase 9/10's
// "exactly one configured repository" scope — mirrors run_test.go's own
// issueTestCfg helper independently (this file's own tests never gather,
// so the fixture's own shape does not otherwise matter).
func sweepTestCfg() *config.Config {
	return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/widgets"}}}
}

// TestSweepCmdEmptyStore_CompletesAndStampsLastSweep is the end-to-end CLI
// wiring proof for the empty-store case: with zero entities in the store,
// `pg-desk sweep` never needs to gather anything (there is no real
// pg-connector on this test's $PATH, so an accidental gather call would
// fail loudly rather than silently — see internal/pipeline's own
// TestPipelineSweep_EmptyStoreIsNoopButStillStampsLastSweep for the
// per-entity iteration contract itself, exercised there with a fake
// gatherer), and it still completes cleanly and stamps meta.last_sweep.
func TestSweepCmdEmptyStore_CompletesAndStampsLastSweep(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
	})

	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return sweepTestCfg(), nil }

	// sweepCmd's own RunE closes the store it opens (mirrors run.go's `defer
	// func() { _ = st.Close() }()`), so — mirroring run_test.go's own
	// TestRunCmdIssue_ResolvesAndReinterprets convention — this test opens a
	// real on-disk store (not store.OpenForTest's instance, which it would
	// then find closed for its own post-RunE assertions) and reopens the
	// same file afterward to read back what was persisted.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }

	cmd, _, err := rootCmd.Find([]string{"sweep"})
	if err != nil {
		t.Fatalf("rootCmd has no sweep subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, nil); runErr != nil {
		t.Fatalf("sweep: unexpected error sweeping an empty store: %v", runErr)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	if _, found, err := verify.GetMeta(store.MetaKeyLastSweep); err != nil || !found {
		t.Fatalf("GetMeta(last_sweep): found=%v err=%v, want it stamped after sweep completes", found, err)
	}
}
