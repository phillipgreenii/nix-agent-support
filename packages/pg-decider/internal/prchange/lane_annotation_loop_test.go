package prchange

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/apply"
)

// The annotation feed loop (pg2-aoehwj). `pg-desk pr annotation-items` turns a
// PR's annotation changes into pr.changed items whose metadata.origin the role
// renders into pg-pr-change's --origin. A decider's own annotation writes come
// back through the feed; the composed command skips them before the lock, so the
// loop ends. The real pg-desk, pg-decider and pg-pr-change run over the fake (or
// real) tracker, as in every lane scenario.

const annotationConsumer = "annotations:pr-lane"

// annotationItem is the part of a feed item the role reads.
type feedItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Metadata struct {
		EntityID string `json:"entity_id"`
		Origin   string `json:"origin"`
	} `json:"metadata"`
}

// pollFeed runs the real feed verb once and decodes its items.
func pollFeed(t *testing.T, w *e2eWorld) []feedItem {
	t.Helper()
	res := w.r.mustDesk("pr", "annotation-items", "--consumer", annotationConsumer, "--retry-window", "10m")
	var items []feedItem
	if err := json.Unmarshal([]byte(res.stdout), &items); err != nil {
		t.Fatalf("the feed's output is not a JSON array of items: %v\n%s", err, res.stdout)
	}
	return items
}

func annotationLoopScenarios(l *laneRun) {
	tools, run := l.tools, l.run
	run("a-decider-annotation-write-fed-back-through-the-annotation-feed-is-skipped-and-the-loop-ends", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		// The consumer registers at the current maximum: nothing to replay.
		if items := pollFeed(t, w); len(items) != 0 {
			t.Fatalf("the first poll replayed %d pre-existing records: %+v", len(items), items)
		}

		// An operator requests a force review: one item, origin pg-desk.
		w.r.mustDesk("pr", "force-review", id)
		items := pollFeed(t, w)
		if len(items) != 1 || items[0].Metadata.Origin != "pg-desk" || items[0].Type != "pr.changed" || items[0].Metadata.EntityID != id {
			t.Fatalf("the operator's force-review produced %+v, want one pr.changed item of origin pg-desk for %s", items, id)
		}

		// The item's id is a valid composed-command id: it normalizes to the PR,
		// and its delivery key is the 12 hex digits after the @ (a router retry of
		// the same item is the same key, so escalate_after counts it once).
		raw := items[0].ID
		if got, err := NormalizeID(raw); err != nil || got != id {
			t.Fatalf("NormalizeID(%q) = %q, %v, want %q", raw, got, err, id)
		}
		k1, err1 := ResolveKey("", raw)
		k2, err2 := ResolveKey("<no value>", raw)
		if err1 != nil || err2 != nil || k1 != k2 || k1.Source != KeySourceSuffix || !k1.Has() {
			t.Fatalf("ResolveKey(%q) = %+v, %+v (%v, %v), want one id-suffix key", raw, k1, k2, err1, err2)
		}

		// The decider is delivered the item (origin pg-desk: it runs). It creates
		// the forced review item and consumes the flag, which are annotation writes
		// of the decider's own origin.
		before := len(w.r.runRecords())
		w.apply("--origin", items[0].Metadata.Origin, raw)
		recs := w.r.runRecords()
		if len(recs) != before+1 || recs[len(recs)-1].Skipped || recs[len(recs)-1].SeqSource != KeySourceSuffix {
			t.Fatalf("the delivery of an operator change did not run as a normal run: %+v", recs[before:])
		}
		if set, _ := w.forced(id); set {
			t.Fatal("setup: the decider did not consume the force_review flag, so it wrote no annotation")
		}

		// The decider's own writes come back through the feed, every one of them
		// carrying the decider's origin.
		own := pollFeed(t, w)
		if len(own) == 0 {
			t.Fatal("the decider's annotation writes produced no feed items: the scenario does not exercise the loop")
		}
		ids := map[string]bool{}
		for _, it := range own {
			if it.Metadata.Origin != apply.Origin("pr") {
				t.Fatalf("a feed item of the decider's write has origin %q, want %q: %+v", it.Metadata.Origin, apply.Origin("pr"), it)
			}
			if ids[it.ID] {
				t.Fatalf("two records share the item id %q", it.ID)
			}
			ids[it.ID] = true
		}

		// Delivering each of them is a skip: no lock, no tracker write, no decider
		// run, one skipped run record, the skip counter up by one each.
		settled := w.tr.snapshot()
		start := w.callCount()
		deciderRuns := len(w.r.deciderRuns())
		before = len(w.r.runRecords())
		for _, it := range own {
			res := w.r.change("--origin", it.Metadata.Origin, "apply", it.ID)
			if res.code != 0 {
				t.Fatalf("a skipped delivery exited %d:\n%s", res.code, res.stderr)
			}
		}
		recs = w.r.runRecords()
		if len(recs) != before+len(own) {
			t.Fatalf("%d own-origin deliveries wrote %d run records", len(own), len(recs)-before)
		}
		for _, r := range recs[before:] {
			if !r.Skipped || r.SkipReason != "own-origin" || r.SkipCounter != "recorded" || r.Origin != apply.Origin("pr") || r.Lane.Result != "not_run" || r.Decide != nil {
				t.Errorf("a skip's run record = %+v", r)
			}
		}
		if got := sameSnapshot(settled, w.tr.snapshot()); got != "" {
			t.Errorf("a skipped delivery wrote to the tracker: %s", got)
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("a skipped delivery made tracker writes: %v", wr)
		}
		if n := len(w.r.deciderRuns()); n != deciderRuns {
			t.Errorf("a skipped delivery ran the decider (%d runs, was %d)", n, deciderRuns)
		}
		if got, want := w.r.lane()["pr_lane.skips.own-origin"], len(own); got != itoa(want) {
			t.Errorf("the skip counter is %q, want %d", got, want)
		}

		// The loop has ended: the skips wrote no annotation, so the feed is quiet.
		if again := pollFeed(t, w); len(again) != 0 {
			t.Errorf("the feed still delivers after the skips (the loop did not end): %+v", again)
		}
		if !strings.Contains(w.r.scrape(), `pg_desk_pr_lane_skips_total{reason="own-origin"} `+itoa(len(own))) {
			t.Error("the scrape does not export the skip counter")
		}
	})
}
