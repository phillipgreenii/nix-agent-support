package session

// Pool and allowlisted-label attributes on every metric record (bead
// pg2-om899.4). Every Record* alias in session.go is spied, so these tests see
// exactly the attribute set each call site hands the instrument: the pool
// (basename of Deps.PoolPath, or the literal default in default mode) plus the
// session's marked labels filtered through Deps.MetricLabelAllowlist. The
// labeler is the REAL store (telemetry.SetSessionLabeler), so "labels resolved
// before the delete" is proven against the store's real delete-cascades-
// metadata behavior rather than a fake.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// roleAllowlist is the production default allowlist (pgrouter.role only).
var roleAllowlist = []string{"pgrouter.role"}

// wireLabeler makes st the process-global telemetry session labeler for the
// test, clearing it on cleanup.
func wireLabeler(t *testing.T, st *store.Store) {
	t.Helper()
	telemetry.SetSessionLabeler(st)
	t.Cleanup(func() { telemetry.SetSessionLabeler(nil) })
}

// seedMeta writes one metadata key for externalID, marking it label-eligible
// when label is true (the SetMeta-then-MarkAsLabel sequence of `ccpool new
// --meta k=v --label k`).
func seedMeta(t *testing.T, st *store.Store, externalID, key, value string, label bool) {
	t.Helper()
	if err := st.SetMeta(context.Background(), externalID, key, value); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if label {
		if err := st.MarkAsLabel(externalID, key); err != nil {
			t.Fatalf("MarkAsLabel: %v", err)
		}
	}
}

// seedReviewLabels gives externalID the allowlisted role label plus a
// non-allowlisted bead label (both marked as labels, so SessionAttrs returns
// both and only the allowlist separates them).
func seedReviewLabels(t *testing.T, st *store.Store, externalID string) {
	t.Helper()
	seedMeta(t, st, externalID, "pgrouter.role", "review", true)
	seedMeta(t, st, externalID, "pgrouter.bead", "zr-secret", true)
}

// attrMap flattens an attribute slice to key -> string value.
func attrMap(attrs []attribute.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[string(a.Key)] = a.Value.Emit()
	}
	return m
}

// wantAttrs fails unless attrs is EXACTLY want (pool plus allowlisted labels;
// in particular no pgrouter.bead and no path-like value).
func wantAttrs(t *testing.T, what string, attrs []attribute.KeyValue, want map[string]string) {
	t.Helper()
	if got := attrMap(attrs); !reflect.DeepEqual(got, want) {
		t.Errorf("%s attrs = %v, want %v", what, got, want)
	}
}

// captureSlog swaps the default slog logger for a JSON handler writing to a
// buffer (restored on cleanup) and returns a func decoding every record
// written so far.
func captureSlog(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return func() []map[string]any {
		var out []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("decode slog line %q: %v", line, err)
			}
			out = append(out, rec)
		}
		return out
	}
}

// findRecord returns the first record with the given message and external_id.
func findRecord(t *testing.T, recs []map[string]any, msg, externalID string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == msg && r["external_id"] == externalID {
			return r
		}
	}
	t.Fatalf("no slog record %q for %q in %v", msg, externalID, recs)
	return nil
}

// spyCancel captures every recordCancel call.
type cancelCall struct {
	outcome string
	attrs   []attribute.KeyValue
}

func spyCancel(t *testing.T) *[]cancelCall {
	t.Helper()
	orig := recordCancel
	t.Cleanup(func() { recordCancel = orig })
	var calls []cancelCall
	recordCancel = func(outcome string, attrs []attribute.KeyValue) {
		calls = append(calls, cancelCall{outcome, attrs})
	}
	return &calls
}

// idleCancelService is a Service whose "a" session is ready+live, so Cancel
// takes the already-idle short-circuit (outcome=success).
func idleCancelService(t *testing.T, poolPath string, allow []string) *Service {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid", State: store.Ready, TmuxSession: "cc-a"})
	seedReviewLabels(t, st, "a")
	wireLabeler(t, st)
	return New(Deps{
		Tmux: &closeTmux{live: true}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		PoolPath: poolPath, MetricLabelAllowlist: allow,
		Now: func() time.Time { return time.Unix(1, 0) },
	})
}

// TestMetricAttrs_cancelCarriesPoolAndAllowlistedLabel: pool is the basename
// of Deps.PoolPath, pgrouter.role passes the allowlist, pgrouter.bead (a
// marked label, but not allowlisted) is dropped.
func TestMetricAttrs_cancelCarriesPoolAndAllowlistedLabel(t *testing.T) {
	calls := spyCancel(t)
	s := idleCancelService(t, "/Users/x/pools/pg-router-ccpool-review", roleAllowlist)
	if err := s.Cancel(context.Background(), "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("recordCancel calls = %d, want 1", len(*calls))
	}
	wantAttrs(t, "recordCancel", (*calls)[0].attrs,
		map[string]string{"pool": "pg-router-ccpool-review", "pgrouter.role": "review"})
}

// TestMetricAttrs_defaultModePoolIsLiteralDefault: with an empty PoolPath
// (default mode) the pool attribute is the literal default, never empty.
func TestMetricAttrs_defaultModePoolIsLiteralDefault(t *testing.T) {
	calls := spyCancel(t)
	s := idleCancelService(t, "", roleAllowlist)
	if err := s.Cancel(context.Background(), "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	wantAttrs(t, "recordCancel", (*calls)[0].attrs,
		map[string]string{"pool": "default", "pgrouter.role": "review"})
}

// TestMetricAttrs_unsetAllowlistFailsClosed: a Service with no allowlist
// (nil) puts NO session label on metrics, only the pool.
func TestMetricAttrs_unsetAllowlistFailsClosed(t *testing.T) {
	calls := spyCancel(t)
	s := idleCancelService(t, "/pools/alpha", nil)
	if err := s.Cancel(context.Background(), "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	wantAttrs(t, "recordCancel", (*calls)[0].attrs, map[string]string{"pool": "alpha"})
}

// TestMetricAttrs_configuredAllowlistWidensLabels: the allowlist is the
// mechanism — listing pgrouter.bead admits it.
func TestMetricAttrs_configuredAllowlistWidensLabels(t *testing.T) {
	calls := spyCancel(t)
	s := idleCancelService(t, "/pools/alpha", []string{"pgrouter.role", "pgrouter.bead"})
	if err := s.Cancel(context.Background(), "a"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	wantAttrs(t, "recordCancel", (*calls)[0].attrs,
		map[string]string{"pool": "alpha", "pgrouter.role": "review", "pgrouter.bead": "zr-secret"})
}

// TestMetricAttrs_launchOutcomeCarriesPoolAndLabels covers the launch-outcome
// call site (recordLaunchOutcome, reached here via the reuse_live route).
func TestMetricAttrs_launchOutcomeCarriesPoolAndLabels(t *testing.T) {
	orig := recordLaunchOutcomeFn
	t.Cleanup(func() { recordLaunchOutcomeFn = orig })
	var gotRoute, gotOutcome string
	var gotAttrs []attribute.KeyValue
	recordLaunchOutcomeFn = func(route, outcome string, attrs []attribute.KeyValue) {
		gotRoute, gotOutcome, gotAttrs = route, outcome, attrs
	}

	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid", State: store.Ready, TmuxSession: "cc-ext-alpha"})
	seedReviewLabels(t, st, "ext-alpha")
	wireLabeler(t, st)
	s := New(Deps{
		Tmux: &fakeTmux{live: map[string]bool{"cc-ext-alpha": true}}, Trust: &fakeTrust{}, Store: st,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		PoolPath: "/pools/alpha", MetricLabelAllowlist: roleAllowlist,
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if _, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatal(err)
	}
	if gotRoute != "reuse_live" || gotOutcome != "success" {
		t.Fatalf("launch outcome = %s/%s, want reuse_live/success", gotRoute, gotOutcome)
	}
	wantAttrs(t, "recordLaunchOutcome", gotAttrs, map[string]string{"pool": "alpha", "pgrouter.role": "review"})
}

// reapSpies captures every reap-related Record* call with its attribute set.
type reapSpies struct {
	closures  []closureCall
	phantoms  [][]attribute.KeyValue
	preserved []preservedCall
	states    [][]attribute.KeyValue
}

type closureCall struct {
	reason string
	attrs  []attribute.KeyValue
}

type preservedCall struct {
	count int64
	attrs []attribute.KeyValue
}

func spyReap(t *testing.T) *reapSpies {
	t.Helper()
	origC, origP, origH, origS := recordReapClosure, recordReapPhantomPruned, recordSessionsPreservedForHuman, recordSessionStates
	t.Cleanup(func() {
		recordReapClosure, recordReapPhantomPruned, recordSessionsPreservedForHuman, recordSessionStates = origC, origP, origH, origS
	})
	sp := &reapSpies{}
	recordReapClosure = func(reason string, attrs []attribute.KeyValue) {
		sp.closures = append(sp.closures, closureCall{reason, attrs})
	}
	recordReapPhantomPruned = func(attrs []attribute.KeyValue) { sp.phantoms = append(sp.phantoms, attrs) }
	recordSessionsPreservedForHuman = func(count int64, attrs []attribute.KeyValue) {
		sp.preserved = append(sp.preserved, preservedCall{count, attrs})
	}
	recordSessionStates = func(_ []telemetry.SessionStateCount, attrs []attribute.KeyValue) {
		sp.states = append(sp.states, attrs)
	}
	return sp
}

// liveRow is one live session for reapService.
type liveRow struct {
	id    string
	state store.State
	ageS  int64
	role  string // "" = unlabelled
}

// reapService builds a pool's Service over its OWN store with the given live
// rows (labelled per row.role) and returns the set of closed tmux names.
func reapService(t *testing.T, now time.Time, poolPath string, rows ...liveRow) (*Service, *store.Store, map[string]bool) {
	t.Helper()
	ctx := context.Background()
	st := newMemStore(t)
	live := map[string]bool{}
	for _, r := range rows {
		_ = st.Insert(ctx, store.Session{
			ExternalID: r.id, ClaudeSessionID: "csid-" + r.id, State: r.state,
			TmuxSession: "cc-" + r.id, LastActivityAt: now.Unix() - r.ageS,
		})
		live["cc-"+r.id] = true
		if r.role != "" {
			seedMeta(t, st, r.id, "pgrouter.role", r.role, true)
			seedMeta(t, st, r.id, "pgrouter.bead", "zr-"+r.id, true)
		}
	}
	closed := map[string]bool{}
	s := New(Deps{
		Tmux: &reapTmux{live: live, closed: closed}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Exister: fakeExister{ok: true}, PoolPath: poolPath, MetricLabelAllowlist: roleAllowlist,
		Now: func() time.Time { return now },
	})
	return s, st, closed
}

// TestReap_closureAttrsResolvedPerSessionInsideLoop: reap MUST resolve
// attributes per session inside its loop (not once per process): two sessions
// closed in ONE Reap carry their OWN role.
func TestReap_closureAttrsResolvedPerSessionInsideLoop(t *testing.T) {
	sp := spyReap(t)
	now := time.Unix(10_000, 0)
	s, st, _ := reapService(
		t, now, "/pools/alpha",
		liveRow{id: "rev", state: store.Idle, ageS: 7200, role: "review"},
		liveRow{id: "imp", state: store.Idle, ageS: 7300, role: "impl"},
	)
	wireLabeler(t, st)
	if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	roles := []string{}
	for _, c := range sp.closures {
		if c.reason != "idle_ttl" {
			t.Errorf("closure reason = %q, want idle_ttl", c.reason)
		}
		m := attrMap(c.attrs)
		if m["pool"] != "alpha" || len(m) != 2 {
			t.Errorf("closure attrs = %v, want pool=alpha plus exactly pgrouter.role", m)
		}
		roles = append(roles, m["pgrouter.role"])
	}
	sort.Strings(roles)
	if want := []string{"impl", "review"}; !reflect.DeepEqual(roles, want) {
		t.Errorf("closure roles = %v, want %v (one per session, each its own)", roles, want)
	}
}

// TestReap_phantomPruneResolvesLabelsBeforeDelete: Store.Delete also deletes
// the session's metadata, so the phantom-prune metric AND its log line only
// carry the session's labels if they were resolved BEFORE the delete.
func TestReap_phantomPruneResolvesLabelsBeforeDelete(t *testing.T) {
	sp := spyReap(t)
	records := captureSlog(t)
	ctx := context.Background()
	now := time.Unix(10_000, 0)
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{
		ExternalID: "gone", ClaudeSessionID: "csid-gone", TranscriptPath: "/p/gone.jsonl", State: store.Idle,
		TmuxSession: "cc-gone", CreatedAt: now.Unix() - 7200, LastActivityAt: now.Unix() - 7200,
	})
	seedReviewLabels(t, st, "gone")
	wireLabeler(t, st)
	s := New(Deps{
		Tmux: &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}, Trust: &fakeTrust{}, Store: st,
		Prefix: "cc-", Exister: existerByPath{}, PoolPath: "/pools/alpha", MetricLabelAllowlist: roleAllowlist,
		Now: func() time.Time { return now },
	})

	if err := s.Reap(ctx, 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if _, ok, _ := st.GetByExternalID(ctx, "gone"); ok {
		t.Fatal("fixture: the phantom row must have been pruned")
	}
	if labels, _ := st.Labels("gone"); len(labels) != 0 {
		t.Fatalf("fixture: the delete must have removed the labels, still have %v", labels)
	}
	if len(sp.phantoms) != 1 {
		t.Fatalf("recordReapPhantomPruned calls = %d, want 1", len(sp.phantoms))
	}
	wantAttrs(t, "recordReapPhantomPruned", sp.phantoms[0],
		map[string]string{"pool": "alpha", "pgrouter.role": "review"})
	rec := findRecord(t, records(), "ccpool: reap pruned phantom session", "gone")
	if rec["pgrouter.role"] != "review" || rec["pgrouter.bead"] != "zr-secret" {
		t.Errorf("phantom-prune log record must carry the pre-delete labels (logs keep every label); record=%v", rec)
	}
}

// TestClosePurge_resolvesLabelsBeforeDelete: a --purge close deletes the row
// and its metadata, so its narration record is built from labels resolved
// BEFORE the delete.
func TestClosePurge_resolvesLabelsBeforeDelete(t *testing.T) {
	records := captureSlog(t)
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid", State: store.Idle, TmuxSession: "cc-a"})
	seedReviewLabels(t, st, "a")
	wireLabeler(t, st)
	s := New(Deps{
		Tmux: &closeTmux{live: false}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		PoolPath: "/pools/alpha", MetricLabelAllowlist: roleAllowlist,
		Now: func() time.Time { return time.Unix(1, 0) },
	})

	if err := s.Close(ctx, "a", true); err != nil {
		t.Fatalf("Close purge: %v", err)
	}

	if labels, _ := st.Labels("a"); len(labels) != 0 {
		t.Fatalf("fixture: the purge must have removed the labels, still have %v", labels)
	}
	rec := findRecord(t, records(), "ccpool: purged session", "a")
	if rec["pgrouter.role"] != "review" {
		t.Errorf("purge narration must carry the pre-delete label pgrouter.role=review; record=%v", rec)
	}
}

// TestClosePurge_nonPurgeCloseDoesNotNarratePurge: only a --purge close emits
// the purge record.
func TestClosePurge_nonPurgeCloseDoesNotNarratePurge(t *testing.T) {
	records := captureSlog(t)
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "csid", State: store.Idle, TmuxSession: "cc-a"})
	wireLabeler(t, st)
	s := New(Deps{Tmux: &closeTmux{live: false}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Now: func() time.Time { return time.Unix(1, 0) }})
	if err := s.Close(ctx, "a", false); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, r := range records() {
		if r["msg"] == "ccpool: purged session" {
			t.Errorf("a non-purge close must not narrate a purge: %v", r)
		}
	}
}

// TestReap_preservedForHumanOneValuePerPoolAndLabelSet: the gauge emits one
// value per (pool, label set): two needs_input review sessions count 2, the
// needs_input impl session counts 1, and a review session that is not
// preserved contributes to no extra count in its own set.
func TestReap_preservedForHumanOneValuePerPoolAndLabelSet(t *testing.T) {
	sp := spyReap(t)
	now := time.Unix(10_000, 0)
	s, st, _ := reapService(
		t, now, "/pools/alpha",
		liveRow{id: "p1", state: store.NeedsInput, ageS: 10, role: "review"},
		liveRow{id: "p2", state: store.NeedsInput, ageS: 10, role: "review"},
		liveRow{id: "p3", state: store.NeedsInput, ageS: 10, role: "impl"},
		liveRow{id: "w1", state: store.Working, ageS: 10, role: "impl"},
		liveRow{id: "u1", state: store.Working, ageS: 10, role: ""},
	)
	wireLabeler(t, st)
	if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	got := map[string]int64{}
	for _, c := range sp.preserved {
		m := attrMap(c.attrs)
		if m["pool"] != "alpha" {
			t.Errorf("preserved attrs = %v, want pool=alpha", m)
		}
		key := m["pgrouter.role"]
		if _, dup := got[key]; dup {
			t.Errorf("label set %q emitted more than once: %v", key, sp.preserved)
		}
		got[key] = c.count
	}
	want := map[string]int64{"review": 2, "impl": 1, "": 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preserved per label set = %v, want %v", got, want)
	}
}

// TestReap_preservedForHumanEmitsPoolOnlyZeroWhenPoolEmpty: a pool with no
// live sessions still emits one explicit 0 for its pool.
func TestReap_preservedForHumanEmitsPoolOnlyZeroWhenPoolEmpty(t *testing.T) {
	sp := spyReap(t)
	s, st, _ := reapService(t, time.Unix(10_000, 0), "/pools/alpha")
	wireLabeler(t, st)
	if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(sp.preserved) != 1 || sp.preserved[0].count != 0 {
		t.Fatalf("preserved calls = %+v, want exactly one 0", sp.preserved)
	}
	wantAttrs(t, "preserved", sp.preserved[0].attrs, map[string]string{"pool": "alpha"})
}

// TestReap_sessionStatesCarryPool: the pool-wide ccpool_session_states
// snapshot carries the pool (and no per-session label: it aggregates many
// sessions).
func TestReap_sessionStatesCarryPool(t *testing.T) {
	sp := spyReap(t)
	s, st, _ := reapService(t, time.Unix(10_000, 0), "/pools/alpha",
		liveRow{id: "a", state: store.Working, ageS: 10, role: "review"})
	wireLabeler(t, st)
	if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(sp.states) != 1 {
		t.Fatalf("recordSessionStates calls = %d, want 1", len(sp.states))
	}
	wantAttrs(t, "recordSessionStates", sp.states[0], map[string]string{"pool": "alpha"})
}

// TestReapAllShape_twoPoolsEachRecordCarriesItsOwnPool mirrors reap-all: ONE
// process sweeps several pools, each through its OWN Service, sequentially,
// without touching CCPOOL_POOL. Every record of each sweep must carry that
// pool's name and that pool's own session labels, proving the pool is
// threaded per Service and not read from process-global state.
func TestReapAllShape_twoPoolsEachRecordCarriesItsOwnPool(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "/pools/ENV-MUST-BE-IGNORED")
	sp := spyReap(t)
	now := time.Unix(10_000, 0)

	sweep := func(poolPath string, rows ...liveRow) *reapSpies {
		nClosures, nPreserved, nStates := len(sp.closures), len(sp.preserved), len(sp.states)
		s, st, _ := reapService(t, now, poolPath, rows...)
		wireLabeler(t, st) // reap-all re-sets the labeler per pool
		if err := s.Reap(context.Background(), 6, time.Hour); err != nil {
			t.Fatalf("Reap(%s): %v", poolPath, err)
		}
		return &reapSpies{
			closures: sp.closures[nClosures:], preserved: sp.preserved[nPreserved:], states: sp.states[nStates:],
		}
	}

	a := sweep("/pools/alpha",
		liveRow{id: "a1", state: store.Idle, ageS: 7200, role: "review"},
		liveRow{id: "a2", state: store.NeedsInput, ageS: 10, role: "review"})
	b := sweep("/pools/bravo",
		liveRow{id: "b1", state: store.Idle, ageS: 7200, role: "impl"},
		liveRow{id: "b2", state: store.NeedsInput, ageS: 10, role: "impl"})
	def := sweep("", // default pool, as reap-all reaps it first
		liveRow{id: "d1", state: store.Idle, ageS: 7200, role: "worker"})

	check := func(name string, r *reapSpies, wantPool, wantRole string) {
		t.Helper()
		if len(r.closures) == 0 || len(r.preserved) == 0 || len(r.states) == 0 {
			t.Fatalf("%s: missing records: %+v", name, r)
		}
		var all [][]attribute.KeyValue
		for _, c := range r.closures {
			all = append(all, c.attrs)
		}
		for _, c := range r.preserved {
			all = append(all, c.attrs)
		}
		all = append(all, r.states...)
		for _, attrs := range all {
			m := attrMap(attrs)
			if m["pool"] != wantPool {
				t.Errorf("%s: record pool = %q, want %q (attrs %v)", name, m["pool"], wantPool, m)
			}
			if role, ok := m["pgrouter.role"]; ok && role != wantRole {
				t.Errorf("%s: record role = %q, want %q or none (attrs %v)", name, role, wantRole, m)
			}
		}
	}
	check("alpha", a, "alpha", "review")
	check("bravo", b, "bravo", "impl")
	check("default", def, "default", "worker")
}
