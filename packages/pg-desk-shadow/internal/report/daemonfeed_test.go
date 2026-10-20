package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// feedItems adds one item to a fixture's per-slot item map.
func feedItems(m map[time.Time][]schema.Item, id string, when time.Time, origin string, kinds ...string) {
	slot := when.Truncate(time.Minute)
	m[slot] = append(m[slot], schema.Item{EntityID: id, Kinds: kinds, Origin: origin, At: schema.Format(when)})
}

func TestDaemonFeedItemsAreMatchedMissedLateDetectedAndShadowOnlyOnce(t *testing.T) {
	items := map[time.Time][]schema.Item{}
	feedItems(items, "acme/api#1", at(10, 0, 30), schema.OriginDaemonFeed, "head_changed")   // matches the live event at 10:00
	feedItems(items, "acme/api#2", at(10, 50, 0), schema.OriginDaemonFeed, "review_changed") // 30 minutes after the missed live event: late-detected
	feedItems(items, "acme/api#1", at(11, 30, 0), schema.OriginDaemonFeed, "head_changed")   // no live event near it: shadow-only
	feedItems(items, "acme/api#4", at(11, 0, 0), schema.OriginDaemonFeed, "entered_query")   // a PR first seen: first observation, not shadow-only
	feedItems(items, "acme/api#5", at(11, 10, 0), schema.OriginDaemonCIFeed, "ci_changed")   // the ci instance counts too
	f := fixture{
		items: items,
		events: []Dispatch{
			live("acme/api#1", "0123456789ab", at(10, 0, 0)),  // matched
			live("acme/api#2", "0123456789ab", at(10, 20, 0)), // missed, flagged later
			live("acme/api#3", "0123456789ab", at(10, 30, 0)), // missed, never flagged
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 1 || p.Misses.Missed != 2 || p.Misses.LateDetected != 1 {
		t.Errorf("misses = matched %d, missed %d, late %d; want 1, 2, 1", p.Misses.Matched, p.Misses.Missed, p.Misses.LateDetected)
	}
	// The late item is ALSO shadow-only (behavior doc, metric (a)); so is the 11:30 item and the ci item.
	if so := p.ShadowOnly; so.Total != 3 || so.FirstObservation != 1 {
		t.Errorf("shadow-only = %d (want 3), first observation = %d (want 1)", so.Total, so.FirstObservation)
	}
	if len(p.ShadowOnly.OtherOrigins) != 0 {
		t.Errorf("daemon items are not other origins: %v", p.ShadowOnly.OtherOrigins)
	}
	if p.Delay.VsLiveEnqueue.N != 1 || p.Delay.VsLiveEnqueue.Max != 30 {
		t.Errorf("delay = %+v", p.Delay.VsLiveEnqueue)
	}
	if p.Delay.SlotBoundS != 60 {
		t.Errorf("slot bound = %v, want 60", p.Delay.SlotBoundS)
	}
	if md := Markdown(Report{Phases: []PhaseReport{p}}); !strings.Contains(md, "Slot-length bound: 60 s") || !strings.Contains(md, "(slot-length bound 60 s)") {
		t.Error("the delay section must state the slot-length bound next to its figure")
	}
	if p.ShadowOnly.ByKind["entered_query"] != 0 {
		t.Errorf("a first observation must not be reported by kind: %v", p.ShadowOnly.ByKind)
	}
}

func TestAnEnteredQueryItemNearALiveEventIsNeitherFirstObservationNorShadowOnly(t *testing.T) {
	items := map[time.Time][]schema.Item{}
	feedItems(items, "acme/api#1", at(10, 0, 20), schema.OriginDaemonFeed, "entered_query")
	f := fixture{items: items, events: []Dispatch{live("acme/api#1", "0123456789ab", at(10, 0, 0))}}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 1 || p.ShadowOnly.Total != 0 || p.ShadowOnly.FirstObservation != 0 {
		t.Errorf("matched %d, shadow-only %d, first observation %d", p.Misses.Matched, p.ShadowOnly.Total, p.ShadowOnly.FirstObservation)
	}
}

func TestOnlyTheTwoDaemonOriginsAreTheShadowsDetections(t *testing.T) {
	items := map[time.Time][]schema.Item{}
	feedItems(items, "acme/api#1", at(10, 0, 30), legacyOrigin, "head_changed") // the old origin: ignored
	feedItems(items, "acme/api#2", at(10, 20, 30), schema.OriginDaemonFeed, "head_changed")
	slot := at(10, 20, 30).Truncate(time.Minute)
	items[slot][0].Replay = true // a reset replay: ignored
	feedItems(items, "acme/api#3", at(10, 40, 0), legacyOrigin, "head_changed")
	f := fixture{
		items: items,
		events: []Dispatch{
			live("acme/api#1", "0123456789ab", at(10, 0, 0)),
			live("acme/api#2", "0123456789ab", at(10, 20, 0)),
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 0 || p.Misses.Missed != 2 || p.Misses.LateDetected != 0 {
		t.Errorf("neither a replay nor an old-origin item may match or late-detect: %+v", p.Misses)
	}
	if p.ShadowOnly.Total != 0 || len(p.ShadowOnly.OtherOrigins) != 0 || p.Delay.VsLiveEnqueue.N != 0 {
		t.Errorf("shadow-only %d, other origins %v, delay %+v: ignored items count nowhere", p.ShadowOnly.Total, p.ShadowOnly.OtherOrigins, p.Delay.VsLiveEnqueue)
	}
	if p.DataQuality.IgnoredReplayItems != 1 || p.DataQuality.IgnoredLegacyItems != 2 {
		t.Errorf("ignored: replay %d (want 1), old origin %d (want 2)", p.DataQuality.IgnoredReplayItems, p.DataQuality.IgnoredLegacyItems)
	}
	if !strings.Contains(strings.Join(p.DataQuality.Statements, " "), "ignored by every figure") {
		t.Errorf("the data-quality header must say items were ignored: %v", p.DataQuality.Statements)
	}
}

func TestAScratchDirectoryWithOldOriginAndReplayItemsLoadsAndIgnoresThem(t *testing.T) {
	p, _, dir := build(t, SynthOptions{Seeded: true, IgnoredItems: true})
	if _, err := Load(dir); err != nil {
		t.Fatalf("a scratch directory with old-origin items must load: %v", err)
	}
	if p.DataQuality.IgnoredReplayItems != 1 || p.DataQuality.IgnoredLegacyItems != 1 {
		t.Errorf("ignored = %d replay, %d old origin", p.DataQuality.IgnoredReplayItems, p.DataQuality.IgnoredLegacyItems)
	}
	// PR 102's event stays an unexplained miss: its replayed and old-origin items count nowhere.
	base, _, _ := build(t, SynthOptions{Seeded: true})
	if p.Misses.Matched != base.Misses.Matched || p.Misses.Missed != base.Misses.Missed || p.Misses.LateDetected != base.Misses.LateDetected || p.ShadowOnly.Total != base.ShadowOnly.Total {
		t.Errorf("ignored items changed a figure: %+v vs %+v", p.Misses, base.Misses)
	}
}

func TestCIDrivenChangesAreComparedWithTheLiveSide(t *testing.T) {
	p, _, dir := build(t, SynthOptions{Seeded: true, CIChanges: true, LiveCategorization: true})
	ci := p.CIDriven
	if ci.Daemon.Changes != 3 || ci.Daemon.AlsoSeenByOther != 2 {
		t.Errorf("daemon side = %d changes, %d also seen by live; want 3 and 2 (one PR never moved to blocked on the live side)", ci.Daemon.Changes, ci.Daemon.AlsoSeenByOther)
	}
	if !ci.LiveObservable || ci.Live == nil || ci.Live.Changes != 2 || ci.Live.AlsoSeenByOther != 2 {
		t.Fatalf("live side = %+v observable %v; want 2 changes, both also seen by the daemon", ci.Live, ci.LiveObservable)
	}
	if ci.LiveSource == "" || strings.Contains(ci.Statement, "live side not observable") {
		t.Errorf("source %q statement %q", ci.LiveSource, ci.Statement)
	}
	in, _ := Load(dir)
	lab := Labeler{Key: in.Key}
	unmatched := 0
	for _, e := range ci.Daemon.Entries {
		if !strings.HasPrefix(e.PR, "pr-") || strings.Contains(e.PR, "#") {
			t.Errorf("entry %q is not an HMAC label", e.PR)
		}
		if !e.AlsoSeenByOther {
			unmatched++
			if e.PR != lab.Label(synthID(112)) || e.To != "blocked" || e.From != "reviewable" {
				t.Errorf("unmatched entry = %+v", e)
			}
		}
	}
	if unmatched != 1 {
		t.Errorf("unmatched daemon entries = %d, want 1", unmatched)
	}
	// The combined view carries the same figures.
	rep := Report{Phases: []PhaseReport{p}}
	c := Combine(rep.Phases)
	if c.CIDaemonChanges != 3 || c.CIDaemonSeenByLive != 2 || !c.CILiveObservable || c.CILiveChanges != 2 || c.CILiveSeenByDaemon != 2 {
		t.Errorf("combined = %+v", c)
	}
	rep.Combined = c
	if md := Markdown(rep); !strings.Contains(md, "(h) CI-driven categorization changes") || !strings.Contains(md, "daemon side 3 (2 also seen by the live side)") {
		t.Errorf("markdown lacks the CI section:\n%s", md)
	}
}

func TestCIDrivenLiveSideNotObservableWhenNoLiveRowNamesACategorization(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true, CIChanges: true})
	ci := p.CIDriven
	if ci.LiveObservable || ci.Live != nil {
		t.Fatalf("no live row names a categorization: %+v", ci)
	}
	if ci.Daemon.Changes != 3 || ci.Daemon.AlsoSeenByOther != 0 {
		t.Errorf("the daemon side alone is reported: %+v", ci.Daemon)
	}
	if !strings.HasPrefix(ci.Statement, "live side not observable: ") {
		t.Errorf("statement = %q", ci.Statement)
	}
	rep := Report{Phases: []PhaseReport{p}, Combined: Combine([]PhaseReport{p})}
	if rep.Combined.CILiveObservable {
		t.Error("the combined view must not claim a live figure")
	}
	b, _ := json.Marshal(rep)
	if !strings.Contains(string(b), `"live_observable":false`) {
		t.Errorf("the JSON section must carry live_observable false: %s", b)
	}
	if md := Markdown(rep); !strings.Contains(md, "live side not observable: ") {
		t.Error("the markdown must carry the live side not observable statement")
	}
}

func TestCIChangesOrderByTimeThenSeqAndTheFirstItemIsNotAChange(t *testing.T) {
	t0 := at(10, 0, 0)
	pts := []ciPoint{
		{id: "acme/api#1", at: t0, seq: 2, state: rollupState("success")},
		{id: "acme/api#1", at: t0, seq: 1, state: rollupState("failure")}, // same instant, earlier seq: sets the state first
		{id: "acme/api#1", at: t0.Add(time.Minute), seq: 3, state: rollupState("none")},
		{id: "acme/api#2", at: t0, seq: 1, state: rollupState("pending")}, // the first item of a PR is not a change
		{id: "acme/api#2", at: t0.Add(time.Minute), seq: 2, state: rollupState("success")},
	}
	moves := ciMoves(pts)
	if len(moves) != 1 || moves[0].id != "acme/api#1" || moves[0].from != stateBlocked || moves[0].to != stateReviewable {
		t.Errorf("moves = %+v, want one blocked-to-reviewable move of #1", moves)
	}
}

func TestSkippedBudgetRowsFeedTheCostHours(t *testing.T) {
	in := fixture{}.input()
	n10 := 0
	for i := range in.Rows {
		r := &in.Rows[i]
		if r.Kind != schema.KindTick {
			continue
		}
		slot := parseTS(r.Slot)
		switch {
		case slot.Hour() == 11: // an hour holding only skipped rows
			r.Status, r.SkippedBudget, r.ExitCode, r.DurationMS, r.GraphQLCostSum = schema.StatusSkippedBudget, true, nil, 0, 5
		case slot.Equal(at(10, 15, 0)):
			r.Status, r.SkippedBudget, r.ExitCode, r.DurationMS, r.GraphQLCostSum = schema.StatusSkippedBudget, true, nil, 0, 7
		case slot.Hour() == 10:
			r.GraphQLCostSum = 1
			n10++
		}
	}
	p := Compute(in, params())
	hours := map[string]Hour{}
	for _, h := range p.Cost.Hours {
		hours[h.Hour] = h
	}
	h11, ok := hours["2026-01-05T11:00Z"]
	if !ok {
		t.Fatalf("an hour holding only skipped rows needs a bucket: %+v", p.Cost.Hours)
	}
	if h11.GraphQLCost != 60*5 || h11.Ticks != 0 {
		t.Errorf("hour 11 = %+v, want cost 300 and no executed ticks", h11)
	}
	h10 := hours["2026-01-05T10:00Z"]
	if h10.GraphQLCost != n10*1+7 || h10.Ticks != n10 {
		t.Errorf("hour 10 = %+v, want cost %d (executed %d plus the skipped row's 7) and %d executed ticks", h10, n10+7, n10, n10)
	}
	if p.DataQuality.SkippedTicks != 61 || p.DataQuality.ExecutedTicks != 179-61 {
		t.Errorf("tick counts stay executed-only: executed %d skipped %d", p.DataQuality.ExecutedTicks, p.DataQuality.SkippedTicks)
	}
	if p.Cost.TickDurationS.N != 118-2 { // executed ticks outside warm-up
		t.Errorf("durations stay executed-only: %+v", p.Cost.TickDurationS)
	}
}
