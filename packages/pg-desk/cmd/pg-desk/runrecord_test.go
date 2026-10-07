package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestMain keeps every test's run-record writes (a `run` failure, even one a
// test provokes on purpose, appends a record) out of the real state directory
// (bead pg2-dpml1).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pg-desk-run-record-")
	if err != nil {
		panic(err)
	}
	runRecordLogPath = func() string { return filepath.Join(dir, runRecordLogName) }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// redirectRunRecord points the run record at a fresh file for one test.
func redirectRunRecord(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", runRecordLogName)
	orig := runRecordLogPath
	runRecordLogPath = func() string { return path }
	t.Cleanup(func() { runRecordLogPath = orig })
	return path
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("run record not written: %v", err)
	}
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("record %q is not JSON: %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

// An invocation that fails before any pipeline exists still leaves one record.
func TestRunRecord_EarlyFailureAppendsOneRecord(t *testing.T) {
	path := redirectRunRecord(t)
	origConfigLoad := runConfigLoad
	t.Cleanup(func() { runConfigLoad = origConfigLoad })
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return nil, errors.New("no config file found") }

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("no run subcommand: %v", err)
	}
	origChange := runF.change
	t.Cleanup(func() { runF.change = origChange })
	runF.change = "sweep"
	if runErr := cmd.RunE(cmd, []string{"pr", "12"}); runErr == nil {
		t.Fatal("want an error")
	}

	recs := readRecords(t, path)
	if len(recs) != 1 {
		t.Fatalf("want one record, got %d: %v", len(recs), recs)
	}
	r := recs[0]
	if r["entity_type"] != "pr" || r["entity_id"] != "12" || r["change"] != "sweep" || r["path"] != "early" ||
		r["outcome"] != "error" || r["stage"] != "config" || r["pr"] != float64(12) {
		t.Errorf("early-failure record = %v", r)
	}
	for _, k := range []string{"duration_ms", "content_hash_changed", "anchor_written", "ts"} {
		if _, ok := r[k]; !ok {
			t.Errorf("record missing %q: %v", k, r)
		}
	}
}

// A failure the pipeline already logged is not recorded a second time.
func TestRecordEarlyFailure_SkipsPipelineLoggedAndThread(t *testing.T) {
	var buf bytes.Buffer
	start := time.Now()
	recordEarlyFailure(&buf, "pr", "1", gather.ChangeSweep, start, pipeline.TagStage(pipeline.StageGather, errors.New("boom")))
	recordEarlyFailure(&buf, "thread", "t1", gather.ChangeSweep, start, errors.New("boom"))
	if buf.Len() != 0 {
		t.Fatalf("expected no record, got %s", buf.String())
	}
	recordEarlyFailure(&buf, "issue", "T-1", gather.ChangeSweep, start, errors.New("list xrefs: boom"))
	if !strings.Contains(buf.String(), `"path":"early"`) {
		t.Fatalf("untagged issue failure not recorded: %q", buf.String())
	}
}

// An unconfigured-queries Jira-style ticket with no linked PR leaves a noop
// record, and a successful run appends records the file carries across runs.
func TestRunRecord_IssueWithNoLinkedPRRecordsNoop(t *testing.T) {
	path := redirectRunRecord(t)
	origConfigLoad, origStoreOpen := runConfigLoad, runStoreOpen
	t.Cleanup(func() { runConfigLoad, runStoreOpen = origConfigLoad, origStoreOpen })
	st := store.OpenForTest(t)
	runConfigLoad = func(ctx context.Context) (*config.Config, error) {
		cfg := issueTestCfg()
		cfg.TicketPatterns = []string{"PROJ-[0-9]+"}
		return cfg, nil
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	origChange := runF.change
	t.Cleanup(func() { runF.change = origChange })
	runF.change = "changed"

	cmd, _, _ := rootCmd.Find([]string{"run"})
	if err := cmd.RunE(cmd, []string{"issue", "PROJ-1"}); err != nil {
		t.Fatalf("run issue: %v", err)
	}
	recs := readRecords(t, path)
	if len(recs) != 1 || recs[0]["outcome"] != "noop" || recs[0]["entity_type"] != "issue" || recs[0]["change"] != "changed" {
		t.Fatalf("want one issue noop record, got %v", recs)
	}
}
