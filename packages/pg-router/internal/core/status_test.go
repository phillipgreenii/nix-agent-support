package core

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// TestListenerCounts_BumpDeclinedReasonConcurrentSameReason proves
// BumpDeclinedReason (bead pg2-j4uwg) is safe for concurrent callers
// incrementing the SAME reason's counter — the race LoadOrStore's
// "insert-at-most-once, then atomic.Int64.Add" design exists to avoid (two
// goroutines racing the FIRST bump of a brand-new reason key).
func TestListenerCounts_BumpDeclinedReasonConcurrentSameReason(t *testing.T) {
	c := &ListenerCounts{}
	var wg sync.WaitGroup
	const n = 100
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.BumpDeclinedReason("at-capacity")
		}()
	}
	wg.Wait()
	got := c.DeclinedByReasonSnapshot()
	if got["at-capacity"] != int64(n) {
		t.Fatalf("declinedByReason[at-capacity] = %d, want %d", got["at-capacity"], n)
	}
}

// TestListenerCounts_DeclinedByReasonSnapshotIsIndependentCopy proves
// DeclinedByReasonSnapshot returns a plain map a caller may hold onto
// without racing further Bump calls into the same reason key.
func TestListenerCounts_DeclinedByReasonSnapshotIsIndependentCopy(t *testing.T) {
	c := &ListenerCounts{}
	c.BumpDeclinedReason("at-capacity")
	snap := c.DeclinedByReasonSnapshot()
	c.BumpDeclinedReason("at-capacity")
	if snap["at-capacity"] != 1 {
		t.Fatalf("snapshot[at-capacity] = %d, want 1 (a later Bump must not retroactively change an already-taken snapshot)", snap["at-capacity"])
	}
	if got := c.DeclinedByReasonSnapshot()["at-capacity"]; got != 2 {
		t.Fatalf("a FRESH snapshot[at-capacity] = %d, want 2", got)
	}
}

// TestCurrentTick_nilBeforeFirstPublish is the boot-window test: a freshly
// constructed Service must not panic when status-composing logic touches its
// tick cell before any PublishTick call [design: Task 3.5 Step 4].
func TestCurrentTick_nilBeforeFirstPublish(t *testing.T) {
	var svc Service

	got := svc.CurrentTick()
	if got != nil {
		t.Fatalf("CurrentTick() = %+v, want nil before the first PublishTick", got)
	}

	// Status-composing logic (Task 3.8) must nil-check rather than deref
	// unconditionally; simulate that check here so a regression that removes
	// the nil-check panics this test instead of a live status call.
	var runMode string
	if tick := svc.CurrentTick(); tick != nil {
		runMode = tick.RunMode
	} else {
		runMode = "boot"
	}
	if runMode != "boot" {
		t.Fatalf("runMode = %q, want %q", runMode, "boot")
	}
}

// TestPublishTick_currentTickRoundTrips proves PublishTick/CurrentTick carry
// a value through unchanged, and that a second publish fully replaces the
// first (no partial-merge of stale fields).
func TestPublishTick_currentTickRoundTrips(t *testing.T) {
	var svc Service
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeLongRunning,
		Version:    "v1",
		LastTickAt: t0,
		SnapshotAt: t0,
	})
	got := svc.CurrentTick()
	if got == nil || got.RunMode != RunModeLongRunning || got.Version != "v1" {
		t.Fatalf("CurrentTick() = %+v, want the just-published long-running v1 snapshot", got)
	}

	t1 := t0.Add(time.Minute)
	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeDrainAndExit,
		Version:    "v1",
		LastTickAt: t1,
		SnapshotAt: t1,
	})
	got = svc.CurrentTick()
	if got == nil || got.RunMode != RunModeDrainAndExit || !got.LastTickAt.Equal(t1) {
		t.Fatalf("CurrentTick() = %+v, want the second publish to fully replace the first", got)
	}
}

// TestListenerCounts_LastDeliveredAtNanos_ZeroUntilSet proves the zero
// value of ListenerCounts' new LastDeliveredAtNanos field (Task 2) is 0 --
// "never delivered" -- until listenerCountObserver.OnAccept sets it.
func TestListenerCounts_LastDeliveredAtNanos_ZeroUntilSet(t *testing.T) {
	c := &ListenerCounts{}
	if got := c.LastDeliveredAtNanos.Load(); got != 0 {
		t.Fatalf("zero value LastDeliveredAtNanos = %d, want 0", got)
	}
}

// TestStatusListeners_SelfReportStateJoinsByRoleName proves statusListeners'
// new selfReportState key (Task 2, folding in the retired Registry pane's
// one useful signal) joins a Registration onto its matching declared role
// by Registration.ID == role name.
func TestStatusListeners_SelfReportStateJoinsByRoleName(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}
	// Registration.State is participant.Lifecycle (an int-based Stringer,
	// internal/conformance's top-level conformance package,
	// conformance/transport.go:14), not a plain string -- conformance.
	// Started is the constant whose .String() is "started" (matching the
	// existing pattern at statusRegistrations' own `"state": r.State.
	// String()`).
	regs := []Registration{{ID: "df-feedback", Kind: "handler", State: participant.Started, Self: SelfDegraded}}

	rows := statusListeners(declared, nil, counts, regs, nil, nil)
	if got := rows[0].SelfReportState; got != "degraded" {
		t.Fatalf("selfReportState = %v, want %q (the self-status, not the lifecycle state)", got, "degraded")
	}
	if got := rows[0].UnavailableReason; got != "" {
		t.Fatalf("unavailableReason = %v, want empty for an available role", got)
	}
}

// TestStatusListeners_UnavailableRoleShowsSelfStatusAndReason proves a role core
// marked unavailable (docket pg2-12gr4.8) shows "unavailable" as its
// selfReportState and its reason in unavailableReason.
func TestStatusListeners_UnavailableRoleShowsSelfStatusAndReason(t *testing.T) {
	declared := []roles.Role{{Name: "w", Enabled: true, Binds: []string{"x"}}}
	counts := map[string]*ListenerCounts{"w": {}}
	regs := []Registration{{ID: "w", Kind: "handler", State: participant.Started, Self: SelfUnavailable, UnavailableReason: "bad pool dir"}}

	rows := statusListeners(declared, nil, counts, regs, nil, nil)
	if got := rows[0].SelfReportState; got != "unavailable" {
		t.Fatalf("selfReportState = %v, want unavailable", got)
	}
	if got := rows[0].UnavailableReason; got != "bad pool dir" {
		t.Fatalf("unavailableReason = %v, want %q", got, "bad pool dir")
	}
}

// TestStatusListeners_ProgressObject proves each listener carries `progress`
// (docket pg2-12gr4.8): the object the gauges' state yields for a role with a
// progress series, and null for one without (a disabled role).
func TestStatusListeners_ProgressObject(t *testing.T) {
	declared := []roles.Role{
		{Name: "on", Enabled: true, Binds: []string{"x"}},
		{Name: "off", Enabled: false, Binds: []string{"x"}},
	}
	progress := map[string]metrics.RoleProgress{"on": {AgeSeconds: 12.5, Unsuccessful: 2, Busy: 3}}

	rows := statusListeners(declared, nil, nil, nil, nil, progress)
	want := &status.Progress{AgeSeconds: 12.5, Unsuccessful: 2, Busy: 3}
	if got := rows[1].Progress; !reflect.DeepEqual(got, want) { // sorted: off, on
		t.Errorf("on progress = %#v, want %#v", got, want)
	}
	if got := rows[0].Progress; got != nil {
		t.Errorf("off progress = %#v, want nil", got)
	}
}

// TestStatusListeners_SelfReportStateEmptyWhenNeverRegistered proves a
// declared role with no matching Registration reports the empty string,
// never a missing key.
func TestStatusListeners_SelfReportStateEmptyWhenNeverRegistered(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}

	rows := statusListeners(declared, nil, counts, nil, nil, nil)
	if got := rows[0].SelfReportState; got != "" {
		t.Fatalf("selfReportState = %v, want empty string", got)
	}
}

// TestStatusListeners_RoleBindsEnabledExcluded is Task 4.1 Step 1's
// red-first test: composeStatusReply's listeners[] carries the FULL
// declared role set (Options.DeclaredRoles), with `enabled` reflecting the
// CONFIG-level flag and `excluded` computed independently from
// Options.ExcludedRoles (Binding Decision 4) — a config-disabled role
// (enabled=false, never selector-excluded) and a selector-excluded role
// (enabled=true, excluded=true) must render distinctly.
func TestStatusListeners_RoleBindsEnabledExcluded(t *testing.T) {
	reviewCounts := &ListenerCounts{}
	reviewCounts.Delivered.Store(5)
	reviewCounts.Declined.Store(2)
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
			{Name: "feedback", Binds: []string{"feedback-ready"}, Enabled: false},
		},
		excludedRoles:  []string{"worker"},
		listenerCounts: map[string]*ListenerCounts{"review": reviewCounts},
	}
	reply := svc.composeStatusReply(t.Context(), 0)
	listeners := reply.Listeners
	if len(listeners) != 3 {
		t.Fatalf("listeners = %v, want 3 entries (the full declared set)", reply.Listeners)
	}
	byRole := make(map[string]status.Listener, len(listeners))
	for _, l := range listeners {
		byRole[l.Role] = l
	}
	if got := byRole["review"]; got.Enabled != true || got.Excluded != false {
		t.Fatalf("review = %+v, want enabled=true excluded=false", got)
	}
	if got := byRole["worker"]; got.Enabled != true || got.Excluded != true {
		t.Fatalf("worker = %+v, want enabled=true excluded=true (selector-excluded)", got)
	}
	if got := byRole["feedback"]; got.Enabled != false || got.Excluded != false {
		t.Fatalf("feedback = %+v, want enabled=false excluded=false (config-disabled, never selector-excluded)", got)
	}
	if got := byRole["review"].Binds; !reflect.DeepEqual(got, []string{"review-requested"}) {
		t.Fatalf("review.binds = %v, want [review-requested]", byRole["review"].Binds)
	}
	if byRole["review"].Backoff != nil {
		t.Fatalf("review.backoff = %v, want null (no live roleListener reference reaches core)", byRole["review"].Backoff)
	}
	if byRole["review"].Delivered != 5 || byRole["review"].Declined != 2 {
		t.Fatalf("review delivered/declined = %v/%v, want 5/2 (from ListenerCounts)", byRole["review"].Delivered, byRole["review"].Declined)
	}
	if byRole["worker"].Delivered != 0 || byRole["worker"].Declined != 0 {
		t.Fatalf("worker delivered/declined = %v/%v, want 0/0 (no ListenerCounts entry for it)", byRole["worker"].Delivered, byRole["worker"].Declined)
	}
}

// TestStatusListeners_DescriptionAlwaysPresent is pg2-ec754's red-first test:
// composeStatusReply's listeners[] carries the ALWAYS-present `description`
// key, sourced straight from the declared roles.Role.Description — never
// omitted, and "" (not a missing key) for a role that never set one, matching
// this reply's existing always-present-key convention (e.g. binds).
func TestStatusListeners_DescriptionAlwaysPresent(t *testing.T) {
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true, Description: "reviews incoming PRs"},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
		},
	}
	reply := svc.composeStatusReply(t.Context(), 0)
	listeners := reply.Listeners
	if len(listeners) != 2 {
		t.Fatalf("listeners = %v, want 2 entries", reply.Listeners)
	}
	byRole := make(map[string]status.Listener, len(listeners))
	for _, l := range listeners {
		byRole[l.Role] = l
	}
	if got := byRole["review"].Description; got != "reviews incoming PRs" {
		t.Fatalf("review.description = %v, want %q", got, "reviews incoming PRs")
	}
	if got := byRole["worker"].Description; got != "" {
		t.Fatalf("worker.description = %v, want empty string (unset in config)", got)
	}
}

// TestStatusListeners_DeclinedByReasonBreaksDownDeclined proves
// composeStatusReply's listeners[] carries the OPTIONAL declinedByReason
// breakdown (bead pg2-j4uwg) alongside the pre-existing flat declined total
// — additive, never a replacement, and empty (not absent) for a role with
// no ListenerCounts entry at all.
func TestStatusListeners_DeclinedByReasonBreaksDownDeclined(t *testing.T) {
	reviewCounts := &ListenerCounts{}
	reviewCounts.Delivered.Store(5)
	reviewCounts.Declined.Store(3)
	reviewCounts.BumpDeclinedReason("at-capacity")
	reviewCounts.BumpDeclinedReason("at-capacity")
	reviewCounts.BumpDeclinedReason("capacity-unknown")
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
		},
		listenerCounts: map[string]*ListenerCounts{"review": reviewCounts},
	}
	reply := svc.composeStatusReply(t.Context(), 0)
	listeners := reply.Listeners
	if len(listeners) != 2 {
		t.Fatalf("listeners = %v, want 2 entries", reply.Listeners)
	}
	byRole := make(map[string]status.Listener, len(listeners))
	for _, l := range listeners {
		byRole[l.Role] = l
	}
	reviewByReason := byRole["review"].DeclinedByReason
	if reviewByReason["at-capacity"] != 2 || reviewByReason["capacity-unknown"] != 1 {
		t.Fatalf("review.declinedByReason = %v, want at-capacity=2 capacity-unknown=1", reviewByReason)
	}
	var sum int64
	for _, v := range reviewByReason {
		sum += v
	}
	if sum != byRole["review"].Declined {
		t.Fatalf("declinedByReason sums to %d, want it to equal the flat declined total %v", sum, byRole["review"].Declined)
	}
	workerByReason := byRole["worker"].DeclinedByReason
	if workerByReason == nil || len(workerByReason) != 0 {
		t.Fatalf("worker.declinedByReason = %v, want an EMPTY (not absent/nil) map — no ListenerCounts entry for it", byRole["worker"].DeclinedByReason)
	}
}

// TestStatusListeners_SortedByRoleName is bead pg2-d1sem's regression test:
// statusListeners must sort its output by Role.Name, never leave it in the
// config's own declaration order. declared is deliberately given "zeta"
// before "alpha" (and a middle "mid") to prove the fix, not just an
// already-sorted input.
func TestStatusListeners_SortedByRoleName(t *testing.T) {
	declared := []roles.Role{
		{Name: "zeta", Enabled: true},
		{Name: "alpha", Enabled: true},
		{Name: "mid", Enabled: true},
	}

	rows := statusListeners(declared, nil, nil, nil, nil, nil)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	got := []string{rows[0].Role, rows[1].Role, rows[2].Role}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("role order = %v, want alphabetical %v", got, want)
	}
}

// TestStatusListeners_InFlight proves the DEC-OBS-2 (bead pg2-ugcrb)
// inFlight/inFlightEventType widening: a role present in the inFlight map
// (keyed by role name, eventqueue.Queue.InFlightListeners' own contract)
// reports inFlight=true and the offered event's Type; a role absent from it
// reports inFlight=false and an empty inFlightEventType, never a missing
// key either way.
func TestStatusListeners_InFlight(t *testing.T) {
	declared := []roles.Role{
		{Name: "busy-role", Enabled: true},
		{Name: "idle-role", Enabled: true},
	}
	inFlight := map[string]string{"busy-role": "pr.changed"}

	rows := statusListeners(declared, nil, nil, nil, inFlight, nil)
	byRole := map[string]status.Listener{}
	for _, r := range rows {
		byRole[r.Role] = r
	}
	if got := byRole["busy-role"].InFlight; got != true {
		t.Fatalf("busy-role inFlight = %v, want true", got)
	}
	if got := byRole["busy-role"].InFlightEventType; got != "pr.changed" {
		t.Fatalf("busy-role inFlightEventType = %v, want %q", got, "pr.changed")
	}
	if got := byRole["idle-role"].InFlight; got != false {
		t.Fatalf("idle-role inFlight = %v, want false", got)
	}
	if got := byRole["idle-role"].InFlightEventType; got != "" {
		t.Fatalf("idle-role inFlightEventType = %v, want empty string", got)
	}
}

// TestStatusSources_TypeModeLastTickFailure is Task 4.1 Step 7's red-first
// test: composeStatusReply's sources[] carries type/mode ("pull", always —
// no push query type exists), lastTick (present only for a source THIS
// pass's TickSnapshot.Sources actually fired), and failure (present only
// for a source that failed this pass) — over the full configured set,
// UNION Options.ExcludedSources for a selector-excluded source that has
// already vanished from TickSnapshot.Sources entirely.
func TestStatusSources_TypeModeLastTickFailure(t *testing.T) {
	svc := &Service{
		q:               newQueue(t),
		bindings:        testBindings(),
		reg:             NewRegistry(nil),
		excludedSources: []string{"disabled-src"},
	}
	now := time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC)
	svc.PublishTick(TickSnapshot{
		Sources: []SourceReport{
			{Name: "active-src", Type: "pull", LastTick: now},
			{Name: "failing-src", Type: "pull", Failure: &FailureInfo{Count: 2, NextEligible: now}},
		},
	})
	reply := svc.composeStatusReply(t.Context(), 0)
	sources := reply.Sources
	if len(sources) != 3 {
		t.Fatalf("sources = %v, want 3 entries (2 active + 1 selector-excluded)", reply.Sources)
	}
	byName := make(map[string]status.Source, len(sources))
	for _, s := range sources {
		byName[s.Name] = s
	}

	active := byName["active-src"]
	if active.Type != "pull" || active.Mode != "pull" || active.Enabled != true || active.Excluded != false {
		t.Fatalf("active-src = %+v, want type/mode=pull enabled=true excluded=false", active)
	}
	if want := now.UTC(); !active.LastTick.Equal(want) {
		t.Fatalf("active-src.lastTick = %v, want %v", active.LastTick, want)
	}
	if active.Failure != nil {
		t.Fatalf("active-src.failure = %v, want nil (no failure this pass)", active.Failure)
	}

	failing := byName["failing-src"]
	if failing.Failure == nil || failing.Failure.Count != 2 {
		t.Fatalf("failing-src.failure = %v, want {count:2, nextEligible:...}", failing.Failure)
	}
	if !failing.LastTick.IsZero() {
		t.Fatalf("failing-src.lastTick = %v, want omitted (never fired this pass)", failing.LastTick)
	}

	excluded := byName["disabled-src"]
	if excluded.Enabled != true || excluded.Excluded != true {
		t.Fatalf("disabled-src = %+v, want enabled=true excluded=true", excluded)
	}
	if !excluded.LastTick.IsZero() {
		t.Fatalf("disabled-src.lastTick = %v, want omitted (selector-excluded; never fired)", excluded.LastTick)
	}
	if excluded.Failure != nil {
		t.Fatalf("disabled-src.failure = %v, want nil", excluded.Failure)
	}
}

// TestStatusSources_MergedAndSortedByName is bead pg2-d1sem's regression
// test: statusSources must merge active and excluded sources into ONE
// alphabetical-by-name list, never two sequential unsorted groups (active
// then excluded). The excluded source "alpha-excluded" is deliberately
// alphabetically BEFORE the active "zeta-active" (and vice versa for
// "beta-excluded" after "yankee-active"), so a naive active-then-excluded
// concatenation would fail this ordering check.
func TestStatusSources_MergedAndSortedByName(t *testing.T) {
	active := []SourceReport{
		{Name: "zeta-active", Type: "pull"},
		{Name: "yankee-active", Type: "pull"},
	}
	excludedSources := []string{"alpha-excluded", "beta-excluded"}

	rows := statusSources(active, excludedSources, nil, nil, nil)
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4", len(rows))
	}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Name
	}
	want := []string{"alpha-excluded", "beta-excluded", "yankee-active", "zeta-active"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("name order = %v, want alphabetical (active+excluded merged) %v", got, want)
	}
	// The excluded/active flags travel with the right row through the sort.
	byName := make(map[string]status.Source, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	if byName["alpha-excluded"].Excluded != true {
		t.Fatalf("alpha-excluded.excluded = %v, want true", byName["alpha-excluded"].Excluded)
	}
	if byName["zeta-active"].Excluded != false {
		t.Fatalf("zeta-active.excluded = %v, want false", byName["zeta-active"].Excluded)
	}
}

// TestStatusSources_InFlight proves the DEC-OBS-2 (bead pg2-ugcrb) inFlight
// widening: an ACTIVE source present in the inFlight set reports true, an
// active source absent from it reports false, and an EXCLUDED source
// reports false unconditionally — never consulting inFlight at all, since a
// selector-excluded source's query never runs this run (STORY-OP-3).
func TestStatusSources_InFlight(t *testing.T) {
	active := []SourceReport{
		{Name: "fetching-now", Type: "pull"},
		{Name: "idle", Type: "pull"},
	}
	excludedSources := []string{"fetching-now"} // deliberately same name as an active source: proves exclusion wins without consulting inFlight
	inFlight := map[string]bool{"fetching-now": true}

	rows := statusSources(active, nil, nil, inFlight, nil)
	byName := make(map[string]status.Source, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	if got := byName["fetching-now"].InFlight; got != true {
		t.Fatalf("fetching-now (active) inFlight = %v, want true", got)
	}
	if got := byName["idle"].InFlight; got != false {
		t.Fatalf("idle inFlight = %v, want false", got)
	}

	excludedRows := statusSources(nil, excludedSources, nil, inFlight, nil)
	if got := excludedRows[0].InFlight; got != false {
		t.Fatalf("excluded source inFlight = %v, want false unconditionally, even though its name is present in the inFlight set", got)
	}
}

// TestStatusActivity_ParticipantField proves the DEC-OBS-2 (bead pg2-ugcrb)
// participant widening: statusActivity carries Entry.Participant through
// verbatim, always as a present key (never omitted), including the empty
// string for an entry no single participant settled.
func TestStatusActivity_ParticipantField(t *testing.T) {
	rows := statusActivity([]activity.Entry{
		{Type: "pr.changed", Outcome: "delivered", Participant: "df-feedback"},
		{Type: "pr.changed", Outcome: "missed"},
	})
	if got := rows[0].Participant; got != "df-feedback" {
		t.Fatalf("rows[0].participant = %v, want %q", got, "df-feedback")
	}
	if got := rows[1].Participant; got != "" {
		t.Fatalf("rows[1].participant = %v, want empty string (no single participant settled it)", got)
	}
}

// TestStatusQueues_SortedByType is bead pg2-d1sem's regression-only test:
// statusQueues already sorts its output by type via sort.Strings — this
// pins that behavior so a future change can't silently regress it (no
// production code change made for this function).
func TestStatusQueues_SortedByType(t *testing.T) {
	depth := map[string]int{
		"zeta-type":  3,
		"alpha-type": 1,
		"mid-type":   2,
	}

	rows := statusQueues(depth)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	got := []string{rows[0].Type, rows[1].Type, rows[2].Type}
	want := []string{"alpha-type", "mid-type", "zeta-type"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("type order = %v, want alphabetical %v", got, want)
	}
}

// TestStatusSources_DescriptionAlwaysPresent is pg2-ec754's red-first test:
// composeStatusReply's sources[] carries the ALWAYS-present `description`
// key, sourced from Options.SourceDescriptions (resolved once at boot from
// the full configured query set, mirroring SourceIntervalsMs) — never
// omitted, and "" (not a missing key) for a source with no entry in that
// map, whether it's active this pass or selector-excluded.
func TestStatusSources_DescriptionAlwaysPresent(t *testing.T) {
	svc := &Service{
		q:                  newQueue(t),
		bindings:           testBindings(),
		reg:                NewRegistry(nil),
		excludedSources:    []string{"disabled-src"},
		sourceDescriptions: map[string]string{"active-src": "polls the tracker for active-src items"},
	}
	now := time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC)
	svc.PublishTick(TickSnapshot{
		Sources: []SourceReport{
			{Name: "active-src", Type: "pull", LastTick: now},
		},
	})
	reply := svc.composeStatusReply(t.Context(), 0)
	sources := reply.Sources
	if len(sources) != 2 {
		t.Fatalf("sources = %v, want 2 entries (1 active + 1 selector-excluded)", reply.Sources)
	}
	byName := make(map[string]status.Source, len(sources))
	for _, s := range sources {
		byName[s.Name] = s
	}
	if got := byName["active-src"].Description; got != "polls the tracker for active-src items" {
		t.Fatalf("active-src.description = %v, want %q", got, "polls the tracker for active-src items")
	}
	if got := byName["disabled-src"].Description; got != "" {
		t.Fatalf("disabled-src.description = %v, want empty string (no SourceDescriptions entry)", got)
	}
}

// TestStatusResolvedConfig_PerParticipantPresentButEmpty is Task 4.1 Step
// 11's red-first test (operator-widened scope, Binding Decision 7):
// statusResolvedConfig's output always carries a perParticipant key
// holding map[string]any{} — present and empty, never omitted or nil.
func TestStatusResolvedConfig_PerParticipantPresentButEmpty(t *testing.T) {
	got := statusResolvedConfig(ResolvedConfig{RepoRoot: "/repo", ActiveRoles: 1, ActiveQueries: 1})
	if got.PerParticipant == nil || len(got.PerParticipant) != 0 {
		t.Fatalf("perParticipant = %#v, want an empty, non-nil map", got.PerParticipant)
	}
	if got.PollIntervalMs != 0 {
		t.Fatalf("pollIntervalMs = %v, want omitted when PollInterval is nil", got.PollIntervalMs)
	}

	// A non-nil PollInterval is echoed back as pollIntervalMs (Task 3.5 Step
	// 7's pre-existing branch, unchanged by this widening) — carries perParticipant too.
	pi := 10 * time.Second
	got2 := statusResolvedConfig(ResolvedConfig{RepoRoot: "/repo", PollInterval: &pi})
	if got2.PollIntervalMs != 10000 {
		t.Fatalf("pollIntervalMs = %v, want 10000", got2.PollIntervalMs)
	}
	if got2.PerParticipant == nil {
		t.Fatal("perParticipant absent, want present alongside pollIntervalMs too")
	}
}

// TestStatusCounters_MirrorsMetricCatalogVerbatim is Task 4.1 Step 13's
// red-first test (Binding Decision 7): counters reads back VERBATIM from
// the already-landed internal/metrics.Emitter/MetricsReader mechanism —
// the exact values mon.read would independently report for
// pg_router_unconsumed_expired/unknown_type_rejected/deduped — proving
// "never a second, divergently-counted set" by construction.
func TestStatusCounters_MirrorsMetricCatalogVerbatim(t *testing.T) {
	mp, reader := metrics.NewReadableProvider()
	emitter, err := metrics.New(t.Context(), mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	emitter.OnUnconsumedExpired("t1")
	emitter.OnUnknownTypeRejected("t2")
	emitter.OnDeduped("t3")

	svc := &Service{
		q:             newQueue(t),
		bindings:      testBindings(),
		reg:           NewRegistry(nil),
		metricsReader: reader,
	}
	reply := svc.composeStatusReply(t.Context(), 0)
	counters := reply.Counters
	if counters == nil {
		t.Fatalf("counters absent = %v, want present when MetricsReader != nil", reply.Counters)
	}
	want := &status.Counters{
		UnconsumedExpired:   map[string]int64{"t1": 1},
		UnknownTypeRejected: map[string]int64{"t2": 1},
		Deduped:             map[string]int64{"t3": 1},
	}
	if !reflect.DeepEqual(counters, want) {
		t.Fatalf("counters = %+v, want %+v", counters, want)
	}
}

// TestStatusCounters_NilMetricsReaderOmitsKey is Task 4.1 Step 14's
// red-first test: a Service built with MetricsReader left nil never sets
// the `counters` key at all — not nil, not {}, absent — matching
// resolvedConfig's own tick-nil omission idiom.
func TestStatusCounters_NilMetricsReaderOmitsKey(t *testing.T) {
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
	}
	reply := svc.composeStatusReply(t.Context(), 0)
	if reply.Counters != nil {
		t.Fatalf("counters = %v, want omitted entirely when MetricsReader is nil", reply.Counters)
	}
}

// TestStatusListeners_Busy proves each listener's `busy` (bead pg2-8ij5o) is
// metrics.RoleBusy: true for a role with an offer in flight, true for a role with
// busy declines counted since its last progress, false otherwise (an unsuccessful
// count alone, or a disabled role with no progress series, is not busy).
func TestStatusListeners_Busy(t *testing.T) {
	declared := []roles.Role{
		{Name: "flying", Enabled: true},
		{Name: "declined", Enabled: true},
		{Name: "calm", Enabled: true},
		{Name: "failing", Enabled: true},
		{Name: "off", Enabled: false},
	}
	inFlight := map[string]string{"flying": "pr.changed"}
	progress := map[string]metrics.RoleProgress{
		"flying": {}, "declined": {Busy: 2}, "calm": {}, "failing": {Unsuccessful: 4, AgeSeconds: 99999},
	}
	rows := statusListeners(declared, nil, nil, nil, inFlight, progress)
	got := map[string]any{}
	for _, r := range rows {
		got[r.Role] = r.Busy
	}
	want := map[string]any{"flying": true, "declined": true, "calm": false, "failing": false, "off": false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("busy by role = %v, want %v", got, want)
	}
}

// TestStatusSources_Busy proves a source's `busy` is true exactly while a fetch of
// it is in flight, and always false for an excluded source.
func TestStatusSources_Busy(t *testing.T) {
	active := []SourceReport{{Name: "fetching", Type: "pull"}, {Name: "idle", Type: "pull"}}
	inFlight := map[string]bool{"fetching": true, "skipped": true}
	rows := statusSources(active, []string{"skipped"}, nil, inFlight, nil)
	got := map[string]any{}
	for _, r := range rows {
		got[r.Name] = r.Busy
	}
	want := map[string]any{"fetching": true, "idle": false, "skipped": false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("busy by source = %v, want %v", got, want)
	}
}

// TestStatusBusy_ListsTheBusyRowsSorted proves the top-level `busy` object names
// exactly the rows whose own flag is set, sorted, with both lists always present.
func TestStatusBusy_ListsTheBusyRowsSorted(t *testing.T) {
	listeners := []status.Listener{{Role: "zeta", Busy: true}, {Role: "alpha", Busy: true}, {Role: "mid"}}
	sources := []status.Source{{Name: "s2"}}
	got := statusBusy(listeners, sources)
	want := status.Busy{Roles: []string{"alpha", "zeta"}, Sources: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statusBusy = %#v, want %#v", got, want)
	}
}

// A calendar source's status row carries the schedule, policy, next fire and the
// last handled slot, and the reply still validates against its own schema; a
// source with no calendar trigger has no calendar key.
func TestStatusSources_Calendar(t *testing.T) {
	svc := &Service{q: newQueue(t), bindings: testBindings(), reg: NewRegistry(nil)}
	svc.PublishTick(TickSnapshot{
		Sources: []SourceReport{
			{Name: "plain", Type: "pull"},
			{Name: "weekly-scan", Type: "pull", Calendar: &CalendarInfo{
				Schedule: "calendar 03:30 sun America/Los_Angeles", OnMissed: "next-slot", Next: "2026-11-08T03:30-08:00",
				Last: &CalendarLast{Slot: "2026-11-01T03:30-08:00", Outcome: "skipped", Reason: "router not running"},
			}},
			{Name: "fresh", Type: "pull", Calendar: &CalendarInfo{Schedule: "calendar 02:00 daily UTC", OnMissed: "run-now", Next: "2026-11-02T02:00Z"}},
		},
	})
	reply := svc.composeStatusReply(t.Context(), 0)
	rows := reply.Sources
	by := map[string]status.Source{}
	for _, r := range rows {
		by[r.Name] = r
	}
	if by["plain"].Calendar != nil {
		t.Fatal("a non-calendar source must carry no calendar key")
	}
	cal := by["weekly-scan"].Calendar
	if cal == nil || cal.Last == nil || cal.Schedule != "calendar 03:30 sun America/Los_Angeles" || cal.OnMissed != "next-slot" || cal.Next != "2026-11-08T03:30-08:00" ||
		cal.Last.Slot != "2026-11-01T03:30-08:00" || cal.Last.Outcome != "skipped" || cal.Last.Reason != "router not running" {
		t.Fatalf("calendar row = %v", cal)
	}
	if by["fresh"].Calendar.Last != nil {
		t.Fatal("no state entry yet: last must be absent")
	}
	check := func() error {
		b, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		return conformance.CheckBytes(StatusReplySchema, b)
	}
	if err := check(); err != nil {
		t.Fatalf("reply failed its own schema: %v", err)
	}
	// A wrong outcome is rejected by the schema.
	cal.Last.Outcome = "bogus"
	if err := check(); err == nil {
		t.Fatal("schema accepted an unknown calendar outcome")
	}
}
