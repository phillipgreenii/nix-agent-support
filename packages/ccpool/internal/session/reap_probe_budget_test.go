package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// stubRecorder is the recorder-backed Exister with a request counter: it
// answers per transcript path, and after hangAfter requests it behaves like a
// recorder that never answers in time (blocks for hang, then times out).
type stubRecorder struct {
	present map[string]bool
	hang    time.Duration
	reqs    int
}

func (r *stubRecorder) Exists(p string) bool { ok, _ := r.ExistsChecked(p); return ok }
func (r *stubRecorder) ExistsChecked(p string) (bool, error) {
	r.reqs++
	if r.hang > 0 {
		time.Sleep(r.hang)
		return false, fmt.Errorf("the recorder is not available: %w", context.DeadlineExceeded)
	}
	return r.present[p], nil
}

// captureLog routes the default slog logger to a buffer for one test and
// returns a function counting the lines at a level.
func captureLog(t *testing.T) func(level string) int {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func(level string) int { return strings.Count(buf.String(), "level="+level+" ") }
}

// probeRows builds n dead idle rows; with onDisk each row's transcript path
// names a real file in a temp dir, otherwise a path that does not exist.
func probeRows(t *testing.T, n int, onDisk bool) []deadRow {
	t.Helper()
	dir := t.TempDir()
	rows := make([]deadRow, n)
	for i := range rows {
		id := fmt.Sprintf("r%02d", i)
		p := filepath.Join(dir, "missing-"+id+".jsonl")
		if onDisk {
			p = filepath.Join(dir, id+".jsonl")
			if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		rows[i] = deadRow{id: id, state: store.Idle, ageSec: 600, path: p}
	}
	return rows
}

func presentAll(rows []deadRow) map[string]bool {
	m := map[string]bool{}
	for _, r := range rows {
		m[r.path] = true
	}
	return m
}

func TestReapProbeBudget_existingPathsAskTheRecorderNothing(t *testing.T) {
	rows := probeRows(t, 6, true)
	rec := &stubRecorder{}
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = rec
	e.reap(t)
	e.reap(t)
	if rec.reqs != 0 {
		t.Fatalf("recorder got %d requests for %d rows whose transcripts exist; want 0", rec.reqs, len(rows))
	}
	for _, r := range rows {
		if !e.exists(t, r.id) {
			t.Errorf("row %s with an existing transcript was dropped", r.id)
		}
		if _, found, err := e.st.OriginalsConfirmedAt(context.Background(), r.id, r.path); err != nil || !found {
			t.Errorf("row %s: stat-positive was not remembered (%v, %v)", r.id, found, err)
		}
	}
}

// A transcript on disk that the recorder has not ingested is ErrNotFound there
// ("not resumable") and used to prune the row; the stat-first order keeps it.
func TestReapProbeBudget_onDiskButNotIngestedIsKept(t *testing.T) {
	rows := probeRows(t, 2, true)
	rec := &stubRecorder{present: map[string]bool{}} // the recorder knows none of them
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = rec
	e.reap(t)
	if rec.reqs != 0 {
		t.Fatalf("requests = %d; want 0", rec.reqs)
	}
	for _, r := range rows {
		if !e.exists(t, r.id) {
			t.Errorf("row %s whose transcript is on disk was pruned", r.id)
		}
	}
}

func TestReapProbeBudget_missingPathsAskOncePerRowThenAreRemembered(t *testing.T) {
	rows := probeRows(t, 5, false)
	rec := &stubRecorder{present: presentAll(rows)} // the recorder still has them
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = rec
	e.reap(t)
	if rec.reqs != len(rows) {
		t.Fatalf("first pass requests = %d, want %d (one per row)", rec.reqs, len(rows))
	}
	e.reap(t)
	if rec.reqs != len(rows) {
		t.Fatalf("second pass requests = %d, want still %d (positives cached)", rec.reqs, len(rows))
	}
}

func TestReapProbeBudget_hungRecorderIsAskedOnceAndWarnsOnce(t *testing.T) {
	count := captureLog(t)
	rows := probeRows(t, 8, false)
	rec := &stubRecorder{hang: 20 * time.Millisecond}
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = rec
	if err := e.s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap = %v; a hung recorder must not fail the pass", err)
	}
	if rec.reqs != 1 {
		t.Errorf("requests = %d, want 1 (the breaker stops probing after the first timeout)", rec.reqs)
	}
	if n := count("ERROR"); n != 0 {
		t.Errorf("ERROR lines = %d, want 0", n)
	}
	if n := count("WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want exactly 1", n)
	}
	for _, r := range rows {
		if !e.exists(t, r.id) {
			t.Errorf("row %s was pruned while the recorder could not answer", r.id)
		}
	}
	// A recovered recorder is asked again (nothing negative or failed was cached).
	rec.hang, rec.present = 0, presentAll(rows)
	e.reap(t)
	if rec.reqs != 1+len(rows) {
		t.Errorf("requests after recovery = %d, want %d", rec.reqs, 1+len(rows))
	}
}

func TestReapProbeBudget_degradedAnswerErrorPrunesNothing(t *testing.T) {
	rows := probeRows(t, 3, false)
	ex := &perPathExister{errs: map[string]error{}}
	for _, r := range rows {
		ex.errs[r.path] = errors.New("recorder is behind")
	}
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = ex
	e.reap(t)
	for _, r := range rows {
		if !e.exists(t, r.id) {
			t.Errorf("row %s pruned on a degraded answer", r.id)
		}
	}
}

func TestReapProbeBudget_phantomIsPrunedWhenRecorderAnswersMissing(t *testing.T) {
	rows := probeRows(t, 3, false)
	rec := &stubRecorder{present: map[string]bool{rows[0].path: true}}
	e := deadFixture(t, time.Unix(10_000_000, 0), rows...)
	e.s.d.Exister = rec
	e.reap(t)
	if !e.exists(t, rows[0].id) {
		t.Error("row the recorder confirmed was pruned")
	}
	for _, r := range rows[1:] {
		if e.exists(t, r.id) {
			t.Errorf("phantom row %s was kept", r.id)
		}
	}
	if rec.reqs != len(rows) {
		t.Errorf("requests = %d, want %d", rec.reqs, len(rows))
	}
}
