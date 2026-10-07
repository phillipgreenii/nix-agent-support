package session

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// The dead-needs_input backstop (ADR 0086): a `needs_input` row whose tmux
// session is gone and whose last activity is older than the TTL is closed
// (non-purge, reason dead_needs_input_ttl). These tests pin its safety (never a
// live row, never a young row, never a non-needs_input row, never a delete),
// its idempotence, and the two metric corrections that go with it.

const deadTTL = 30 * 24 * time.Hour

// mutClock is a settable store clock, so a test can place close_reason's
// closed_at at a chosen instant.
type mutClock struct{ t time.Time }

func (c *mutClock) Now() time.Time { return c.t }

type deadRow struct {
	id       string
	state    store.State
	ageSec   int64 // seconds since last activity at `now`
	live     bool  // tmux reports the session
	closedAt int64 // pre-stamped closed_at (0 = never closed)
}

type deadEnv struct {
	s     *Service
	st    *store.Store
	tm    *reapTmux
	clock *mutClock
}

func deadFixture(t *testing.T, now time.Time, rows ...deadRow) *deadEnv {
	t.Helper()
	ctx := context.Background()
	clk := &mutClock{t: now}
	st, err := store.Open(":memory:", clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tm := &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}
	for _, r := range rows {
		if err := st.Insert(ctx, store.Session{
			ExternalID: r.id, ClaudeSessionID: "csid-" + r.id, TranscriptPath: "/p/" + r.id + ".jsonl",
			State: r.state, TmuxSession: "cc-" + r.id, LastActivityAt: now.Unix() - r.ageSec,
			ClosedAt: r.closedAt,
		}); err != nil {
			t.Fatalf("Insert %s: %v", r.id, err)
		}
		tm.live["cc-"+r.id] = r.live
	}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true}, Now: clk.Now})
	return &deadEnv{s: s, st: st, tm: tm, clock: clk}
}

func (e *deadEnv) row(t *testing.T, id string) store.Session {
	t.Helper()
	r, ok, err := e.st.GetByExternalID(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetByExternalID(%s): ok=%v err=%v", id, ok, err)
	}
	return r
}

func (e *deadEnv) reap(t *testing.T, opts ...ReapOption) {
	t.Helper()
	if err := e.s.Reap(context.Background(), 6, time.Hour, opts...); err != nil {
		t.Fatalf("Reap: %v", err)
	}
}

func sec(d time.Duration) int64 { return int64(d / time.Second) }

func TestReapDeadNeedsInput_closesPastTTLWithoutPurgeOrTeardown(t *testing.T) {
	withReapSpies(t, func(closures *[]string, _ *int, _ *[]int64) {
		now := time.Unix(10_000_000, 0)
		e := deadFixture(t, now, deadRow{id: "stale", state: store.NeedsInput, ageSec: sec(deadTTL) + 60})
		e.reap(t, WithDeadNeedsInputTTL(deadTTL))

		r := e.row(t, "stale") // the row is kept, not purged
		if r.CloseReason != "dead_needs_input_ttl" {
			t.Fatalf("close_reason = %q, want dead_needs_input_ttl", r.CloseReason)
		}
		if r.ClosedAt != now.Unix() {
			t.Errorf("closed_at = %d, want %d", r.ClosedAt, now.Unix())
		}
		if r.State != store.NeedsInput {
			t.Errorf("state = %q; a close must not fabricate a state (ADR 0015)", r.State)
		}
		if r.TranscriptPath != "/p/stale.jsonl" || r.ClaudeSessionID != "csid-stale" {
			t.Errorf("row identity/transcript must be untouched: %+v", r)
		}
		if len(e.tm.closed) != 0 {
			t.Errorf("a dead row has no tmux session to tear down; tmux closed = %v", e.tm.closed)
		}
		if len(*closures) != 1 || (*closures)[0] != "dead_needs_input_ttl" {
			t.Errorf("reap closure metric = %v, want exactly [dead_needs_input_ttl]", *closures)
		}
	})
}

func TestReapDeadNeedsInput_neverClosesWithinTTL(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "young", state: store.NeedsInput, ageSec: sec(deadTTL) - 60},
		deadRow{id: "exactly", state: store.NeedsInput, ageSec: sec(deadTTL)}, // strictly older than TTL only
		deadRow{id: "fresh", state: store.NeedsInput, ageSec: 5})
	e.reap(t, WithDeadNeedsInputTTL(deadTTL))
	for _, id := range []string{"young", "exactly", "fresh"} {
		if got := e.row(t, id).CloseReason; got != "" {
			t.Errorf("%s: close_reason = %q, want none (not older than the TTL)", id, got)
		}
	}
}

func TestReapDeadNeedsInput_neverClosesLiveRow(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	// Far older than the TTL, but the tmux session is alive: a human is
	// (potentially) attached. Neither this pass nor the idle pass may touch it.
	e := deadFixture(t, now, deadRow{id: "attended", state: store.NeedsInput, ageSec: 10 * sec(deadTTL), live: true})
	e.reap(t, WithDeadNeedsInputTTL(time.Hour*24))
	if got := e.row(t, "attended"); got.CloseReason != "" || got.ClosedAt != 0 {
		t.Fatalf("live needs_input row was closed: %+v", got)
	}
	if len(e.tm.closed) != 0 {
		t.Fatalf("tmux teardown happened for a live preserved row: %v", e.tm.closed)
	}
}

func TestReapDeadNeedsInput_neverClosesOtherStates(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	old := 10 * sec(deadTTL)
	e := deadFixture(t, now,
		deadRow{id: "idle", state: store.Idle, ageSec: old},
		deadRow{id: "working", state: store.Working, ageSec: old},
		deadRow{id: "errored", state: store.Errored, ageSec: old},
		deadRow{id: "ready", state: store.Ready, ageSec: old})
	e.reap(t, WithDeadNeedsInputTTL(deadTTL))
	for _, id := range []string{"idle", "working", "errored", "ready"} {
		if got := e.row(t, id).CloseReason; got != "" {
			t.Errorf("%s: close_reason = %q; only needs_input rows are in scope", id, got)
		}
	}
}

func TestReapDeadNeedsInput_disabledByDefaultAndByZero(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now, deadRow{id: "stale", state: store.NeedsInput, ageSec: 10 * sec(deadTTL)})
	e.reap(t) // no option
	e.reap(t, WithDeadNeedsInputTTL(0))
	e.reap(t, WithDeadNeedsInputTTL(-time.Hour))
	if got := e.row(t, "stale").CloseReason; got != "" {
		t.Fatalf("close_reason = %q; the backstop must be off unless a positive TTL is given", got)
	}
}

func TestReapDeadNeedsInput_isIdempotent(t *testing.T) {
	withReapSpies(t, func(closures *[]string, _ *int, _ *[]int64) {
		now := time.Unix(10_000_000, 0)
		e := deadFixture(t, now, deadRow{id: "stale", state: store.NeedsInput, ageSec: sec(deadTTL) + 60})
		e.reap(t, WithDeadNeedsInputTTL(deadTTL))
		first := e.row(t, "stale")

		e.clock.t = now.Add(10 * time.Minute) // a later sweep
		e.s.d.Now = e.clock.Now
		e.reap(t, WithDeadNeedsInputTTL(deadTTL))
		e.reap(t, WithDeadNeedsInputTTL(deadTTL))

		if got := e.row(t, "stale"); got.ClosedAt != first.ClosedAt || got.CloseReason != first.CloseReason {
			t.Errorf("a repeat sweep re-stamped the row: first=%+v now=%+v", first, got)
		}
		if len(*closures) != 1 {
			t.Errorf("closure metric fired %d times across three sweeps, want 1: %v", len(*closures), *closures)
		}
	})
}

// A closed row that was later RESUMED back to needs_input (last_activity_at
// moved past closed_at) is a human-awaited row again, and is closable again
// once it has then sat for the TTL.
func TestReapDeadNeedsInput_resumedRowIsAwaitingAgain(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	closedAt := now.Unix() - 2*sec(deadTTL)
	e := deadFixture(t, now,
		deadRow{id: "resumed-young", state: store.NeedsInput, ageSec: 3600, closedAt: closedAt},
		deadRow{id: "resumed-stale", state: store.NeedsInput, ageSec: sec(deadTTL) + 60, closedAt: closedAt - sec(deadTTL)})
	e.reap(t, WithDeadNeedsInputTTL(deadTTL))
	if got := e.row(t, "resumed-young"); got.ClosedAt != closedAt {
		t.Errorf("resumed-young was re-closed: %+v", got)
	}
	if got := e.row(t, "resumed-stale"); got.ClosedAt != now.Unix() || got.CloseReason != "dead_needs_input_ttl" {
		t.Errorf("resumed-stale must be closed again after sitting a full TTL: %+v", got)
	}
}

// The unlocked Pass 0 snapshot can be stale by the time the backstop runs: a
// session resumed (tmux live) or touched (fresh activity) in between must NOT be
// closed. closeStaleDeadNeedsInput re-reads under the per-row lock.
func TestReapDeadNeedsInput_revalidatesUnderLock(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "resumed", state: store.NeedsInput, ageSec: 5, live: true},
		deadRow{id: "touched", state: store.NeedsInput, ageSec: 5},
		deadRow{id: "moved-on", state: store.Working, ageSec: 5})
	stale := func(id string) store.Session {
		r := e.row(t, id)
		r.State = store.NeedsInput
		r.LastActivityAt = now.Unix() - 10*sec(deadTTL) // the stale snapshot claims it is old
		return r
	}
	snapshot := []store.Session{stale("resumed"), stale("touched"), stale("moved-on")}
	out, err := e.s.closeStaleDeadNeedsInput(context.Background(), snapshot, now, deadTTL)
	if err != nil {
		t.Fatalf("closeStaleDeadNeedsInput: %v", err)
	}
	for _, id := range []string{"resumed", "touched", "moved-on"} {
		if got := e.row(t, id); got.CloseReason != "" || got.ClosedAt != 0 {
			t.Errorf("%s was closed from a stale snapshot: %+v", id, got)
		}
	}
	if len(out) != 3 {
		t.Fatalf("returned %d rows, want 3", len(out))
	}
	// The returned slice carries the FRESH rows, so downstream metrics are not
	// computed from the stale snapshot.
	if out[1].LastActivityAt != now.Unix()-5 || out[2].State != store.Working {
		t.Errorf("returned rows must be the re-read ones: %+v", out)
	}
}

// ccpool_sessions_preserved_for_human must count a dead needs_input row (the 0.0
// it reported while one sat for weeks was the misleading signal), and stop
// counting it once it is closed.
func TestReapDeadNeedsInput_preservedMetricCountsDeadRowsUntilClosed(t *testing.T) {
	withReapSpies(t, func(_ *[]string, _ *int, preserved *[]int64) {
		now := time.Unix(10_000_000, 0)
		e := deadFixture(t, now,
			deadRow{id: "dead", state: store.NeedsInput, ageSec: 3600},
			deadRow{id: "attended", state: store.NeedsInput, ageSec: 3600, live: true},
			deadRow{id: "dead-idle", state: store.Idle, ageSec: 3600})
		e.reap(t) // no backstop
		if len(*preserved) != 1 || (*preserved)[0] != 2 {
			t.Fatalf("preserved gauge = %v, want one value 2 (one live + one dead needs_input)", *preserved)
		}

		// Past the TTL the backstop closes the dead row; the very same sweep
		// already reports without it.
		*preserved = nil
		e2 := deadFixture(t, now,
			deadRow{id: "dead", state: store.NeedsInput, ageSec: sec(deadTTL) + 60},
			deadRow{id: "attended", state: store.NeedsInput, ageSec: sec(deadTTL) + 60, live: true})
		e2.reap(t, WithDeadNeedsInputTTL(deadTTL))
		if len(*preserved) != 1 || (*preserved)[0] != 1 {
			t.Fatalf("preserved gauge after close = %v, want one value 1 (only the live row)", *preserved)
		}
	})
}

// ccpool_session_states{live="false",state="needs_input"} (the alert's signal)
// reads 1 for a dead awaiting row, and 0 once the row is closed, so the alert
// resolves instead of firing forever on a row ccpool has already dealt with.
func TestReapDeadNeedsInput_sessionStatesBucketClearsOnClose(t *testing.T) {
	orig := recordSessionStates
	t.Cleanup(func() { recordSessionStates = orig })
	var got []telemetry.SessionStateCount
	recordSessionStates = func(c []telemetry.SessionStateCount, _ []attribute.KeyValue) { got = c }
	bucket := func() int64 {
		for _, c := range got {
			if c.State == "needs_input" && !c.Live {
				return c.Count
			}
		}
		t.Fatal("no live=false needs_input bucket emitted")
		return -1
	}

	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now, deadRow{id: "dead", state: store.NeedsInput, ageSec: sec(deadTTL) + 60})

	e.reap(t) // backstop off: the row is awaiting a human
	if b := bucket(); b != 1 {
		t.Fatalf("live=false needs_input before close = %d, want 1", b)
	}
	e.reap(t, WithDeadNeedsInputTTL(deadTTL)) // closes it in this sweep
	if b := bucket(); b != 0 {
		t.Fatalf("live=false needs_input after close = %d, want 0", b)
	}
	e.reap(t) // a later sweep, backstop off: still closed, still 0
	if b := bucket(); b != 0 {
		t.Fatalf("live=false needs_input on a later sweep = %d, want 0", b)
	}
}

// An operator-closed dead needs_input row (close reason `operator`) leaves the
// needs_input bucket too: nothing awaits a human once it is closed.
func TestAwaitingHumanDead(t *testing.T) {
	cases := []struct {
		name string
		r    store.Session
		want bool
	}{
		{"never closed", store.Session{State: store.NeedsInput, LastActivityAt: 100}, true},
		{"closed after last activity", store.Session{State: store.NeedsInput, LastActivityAt: 100, ClosedAt: 200, CloseReason: "operator"}, false},
		{"closed at last activity", store.Session{State: store.NeedsInput, LastActivityAt: 100, ClosedAt: 100}, false},
		{"resumed after close", store.Session{State: store.NeedsInput, LastActivityAt: 300, ClosedAt: 200}, true},
		{"not needs_input", store.Session{State: store.Idle, LastActivityAt: 100}, false},
	}
	for _, c := range cases {
		if got := awaitingHumanDead(c.r); got != c.want {
			t.Errorf("%s: awaitingHumanDead = %v, want %v", c.name, got, c.want)
		}
	}
}
