package hygiene

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"

	"github.com/phillipgreenii/pg-router/internal/handlers/eventlog"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
)

// recordSweepPass appends one sweep_pass events.jsonl row (who, role, pass,
// elapsed_ms, found) and logs one "sweeps done" line.
func TestRecordSweepPass_appendsOneRow(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lw.Close() }()
	msgs := dtest.CaptureSlog(t, func() {
		recordSweepPass("dispatch", "worker", "orphan_reconcile", 2, 5*time.Millisecond, executor.Deps{Log: lw})
	})
	if n := countMsgs(msgs, "sweeps done"); n != 1 {
		t.Errorf("want 1 'sweeps done' log line, got %d (%v)", n, msgs)
	}
	_ = lw.Close()
	recs := dtest.EventRecords(t, filepath.Dir(logPath), "sweep_pass")
	if len(recs) != 1 {
		t.Fatalf("want 1 sweep_pass row, got %d: %v", len(recs), recs)
	}
	r := recs[0]
	if r["who"] != "dispatch" || r["role"] != "worker" || r["pass"] != "orphan_reconcile" {
		t.Errorf("row = %v", r)
	}
	if r["elapsed_ms"] != float64(5) || r["found"] != float64(2) {
		t.Errorf("row elapsed_ms/found = %v/%v, want 5/2", r["elapsed_ms"], r["found"])
	}
}

// A nil event log is tolerated.
func TestRecordSweepPass_nilLogIsFine(t *testing.T) {
	recordSweepPass("janitor", "w", "lock_gc", 0, 0, executor.Deps{})
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
