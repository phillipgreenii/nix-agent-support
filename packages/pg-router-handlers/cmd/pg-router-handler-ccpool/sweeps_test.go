package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// pg2-fx5x0l.1: every runSweeps pass is timed and recorded as one sweep_pass
// events.jsonl row (role, pass, elapsed_ms, found) plus a "sweeps done" log line,
// without changing what the passes do.
func TestRunSweeps_recordsOneSweepPassRowPerPass(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "") // default-pool pass would build a real runner
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}},
	}}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lw.Close() }()
	deps := executor.Deps{CC: cc, BD: br, LockDir: t.TempDir(), Log: lw}
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{}}
	cfg := config.Config{SessionPrefix: "pg-router-"}

	msgs := captureSlog(t, func() { runSweeps(context.Background(), "dispatch", role, cfg, deps) })

	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-done" {
		t.Errorf("behavior must be unchanged: closed=%v", cc.Closed)
	}
	if n := countMsgs(msgs, "sweeps done"); n != 4 {
		t.Errorf("want 4 'sweeps done' log lines, got %d (%v)", n, msgs)
	}
	_ = lw.Close()
	stateDir := filepath.Dir(logPath)
	recs := eventRecords(t, stateDir, "sweep_pass")
	want := []string{"closed_bead_reconcile", "orphan_reconcile", "worktree_sweep", "lock_gc"}
	if len(recs) != len(want) {
		t.Fatalf("want %d sweep_pass rows, got %d: %v", len(want), len(recs), recs)
	}
	for i, r := range recs {
		if r["pass"] != want[i] || r["role"] != "worker" || r["who"] != "dispatch" {
			t.Errorf("row %d = %v, want pass=%s role=worker who=dispatch", i, r, want[i])
		}
		if _, ok := r["elapsed_ms"].(float64); !ok {
			t.Errorf("row %d elapsed_ms missing or not a number: %v", i, r)
		}
		if _, ok := r["found"].(float64); !ok {
			t.Errorf("row %d found missing or not a number: %v", i, r)
		}
	}
	if recs[0]["found"].(float64) != 1 {
		t.Errorf("closed_bead_reconcile found = %v, want 1", recs[0]["found"])
	}
}

// A role without a ccpool block runs no pass, so it records nothing; a nil event
// log is tolerated.
func TestRunSweeps_noCCPoolRecordsNothingAndNilLogIsFine(t *testing.T) {
	msgs := captureSlog(t, func() {
		runSweeps(context.Background(), "dispatch", roles.Role{Name: "cmd"}, config.Config{}, executor.Deps{})
	})
	if countMsgs(msgs, "sweeps done") != 0 {
		t.Errorf("no ccpool block must record nothing: %v", msgs)
	}
	t.Setenv("CCPOOL_POOL", "")
	runSweeps(context.Background(), "postStartup", roles.Role{Name: "w", CCPool: &roles.CCPoolConfig{}}, config.Config{},
		executor.Deps{CC: &fakeCC{}, BD: fakeBR{}, LockDir: t.TempDir()}) // Log nil: must not panic
}

func countMsgs(msgs []string, m string) int {
	n := 0
	for _, s := range msgs {
		if s == m {
			n++
		}
	}
	return n
}
