package discover

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"

	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
)

const pollIv = 10 * time.Second

func ny(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// slotAt is the 02:00 America/New_York slot on 2027-06-d (EDT, no transition).
func slotAt(t *testing.T, d int) time.Time {
	return time.Date(2027, 6, d, 2, 0, 0, 0, ny(t))
}

// spyTimer is a real TimerQuery that records the slots it was asked for and the
// events it produced, and can be told to fail.
type spyTimer struct {
	query.TimerQuery
	fail  *bool
	evts  *[]event.Event
	slots *[]query.Slot
}

func (s spyTimer) RunSlot(ctx context.Context, env query.Env, sl query.Slot) ([]event.Event, error) {
	*s.slots = append(*s.slots, sl)
	if s.fail != nil && *s.fail {
		return nil, errors.New("boom")
	}
	e, err := s.TimerQuery.RunSlot(ctx, env, sl)
	*s.evts = append(*s.evts, e...)
	return e, err
}

type calSrc struct {
	src   query.Source
	fail  *bool
	evts  *[]event.Event
	slots *[]query.Slot
}

func newCalSrc(t *testing.T, name, onMissed string, at query.ClockTime, loc *time.Location) calSrc {
	fail := new(bool)
	evts := &[]event.Event{}
	slots := &[]query.Slot{}
	tq := query.TimerQuery{
		Meta: query.Meta{EmitTypes: []string{"scan.due"}, Trig: query.CalendarTrigger{
			At: at, Loc: loc, OnMissed: onMissed,
		}},
		Metadata: map[string]string{"lens": "security"},
	}
	return calSrc{
		src:  query.Source{Name: name, Query: spyTimer{TimerQuery: tq, fail: fail, evts: evts, slots: slots}},
		fail: fail, evts: evts, slots: slots,
	}
}

// harness drives produce with a fake clock and the state an Orchestrator would
// keep across passes (slots, failed slots, sampler, state file).
type harness struct {
	t       *testing.T
	q       *eventqueue.Queue
	srcs    query.SourceSet
	clock   time.Time
	start   time.Time
	slots   map[string]SlotRecord
	failed  map[string]time.Time
	sampler *calstate.Sampler
	path    string
	last    ProduceReport
}

func newHarness(t *testing.T, clock, start time.Time, srcs ...calSrc) *harness {
	h := &harness{
		t: t, q: newQueue(t), clock: clock, start: start,
		slots: map[string]SlotRecord{}, failed: map[string]time.Time{},
		sampler: calstate.NewSampler(pollIv, nil), path: filepath.Join(t.TempDir(), calstate.FileName),
	}
	for _, s := range srcs {
		h.srcs = append(h.srcs, s.src)
	}
	return h
}

func (h *harness) pass(at time.Time) ProduceReport {
	h.t.Helper()
	h.clock = at
	cad := Cadence{
		PollInterval: pollIv, Slots: h.slots, FailedSlots: h.failed, ProcessStart: h.start,
		Asleep:   h.sampler.AsleepAt,
		Save:     func(m map[string]SlotRecord) error { return calstate.Write(h.path, m) },
		LastTick: map[string]time.Time{},
	}
	rpt, err := produce(context.Background(), query.Env{}, h.srcs, h.q, core.NewBindings("scan.due"), cad,
		realSleep, func() time.Time { return h.clock }, nil, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.last = rpt
	return rpt
}

func (h *harness) depth() int { return h.q.DepthByType()["scan.due"] }

// restart simulates a router restart: a new process start, the state reloaded
// from the state file, no in-memory failed slots, a fresh sampler.
func (h *harness) restart(start time.Time) {
	h.start = start
	recs, err := calstate.Read(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	h.slots = map[string]SlotRecord{}
	for k, v := range recs {
		h.slots[k] = v
	}
	h.failed = map[string]time.Time{}
	h.sampler = calstate.NewSampler(pollIv, nil)
}

func (h *harness) rec(name string) SlotRecord { return h.slots[name] }

// sleepSpan fabricates a host sleep across [a, b] through the sampler.
func sleepSpan(s *calstate.Sampler, a, b time.Time) {
	s.Observe(calstate.Reading{Wall: a, Mono: 0})
	s.Observe(calstate.Reading{Wall: b, Mono: pollIv})
}

// primed returns a harness that already ran its first-start pass at start-of-day
// before slot day d, so last_slot = the slot of day d-1.
func primed(t *testing.T, d int, onMissed string) (*harness, calSrc) {
	cs := newCalSrc(t, "weekly", onMissed, query.ClockTime{Hour: 2}, ny(t))
	first := slotAt(t, d-1).Add(10 * time.Hour)
	h := newHarness(t, first, first.Add(-24*time.Hour), cs)
	h.pass(first)
	return h, cs
}

func TestCalendar_firstStartFiresNothing(t *testing.T) {
	h, _ := primed(t, 10, "run-now")
	if h.depth() != 0 {
		t.Fatalf("first start enqueued %d", h.depth())
	}
	r := h.rec("weekly")
	if !r.LastSlot.Equal(slotAt(t, 9)) || r.Outcome != calstate.OutcomeSkipped || r.Reason != calstate.ReasonFirstStart {
		t.Fatalf("first-start record = %+v", r)
	}
	if !h.last.Calendar["weekly"].FirstStart {
		t.Fatal("report must say first start")
	}
}

func TestCalendar_oneShotWithNoStateBehavesAsFirstStart(t *testing.T) {
	cs := newCalSrc(t, "weekly", "run-now", query.ClockTime{Hour: 2}, ny(t))
	q := newQueue(t)
	now := slotAt(t, 10).Add(time.Minute)
	_, err := produce(context.Background(), query.Env{}, query.SourceSet{cs.src}, q, core.NewBindings("scan.due"),
		Cadence{}, realSleep, func() time.Time { return now }, nil, nil)
	if err != nil || q.DepthByType()["scan.due"] != 0 {
		t.Fatalf("err=%v depth=%v", err, q.DepthByType())
	}
}

func TestCalendar_firesOnTimeOnceAndShapesTheEvent(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(-time.Minute))
	if h.depth() != 0 {
		t.Fatal("fired before the slot")
	}
	rpt := h.pass(slotAt(t, 10).Add(3 * time.Second))
	if h.depth() != 1 || !rpt.Calendar["weekly"].Fired {
		t.Fatalf("depth=%d report=%+v", h.depth(), rpt.Calendar["weekly"])
	}
	e := (*cs.evts)[0]
	if e.ID != "calendar:weekly:2027-06-10T06:00:00Z" || e.Item.ID != "2027-06-10T02:00:00-04:00" {
		t.Fatalf("event id %q (UTC) / item id %q (trigger offset)", e.ID, e.Item.ID)
	}
	if e.Item.Metadata["scheduled_for"] != "2027-06-10T02:00:00-04:00" || e.Item.Metadata["lens"] != "security" {
		t.Fatalf("metadata %v", e.Item.Metadata)
	}
	if _, ok := e.Item.Metadata["on_missed"]; ok {
		t.Fatal("on-time tick must omit on_missed")
	}
	r := h.rec("weekly")
	if r.Outcome != calstate.OutcomeFired || r.Reason != "" || !r.LastSlot.Equal(slotAt(t, 10)) {
		t.Fatalf("record %+v (3s late is within one poll interval, so no reason)", r)
	}
	h.pass(slotAt(t, 10).Add(time.Hour))
	if h.depth() != 1 || len(*cs.evts) != 1 {
		t.Fatal("a slot must yield at most one tick")
	}
}

// A pass delayed hours past the slot by load back-off or a slow query, while the
// wall and monotonic clocks advance together, is on time: it fires once and
// records how late.
func TestCalendar_delayedPassIsOnTimeAndRecordsLateness(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.sampler.Observe(calstate.Reading{Wall: slotAt(t, 10).Add(-time.Hour), Mono: 0})
	late := slotAt(t, 10).Add(5 * time.Hour)
	h.sampler.Observe(calstate.Reading{Wall: late, Mono: 6 * time.Hour})
	rpt := h.pass(late)
	cr := rpt.Calendar["weekly"]
	if h.depth() != 1 || cr.Missed || cr.Late != 5*time.Hour {
		t.Fatalf("depth=%d report=%+v", h.depth(), cr)
	}
	if r := h.rec("weekly"); r.Outcome != calstate.OutcomeFired || r.Reason != "late by 5h0m0s" {
		t.Fatalf("record %+v", r)
	}
	if _, ok := (*cs.evts)[0].Item.Metadata["on_missed"]; ok {
		t.Fatal("a late but on-time tick is not a catch-up")
	}
}

func TestCalendar_hostSleepAcrossSlot_runNowCatchesUp(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	sleepSpan(h.sampler, slotAt(t, 10).Add(-time.Hour), slotAt(t, 10).Add(2*time.Hour))
	rpt := h.pass(slotAt(t, 10).Add(2 * time.Hour))
	cr := rpt.Calendar["weekly"]
	if h.depth() != 1 || !cr.Missed || cr.MissedReason != calstate.ReasonHostAsleep {
		t.Fatalf("depth=%d report=%+v", h.depth(), cr)
	}
	if (*cs.evts)[0].Item.Metadata["on_missed"] != "true" {
		t.Fatal("catch-up tick must carry on_missed=true")
	}
	if r := h.rec("weekly"); r.Outcome != calstate.OutcomeCatchUp || r.Reason != calstate.ReasonHostAsleep {
		t.Fatalf("record %+v", r)
	}
}

func TestCalendar_hostSleepAcrossSlot_nextSlotSkipsAndAdvancesSuccessClock(t *testing.T) {
	h, cs := primed(t, 10, "next-slot")
	sleepSpan(h.sampler, slotAt(t, 10).Add(-time.Hour), slotAt(t, 10).Add(2*time.Hour))
	now := slotAt(t, 10).Add(2 * time.Hour)
	rpt := h.pass(now)
	if h.depth() != 0 || len(*cs.slots) != 0 {
		t.Fatalf("next-slot must not run or enqueue; depth=%d", h.depth())
	}
	if r := h.rec("weekly"); r.Outcome != calstate.OutcomeSkipped || r.Reason != calstate.ReasonHostAsleep || !r.LastSlot.Equal(slotAt(t, 10)) {
		t.Fatalf("record %+v", r)
	}
	if !rpt.LastTick["weekly"].Equal(now) {
		t.Fatalf("success clock not advanced: %v", rpt.LastTick)
	}
	// The following slot is on time again.
	h.pass(slotAt(t, 11).Add(time.Second))
	if h.depth() != 1 {
		t.Fatalf("slot after a skipped one must fire, depth=%d", h.depth())
	}
}

// A sleep that ends before the slot leaves the slot on time.
func TestCalendar_sleepEndingBeforeSlotIsOnTime(t *testing.T) {
	h, _ := primed(t, 10, "next-slot")
	sleepSpan(h.sampler, slotAt(t, 10).Add(-5*time.Hour), slotAt(t, 10).Add(-3*time.Hour))
	h.pass(slotAt(t, 10).Add(time.Second))
	if h.depth() != 1 || h.last.Calendar["weekly"].Missed {
		t.Fatalf("depth=%d %+v", h.depth(), h.last.Calendar["weekly"])
	}
}

// A producer pass that runs long right after a wake, while the sampler keeps
// reading, does not turn the next slot into a missed one.
func TestCalendar_longPassAfterWakeKeepsNextSlotOnTime(t *testing.T) {
	h, _ := primed(t, 10, "next-slot")
	sleepSpan(h.sampler, slotAt(t, 10).Add(-6*time.Hour), slotAt(t, 10).Add(-5*time.Hour))
	// The sampler keeps reading, wall and monotonic together, through the long pass.
	w := slotAt(t, 10).Add(-5 * time.Hour)
	mono := pollIv
	for w.Before(slotAt(t, 11).Add(2 * time.Hour)) {
		w, mono = w.Add(time.Minute), mono+time.Minute
		h.sampler.Observe(calstate.Reading{Wall: w, Mono: mono})
	}
	h.pass(slotAt(t, 10).Add(time.Second))
	h.pass(slotAt(t, 11).Add(2 * time.Hour)) // the pass ran long
	if h.depth() != 2 {
		t.Fatalf("both slots are on time, depth=%d", h.depth())
	}
	if len(h.sampler.Spans()) != 1 {
		t.Fatalf("a long pass stretched or added a span: %v", h.sampler.Spans())
	}
}

// A sleep across several slots under run-now: one catch-up tick for the latest
// slot, the earlier ones passed over and counted.
func TestCalendar_sleepAcrossSeveralSlotsRunNowFiresOnceAndCountsSuperseded(t *testing.T) {
	h, cs := primed(t, 10, "run-now") // last_slot = June 9
	sleepSpan(h.sampler, slotAt(t, 9).Add(time.Hour), slotAt(t, 13).Add(time.Hour))
	h.pass(slotAt(t, 13).Add(time.Hour))
	cr := h.last.Calendar["weekly"]
	if h.depth() != 1 || len(*cs.evts) != 1 {
		t.Fatalf("exactly one catch-up tick, depth=%d", h.depth())
	}
	if !(*cs.slots)[0].Time.Equal(slotAt(t, 13)) || !(*cs.slots)[0].OnMissed {
		t.Fatalf("catch-up slot %+v, want June 13 with OnMissed", (*cs.slots)[0])
	}
	if cr.Superseded != 3 || !cr.FirstSuperseded.Equal(slotAt(t, 10)) || !cr.LastSuperseded.Equal(slotAt(t, 12)) {
		t.Fatalf("superseded = %d first %v last %v", cr.Superseded, cr.FirstSuperseded, cr.LastSuperseded)
	}
}

func TestCalendar_runNowWithZeroAndOneMissedSlot(t *testing.T) {
	h, _ := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(-time.Hour)) // no slot due: zero missed
	if h.depth() != 0 || h.last.Calendar["weekly"].Due {
		t.Fatal("nothing due yet")
	}
	sleepSpan(h.sampler, slotAt(t, 10).Add(-time.Minute), slotAt(t, 10).Add(time.Hour))
	h.pass(slotAt(t, 10).Add(time.Hour))
	if h.depth() != 1 || h.last.Calendar["weekly"].Superseded != 0 {
		t.Fatalf("one missed slot, one tick; %+v", h.last.Calendar["weekly"])
	}
}

// A process started after the slot, with a remembered earlier slot, missed it.
func TestCalendar_processStartedAfterSlotIsMissed(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.restart(slotAt(t, 10).Add(time.Hour))
	h.pass(slotAt(t, 10).Add(2 * time.Hour))
	cr := h.last.Calendar["weekly"]
	if !cr.Missed || cr.MissedReason != calstate.ReasonRouterNotRunning || h.depth() != 1 {
		t.Fatalf("depth=%d %+v", h.depth(), cr)
	}
	if (*cs.evts)[0].Item.Metadata["on_missed"] != "true" {
		t.Fatal("catch-up expected")
	}
	if r := h.rec("weekly"); r.Outcome != calstate.OutcomeCatchUp || r.Reason != calstate.ReasonRouterNotRunning {
		t.Fatalf("record %+v", r)
	}
}

func TestCalendar_twoQueriesOneEventTypeBothEnqueued(t *testing.T) {
	a := newCalSrc(t, "scan-a", "run-now", query.ClockTime{Hour: 2}, ny(t))
	b := newCalSrc(t, "scan-b", "run-now", query.ClockTime{Hour: 2}, ny(t))
	first := slotAt(t, 9).Add(10 * time.Hour)
	h := newHarness(t, first, first.Add(-time.Hour), a, b)
	h.pass(first)
	h.pass(slotAt(t, 10).Add(time.Second))
	if h.depth() != 2 {
		t.Fatalf("depth=%d, want both enqueued", h.depth())
	}
	if (*a.evts)[0].ID == (*b.evts)[0].ID {
		t.Fatalf("ids must differ: %q", (*a.evts)[0].ID)
	}
}

func TestCalendar_restartAcrossSlotNoDoubleFireNoLoss(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(time.Second))
	h.restart(slotAt(t, 10).Add(time.Minute)) // second Orchestrator on the same state
	h.pass(slotAt(t, 10).Add(2 * time.Minute))
	if len(*cs.evts) != 1 {
		t.Fatalf("double fire across a restart: %d", len(*cs.evts))
	}
	h.pass(slotAt(t, 11).Add(time.Second)) // and the next slot is not lost
	if len(*cs.evts) != 2 {
		t.Fatalf("lost the next slot: %d", len(*cs.evts))
	}
}

// A slot inside a DST gap resolves to the transition instant; a restart right
// after it must not fire it again.
func TestCalendar_dstGapSlotAcrossRestart(t *testing.T) {
	cs := newCalSrc(t, "weekly", "run-now", query.ClockTime{Hour: 2, Minute: 30}, ny(t))
	gapSlot := time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC) // 03:00 EDT, the instant after the gap
	first := gapSlot.Add(-20 * time.Hour)
	h := newHarness(t, first, first.Add(-time.Hour), cs)
	h.pass(first)
	h.pass(gapSlot.Add(time.Second))
	if len(*cs.evts) != 1 || !(*cs.slots)[0].Time.Equal(gapSlot) {
		t.Fatalf("slots %v", *cs.slots)
	}
	h.restart(gapSlot.Add(time.Minute))
	h.pass(gapSlot.Add(2 * time.Minute))
	if len(*cs.evts) != 1 {
		t.Fatal("fired the gap slot twice across the restart")
	}
}

func TestCalendar_failedEnqueueIsRetriedThenRecorded(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	*cs.fail = true
	rpt := h.pass(slotAt(t, 10).Add(time.Second))
	if _, bad := rpt.SourceErrors["weekly"]; !bad || !h.last.Calendar["weekly"].EnqueueFailed {
		t.Fatalf("expected a recorded failure: %+v", h.last.Calendar["weekly"])
	}
	if !h.rec("weekly").LastSlot.Equal(slotAt(t, 9)) {
		t.Fatalf("slot recorded despite the failed enqueue: %+v", h.rec("weekly"))
	}
	*cs.fail = false
	h.pass(slotAt(t, 10).Add(11 * time.Second))
	if h.depth() != 1 || !h.rec("weekly").LastSlot.Equal(slotAt(t, 10)) || h.rec("weekly").Outcome != calstate.OutcomeFired {
		t.Fatalf("depth=%d rec=%+v", h.depth(), h.rec("weekly"))
	}
}

func TestCalendar_failedSlotPassedOverByNextSlotIsRecordedSkipped(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	*cs.fail = true
	h.pass(slotAt(t, 10).Add(time.Second))
	h.pass(slotAt(t, 11).Add(time.Second)) // still failing, and the next slot is here
	cr := h.last.Calendar["weekly"]
	if !cr.FailedSlotSkipped.Equal(slotAt(t, 10)) || cr.Superseded != 0 {
		t.Fatalf("report %+v (the failed slot is excluded from the superseded count)", cr)
	}
	if r := h.rec("weekly"); r.Outcome != calstate.OutcomeSkipped || r.Reason != calstate.ReasonEnqueueFailed || !r.LastSlot.Equal(slotAt(t, 10)) {
		t.Fatalf("record %+v", r)
	}
}

func TestCalendar_clockJumpForwardThenCorrectionClampsWithoutDuplicate(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(time.Second))
	// A forward jump behaves as a wake: slot 12 fires, slot 11 is passed over.
	h.pass(slotAt(t, 12).Add(time.Hour))
	if len(*cs.evts) != 2 || h.last.Calendar["weekly"].Superseded != 1 {
		t.Fatalf("events=%d %+v", len(*cs.evts), h.last.Calendar["weekly"])
	}
	// The clock is corrected back: the remembered slot is in the future.
	h.pass(slotAt(t, 10).Add(time.Hour))
	cr := h.last.Calendar["weekly"]
	if !cr.Clamped || !cr.ClampedFrom.Equal(slotAt(t, 12)) || len(*cs.evts) != 2 {
		t.Fatalf("clamp %+v events=%d", cr, len(*cs.evts))
	}
	if !h.rec("weekly").LastSlot.Equal(slotAt(t, 10)) {
		t.Fatalf("not clamped to Prev(now): %+v", h.rec("weekly"))
	}
}

// The stored slot is an instant, so a state file written for another schedule
// is simply compared against the configured one's Prev(now).
func TestCalendar_stateFromAnotherScheduleIsJustAnInstant(t *testing.T) {
	cs := newCalSrc(t, "weekly", "run-now", query.ClockTime{Hour: 2}, ny(t))
	h := newHarness(t, slotAt(t, 10).Add(time.Minute), slotAt(t, 1), cs)
	h.slots["weekly"] = SlotRecord{LastSlot: time.Date(2027, 6, 10, 5, 0, 0, 0, time.UTC), Outcome: calstate.OutcomeFired}
	h.pass(slotAt(t, 10).Add(time.Minute)) // Prev = 06:00Z, later than the stored 05:00Z
	if len(*cs.evts) != 1 {
		t.Fatalf("events=%d", len(*cs.evts))
	}
}

func TestCalendar_sameSlotIdDedupedWhileRetained(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(time.Second))
	again := ToQueueEvent((*cs.evts)[0])
	if _, err := h.q.Enqueue(again); err != nil {
		t.Fatal(err)
	}
	if h.depth() != 1 {
		t.Fatalf("same slot id must dedupe while retained, depth=%d", h.depth())
	}
}

func TestCalendar_expiryAttributesReachTheQueueEvent(t *testing.T) {
	h, cs := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(time.Second))
	qe := ToQueueEvent((*cs.evts)[0])
	if !qe.At.Equal(slotAt(t, 10)) || !qe.ExpiresAt.Equal(slotAt(t, 11)) {
		t.Fatalf("at=%v expiresAt=%v, want slot and next slot", qe.At, qe.ExpiresAt)
	}
}

func TestCalendar_levelBlockedConsumerStillEnqueues(t *testing.T) {
	h, _ := primed(t, 10, "run-now")
	h.q = levelQueueAt(t, "LOW", map[string][]levels.Consumer{"weekly": {{Req: diskOKReq(), Enabled: true}}})
	h.pass(slotAt(t, 10).Add(time.Second))
	if h.depth() != 1 {
		t.Fatalf("a level-blocked pass must still enqueue the timer's tick, depth=%d", h.depth())
	}
}

func TestCalendar_stateFileHoldsTheSlotAfterAFire(t *testing.T) {
	h, _ := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(time.Second))
	recs, err := calstate.Read(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if r := recs["weekly"]; !r.LastSlot.Equal(slotAt(t, 10)) || r.Outcome != "fired" || r.DecidedAt.IsZero() {
		t.Fatalf("file record %+v", r)
	}
}
