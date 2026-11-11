package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// countingRecorder stands in for the recorder-backed Exister: it counts every
// request the reap sweep sends it and answers per transcript path.
type countingRecorder struct {
	present map[string]bool
	err     error
	reqs    int
}

func (c *countingRecorder) Exists(p string) bool { ok, _ := c.ExistsChecked(p); return ok }
func (c *countingRecorder) ExistsChecked(p string) (bool, error) {
	c.reqs++
	return c.present[p], c.err
}

func probeFixture(t *testing.T, rec *countingRecorder) *deadEnv {
	t.Helper()
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "a", state: store.Idle, ageSec: 600},
		deadRow{id: "b", state: store.Idle, ageSec: 600})
	e.s.d.Exister = rec
	return e
}

func TestReapOriginalsProbe_secondSweepOverUnchangedDeadRowsAsksNothing(t *testing.T) {
	rec := &countingRecorder{present: map[string]bool{"/p/a.jsonl": true, "/p/b.jsonl": true}}
	e := probeFixture(t, rec)
	e.reap(t)
	if rec.reqs != 2 {
		t.Fatalf("first sweep sent %d requests, want 2 (one per dead row)", rec.reqs)
	}
	e.reap(t)
	if rec.reqs != 2 {
		t.Fatalf("second sweep sent %d total requests, want still 2", rec.reqs)
	}
	for _, id := range []string{"a", "b"} {
		if !e.exists(t, id) {
			t.Fatalf("row %s was dropped", id)
		}
	}
}

func TestReapOriginalsProbe_askedAgainAfterTTL(t *testing.T) {
	rec := &countingRecorder{present: map[string]bool{"/p/a.jsonl": true, "/p/b.jsonl": true}}
	e := probeFixture(t, rec)
	e.reap(t)
	e.clock.t = e.clock.t.Add(originalsProbeTTL + time.Minute)
	rec.present["/p/a.jsonl"] = false // the originals are gone now
	e.reap(t)
	if rec.reqs != 4 {
		t.Fatalf("requests = %d, want 4 after the TTL", rec.reqs)
	}
	if e.exists(t, "a") {
		t.Fatal("row a whose originals are gone was kept")
	}
	if !e.exists(t, "b") {
		t.Fatal("row b was pruned")
	}
}

func TestReapOriginalsProbe_absentAnswerIsNotRemembered(t *testing.T) {
	rec := &countingRecorder{present: map[string]bool{}}
	e := probeFixture(t, rec)
	// Older rows are not fresh-starting, so absent originals prune them.
	e.reap(t)
	if e.exists(t, "a") || e.exists(t, "b") {
		t.Fatal("phantom rows were kept")
	}
	cached, found, err := e.st.OriginalsConfirmedAt(context.Background(), "a", "/p/a.jsonl")
	if err != nil || found || cached != 0 {
		t.Fatalf("a negative answer was stored: %d %v %v", cached, found, err)
	}
}

func TestReapOriginalsProbe_outageNeverPrunesAndIsNotCached(t *testing.T) {
	rec := &countingRecorder{err: errors.New("daemon offline")}
	e := probeFixture(t, rec)
	if err := e.s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap = %v; an outage is a single WARN, not a failure", err)
	}
	for _, id := range []string{"a", "b"} {
		if !e.exists(t, id) {
			t.Fatalf("row %s pruned during an outage", id)
		}
	}
	// Recovery: the next sweep asks again (nothing was cached) and keeps the rows.
	rec.err = nil
	rec.present = map[string]bool{"/p/a.jsonl": true, "/p/b.jsonl": true}
	before := rec.reqs
	e.reap(t)
	if rec.reqs <= before {
		t.Fatal("the recovered sweep did not ask the recorder")
	}
}

func TestReapOriginalsProbe_changedTranscriptPathInvalidates(t *testing.T) {
	rec := &countingRecorder{present: map[string]bool{"/p/a.jsonl": true, "/p/b.jsonl": true}}
	e := probeFixture(t, rec)
	e.reap(t)
	if err := e.st.SetOriginalsConfirmed(context.Background(), "a", "/old/path.jsonl", e.clock.t.Unix()); err != nil {
		t.Fatal(err)
	}
	before := rec.reqs
	e.reap(t)
	if rec.reqs != before+1 {
		t.Fatalf("requests = %d, want %d (only row a re-probed)", rec.reqs, before+1)
	}
}
