package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	e := Entry{
		RunID: "run-1", Command: "report", Classifier: "cli:claude",
		Since: "2026-08-01", Until: "2026-08-02",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		CandidatesIn: 10, Calls: 1, Batches: 1, USD: 0.05, Done: false,
	}
	if err := Append(path, e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].RunID != "run-1" || got[0].USD != 0.05 {
		t.Errorf("got %+v, want one entry for run-1 at usd=0.05", got)
	}
}

// TestLatestCollapsesToTheMostRecentSnapshotPerRun is the property a killed
// run depends on: several snapshots get appended for ONE run as it
// progresses (one per batch), and a reader must see only the newest one, not
// sum them (each snapshot already carries the run's cumulative total).
func TestLatestCollapsesToTheMostRecentSnapshotPerRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	base := time.Now().UTC()
	for i, calls := range []int{1, 2, 3} {
		e := Entry{
			RunID: "run-1", Command: "report", Classifier: "cli:claude",
			StartedAt: base, UpdatedAt: base.Add(time.Duration(i) * time.Second),
			Calls: calls, USD: float64(calls) * 0.01, Done: calls == 3,
		}
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Load returned %d raw entries, want 3 (one per Append)", len(entries))
	}
	latest := Latest(entries)
	if len(latest) != 1 {
		t.Fatalf("Latest collapsed to %d runs, want 1", len(latest))
	}
	if latest[0].Calls != 3 || latest[0].USD != 0.03 || !latest[0].Done {
		t.Errorf("latest snapshot = %+v, want the LAST appended one (calls=3 usd=0.03 done=true)", latest[0])
	}
}

// TestLatestOfAKilledRunReportsDoneFalse is the ledger's answer to "did that
// run finish": the last snapshot a killed run managed to write is written
// with Done=false, and nothing overwrites it with a final Done=true because
// the run never reached its own end.
func TestLatestOfAKilledRunReportsDoneFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := Append(path, Entry{RunID: "killed", Classifier: "cli:claude", Calls: 2, USD: 0.02, Done: false}); err != nil {
		t.Fatal(err)
	}
	entries, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	latest := Latest(entries)
	if len(latest) != 1 || latest[0].Done {
		t.Errorf("latest = %+v, want exactly one entry with Done=false", latest)
	}
}

func TestAverageCostPerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	entries := []Entry{
		{RunID: "r1", Classifier: "cli:claude", Calls: 2, USD: 0.10, Done: true},
		{RunID: "r2", Classifier: "cli:claude", Calls: 3, USD: 0.20, Done: true},
		{RunID: "r3", Classifier: "baseline", Calls: 5, USD: 0, Done: true},
	}
	for _, e := range entries {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	avg, calls, ok := AverageCostPerCall(loaded, "cli:claude")
	if !ok {
		t.Fatal("expected historical cost data for cli:claude")
	}
	if calls != 5 {
		t.Errorf("calls=%d, want 5 (2+3 across two runs)", calls)
	}
	wantAvg := 0.30 / 5.0
	if diff := avg - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("avg=%v, want %v", avg, wantAvg)
	}

	if _, _, ok := AverageCostPerCall(loaded, "cli:something-never-run"); ok {
		t.Error("a classifier with no recorded calls must report ok=false, not a fabricated average")
	}
}

func TestLoadOfMissingLedgerIsNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load of a missing ledger = %v, want an errors.Is(err, os.ErrNotExist) error", err)
	}
}

func TestLoadToleratesATruncatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := Append(path, Entry{RunID: "good", Classifier: "cli:claude", Calls: 1}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"run_id":"trunc`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load must tolerate a truncated LAST line, got error: %v", err)
	}
	if len(got) != 1 || got[0].RunID != "good" {
		t.Errorf("got %+v, want only the complete entry", got)
	}
}
