package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runFailureLines returns every "run_failed" line `run` wrote to stderr.
func runFailureLines(t *testing.T, stderr string) []runFailureLine {
	t.Helper()
	var lines []runFailureLine
	for _, raw := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if !strings.Contains(raw, `"event":"run_failed"`) {
			continue
		}
		var l runFailureLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("run_failed line %q is not JSON: %v", raw, err)
		}
		lines = append(lines, l)
	}
	return lines
}

// runWithStderr runs `pg-desk run` through its RunE with stderr captured and
// returns RunE's error plus the captured stderr.
func runWithStderr(t *testing.T, args ...string) (stderr string, runErr error) {
	t.Helper()
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	var buf bytes.Buffer
	cmd.SetErr(&buf)
	cmd.SetContext(context.Background())
	t.Cleanup(func() { cmd.SetErr(nil) })
	runErr = cmd.RunE(cmd, args)
	return buf.String(), runErr
}

func stubRunSeams(t *testing.T) {
	t.Helper()
	origConfigLoad, origStoreOpen, origResolve := runConfigLoad, runStoreOpen, runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad, runStoreOpen, runResolveBeadPR = origConfigLoad, origStoreOpen, origResolve
	})
}

// openRunTestStore opens a real on-disk store (run closes the one it is
// handed, so OpenForTest's instance would be unusable afterwards).
func openRunTestStore(t *testing.T) *store.Store {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	return st
}

// TestRunFailureLine_ByStage proves every way `run pr` can fail emits exactly
// one stage-tagged run_failed line on stderr and still returns the error (so
// main.go's exit 1 contract is unchanged).
func TestRunFailureLine_ByStage(t *testing.T) {
	t.Run("unknown entity type", func(t *testing.T) {
		stubRunSeams(t)
		stderr, runErr := runWithStderr(t, "pull-request", "1")
		if runErr == nil {
			t.Fatal("expected an error")
		}
		lines := runFailureLines(t, stderr)
		if len(lines) != 1 || lines[0].Stage != runStageArgs {
			t.Fatalf("run_failed lines = %+v, want one with stage %q (stderr: %s)", lines, runStageArgs, stderr)
		}
	})

	t.Run("config load", func(t *testing.T) {
		stubRunSeams(t)
		runConfigLoad = func(ctx context.Context) (*config.Config, error) {
			return nil, errors.New("no config file found")
		}
		stderr, runErr := runWithStderr(t, "pr", "acme/widgets#9")
		if runErr == nil || !strings.Contains(runErr.Error(), "no config file found") {
			t.Fatalf("RunE error = %v, want the config failure unchanged", runErr)
		}
		lines := runFailureLines(t, stderr)
		if len(lines) != 1 {
			t.Fatalf("want exactly one run_failed line, got %d (stderr: %s)", len(lines), stderr)
		}
		l := lines[0]
		if l.Stage != runStageConfig || l.ErrorClass != "error" || l.EntityType != "pr" || l.EntityID != "acme/widgets#9" {
			t.Fatalf("run_failed line = %+v, want stage %q, class error, entity pr acme/widgets#9", l, runStageConfig)
		}
		if !strings.Contains(l.Error, "no config file found") {
			t.Fatalf("run_failed error = %q, want the original message", l.Error)
		}
	})

	t.Run("store open locked", func(t *testing.T) {
		stubRunSeams(t)
		runConfigLoad = func(ctx context.Context) (*config.Config, error) { return issueTestCfg(), nil }
		runStoreOpen = func() (*store.Store, error) { return nil, errors.New("open: database is locked") }
		stderr, runErr := runWithStderr(t, "pr", "acme/widgets#9")
		if runErr == nil {
			t.Fatal("expected an error")
		}
		lines := runFailureLines(t, stderr)
		if len(lines) != 1 || lines[0].Stage != runStageStoreOpen || lines[0].ErrorClass != "store_busy" {
			t.Fatalf("run_failed lines = %+v, want stage %q class store_busy (stderr: %s)", lines, runStageStoreOpen, stderr)
		}
	})

	t.Run("resolve bead", func(t *testing.T) {
		stubRunSeams(t)
		runConfigLoad = func(ctx context.Context) (*config.Config, error) { return issueTestCfg(), nil }
		st := openRunTestStore(t)
		runStoreOpen = func() (*store.Store, error) { return st, nil }
		runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
			return "", "", errors.New("bead has no PR reference")
		}
		stderr, runErr := runWithStderr(t, "issue", "anchor-1")
		if runErr == nil {
			t.Fatal("expected an error")
		}
		lines := runFailureLines(t, stderr)
		if len(lines) != 1 || lines[0].Stage != runStageResolveBead {
			t.Fatalf("run_failed lines = %+v, want stage %q (stderr: %s)", lines, runStageResolveBead, stderr)
		}
	})

	t.Run("pipeline gather stage", func(t *testing.T) {
		stubRunSeams(t)
		runConfigLoad = func(ctx context.Context) (*config.Config, error) { return issueTestCfg(), nil }
		st := openRunTestStore(t)
		runStoreOpen = func() (*store.Store, error) { return st, nil }
		// No pg-connector on PATH: gather cannot even start the child, which is
		// a stage-tagged pipeline failure without any fake seam.
		t.Setenv("PATH", t.TempDir())

		stderr, runErr := runWithStderr(t, "pr", "acme/widgets#9")
		if runErr == nil {
			t.Fatal("expected an error when pg-connector cannot be started")
		}
		lines := runFailureLines(t, stderr)
		if len(lines) != 1 || lines[0].Stage != "gather" {
			t.Fatalf("run_failed lines = %+v, want one with stage gather (stderr: %s)", lines, stderr)
		}
		// The pipeline's own per-entity line carries the same stage.
		if !strings.Contains(stderr, `"outcome":"error"`) || !strings.Contains(stderr, `"stage":"gather"`) {
			t.Fatalf("pipeline log line should carry stage gather, stderr: %s", stderr)
		}
	})
}

// TestRunFailureLine_UntaggedErrorFallsBack proves a failure from a path with
// no stage tag (the Jira and thread re-interpret paths) still gets one
// record, labelled with the generic run stage and a classified error.
func TestRunFailureLine_UntaggedErrorFallsBack(t *testing.T) {
	var buf bytes.Buffer
	logRunFailure(&buf, "thread", "t1", "sweep", errors.New("run thread t1: signal: killed"))
	var l runFailureLine
	if err := json.Unmarshal(buf.Bytes(), &l); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	if l.Event != "run_failed" || l.Stage != runStageUnknown || l.ErrorClass != "killed" {
		t.Fatalf("line = %+v, want event run_failed, stage %q, class killed", l, runStageUnknown)
	}
}

// TestRunFailureLine_NotEmittedOnSuccess proves the failure line is
// failure-only: a successful run adds no run_failed record.
func TestRunFailureLine_NotEmittedOnSuccess(t *testing.T) {
	stubRunSeams(t)
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return issueTestCfg(), nil }
	st := openRunTestStore(t)
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	seedPRFacts(t, st, "acme/widgets#7")
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		return "acme/widgets", "acme/widgets#7", nil
	}

	stderr, runErr := runWithStderr(t, "issue", "anchor-1")
	if runErr != nil {
		t.Fatalf("run issue: unexpected error: %v", runErr)
	}
	if lines := runFailureLines(t, stderr); len(lines) != 0 {
		t.Fatalf("a successful run must not emit run_failed, got %+v", lines)
	}
}
