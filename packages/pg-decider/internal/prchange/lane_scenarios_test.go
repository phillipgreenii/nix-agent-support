package prchange

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The adoption, fail-closed and drift scenarios of the lane suite (docket
// pg2-6mo45.3, packet P4c-1), run over the fake bd by TestLaneFake and over a
// real bd by TestLaneE2E (lane_world_test.go).

// adoptionScenarios are three views of one history (plan, apply, apply again) of
// N PRs with keyless anchors and review items, so they are steps of one world.
func adoptionScenarios(l *laneRun) {
	world := newE2EWorld(l.t, l.tools, e2eSeeded, false)
	var planned map[string]int // the plans' (kind, op) counts over the N PRs, read before the first apply
	var beforeFirst, afterFirst map[string]string

	l.step("n-keyless-anchors-are-adopted-from-the-trackers-own-listing-with-no-creates", func(t *testing.T) {
		w := world.use(t)
		before := w.tr.snapshot()
		beforeFirst = before
		if len(before) != 2*e2eSeeded {
			t.Fatalf("setup: the tracker holds %d beads, want %d", len(before), 2*e2eSeeded)
		}
		for _, it := range w.tr.items() {
			if _, ok := metaOf(it)["dedup_key"]; ok {
				t.Fatalf("setup: %v is already keyed", it["id"])
			}
		}
		planned = map[string]int{}
		start := w.callCount()
		for _, id := range w.ids {
			p := pairs(w.planJSON(id))
			for k, n := range p {
				planned[k] += n
			}
			if p["anchor create"] != 0 || p["review-pr create"] != 0 {
				t.Fatalf("%s: the plan creates what the tracker already lists: %v", id, p)
			}
			if p["anchor update"] == 0 {
				t.Fatalf("%s: the plan does not adopt the keyless anchor (no anchor update): %v", id, p)
			}
			res := w.r.change("plan", id)
			if res.code != 0 {
				t.Fatalf("plan %s exit %d:\n%s", id, res.code, res.stderr)
			}
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("plan wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("plan made tracker writes: %v", wr)
		}

		start = w.callCount()
		for _, id := range w.ids {
			res := w.applyRetrying("apply", id)
			if res.code != 0 {
				t.Fatalf("apply %s exit %d:\n%s", id, res.code, res.stderr)
			}
			rec := w.lastRecord()
			if rec.Adoption == nil || rec.Adoption.Outcome != "adopted" {
				t.Errorf("%s: adoption outcome %+v, want adopted", id, rec.Adoption)
			}
		}
		for _, wr := range w.writes(start) {
			if strings.HasPrefix(wr, "issue create") {
				t.Errorf("apply created a bead: %s", wr)
			}
		}
		afterFirst = w.tr.snapshot()
		if len(afterFirst) != len(before) {
			t.Fatalf("the tracker holds %d beads after adoption, want the same %d (no creates): %s", len(afterFirst), len(before), sameSnapshot(before, afterFirst))
		}
		keyed := 0
		for _, it := range w.tr.items() {
			k, _ := metaOf(it)["dedup_key"].(string)
			if k == "" {
				t.Errorf("%v (%s) was not keyed by the adoption", it["id"], itemKind(it))
				continue
			}
			keyed++
		}
		if keyed != 2*e2eSeeded {
			t.Errorf("%d items keyed, want %d (N anchors and N review items)", keyed, 2*e2eSeeded)
		}
	})

	l.step("the-second-apply-writes-nothing", func(t *testing.T) {
		w := world.use(t)
		if afterFirst == nil {
			t.Fatal("the adoption scenario recorded no snapshot: the comparison would be vacuous")
		}
		start := w.callCount()
		for _, id := range w.ids {
			res := w.applyRetrying("apply", id)
			if res.code != 0 {
				t.Fatalf("second apply %s exit %d:\n%s", id, res.code, res.stderr)
			}
			rec := w.lastRecord()
			if rec.Residual == nil || *rec.Residual != 0 || (rec.Decide != nil && len(rec.Decide.Actions) != 0) {
				t.Errorf("%s: the second apply still plans actions (residual %v, decide %+v)", id, rec.Residual, rec.Decide)
			}
		}
		if got := w.tr.snapshot(); sameSnapshot(afterFirst, got) != "" {
			t.Fatalf("the second apply wrote to the tracker: %s", sameSnapshot(afterFirst, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("the second apply made tracker writes: %v", wr)
		}
	})

	l.step("plan-and-apply-agree-per-kind-and-operation", func(t *testing.T) {
		w := world.use(t)
		// The plans read before the first apply against what the apply did to
		// the tracker, per kind: the (kind, operation) pairs the plans list are
		// exactly the ones the tracker shows after it, and nothing else changed.
		if len(planned) == 0 || beforeFirst == nil || afterFirst == nil {
			t.Fatal("the adoption scenario recorded no plan or snapshot: the comparison would be vacuous")
		}
		byID := map[string]map[string]any{}
		for _, it := range w.tr.items() {
			byID[it["id"].(string)] = it
		}
		touched := map[string]int{}
		for id, v := range afterFirst {
			old, existed := beforeFirst[id]
			op := ""
			switch {
			case !existed:
				op = "create"
			case old != v:
				op = "update"
			default:
				continue
			}
			touched[itemKind(byID[id])+" "+op]++
		}
		for k, n := range planned {
			if touched[k] == 0 {
				t.Errorf("the plans list %q (%d actions) and the tracker shows no such change after the apply: tracker %v", k, n, touched)
			}
		}
		for k, n := range touched {
			if planned[k] == 0 {
				t.Errorf("the tracker shows %q on %d items and the plans did not list it: plans %v", k, n, planned)
			}
		}
		// Every item an apply updated is one a plan listed an update for, by kind:
		// the plans' counts per kind are at least the items changed.
		for k, n := range touched {
			if planned[k] < n {
				t.Errorf("%q: %d items changed but the plans list only %d", k, n, planned[k])
			}
		}
	})
}

// unhydratedScenarios: a view that was never hydrated is refused.
func unhydratedScenarios(l *laneRun) {
	l.run("an-unhydrated-view-is-refused-and-nothing-is-written", func(t *testing.T) {
		w := newE2EWorld(t, l.tools, 1, true)
		id := w.ids[0]
		if got := w.r.neverHydrated(); got != "1" {
			t.Fatalf("setup: the never-hydrated count is %s, want 1", got)
		}
		// A refresh that reports success and stores nothing (the loss the
		// refusal exists for): pg-desk's other verbs, `pr show` included, are the
		// real ones, so the command reads a real version-0 view.
		real := filepath.Join(w.r.binDir, "pg-desk")
		kept := real + ".real"
		must(t, os.Rename(real, kept))
		must(t, os.WriteFile(real, []byte("#!/bin/sh\ncase \"$*\" in\n'pr refresh '*) echo 'no change'; exit 0;;\nesac\nexec "+shq(kept)+" \"$@\"\n"), 0o755))
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, id)
			if res.code != ExitUnhydrated {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitUnhydrated, res.stderr)
			}
			contains(t, mode+" refusal", res.stderr, "class=unhydrated", "view refused", "the decider was not run")
		}
		if n := len(w.r.deciderRuns()) + len(w.r.deciderPlanReads()); n != 0 {
			t.Errorf("the decider ran %d times for a refused view", n)
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a refused run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a refused run made tracker writes: %v", wr)
		}
		// The same PR, hydrated for real, is accepted.
		must(t, os.Remove(real))
		must(t, os.Rename(kept, real))
		if res := w.r.change("plan", id); res.code != 0 {
			t.Fatalf("after a real refresh the plan exited %d:\n%s", res.code, res.stderr)
		}
	})
}

// failClosedScenarios share one world that no scenario of it writes to: each
// run it makes is refused (or is a plan), so the tracker's own listing before
// and after is the proof that nothing was written. They change the world's
// ceiling in turn, so they are steps.
func failClosedScenarios(l *laneRun) {
	var closed *e2eWorld
	failClosedWorld := func(t *testing.T) *e2eWorld {
		t.Helper()
		if closed == nil {
			closed = newE2EWorld(l.t, l.tools, 1, false)
		}
		return closed.use(t)
	}

	l.step("a-listing-above-the-ceiling-fails-the-run-and-writes-nothing", func(t *testing.T) {
		w := failClosedWorld(t)
		_, items := w.listing()
		w.setCeiling(items - 1)
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, w.ids[0])
			if res.code != ExitDecideFailed {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitDecideFailed, res.stderr)
			}
			contains(t, mode+" failure line", res.stderr, "decide_failed", "above the ceiling", fmt.Sprintf("work_items_ceiling=%d", items-1))
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a failed-closed run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a failed-closed run made tracker writes: %v", wr)
		}
		lane := w.r.lane()
		if lane["pr_lane.state.lookup_ceiling"] != strconv.Itoa(items-1) || lane["pr_lane.state.lookup_items"] != strconv.Itoa(items) {
			t.Errorf("the lane gauges do not carry the listing (%d items, ceiling %d): %v", items, items-1, lane)
		}
	})

	l.step("a-listing-of-exactly-the-ceiling-is-accepted", func(t *testing.T) {
		w := failClosedWorld(t)
		_, items := w.listing()
		w.setCeiling(items)
		res := w.r.change("plan", w.ids[0])
		if res.code != 0 {
			t.Fatalf("a listing of %d items at ceiling %d was refused: exit %d:\n%s", items, items, res.code, res.stderr)
		}
	})

	l.step("a-listing-that-reports-itself-truncated-fails-the-run-and-writes-nothing", func(t *testing.T) {
		w := failClosedWorld(t)
		w.setCeiling(1000)
		raw, _ := w.listing()
		// The beads backend never truncates the unranged listing, so a tracker
		// that does is shown by the real answer with the flag set.
		var ans map[string]json.RawMessage
		must(t, json.Unmarshal(raw, &ans))
		ans["truncated"] = json.RawMessage("true")
		out, err := json.Marshal(ans)
		must(t, err)
		w.r.ctlWrite("work-items.out", string(out)+"\n")
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, w.ids[0])
			if res.code != ExitDecideFailed {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitDecideFailed, res.stderr)
			}
			contains(t, mode+" failure line", res.stderr, "decide_failed", "truncated")
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a failed-closed run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a failed-closed run made tracker writes: %v", wr)
		}
	})
}

// driftScenarios check pg-desk's copy of the decider's rule and reason lists.
func driftScenarios(l *laneRun) {
	l.run("pg-desks-zero-filled-decision-labels-equal-the-deciders-rules-and-reasons", func(t *testing.T) {
		// serve answers /metrics once the store has been written to: hydrate the
		// fixture PR. No lane run is recorded, so every series is the zero fill.
		r := newRigWith(t, fxMerged, rigOpts{cutover: true, bins: &l.tools.bins})
		r.hydrate()
		rules, reasons, series := decisionLabels(r.scrape())
		if series == 0 {
			t.Fatal("a fresh schema-2 store's scrape has no pg_decider_decisions_total series: the comparison would be vacuous")
		}
		dRules, dReasons := deciderRuleIDs(), deciderSkipReasons(t)
		if len(dRules) < 13 || len(dReasons) < 5 {
			t.Fatalf("the decider's lists are suspiciously short: %d rules %v, %d reasons %v", len(dRules), dRules, len(dReasons), dReasons)
		}
		drift := append(labelDrift("rule", dRules, rules), labelDrift("reason", dReasons, reasons)...)
		if len(drift) != 0 {
			t.Fatalf("pg-desk's copy of the decider's lists has drifted (update packages/pg-desk/internal/lanestate/vocab.go):\n%s", strings.Join(drift, "\n"))
		}
	})

	l.run("the-drift-check-fails-on-a-decider-value-pg-desk-lacks-and-on-the-reverse", func(t *testing.T) {
		rules := []string{"adoption", "review.head-advanced"}
		reasons := []string{"hidden", "not matched"}
		if d := labelDrift("rule", rules, rules); len(d) != 0 {
			t.Fatalf("equal lists drift: %v", d)
		}
		cases := []struct {
			name           string
			dRules, dRsns  []string
			kRules, kRsns  []string
			want, wantKind string
		}{
			{"a decider reason absent from pg-desk's copy", rules, append(append([]string{}, reasons...), "newly-added"), rules, reasons, "newly-added", "reason"},
			{"a pg-desk reason the decider lacks", rules, reasons, rules, append(append([]string{}, reasons...), "stale"), "stale", "reason"},
			{"a decider rule absent from pg-desk's copy", append(append([]string{}, rules...), "review.new-rule"), reasons, rules, reasons, "review.new-rule", "rule"},
			{"a pg-desk rule the decider lacks", rules, reasons, append(append([]string{}, rules...), "gone.rule"), reasons, "gone.rule", "rule"},
		}
		for _, c := range cases {
			drift := append(labelDrift("rule", c.dRules, c.kRules), labelDrift("reason", c.dRsns, c.kRsns)...)
			if len(drift) != 1 || !strings.Contains(drift[0], c.wantKind+" \""+c.want+"\"") {
				t.Errorf("%s: drift %v, want exactly one finding naming %q", c.name, drift, c.want)
			}
		}
	})
}
