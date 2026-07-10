package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// fakeDetector emits a fixed label-key=value pair for every session it
// sees. Used to verify the grouping path.
type fakeDetector struct {
	key string
	fn  func(labels.Session) string
}

func (d fakeDetector) Name() string { return d.key }
func (d fakeDetector) Detect(s labels.Session) labels.Set {
	return labels.Set{d.key: d.fn(s)}
}

func TestLabelsForSession_CachesPerSession(t *testing.T) {
	calls := 0
	d := fakeDetector{key: "workspace.scope", fn: func(s labels.Session) string {
		calls++
		return "gascity"
	}}
	cap := labels.NewCardinalityCap(10)
	cache := map[string]labels.Set{}

	sv := &aggregate.SessionView{Session: &session.Session{SessionID: "s1"}}
	labelsForSession(sv, []labels.Detector{d}, nil, cap, cache)
	labelsForSession(sv, []labels.Detector{d}, nil, cap, cache)

	if calls != 1 {
		t.Errorf("detector called %d times, want 1 (cached)", calls)
	}
	if cache["s1"]["workspace.scope"] != "gascity" {
		t.Errorf("cache miss: %+v", cache["s1"])
	}
}

// TestLabelsForSession_CacheNotMutatedByCaller confirms that callers
// cannot pollute the cache by mutating the returned Set. The grouping
// loop in updateGauges adds a `state` key per tick — if the cache stored
// the same map reference, stale `state` values would survive across
// ticks. Defends against that.
func TestLabelsForSession_CacheNotMutatedByCaller(t *testing.T) {
	d := fakeDetector{key: "workspace.scope", fn: func(s labels.Session) string { return "gascity" }}
	cap := labels.NewCardinalityCap(10)
	cache := map[string]labels.Set{}

	sv := &aggregate.SessionView{Session: &session.Session{SessionID: "s1"}}
	first := labelsForSession(sv, []labels.Detector{d}, nil, cap, cache)
	// Caller pollutes the returned set (simulating updateGauges adding `state`).
	first["state"] = "working"
	first["bogus"] = "should-not-leak"

	// Re-fetch — the cache copy must not include the caller's additions.
	again := labelsForSession(sv, []labels.Detector{d}, nil, cap, cache)
	if _, ok := again["state"]; ok {
		t.Errorf("cache leaked caller's `state` key: %+v", again)
	}
	if _, ok := again["bogus"]; ok {
		t.Errorf("cache leaked caller's `bogus` key: %+v", again)
	}
	if again["workspace.scope"] != "gascity" {
		t.Errorf("cache lost legitimate value: %+v", again)
	}
}

// failThenSucceedDecorator is a FailableDetector that fails on its first
// invocation (simulating a transient timeout / non-zero exit) then succeeds,
// emitting a scope label. Used to exercise the ADR 0024 D5 nil-cache rule.
type failThenSucceedDecorator struct {
	calls int
	scope string
}

func (d *failThenSucceedDecorator) Name() string { return "fake-scope" }
func (d *failThenSucceedDecorator) Detect(s labels.Session) labels.Set {
	set, _ := d.DetectOK(s)
	return set
}

func (d *failThenSucceedDecorator) DetectOK(s labels.Session) (labels.Set, bool) {
	d.calls++
	if d.calls == 1 {
		return nil, false // transient failure on first sighting
	}
	return labels.Set{"workspace.scope": d.scope}, true
}

// TestLabelsForSession_DoesNotCacheFailedDecorator is the ADR 0024 D5
// regression guard: a decorator that fails on the first tick must NOT have its
// (empty) result cached — the next tick retries and the now-successful scope
// label appears, rather than the session sticking at the DefaultScope
// (`personal`) for its whole lifetime.
func TestLabelsForSession_DoesNotCacheFailedDecorator(t *testing.T) {
	dec := &failThenSucceedDecorator{scope: "ziprecruiter"}
	cap := labels.NewCardinalityCap(10)
	cache := map[string]labels.Set{}
	sv := &aggregate.SessionView{Session: &session.Session{SessionID: "s1"}}

	// Tick 1: decorator fails -> no scope, and nothing cached for the session.
	first := labelsForSession(sv, nil, []labels.FailableDetector{dec}, cap, cache)
	if _, ok := first["workspace.scope"]; ok {
		t.Errorf("tick 1 should carry no scope (decorator failed): %+v", first)
	}
	if _, cached := cache["s1"]; cached {
		t.Fatalf("a failed decorator result must NOT be cached: %+v", cache["s1"])
	}

	// Tick 2: decorator succeeds -> scope appears (retry, not stuck), and is
	// now cached for reuse.
	second := labelsForSession(sv, nil, []labels.FailableDetector{dec}, cap, cache)
	if second["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("retry should surface decorator labels, got %+v", second)
	}
	if cache["s1"]["workspace.scope"] != "ziprecruiter" {
		t.Fatalf("successful result should now be cached: %+v", cache["s1"])
	}
}

// successEmptyDecorator always succeeds but returns no labels. It counts calls
// so the test can prove a successful-empty result is CACHED (not re-run every
// tick) — only failures skip the cache.
type successEmptyDecorator struct{ calls int }

func (d *successEmptyDecorator) Name() string { return "empty" }
func (d *successEmptyDecorator) Detect(s labels.Session) labels.Set {
	set, _ := d.DetectOK(s)
	return set
}

func (d *successEmptyDecorator) DetectOK(s labels.Session) (labels.Set, bool) {
	d.calls++
	return labels.Set{}, true
}

// TestLabelsForSession_CachesSuccessfulEmptyDecorator confirms the nil-cache
// rule is narrow: a decorator that SUCCEEDS but legitimately emits no labels
// is still cached, so it is not re-run on every subsequent tick.
func TestLabelsForSession_CachesSuccessfulEmptyDecorator(t *testing.T) {
	dec := &successEmptyDecorator{}
	cache := map[string]labels.Set{}
	sv := &aggregate.SessionView{Session: &session.Session{SessionID: "s1"}}

	labelsForSession(sv, nil, []labels.FailableDetector{dec}, nil, cache)
	labelsForSession(sv, nil, []labels.FailableDetector{dec}, nil, cache)

	if dec.calls != 1 {
		t.Errorf("successful-empty decorator called %d times, want 1 (cached)", dec.calls)
	}
	if _, ok := cache["s1"]; !ok {
		t.Error("successful-empty result should be cached")
	}
}

func TestCanonicalKey_StableOrdering(t *testing.T) {
	a := labels.Set{"workspace.scope": "gascity", "state": "working"}
	b := labels.Set{"state": "working", "workspace.scope": "gascity"}
	if canonicalKey(a) != canonicalKey(b) {
		t.Errorf("canonicalKey not stable across map iteration")
	}
}

// TestEmitErrorMetrics_FiresOnNewError verifies that emitErrorMetrics
// increments previousErrors only when a new (later) error timestamp appears.
func TestEmitErrorMetrics_FiresOnNewError(t *testing.T) {
	// nil emitter — just verify it doesn't panic and updates previousErrors.
	prev := map[string]time.Time{}
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "sid-1"},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError: &transcript.ErrorRecord{
				Kind: transcript.ErrRateLimit,
				At:   now,
			},
		},
	}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{Sessions: []*aggregate.SessionView{sv}},
		},
	}

	// First call: should update previousErrors["sid-1"] to now.
	emitErrorMetrics(nil, tree, prev)
	if !prev["sid-1"].Equal(now) {
		t.Errorf("previousErrors not updated: got %v, want %v", prev["sid-1"], now)
	}

	// Second call with same timestamp: no new fire needed.
	prev2 := map[string]time.Time{"sid-1": now}
	emitErrorMetrics(nil, tree, prev2)
	if !prev2["sid-1"].Equal(now) {
		t.Error("previousErrors should remain at same timestamp")
	}

	// Third call with later timestamp: should update.
	later := now.Add(time.Minute)
	sv.LastError = &transcript.ErrorRecord{Kind: transcript.ErrRateLimit, At: later}
	prev3 := map[string]time.Time{"sid-1": now}
	emitErrorMetrics(nil, tree, prev3)
	if !prev3["sid-1"].Equal(later) {
		t.Errorf("previousErrors not advanced: got %v, want %v", prev3["sid-1"], later)
	}
}

func TestEmitErrorMetrics_NilTree(t *testing.T) {
	// nil tree should not panic.
	prev := map[string]time.Time{}
	emitErrorMetrics(nil, nil, prev)
}

// TestEmitErrorMetrics_ContextLimit verifies a context-limit error is handled
// on the newly-advanced edge (previousErrors advances) and that the
// RecordContextLimitHit branch is nil-safe. Counter emission itself is an
// SDK concern; detection is covered in transcript.TestLastAPIErrorDetectsContextLimit.
func TestEmitErrorMetrics_ContextLimit(t *testing.T) {
	prev := map[string]time.Time{}
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	sv := &aggregate.SessionView{
		Session: &session.Session{SessionID: "sid-ctx"},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model: "claude-opus-4-8",
			LastError: &transcript.ErrorRecord{
				Kind:           transcript.ErrInvalidRequest,
				Text:           "Prompt is too long: 215000 tokens > 200000 maximum",
				At:             now,
				IsTerminal:     true,
				IsContextLimit: true,
			},
		},
	}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{Sessions: []*aggregate.SessionView{sv}},
		},
	}

	// nil emitter — must not panic and must advance the dedup bookkeeping.
	emitErrorMetrics(nil, tree, prev)
	if !prev["sid-ctx"].Equal(now) {
		t.Errorf("previousErrors not advanced for context-limit error: got %v, want %v", prev["sid-ctx"], now)
	}
}

func TestEmitErrorMetrics_NoError(t *testing.T) {
	// Sessions with no LastError should not populate previousErrors.
	prev := map[string]time.Time{}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{Sessions: []*aggregate.SessionView{
				{Session: &session.Session{SessionID: "sid-1"}},
			}},
		},
	}
	emitErrorMetrics(nil, tree, prev)
	if len(prev) != 0 {
		t.Errorf("unexpected previousErrors entries: %v", prev)
	}
}

// TestDeferredNudgeCounts_WindowPending covers the ADR 0024 D5 deferral
// derivation: a session counts as window_pending (auto-resume deliberately
// WAITING, not broken) iff auto-resume is on AND it is Blocked/usage_limit with
// a still-in-future per-session reset — the window_reset producer will wait
// rather than nudge. Everything else (working, non-usage-limit blocker,
// elapsed/absent reset) is excluded, and an auto-resume-off tree defers nothing.
func TestDeferredNudgeCounts_WindowPending(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)
	past := now.Add(-30 * time.Minute)

	mk := func(id string, status session.Status, blocker session.Blocker, reset time.Time) *aggregate.SessionView {
		return &aggregate.SessionView{
			Session:           &session.Session{SessionID: id, Status: status, Blocker: blocker},
			SessionEnrichment: aggregate.SessionEnrichment{RateLimitResetsAt: reset},
		}
	}

	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{
		mk("s1", session.Blocked, session.UsageLimit, future),      // deferred: waiting on window
		mk("s2", session.Blocked, session.UsageLimit, future),      // deferred: waiting on window
		mk("s3", session.Blocked, session.UsageLimit, past),        // reset elapsed -> would nudge, not deferred
		mk("s4", session.Blocked, session.UsageLimit, time.Time{}), // reset-less pause -> not window_pending
		mk("s5", session.Working, session.NoBlocker, future),       // working -> not deferred
		mk("s6", session.Blocked, session.ErrorBlocker, future),    // blocked on error, not usage_limit
	}}}}

	got := deferredNudgeCounts(tree, true, now)
	if got["window_pending"] != 2 {
		t.Errorf("auto-resume on: window_pending = %d, want 2; got=%v", got["window_pending"], got)
	}

	// auto-resume off: the producers cancel rather than defer, so nothing is
	// "deliberately waiting" — the gauge must report nothing (drops to 0 via
	// the emitter's carry-forward-zero).
	if off := deferredNudgeCounts(tree, false, now); len(off) != 0 {
		t.Errorf("auto-resume off: got %v, want empty", off)
	}

	// nil tree: empty, no panic.
	if got := deferredNudgeCounts(nil, true, now); len(got) != 0 {
		t.Errorf("nil tree: got %v, want empty", got)
	}
}

func TestPruneLabelCache_DropsVanishedSessions(t *testing.T) {
	cache := map[string]labels.Set{
		"alive":   {"a": "1"},
		"vanish":  {"b": "2"},
		"vanish2": {"c": "3"},
	}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Sessions: []*aggregate.SessionView{
					{Session: &session.Session{SessionID: "alive"}},
				},
			},
		},
	}
	pruneLabelCache(cache, tree)
	if _, ok := cache["alive"]; !ok {
		t.Error("alive entry dropped")
	}
	if _, ok := cache["vanish"]; ok {
		t.Error("vanish entry should be pruned")
	}
	if _, ok := cache["vanish2"]; ok {
		t.Error("vanish2 entry should be pruned")
	}
}

// TestBuildSessionInfoRows_OnlyNonDormant confirms the cardinality-cap
// policy: dormant sessions are dropped from the per-session emit,
// matching the TUI "active" view and keeping session_id series bounded
// by the live process count rather than session history.
func TestBuildSessionInfoRows_OnlyNonDormant(t *testing.T) {
	working := &aggregate.SessionView{
		Session: &session.Session{
			SessionID:    "sid-work",
			Name:         "feat-x",
			Cwd:          "/repo/a",
			TerminalHost: "tmux",
			Status:       session.Working,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model:         "claude-opus-4-7",
			SessionTokens: 100,
			CostUSD:       0.10,
		},
	}
	idle := &aggregate.SessionView{
		Session: &session.Session{
			SessionID:    "sid-idle",
			Cwd:          "/repo/b",
			TerminalHost: "cmux (bridge disconnected)",
			Status:       session.Idle,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			Model: "claude-sonnet-4-7",
			LastError: &transcript.ErrorRecord{
				Kind:       transcript.ErrRateLimit,
				IsTerminal: true,
				At:         time.Now(),
			},
		},
	}
	idleNonTerminalErr := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "sid-idle-recovered",
			Status:    session.Idle,
		},
		SessionEnrichment: aggregate.SessionEnrichment{
			LastError: &transcript.ErrorRecord{
				Kind:       transcript.ErrRateLimit,
				IsTerminal: false, // user resumed after error
			},
		},
	}
	dormant := &aggregate.SessionView{
		Session: &session.Session{
			SessionID: "sid-dorm",
			Cwd:       "/repo/c",
			Status:    session.Idle,
			LongIdle:  true, // ADR 0024: dormant is the long-idle age refinement
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{
		{Sessions: []*aggregate.SessionView{working, idle, idleNonTerminalErr, dormant}},
	}}

	rows := buildSessionInfoRows(tree, "max_5x", nil, nil, labels.NewCardinalityCap(10), map[string]labels.Set{})
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (dormant excluded, three non-dormant kept)", len(rows))
	}

	byID := map[string]otel.SessionInfo{}
	for _, r := range rows {
		byID[r.SessionID] = r
		if r.SessionID == "sid-dorm" {
			t.Error("dormant session leaked into emit")
		}
	}

	// Working row: terminal abbrev TMUX, no error_kind.
	w := byID["sid-work"]
	if w.TerminalHost != "TMUX" {
		t.Errorf("working terminal = %q, want TMUX", w.TerminalHost)
	}
	if w.ErrorKind != "" {
		t.Errorf("working ErrorKind = %q, want empty", w.ErrorKind)
	}
	if w.Tokens != 100 || w.CostUSD != 0.10 {
		t.Errorf("working tokens/cost = %d/%v, want 100/0.10", w.Tokens, w.CostUSD)
	}
	if w.Status != "working" {
		t.Errorf("working status = %q, want working", w.Status)
	}
	if w.Labels["plan_tier"] != "max_5x" {
		t.Errorf("working plan_tier label = %q, want max_5x", w.Labels["plan_tier"])
	}

	// Idle row with terminal error: error_kind populated, cmux refinement
	// collapses to CMUX.
	i := byID["sid-idle"]
	if i.TerminalHost != "CMUX" {
		t.Errorf("idle terminal = %q, want CMUX (cmux refinements collapse)", i.TerminalHost)
	}
	if i.ErrorKind != "rate_limit" {
		t.Errorf("idle ErrorKind = %q, want rate_limit", i.ErrorKind)
	}

	// Idle row with NON-terminal error: error_kind MUST be empty.
	r := byID["sid-idle-recovered"]
	if r.ErrorKind != "" {
		t.Errorf("non-terminal error ErrorKind = %q, want empty (user resumed)", r.ErrorKind)
	}
}

// TestBuildSessionInfoRows_NilTree returns nil rather than panicking.
func TestBuildSessionInfoRows_NilTree(t *testing.T) {
	if got := buildSessionInfoRows(nil, "", nil, nil, nil, nil); got != nil {
		t.Errorf("buildSessionInfoRows(nil) = %v, want nil", got)
	}
}
