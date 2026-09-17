package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestRunCmdRejectsUnsupportedEntityType proves the Binding decisions'
// "run thread is NOT implemented by this packet — the CLI stub returns
// 'not implemented, see Phase 13' if invoked with that type" (run issue is
// now implemented by THIS packet — see the TestRunCmdIssue_* tests below)
// — and that an entirely unknown type is rejected too, rather than
// silently falling through to the pr pipeline.
func TestRunCmdRejectsUnsupportedEntityType(t *testing.T) {
	cases := []struct {
		entityType string
		wantSubstr []string
	}{
		{"thread", []string{"not implemented", "Phase 13"}},
		{"pull-request", []string{"unknown entity type"}},
	}
	for _, c := range cases {
		t.Run(c.entityType, func(t *testing.T) {
			cmd, _, err := rootCmd.Find([]string{"run"})
			if err != nil {
				t.Fatalf("rootCmd has no run subcommand: %v", err)
			}
			runErr := cmd.RunE(cmd, []string{c.entityType, "1"})
			if runErr == nil {
				t.Fatalf("run %s: expected an error, got nil", c.entityType)
			}
			for _, want := range c.wantSubstr {
				if !strings.Contains(runErr.Error(), want) {
					t.Fatalf("run %s: error = %q, want it to contain %q", c.entityType, runErr.Error(), want)
				}
			}
		})
	}
}

// TestRunCmdRequiresTwoArgs proves the CLI wiring: `pg-desk run` takes
// exactly two positional args (<type> <id>), enforced by cobra's own Args
// validation before RunE ever runs.
func TestRunCmdRequiresTwoArgs(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if cmd.Args == nil {
		t.Fatal("run subcommand has no Args validator")
	}
	for _, args := range [][]string{{}, {"pr"}, {"pr", "1", "extra"}} {
		if err := cmd.Args(cmd, args); err == nil {
			t.Fatalf("Args(%v): expected an error for the wrong arg count, got nil", args)
		}
	}
	if err := cmd.Args(cmd, []string{"pr", "1"}); err != nil {
		t.Fatalf("Args([pr 1]): unexpected error: %v", err)
	}
}

// TestRunCmdDefaultChangeIsSweep proves "an absent --change means sweep"
// [design 7.2] at the flag-definition level.
func TestRunCmdDefaultChangeIsSweep(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	f := cmd.Flags().Lookup("change")
	if f == nil {
		t.Fatal("run subcommand has no --change flag")
	}
	if f.DefValue != "sweep" {
		t.Fatalf("--change default = %q, want %q", f.DefValue, "sweep")
	}
}

// TestRunCmdRequiresConfigAndStore proves the CLI wiring for the pr path:
// a config-load failure surfaces as a RunE error rather than a panic or
// silent success, and never reaches runStoreOpen (so this test never
// touches the real $XDG_STATE_HOME/pg-desk/store.db).
func TestRunCmdRequiresConfigAndStore(t *testing.T) {
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

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"pr", "1"})
	if runErr == nil {
		t.Fatal("run pr: expected an error when config loading fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "no config file found") {
		t.Fatalf("run pr: error = %q, want it to wrap the config-load failure", runErr.Error())
	}
	if storeOpenCalled {
		t.Fatal("run pr: runStoreOpen was called despite a config-load failure")
	}
}

// issueTestCfg is a minimal single-repo Config, matching Phase 9/10's
// "exactly one configured repository" scope — mirrors
// internal/pipeline/pipeline_test.go's own testCfg helper independently
// (this package does not import that test file).
func issueTestCfg() *config.Config {
	return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/widgets"}}}
}

// seedPRFacts writes a minimal, well-formed entity row for "pr"
// entityID, so a `run issue` re-interpretation has stored facts to
// re-read (Store.GetEntity) without ever calling gather. The pr_show
// payload is nested under Facts.PRShow exactly as internal/pipeline's own
// persist() marshals it — mirroring internal/pipeline/pipeline_test.go's
// own minimalFacts helper independently (this package does not import
// that test file).
func seedPRFacts(t *testing.T, st *store.Store, entityID string) {
	t.Helper()
	prShow, err := json.Marshal(map[string]any{
		"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 7, "state": "open",
	})
	if err != nil {
		t.Fatalf("marshal pr show fixture: %v", err)
	}
	factsJSON, err := json.Marshal(gather.Facts{PRShow: prShow, AsOf: "2026-09-17T00:00:00Z", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("marshal facts fixture: %v", err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: "acme/widgets", EntityType: entityTypePR, EntityID: entityID,
		Facts: string(factsJSON), AsOf: "2026-09-17T00:00:00Z", ContentHash: "fixture",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
}

// TestRunCmdIssue_ResolvesAndReinterprets proves the "issue" case's own
// dispatch [design 7.2, 6.1]: it resolves the triggering bead to its
// linked PR via runResolveBeadPR (never through internal/gather, which is
// never invoked for entityType "issue"), then re-runs interpret for that
// PR, persisting a fresh interpretation row.
func TestRunCmdIssue_ResolvesAndReinterprets(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := issueTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }

	// runCmd's own RunE closes the store it opens (see run.go's `defer
	// func() { _ = st.Close() }()`), so this test opens a real on-disk
	// store (not store.OpenForTest's own instance, which it would then
	// find closed for its own post-RunE assertions) and reopens the same
	// file afterward to read back what was persisted.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	seedPRFacts(t, st, "acme/widgets#7")

	resolveCalls := 0
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalls++
		if beadID != "anchor-1" {
			t.Fatalf("runResolveBeadPR called with beadID=%q, want %q", beadID, "anchor-1")
		}
		return "acme/widgets", "acme/widgets#7", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"issue", "anchor-1", "--change", "changed"}); runErr != nil {
		t.Fatalf("run issue: unexpected error: %v", runErr)
	}
	if resolveCalls != 1 {
		t.Fatalf("runResolveBeadPR called %d times, want 1", resolveCalls)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdIssue_ResolveFailureReturnsClearError proves the acceptance
// criterion "a bead whose metadata and title both fail to match any known
// shape returns a clear, non-panicking error" at the CLI-wiring level:
// runResolveBeadPR's own error (internal/beadref's clear-error contract)
// surfaces as a wrapped RunE error, and runStoreOpen's store is never
// touched again after that (this test reaching its assertions at all is
// itself proof RunE did not panic).
func TestRunCmdIssue_ResolveFailureReturnsClearError(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := issueTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	st := store.OpenForTest(t)
	runStoreOpen = func() (*store.Store, error) { return st, nil }

	wantErr := errors.New("beadref: bead unresolvable-1 (title \"some other bead\") matches no known bead shape (anchor, feedback-cycle, review-request, or a title-adopted merge-request)")
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		return "", "", wantErr
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"issue", "unresolvable-1"})
	if runErr == nil {
		t.Fatal("run issue: expected an error for an unresolvable bead, got nil")
	}
	if !strings.Contains(runErr.Error(), "matches no known bead shape") {
		t.Fatalf("run issue: error = %q, want it to wrap the resolve failure", runErr.Error())
	}
}

// TestRunCmdIssue_ConfigFailureNeverResolves mirrors
// TestRunCmdRequiresConfigAndStore for the issue path: a config-load
// failure surfaces as a RunE error and never reaches runResolveBeadPR or
// runStoreOpen.
func TestRunCmdIssue_ConfigFailureNeverResolves(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	wantErr := errors.New("no config file found")
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return nil, wantErr }
	storeOpenCalled := false
	runStoreOpen = func() (*store.Store, error) {
		storeOpenCalled = true
		return nil, nil
	}
	resolveCalled := false
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalled = true
		return "", "", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"issue", "anchor-1"})
	if runErr == nil {
		t.Fatal("run issue: expected an error when config loading fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "no config file found") {
		t.Fatalf("run issue: error = %q, want it to wrap the config-load failure", runErr.Error())
	}
	if storeOpenCalled {
		t.Fatal("run issue: runStoreOpen was called despite a config-load failure")
	}
	if resolveCalled {
		t.Fatal("run issue: runResolveBeadPR was called despite a config-load failure")
	}
}
