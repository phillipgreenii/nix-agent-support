package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

// The live router names a per-change event `<pr id>@<hash>` (the retry-window
// work); the shadow flow and the run record carry the bare PR id. Everything
// that joins the two sides MUST compare bare ids.

func TestBaseIDStripsTheChangeHashSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"acme/api#1":                       "acme/api#1",
		"acme/api#1@0123456789ab":          "acme/api#1",
		"pr.changed:acme/api#1@0123abcd":   "pr.changed:acme/api#1",
		"pr.changed:acme/api#1":            "pr.changed:acme/api#1",
		"":                                 "",
		"acme/api#1@":                      "acme/api#1",
		"acme/api#1@0123456789ab@ffffffff": "acme/api#1",
	} {
		if got := BaseID(in); got != want {
			t.Errorf("BaseID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadEventsNormalisesTheBeadAndTheChangeFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-events.jsonl")
	ev := `{"time":"2026-01-05T10:00:00Z","kind":"dispatch","event_type":"pr.changed","bead":"acme/api#1@0123456789ab","change":"pr.changed:acme/api#1@0123456789ab","enqueued_at":"2026-01-05T09:50:00Z"}
{"time":"2026-01-05T10:01:00Z","kind":"dispatch","event_type":"pr.changed","change":"pr.changed:acme/api#2@aaaaaaaaaaaa","enqueued_at":"2026-01-05T09:51:00Z"}
`
	if err := os.WriteFile(path, []byte(ev), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := readEvents(path)
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v %v", events, err)
	}
	if events[0].ID != "acme/api#1" || events[1].ID != "acme/api#2" {
		t.Errorf("ids must be bare: %q %q", events[0].ID, events[1].ID)
	}
	if events[0].Raw != "acme/api#1@0123456789ab" {
		t.Errorf("the raw per-change id must be kept for de-duplication: %q", events[0].Raw)
	}
}

// fixture builds an Input around one PR: a tick per minute from 09:01 to
// 11:59 (the first two are warm-up), minus the gap, with the given items.
type fixture struct {
	gapFrom, gapTo time.Time
	items          map[time.Time][]schema.Item
	events         []Dispatch
	queue          []QueueRow
}

func at(h, m, s int) time.Time { return time.Date(2026, 1, 5, h, m, s, 0, time.UTC) }

func (f fixture) input() Input {
	t0 := at(9, 0, 0)
	rows := []schema.Row{{Schema: schema.Version, Kind: schema.KindRunStart, Phase: "A", At: schema.Format(t0)}}
	if !f.gapFrom.IsZero() {
		rows = append(rows, schema.Row{Schema: schema.Version, Kind: schema.KindGap, Phase: "A", Reason: schema.GapSleep, GapFrom: schema.Format(f.gapFrom), GapTo: schema.Format(f.gapTo), At: schema.Format(f.gapTo)})
	}
	n := 0
	for slot := t0.Add(time.Minute); slot.Before(at(12, 0, 0)); slot = slot.Add(time.Minute) {
		if !f.gapFrom.IsZero() && !slot.Before(f.gapFrom) && slot.Before(f.gapTo) {
			continue
		}
		n++
		ts := slot.Add(time.Second)
		rows = append(rows, schema.Row{
			Schema: schema.Version, Kind: schema.KindTick, Phase: "A", Slot: schema.Format(slot), TickStart: schema.Format(ts),
			TickEnd: schema.Format(ts.Add(12 * time.Second)), DurationMS: 12000, ExitCode: schema.ExitPtr(0), Status: schema.StatusOK,
			Warmup: n <= 2, Items: f.items[slot],
		})
	}
	// Heartbeats keep the router "up" for the whole window.
	events := append([]Dispatch(nil), f.events...)
	for m := 0; m < 180; m += 3 {
		events = append(events, Dispatch{Time: t0.Add(time.Duration(m) * time.Minute), EventType: "desk.heartbeat", EnqueuedAt: t0.Add(time.Duration(m) * time.Minute)})
	}
	return Input{
		Manifest: scratch.Manifest{Phase: "A", CreatedAt: schema.Format(t0), Seeded: true, SweepMaxAge: "8760h", ReconcileAge: "30m"},
		Rows:     rows, Events: events, Queue: f.queue, Key: []byte("k"), SeedIDs: map[string]bool{"acme/api#1": true, "acme/api#2": true, "acme/api#3": true},
	}
}

func shadowItem1(id string, when time.Time) map[time.Time][]schema.Item {
	return map[time.Time][]schema.Item{when.Truncate(time.Minute): {{EntityID: id, Kinds: []string{"head_changed"}, Origin: "pg-connector", At: schema.Format(when), Fields: []string{"head_sha"}}}}
}

func live(id, hash string, enq time.Time) Dispatch {
	raw := id
	if hash != "" {
		raw = id + "@" + hash
	}
	return Dispatch{Time: enq.Add(20 * time.Minute), EventType: "pr.changed", EnqueuedAt: enq, StartedAt: enq.Add(18 * time.Minute), ID: BaseID(raw), Raw: raw, Role: "desk-pr"}
}

func TestHashSuffixedLiveEventMatchesTheBareShadowItem(t *testing.T) {
	f := fixture{
		items:  shadowItem1("acme/api#1", at(10, 5, 20)),
		events: []Dispatch{live("acme/api#1", "0123456789ab", at(10, 5, 0))},
	}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 1 || p.Misses.Missed != 0 {
		t.Fatalf("a suffixed live event must match the bare shadow item: %+v", p.Misses)
	}
	if p.Delay.VsLiveEnqueue.N != 1 || p.Delay.VsLiveEnqueue.Max != 20 {
		t.Errorf("delay = %+v", p.Delay.VsLiveEnqueue)
	}
	if p.Misses.CoverageCeiling != 1 {
		t.Errorf("coverage = %v", p.Misses.CoverageCeiling)
	}
}

func TestSuffixedLiveEventForASeededPRIsNotCopyStaleness(t *testing.T) {
	// No shadow item at all: a miss, but the PR IS in the seeded set, so the
	// class is not copy-staleness (the bare id is what the seed set holds).
	f := fixture{events: []Dispatch{live("acme/api#2", "0123456789ab", at(10, 5, 0))}}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 1 {
		t.Fatalf("misses = %+v", p.Misses)
	}
	if got := p.Misses.Entries[0].Class; got != ClassUnexplained {
		t.Errorf("class = %q, want %q", got, ClassUnexplained)
	}
}

func TestSuffixedLiveEventForAPRNotInTheSeedIsCopyStaleness(t *testing.T) {
	f := fixture{events: []Dispatch{live("acme/api#9", "0123456789ab", at(10, 5, 0))}}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 1 || p.Misses.Entries[0].Class != ClassCopyStaleness {
		t.Errorf("misses = %+v", p.Misses)
	}
}

func TestQueueEvictOfASuffixedEventIdIsLiveOnly(t *testing.T) {
	f := fixture{
		events: []Dispatch{live("acme/api#1", "0123456789ab", at(10, 5, 0))},
		queue:  []QueueRow{{Op: "evict", EventID: "pr.changed:acme/api#1@0123456789ab", At: at(10, 4, 50), Reason: "reemit"}},
	}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 1 || p.Misses.Entries[0].Class != ClassLiveOnly {
		t.Errorf("a re-emitted suffixed event is live-only: %+v", p.Misses)
	}
}

func TestTwoDistinctChangesAreNotCoalescing(t *testing.T) {
	// Two enqueues of the SAME PR with DIFFERENT hashes are two changes, not
	// the router coalescing one event; the same hash twice is coalescing.
	distinct := fixture{
		events: []Dispatch{live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0))},
		queue: []QueueRow{
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@aaaaaaaaaaaa", At: at(10, 4, 0)},
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@bbbbbbbbbbbb", At: at(10, 4, 30)},
		},
	}
	if p := Compute(distinct.input(), params()); p.Misses.Entries[0].Class != ClassUnexplained {
		t.Errorf("distinct hashes must not be classed as coalescing: %+v", p.Misses.Entries)
	}
	same := fixture{
		events: []Dispatch{live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0))},
		queue: []QueueRow{
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@aaaaaaaaaaaa", At: at(10, 4, 0)},
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@aaaaaaaaaaaa", At: at(10, 4, 30)},
		},
	}
	if p := Compute(same.input(), params()); p.Misses.Entries[0].Class != ClassLiveOnly {
		t.Errorf("the same event enqueued twice is coalescing: %+v", p.Misses.Entries)
	}
}

func TestDifferentChangesOfOnePRAtOneInstantAreBothCounted(t *testing.T) {
	// Same PR, same enqueue instant, different hashes: two live events.
	f := fixture{events: []Dispatch{
		live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0)),
		live("acme/api#1", "bbbbbbbbbbbb", at(10, 5, 0)),
	}}
	p := Compute(f.input(), params())
	if p.Live.Total != 2 {
		t.Errorf("live detected = %d, want 2 (de-duplication keys on the raw per-change id)", p.Live.Total)
	}
}

func TestMissedEventLaterDetectedByTheShadowIsReported(t *testing.T) {
	// The collector slept 10:00-10:20; the live feed enqueued at 10:05 and the
	// shadow's first tick after the gap flagged the PR at 10:21:30.
	f := fixture{
		gapFrom: at(10, 0, 0), gapTo: at(10, 20, 0),
		items: shadowItem1("acme/api#1", at(10, 21, 30)),
		events: []Dispatch{
			live("acme/api#1", "0123456789ab", at(10, 5, 0)),
			live("acme/api#2", "0123456789ab", at(10, 6, 0)), // never detected
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 2 || p.Misses.ByClass[ClassCollectorDown] != 2 {
		t.Fatalf("both are collector-down misses: %+v", p.Misses)
	}
	if p.Misses.LateDetected != 1 {
		t.Errorf("late detected = %d, want 1", p.Misses.LateDetected)
	}
	if want := 0.5; p.Misses.CoverageCeiling != want {
		t.Errorf("coverage = %v, want %v ((matched 0 + late 1) / in window 2)", p.Misses.CoverageCeiling, want)
	}
	if d := p.Delay.MissedThenDetected; d.N != 1 || d.Max != (16*time.Minute+30*time.Second).Seconds() {
		t.Errorf("late delay = %+v", d)
	}
	var withLate, without int
	for _, e := range p.Misses.Entries {
		if e.LateSeconds != nil {
			withLate++
		} else {
			without++
		}
	}
	if withLate != 1 || without != 1 {
		t.Errorf("entries with a later detection %d, without %d", withLate, without)
	}
}

func TestCoverageWhileTheCollectorWasUpExcludesCollectorDownEvents(t *testing.T) {
	// 10:30 gap: #3's event falls in it and is never detected (collector-down).
	// #1 is matched, #2 is an unexplained miss with no shadow item.
	f := fixture{
		gapFrom: at(10, 28, 0), gapTo: at(10, 40, 0),
		items: shadowItem1("acme/api#1", at(10, 5, 20)),
		events: []Dispatch{
			live("acme/api#1", "0123456789ab", at(10, 5, 0)),
			live("acme/api#2", "0123456789ab", at(10, 15, 0)),
			live("acme/api#3", "0123456789ab", at(10, 30, 0)),
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.ByClass[ClassCollectorDown] != 1 || p.Misses.Matched != 1 || p.Misses.Unexplained != 1 {
		t.Fatalf("misses = %+v", p.Misses)
	}
	if want := 1.0 / 3; p.Misses.CoverageCeiling < want-1e-9 || p.Misses.CoverageCeiling > want+1e-9 {
		t.Errorf("coverage = %v, want %v", p.Misses.CoverageCeiling, want)
	}
	if want := 0.5; p.Misses.CoverageCeilingWhenUp != want {
		t.Errorf("coverage while up = %v, want %v (1 matched of the 2 events outside the gap)", p.Misses.CoverageCeilingWhenUp, want)
	}
}

func TestOneShadowItemIsNotTheLateDetectionOfSeveralEvents(t *testing.T) {
	// One PR, one collector gap, ONE shadow item at 10:21:30 and three live
	// changes: two inside the gap and one at 10:21:20 that the item matches.
	// The item matches the third event, so it cannot also be a late detection;
	// the ceiling must not claim all three were covered.
	f := fixture{
		gapFrom: at(10, 0, 0), gapTo: at(10, 20, 0),
		items: shadowItem1("acme/api#1", at(10, 21, 30)),
		events: []Dispatch{
			live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0)),
			live("acme/api#1", "bbbbbbbbbbbb", at(10, 6, 0)),
			live("acme/api#1", "cccccccccccc", at(10, 21, 20)),
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 1 || p.Misses.Missed != 2 || p.Misses.LateDetected != 0 {
		t.Fatalf("misses = %+v", p.Misses)
	}
	if want := 1.0 / 3; p.Misses.CoverageCeiling < want-1e-9 || p.Misses.CoverageCeiling > want+1e-9 {
		t.Errorf("ceiling = %v, want %v", p.Misses.CoverageCeiling, want)
	}
}

func TestOneUnmatchedShadowItemStandsForOnlyOneMissedEvent(t *testing.T) {
	// Two missed events in the gap, one later shadow item: it is the late
	// detection of the EARLIER miss only.
	f := fixture{
		gapFrom: at(10, 0, 0), gapTo: at(10, 20, 0),
		items: shadowItem1("acme/api#1", at(10, 25, 0)),
		events: []Dispatch{
			live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0)),
			live("acme/api#1", "bbbbbbbbbbbb", at(10, 6, 0)),
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 2 || p.Misses.LateDetected != 1 {
		t.Fatalf("misses = %+v", p.Misses)
	}
	if got := *p.Misses.Entries[0].LateSeconds; got != (20*time.Minute).Seconds() || p.Misses.Entries[1].LateSeconds != nil {
		t.Errorf("late seconds = %v, second = %v", got, p.Misses.Entries[1].LateSeconds)
	}
}

func TestCeilingIsNotAvailableWithoutEvents(t *testing.T) {
	p := Compute(fixture{}.input(), params())
	if p.Live.InWindow != 0 || p.Misses.CoverageCeiling != 0 || p.Misses.CoverageCeilingWhenUp != 0 {
		t.Fatalf("live %+v misses %+v", p.Live, p.Misses)
	}
	rep := Report{Schema: SchemaID, Phases: []PhaseReport{p}}
	if md := Markdown(rep); !strings.Contains(md, "n/a (no events)") {
		t.Error("a ceiling with no events must read n/a, not 0.0 percent")
	}
}

// The bare id shape (rows written before per-change event ids, and every run
// record row) must keep working end to end.

func TestBareLiveEventStillMatchesTheShadowItem(t *testing.T) {
	f := fixture{
		items:  shadowItem1("acme/api#1", at(10, 5, 20)),
		events: []Dispatch{live("acme/api#1", "", at(10, 5, 0))},
	}
	p := Compute(f.input(), params())
	if p.Misses.Matched != 1 || p.Misses.Missed != 0 {
		t.Fatalf("a bare live event must match: %+v", p.Misses)
	}
}

func TestBareQueueRowsStillClassifyCoalescing(t *testing.T) {
	reemit := fixture{
		events: []Dispatch{live("acme/api#1", "", at(10, 5, 0))},
		queue:  []QueueRow{{Op: "evict", EventID: "pr.changed:acme/api#1", At: at(10, 4, 50), Reason: "reemit"}},
	}
	if p := Compute(reemit.input(), params()); p.Misses.Entries[0].Class != ClassLiveOnly {
		t.Errorf("a bare re-emit is live-only: %+v", p.Misses.Entries)
	}
	twice := fixture{
		events: []Dispatch{live("acme/api#1", "", at(10, 5, 0))},
		queue: []QueueRow{
			{Op: "enqueue", EventID: "pr.changed:acme/api#1", At: at(10, 4, 0)},
			{Op: "enqueue", EventID: "pr.changed:acme/api#1", At: at(10, 4, 30)},
		},
	}
	if p := Compute(twice.input(), params()); p.Misses.Entries[0].Class != ClassLiveOnly {
		t.Errorf("two bare enqueues are coalescing: %+v", p.Misses.Entries)
	}
}

func TestAnotherChangesQueueRowsDoNotExplainThisMiss(t *testing.T) {
	// Change A (hash a) was enqueued once and missed. Change B of the SAME PR
	// (hash b) was enqueued twice and re-emitted. That says nothing about A: a
	// genuine miss must stay unexplained, the one class that counts as failure.
	f := fixture{
		events: []Dispatch{live("acme/api#1", "aaaaaaaaaaaa", at(10, 5, 0))},
		queue: []QueueRow{
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@aaaaaaaaaaaa", At: at(10, 4, 0)},
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@bbbbbbbbbbbb", At: at(10, 4, 10)},
			{Op: "enqueue", EventID: "pr.changed:acme/api#1@bbbbbbbbbbbb", At: at(10, 4, 20)},
			{Op: "evict", EventID: "pr.changed:acme/api#1@bbbbbbbbbbbb", At: at(10, 4, 30), Reason: "reemit"},
		},
	}
	p := Compute(f.input(), params())
	if p.Misses.Missed != 1 || p.Misses.Entries[0].Class != ClassUnexplained {
		t.Errorf("misses = %+v", p.Misses)
	}
}

func TestIdenticalRawIdAndEnqueueTimeIsOneEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router-events.jsonl")
	row := `{"time":"2026-01-05T10:25:00Z","kind":"dispatch","event_type":"pr.changed","bead":"acme/api#1@aaaaaaaaaaaa","enqueued_at":"2026-01-05T10:05:00Z"}`
	// Differs only by the dispatch time, so the exact-line de-duplication of the
	// reader does not collapse it; the report must.
	row2 := `{"time":"2026-01-05T10:26:00Z","kind":"dispatch","event_type":"pr.changed","bead":"acme/api#1@aaaaaaaaaaaa","enqueued_at":"2026-01-05T10:05:00Z"}`
	if err := os.WriteFile(path, []byte(row+"\n"+row2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, _, err := readEvents(path)
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v %v", events, err)
	}
	f := fixture{events: events}
	if p := Compute(f.input(), params()); p.Live.Total != 1 {
		t.Errorf("the same per-change event dispatched twice is one live event: %d", p.Live.Total)
	}
}

func TestLateDetectionIsBoundedByTheLateWindow(t *testing.T) {
	// An item for the same PR hours later is a different change, not a late detection.
	f := fixture{
		gapFrom: at(10, 0, 0), gapTo: at(10, 20, 0),
		items:  shadowItem1("acme/api#1", at(11, 50, 0)),
		events: []Dispatch{live("acme/api#1", "0123456789ab", at(10, 5, 0))},
	}
	prm := params()
	prm.LateWindow = 30 * time.Minute
	p := Compute(f.input(), prm)
	if p.Misses.LateDetected != 0 {
		t.Errorf("an item %v after the live event is outside the late window %v", 105*time.Minute, prm.LateWindow)
	}
	if def := params().LateWindow; def != 3*time.Hour {
		t.Errorf("the default late window must cover the longest observed sleep gap (about 2h): %v", def)
	}
}
