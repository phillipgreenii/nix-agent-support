package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

func params() Params {
	p := DefaultParams()
	p.Loc = time.UTC
	return p
}

func build(t *testing.T, o SynthOptions) (PhaseReport, Synthetic, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Synthesize(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Build([]string{dir}, params())
	if err != nil {
		t.Fatal(err)
	}
	return rep.Phases[0], s, dir
}

func missClass(p PhaseReport, id string, lab Labeler) string {
	for _, e := range p.Misses.Entries {
		if e.PR == lab.Label(id) {
			return e.Class
		}
	}
	return ""
}

func TestInjectedFeedMissesAreClassifiedExactlyOnce(t *testing.T) {
	p, _, dir := build(t, SynthOptions{Seeded: true})
	in, _ := Load(dir)
	lab := Labeler{Key: in.Key}
	want := map[int]string{
		102: ClassUnexplained,   // injected feed miss: nothing explains it
		103: ClassCollectorDown, // inside the recorded sleep gap
		104: ClassLiveOnly,      // the live queue re-emitted it
		105: ClassBlindSpot,     // a later sweep-origin hydration changed only mergeability
		108: ClassUnexplained,   // only a local-reconcile item: NOT SHADOW-DETECTED
	}
	for n, class := range want {
		if got := missClass(p, synthID(n), lab); got != class {
			t.Errorf("PR %d: class %q, want %q", n, got, class)
		}
	}
	total := 0
	for _, c := range MissClasses {
		total += p.Misses.ByClass[c]
	}
	if total != p.Misses.Missed || len(p.Misses.Entries) != p.Misses.Missed {
		t.Errorf("every miss must have exactly one class: sum %d, missed %d, entries %d", total, p.Misses.Missed, len(p.Misses.Entries))
	}
	if p.Misses.Unexplained != 2 {
		t.Errorf("only unexplained counts as failure: %d", p.Misses.Unexplained)
	}
}

func TestNewPRReconcileFromPgConnectorIsShadowDetected(t *testing.T) {
	p, _, dir := build(t, SynthOptions{Seeded: true})
	in, _ := Load(dir)
	lab := Labeler{Key: in.Key}
	if c := missClass(p, synthID(106), lab); c != "" {
		t.Errorf("a first-observation reconcile with origin pg-connector must count as detected, got miss %q", c)
	}
	if p.Misses.Matched != 2 { // 101 and 106
		t.Errorf("matched = %d, want 2", p.Misses.Matched)
	}
}

func TestLocalReconcileAndSweepOriginItemsAreNotShadowDetected(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true})
	if p.ShadowOnly.OtherOrigins["local-reconcile"] != 1 || p.ShadowOnly.OtherOrigins["sweep"] != 1 {
		t.Errorf("other origins must be reported separately: %v", p.ShadowOnly.OtherOrigins)
	}
	if p.ShadowOnly.OtherReal["sweep"] != 1 || p.ShadowOnly.OtherReal["local-reconcile"] != 0 {
		t.Errorf("a sweep-origin find is REAL only with a projection field change: %v", p.ShadowOnly.OtherReal)
	}
}

func TestShadowOnlyNoiseItem(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true})
	if p.ShadowOnly.Total != 1 || p.ShadowOnly.ByKind["head_changed"] != 1 || p.ShadowOnly.ByField["head_sha"] != 1 {
		t.Errorf("shadow-only = %+v", p.ShadowOnly)
	}
}

func TestSweepCaughtShadowCaughtAndShadowMissed(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true})
	s := p.Sweep
	if s.Headline != 2 || s.ShadowDetected != 1 || s.ShadowMissed != 1 || s.MissedBlindSpot != 1 {
		t.Errorf("sweep = %+v", s)
	}
	if s.OtherCauses["created"] != 1 {
		t.Errorf("created must be reported separately: %v", s.OtherCauses)
	}
	if s.FeedMissed != 1 || s.FeedMissedRate != 0.5 {
		t.Errorf("the rate at which the live sweep catches what the feed missed = %v (%d)", s.FeedMissedRate, s.FeedMissed)
	}
}

func TestEverySweepSayingHashChangedIsNotSweepCaught(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true, AllHashChanged: true})
	b := p.DataQuality.Baseline
	if b.HashChangedShare < 0.99 || b.HashSignalUsable {
		t.Errorf("baseline = %+v", b)
	}
	if !strings.Contains(b.HashStatement, "UNUSABLE") {
		t.Errorf("the report must state the hash signal is unusable: %q", b.HashStatement)
	}
	// Only anchor-writing rows are SWEEP-CAUGHT: 40 hash-changed rows add nothing.
	if p.Sweep.Headline != 2 {
		t.Errorf("headline = %d, want 2", p.Sweep.Headline)
	}
}

func TestAbsentRunRecordLog(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true, NoRunRecord: true})
	if p.Sweep.Headline != 0 || p.DataQuality.Baseline.RunRecordPresent {
		t.Errorf("absent run record: %+v", p.DataQuality.Baseline)
	}
	found := false
	for _, s := range p.DataQuality.Statements {
		found = found || strings.Contains(s, "run-record copy is ABSENT")
	}
	if !found {
		t.Error("the data-quality header must say the run-record copy is absent")
	}
}

func TestWarmupEventsAreExcludedAndEnqueueTimeIsUsed(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true})
	if p.Live.Total != 8 || p.Live.InWindow != 7 {
		t.Errorf("live detected = %d (the row without event_type is unusable)", p.Live.Total)
	}
	// Detection delay is relative to enqueued_at (shadow 30s and 20s after it), not started_at (+18m).
	if p.Delay.VsLiveEnqueue.N != 2 || p.Delay.VsLiveEnqueue.Max != 30 || p.Delay.VsLiveEnqueue.P50 != 20 {
		t.Errorf("delay vs enqueue = %+v", p.Delay.VsLiveEnqueue)
	}
	if p.Live.MedianWaitSeconds != 18*60 {
		t.Errorf("enqueue-to-start wait = %v", p.Live.MedianWaitSeconds)
	}
}

func TestDataQualityAndStopCriteria(t *testing.T) {
	p, _, _ := build(t, SynthOptions{Seeded: true})
	dq := p.DataQuality
	if dq.Gaps["sleep"].Count != 1 || dq.WarmupTicks != 2 || dq.SkippedTicks != 1 {
		t.Errorf("data quality = %+v", dq)
	}
	if dq.UptimeSpec <= dq.UptimeRaw {
		t.Errorf("the spec uptime excludes sleep gaps (%.3f) and must exceed the raw one (%.3f)", dq.UptimeSpec, dq.UptimeRaw)
	}
	if p.Stop.Complete {
		t.Error("three hours of ticks cannot satisfy the stop criteria")
	}
	if len(p.DataQuality.Statements) < 2 {
		t.Errorf("seeded runs must state the warm-up simplification: %v", p.DataQuality.Statements)
	}
}

func TestLabelsAreStableKeyedAndHideTheID(t *testing.T) {
	a, b := Labeler{Key: []byte("k1")}, Labeler{Key: []byte("k2")}
	id := synthID(101)
	if a.Label(id) != a.Label(id) || a.Label(id) == b.Label(id) || strings.Contains(a.Label(id), "101") {
		t.Errorf("labels: %s %s", a.Label(id), b.Label(id))
	}
	if !strings.HasPrefix(a.Label(id), "pr-") || len(a.Label(id)) != 11 {
		t.Errorf("label shape %q", a.Label(id))
	}
}

func TestReportContainsNoInputIDSlugOrTitle(t *testing.T) {
	dir := t.TempDir()
	s, err := Synthesize(dir, SynthOptions{Seeded: true})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Build([]string{dir}, params())
	if err != nil {
		t.Fatal(err)
	}
	jp, mp, err := Write(rep, filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{jp, mp} {
		b, _ := os.ReadFile(p)
		if l := Leaks(string(b), s); len(l) > 0 {
			t.Errorf("%s leaks %v", p, l)
		}
		for _, n := range []string{"101", "102", "103"} { // bare numbers of the ids
			if strings.Contains(string(b), "#"+n) {
				t.Errorf("%s leaks a PR number", p)
			}
		}
	}
	if err := SelfTest(os.Stderr); err != nil {
		t.Fatal(err)
	}
}

func TestReportIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Synthesize(dir, SynthOptions{Seeded: true}); err != nil {
		t.Fatal(err)
	}
	var out [2]string
	for i := range out {
		rep, err := Build([]string{dir}, params())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rep)
		out[i] = string(b) + Markdown(rep)
	}
	if out[0] != out[1] {
		t.Error("re-running the generator on the same inputs changed the output")
	}
}

func TestCombineAcrossPhases(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if _, err := Synthesize(a, SynthOptions{Seeded: true, Phase: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Synthesize(b, SynthOptions{Seeded: false, Phase: "B"}); err != nil {
		t.Fatal(err)
	}
	rep, err := Build([]string{b, a}, params())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Phases) != 2 || rep.Phases[0].Phase != "A" || rep.Phases[1].Phase != "B" || rep.Combined == nil {
		t.Fatalf("phases = %d combined = %v", len(rep.Phases), rep.Combined)
	}
	c := rep.Combined
	if c.Missed != rep.Phases[0].Misses.Missed+rep.Phases[1].Misses.Missed || c.LiveDetected != rep.Phases[0].Live.InWindow+rep.Phases[1].Live.InWindow {
		t.Errorf("combined counts do not add up: %+v", c)
	}
	if c.TickDurationS.N != rep.Phases[0].Cost.TickDurationS.N+rep.Phases[1].Cost.TickDurationS.N {
		t.Errorf("combined distributions must be recomputed from the merged samples: %+v", c.TickDurationS)
	}
	if !strings.Contains(Markdown(rep), "Combined (A + B)") {
		t.Error("the markdown must carry the combined section")
	}
	if !strings.Contains(strings.Join(rep.Phases[1].DataQuality.Statements, " "), "No seeding") {
		t.Error("phase B statements")
	}
}

func TestLiveReaderTimestampsAndShapes(t *testing.T) {
	dir := t.TempDir()
	l := scratch.Layout{Root: dir}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	// Queue rows carry the local offset, and it changes on a DST boundary.
	q := strings.Join([]string{
		`{"op":"enqueue","eventId":"pr.changed:acme/api#1","at":"2026-10-31T23:30:00.5-04:00"}`,
		`{"op":"evict","eventId":"pr.changed:acme/api#1","at":"2026-11-01T01:10:00-05:00","reason":"reemit"}`,
		`{"op":"archive","eventId":"pr.changed:acme/api#1","at":"2026-11-01T06:00:00Z"}`,
		`{"_shadow":"reset","reason":"inode-changed"}`,
		`{"op":"evict","eventId":"pr.changed:acme/api#1","at":"2026-11-01T01:10:00-05:00","reason":"reemit"}`, // exact duplicate after a reset marker
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(l.LiveDir(), "router-queue.jsonl"), []byte(q), 0o600); err != nil {
		t.Fatal(err)
	}
	ev := strings.Join([]string{
		`{"time":"2026-10-31T23:40:00Z","kind":"dispatch","event_type":"pr.changed","bead":"acme/api#1","enqueued_at":"2026-10-31T23:30:00Z","started_at":"2026-10-31T23:55:00Z","role":"desk-pr"}`,
		`{"time":"2026-10-31T23:41:00Z","kind":"dispatch","bead":"acme/api#2","change":"pr.changed:acme/api#2"}`, // before event_type existed
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(l.LiveDir(), "router-events.jsonl"), []byte(ev), 0o600); err != nil {
		t.Fatal(err)
	}
	queue, markers, err := readQueue(filepath.Join(l.LiveDir(), "router-queue.jsonl"))
	if err != nil || markers != 1 {
		t.Fatalf("queue %v markers %d", err, markers)
	}
	if len(queue) != 3 {
		t.Fatalf("exact duplicate lines must collapse: %d rows", len(queue))
	}
	utc := func(r QueueRow) string { return r.At.Format(time.RFC3339Nano) }
	if utc(queue[0]) != "2026-11-01T03:30:00.5Z" || utc(queue[1]) != "2026-11-01T06:10:00Z" {
		t.Errorf("offsets must normalise to UTC across the DST change: %s %s", utc(queue[0]), utc(queue[1]))
	}
	events, _, err := readEvents(filepath.Join(l.LiveDir(), "router-events.jsonl"))
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v %v", events, err)
	}
	if events[0].EnqueuedAt.IsZero() || !events[0].StartedAt.After(events[0].EnqueuedAt) || !events[1].EnqueuedAt.IsZero() || events[1].ID != "acme/api#2" {
		t.Errorf("events = %+v", events)
	}
}

func TestMissingRowsAreTolerated(t *testing.T) {
	dir := t.TempDir()
	if _, err := Synthesize(dir, SynthOptions{Seeded: true}); err != nil {
		t.Fatal(err)
	}
	// A half-written final line, as a crash leaves.
	f, _ := os.OpenFile(scratch.Layout{Root: dir}.TicksFile(), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"schema":"x","kind":"tick","slo`)
	_ = f.Close()
	if rows, err := schema.ReadAll(scratch.Layout{Root: dir}.TicksFile()); err != nil || len(rows) == 0 {
		t.Fatalf("%d %v", len(rows), err)
	}
}
