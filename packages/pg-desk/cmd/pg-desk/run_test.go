package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestRunCmdRejectsUnsupportedEntityType proves the Binding decisions'
// "run issue/run thread are NOT implemented by this packet — the CLI
// stub returns 'not implemented, see Phase 10/13' if invoked with those
// types" — and that an entirely unknown type is rejected too, rather than
// silently falling through to the pr pipeline.
func TestRunCmdRejectsUnsupportedEntityType(t *testing.T) {
	cases := []struct {
		entityType string
		wantSubstr []string
	}{
		{"issue", []string{"not implemented", "Phase 10"}},
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
