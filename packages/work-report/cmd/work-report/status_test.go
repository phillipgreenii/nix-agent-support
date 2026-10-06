package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

func statusAt(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// statusStore returns a store path under a fresh temp dir; the store is
// created and populated through the store API, then closed.
func statusStore(t *testing.T, populate func(*store.Store)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if populate != nil {
		populate(st)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeStatus(t *testing.T, out string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, out)
	}
	return v
}

func TestStatusEmptyStoreExitsZeroWithEmptySources(t *testing.T) {
	isolate(t)
	path := statusStore(t, nil)
	out, err := runCLI(t, "status", "--output", "json", "--store", path)
	if err != nil {
		t.Fatalf("status on an empty store: %v", err)
	}
	v := decodeStatus(t, out)
	srcs, ok := v["sources"].([]any)
	if !ok || len(srcs) != 0 {
		t.Errorf("sources = %#v, want an empty array", v["sources"])
	}
	if v["last_report"] != nil {
		t.Errorf("last_report = %#v, want null", v["last_report"])
	}
	if size, _ := v["store_size_bytes"].(float64); size <= 0 {
		t.Errorf("store_size_bytes = %v, want > 0", v["store_size_bytes"])
	}

	human, err := runCLI(t, "status", "--store", path)
	if err != nil {
		t.Fatalf("human status on an empty store: %v", err)
	}
	if !strings.Contains(human, "none yet") || !strings.Contains(human, "last report: none") {
		t.Errorf("human output lacks the empty-store lines:\n%s", human)
	}
}

func TestStatusReportsDegradedLastPullAndEarlierSuccess(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	path := statusStore(t, func(st *store.Store) {
		pull := func(status, reason string, end time.Time) {
			t.Helper()
			if err := st.RecordPull(ctx, store.PullRow{
				Source: "example-backend", Since: statusAt(2026, 3, 1, 0, 0), Before: statusAt(2026, 3, 2, 0, 0),
				StartedAt: end.Add(-time.Minute), EndedAt: end, Status: status, Reason: reason,
			}); err != nil {
				t.Fatal(err)
			}
		}
		pull("succeeded", "", statusAt(2026, 3, 2, 10, 0))
		pull("degraded", "backend timed out", statusAt(2026, 3, 2, 11, 0))

		for i, at := range []time.Time{statusAt(2026, 3, 1, 1, 0), statusAt(2026, 3, 1, 5, 0)} {
			if _, err := st.Append(ctx, store.Entry{
				ID: "e" + string(rune('1'+i)), ExternalID: "x", SourceID: "example-backend", Type: "change",
				OccurredAt: at, Summary: "s", Fields: json.RawMessage(`{}`),
			}, statusAt(2026, 3, 2, 12, 0)); err != nil {
				t.Fatal(err)
			}
		}
	})

	out, err := runCLI(t, "status", "--output", "json", "--store", path)
	if err != nil {
		t.Fatal(err)
	}
	v := decodeStatus(t, out)
	srcs := v["sources"].([]any)
	if len(srcs) != 1 {
		t.Fatalf("sources = %#v, want one", srcs)
	}
	s := srcs[0].(map[string]any)
	last := s["last_outcome"].(map[string]any)
	if last["status"] != "degraded" || last["reason"] != "backend timed out" || last["at"] != "2026-03-02T11:00:00Z" {
		t.Errorf("last_outcome = %#v", last)
	}
	if s["last_success_at"] != "2026-03-02T10:00:00Z" {
		t.Errorf("last_success_at = %v, want the earlier success", s["last_success_at"])
	}
	if s["entry_count"] != float64(2) {
		t.Errorf("entry_count = %v, want 2", s["entry_count"])
	}
	if s["newest_occurred_at"] != "2026-03-01T05:00:00Z" {
		t.Errorf("newest_occurred_at = %v", s["newest_occurred_at"])
	}
	if v["last_report"] != nil {
		t.Errorf("last_report = %#v, want null before any report", v["last_report"])
	}

	human, err := runCLI(t, "status", "--store", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"example-backend", "degraded (backend timed out)", "entries:            2"} {
		if !strings.Contains(human, want) {
			t.Errorf("human output lacks %q:\n%s", want, human)
		}
	}
}

func TestStatusNeverPulledSourceHasNullTimes(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	path := statusStore(t, func(st *store.Store) {
		if _, err := st.Append(ctx, store.Entry{
			ID: "e1", ExternalID: "x", SourceID: "example-backend", Type: "change",
			OccurredAt: statusAt(2026, 3, 1, 1, 0), Summary: "s", Fields: json.RawMessage(`{}`),
		}, statusAt(2026, 3, 2, 12, 0)); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordPull(ctx, store.PullRow{
			Source: "other-backend", StartedAt: statusAt(2026, 3, 2, 8, 0), EndedAt: statusAt(2026, 3, 2, 9, 0),
			Status: "disabled", Reason: "disabled by config",
		}); err != nil {
			t.Fatal(err)
		}
	})
	out, err := runCLI(t, "status", "--output", "json", "--store", path)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, e := range decodeStatus(t, out)["sources"].([]any) {
		m := e.(map[string]any)
		by[m["source"].(string)] = m
	}
	ex := by["example-backend"]
	if ex["last_success_at"] != nil || ex["last_outcome"].(map[string]any)["at"] != nil {
		t.Errorf("a never-pulled source must render null times: %#v", ex)
	}
	if ex["entry_count"] != float64(1) {
		t.Errorf("entry_count = %v", ex["entry_count"])
	}
	ot := by["other-backend"]
	if ot["newest_occurred_at"] != nil || ot["entry_count"] != float64(0) {
		t.Errorf("a source with no entries must have null newest_occurred_at and count 0: %#v", ot)
	}
}

func TestStatusLastReportPresentAfterStoredReport(t *testing.T) {
	isolate(t)
	path := statusStore(t, func(st *store.Store) {
		if err := st.RecordReport(context.Background(), store.ReportRow{
			Since: statusAt(2026, 3, 1, 0, 0), Kind: "activity",
			GeneratedAt: statusAt(2026, 3, 2, 14, 0), Generator: "test:1", Content: "body",
		}); err != nil {
			t.Fatal(err)
		}
	})
	out, err := runCLI(t, "status", "--output", "json", "--store", path)
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := decodeStatus(t, out)["last_report"].(map[string]any)
	if !ok {
		t.Fatalf("last_report missing:\n%s", out)
	}
	if rep["kind"] != "activity" || rep["generated_at"] != "2026-03-02T14:00:00Z" ||
		rep["since"] != "2026-03-01T00:00:00Z" || rep["before"] != nil {
		t.Errorf("last_report = %#v", rep)
	}
	human, err := runCLI(t, "status", "--store", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "last report: activity") || !strings.Contains(human, "before open") {
		t.Errorf("human output lacks the last report line:\n%s", human)
	}
}

func TestStatusRejectsUnsupportedOutput(t *testing.T) {
	isolate(t)
	if _, err := runCLI(t, "status", "--output", "pg-router", "--store", statusStore(t, nil)); err == nil {
		t.Error("--output pg-router must be rejected by status")
	}
}
