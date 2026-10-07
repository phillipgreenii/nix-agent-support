package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Tests for the richer events.jsonl "dispatch" row (bead pg2-n7da9): enqueued_at,
// started_at, duration_ms, event_type and change join the existing keys, so a
// per-listener wait-vs-run table can be computed from the log alone.

// sleepHandler is a fakeHandler whose Dispatch takes d, so duration_ms is a
// measurable, non-trivial number.
type sleepHandler struct {
	*fakeHandler
	d time.Duration
}

func (h sleepHandler) Dispatch(ctx context.Context, role roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	time.Sleep(h.d)
	return h.fakeHandler.Dispatch(ctx, role, evt)
}

func dispatchRows(t *testing.T, logPath string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range readEventLog(t, logPath) {
		if r["kind"] == "dispatch" {
			out = append(out, r)
		}
	}
	return out
}

func TestDispatchRow_CarriesTimelineAndIdentityFromTheOffer(t *testing.T) {
	cfg := fastCfg(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = sleepHandler{fakeHandler: &fakeHandler{}, d: 30 * time.Millisecond}
	o.Log = lw
	l := o.NewListener(context.Background(), workerRole(o))

	enqueued := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	started := time.Date(2026, 10, 7, 4, 0, 27, 0, time.UTC)
	evt := eventqueue.Event{ID: "ZR/repo#9@41", Type: "pr.reconcile", At: enqueued, Payload: map[string]any{"id": "ZR/repo#9"}}
	if res := l.Offer(eventqueue.Offering{ID: "dsp-1", Event: evt, StartedAt: started}); !res.Accepted {
		t.Fatalf("Offer = %+v, want accepted", res)
	}

	rows := dispatchRows(t, logPath)
	if len(rows) != 1 {
		t.Fatalf("dispatch rows = %v, want exactly one", rows)
	}
	r := rows[0]
	// Existing keys are untouched.
	if r["role"] != workerRole(o).Name || r["bead"] != "ZR/repo#9" || r["msg"] != "dispatch result" || r["level"] != "info" {
		t.Fatalf("existing keys changed: %v", r)
	}
	if r["event_type"] != "pr.reconcile" || r["change"] != "ZR/repo#9@41" {
		t.Fatalf("event_type/change = %v/%v", r["event_type"], r["change"])
	}
	if r["enqueued_at"] != "2026-10-07T04:00:00Z" || r["started_at"] != "2026-10-07T04:00:27Z" {
		t.Fatalf("enqueued_at/started_at = %v/%v, want the event's At and the offer's StartedAt", r["enqueued_at"], r["started_at"])
	}
	dur, ok := r["duration_ms"].(float64) // JSON numbers decode as float64
	if !ok || dur < 30 || dur > 5000 {
		t.Fatalf("duration_ms = %v, want a whole-millisecond run time of at least the handler's 30ms", r["duration_ms"])
	}
	if dur != float64(int64(dur)) {
		t.Fatalf("duration_ms = %v, want whole milliseconds", dur)
	}
}

func TestDispatchRow_FailedDispatchStillCarriesTheTimeline(t *testing.T) {
	cfg := fastCfg(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = sleepHandler{fakeHandler: &fakeHandler{err: fmt.Errorf("boom")}, d: 5 * time.Millisecond}
	o.Log = lw
	l := o.NewListener(context.Background(), workerRole(o))
	evt := eventqueue.Event{ID: "c1", Type: "pr.changed", At: time.Now().Add(-time.Minute), Payload: map[string]any{"id": "b"}}
	l.Offer(eventqueue.Offering{ID: "dsp-1", Event: evt}) // no StartedAt: the row falls back to the wall start

	rows := dispatchRows(t, logPath)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	r := rows[0]
	if r["level"] != "warn" || r["error"] == nil {
		t.Fatalf("failure row lost its warn/error: %v", r)
	}
	if r["event_type"] != "pr.changed" || r["change"] != "c1" || r["started_at"] == nil || r["duration_ms"] == nil || r["enqueued_at"] == nil {
		t.Fatalf("failure row missing timeline fields: %v", r)
	}
}

func TestDispatchRow_RunOneCarriesTheTimelineToo(t *testing.T) {
	cfg := fastCfg(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Log = lw
	evt := eventqueue.Event{ID: "x1", Type: "work.ready", At: time.Now(), Payload: map[string]any{"id": "zr-w"}}
	if err := o.RunOne(context.Background(), workerRole(o), evt); err != nil {
		t.Fatal(err)
	}
	rows := dispatchRows(t, logPath)
	if len(rows) != 1 || rows[0]["event_type"] != "work.ready" || rows[0]["change"] != "x1" || rows[0]["duration_ms"] == nil {
		t.Fatalf("run-role row = %v", rows)
	}
}

func TestDispatchMeta_OmitsZeroInstants(t *testing.T) {
	fields := map[string]any{}
	dispatchMeta{EventType: "t"}.addTo(fields)
	if _, ok := fields["enqueued_at"]; ok {
		t.Fatalf("zero enqueued_at must be omitted, not written as year 1: %v", fields)
	}
	if _, ok := fields["started_at"]; ok {
		t.Fatalf("zero started_at must be omitted: %v", fields)
	}
	if _, ok := fields["duration_ms"]; ok {
		t.Fatalf("duration_ms without a start is meaningless and must be omitted: %v", fields)
	}
	if fields["event_type"] != "t" {
		t.Fatalf("fields = %v", fields)
	}
}
