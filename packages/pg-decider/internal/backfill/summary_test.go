package backfill

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func actions(kind string, ops ...string) []Planned {
	var out []Planned
	for _, op := range ops {
		rule := "r." + kind
		if op == "adopt" {
			rule, op = "adoption", "update"
		}
		out = append(out, Planned{Rule: rule, Kind: kind, Op: op})
	}
	return out
}

func readGolden(t *testing.T, lines ...recLine) *Log {
	t.Helper()
	p := tmpRecord(t)
	var raw [][]byte
	for _, l := range lines {
		raw = append(raw, l.json(t))
	}
	writeLog(t, p, raw...)
	lg, err := ReadLog(p)
	if err != nil {
		t.Fatal(err)
	}
	return lg
}

func TestThePlanSummaryPrintsTheFixedFieldsFromTheLatestRecordPerPRAndMode(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		// #1: an older plan that wanted a create, replaced by a later one that wants an adoption.
		recLine{at: t0, pr: "o/r#1", mode: ModePlan, actions: actions("anchor", "create")},
		recLine{at: t0.Add(time.Hour), pr: "o/r#1", mode: ModePlan, actions: append(actions("anchor", "adopt"), actions("review-pr", "create")...)},
		// #2: a plan that closes and updates.
		recLine{at: t0.Add(2 * time.Hour), pr: "o/r#2", mode: ModePlan, actions: append(actions("anchor", "close"), actions("fix-ci", "update", "reopen")...)},
		// #3: unadoptable.
		recLine{at: t0.Add(3 * time.Hour), pr: "o/r#3", mode: ModePlan, adoption: &AdoptionRecord{Outcome: "unadoptable", Candidates: []AnchorCandidate{{ID: "bd-9", State: "open", Keyed: false, MatchedBy: "title"}, {ID: "bd-8", Keyed: true}}}},
		// An apply-mode record of #1 must not count as a plan.
		recLine{at: t0.Add(4 * time.Hour), pr: "o/r#1", mode: ModeApply, actions: actions("anchor", "create"), residual: intp(0)},
	)
	set := testSet("o/r#1", "o/r#2", "o/r#3")
	s := Summarize(lg, set, ModePlan, 48*time.Hour, t0.Add(5*time.Hour))
	if !s.OK() {
		t.Fatalf("problems %v", s.Problems)
	}
	if s.Enumerated != 3 || s.WithRecord != 3 {
		t.Fatalf("enumerated %d with record %d", s.Enumerated, s.WithRecord)
	}
	want := map[string]map[string]int{
		"anchor":    {"adopt": 1, "close": 1},
		"review-pr": {"create": 1},
		"fix-ci":    {"update": 1, "reopen": 1},
	}
	for k, ops := range want {
		for op, n := range ops {
			if s.Counts[k][op] != n {
				t.Errorf("%s %s = %d, want %d", k, op, s.Counts[k][op], n)
			}
		}
	}
	if s.Counts["anchor"]["create"] != 0 {
		t.Error("the older plan record was counted")
	}
	if len(s.Unadoptable) != 1 || s.Unadoptable[0].PR != "o/r#3" || s.Unadoptable[0].Why != "bd-9" {
		t.Errorf("unadoptable %+v", s.Unadoptable)
	}
	if !s.Oldest.Equal(t0.Add(time.Hour)) || s.OldestPR != "o/r#1" || s.Age != 4*time.Hour {
		t.Errorf("oldest %v %s age %s", s.Oldest, s.OldestPR, s.Age)
	}
	var out bytes.Buffer
	s.Print(&out)
	for _, w := range []string{"enumerated: 3", "with plan-mode record: 3", "without a usable plan-mode record: 0", "anchor: adopt=1 create=0 reopen=0 close=1 update=0 annotate=0", "unadoptable items: 1", "PRs that would get a second anchor: 0", "oldest plan record: ", "checks: PASS"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}

func TestThePlanSummaryNamesEveryPRWithoutAUsablePlanRecord(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		recLine{at: t0, pr: "o/r#1", mode: ModePlan},
		recLine{at: t0, pr: "o/r#2", mode: ModePlan, exit: 21, class: "degraded"},
		recLine{at: t0, pr: "o/r#3", mode: ModePlan, exit: 25, class: "unhydrated"},
		recLine{at: t0, pr: "o/r#4", mode: ModePlan, planErr: "plan could not be read"},
		// #5 has only an apply-mode record.
		recLine{at: t0, pr: "o/r#5", mode: ModeApply},
	)
	set := testSet("o/r#1", "o/r#2", "o/r#3", "o/r#4", "o/r#5", "o/r#6")
	s := Summarize(lg, set, ModePlan, 0, t0)
	if s.OK() || s.WithRecord != 1 || len(s.Missing) != 5 {
		t.Fatalf("with record %d missing %+v problems %v", s.WithRecord, s.Missing, s.Problems)
	}
	var out bytes.Buffer
	s.Print(&out)
	for _, w := range []string{"o/r#2: the latest plan-mode run failed (exit 21, class degraded)", "o/r#3: the latest plan-mode run failed (exit 25, class unhydrated)", "o/r#4: ", "o/r#5: no plan-mode record", "o/r#6: no plan-mode record", "checks: FAIL", "coverage: 5 of 6"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
	if strings.Contains(out.String(), "o/r#1:") {
		t.Errorf("a covered PR is named:\n%s", out.String())
	}
}

func TestThePlanSummaryFlagsAPlanOlderThanTheBoundAndASecondAnchor(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		recLine{at: t0, pr: "o/r#1", mode: ModePlan},
		recLine{
			at: t0.Add(47 * time.Hour), pr: "o/r#2", mode: ModePlan,
			actions:  actions("anchor", "create"),
			adoption: &AdoptionRecord{Outcome: "none", Candidates: []AnchorCandidate{{ID: "bd-1", State: "closed", Keyed: true}}},
		},
	)
	set := testSet("o/r#1", "o/r#2")
	now := t0.Add(49 * time.Hour)
	s := Summarize(lg, set, ModePlan, 48*time.Hour, now)
	if !s.Stale || s.Age != 49*time.Hour || len(s.SecondAnch) != 1 || s.SecondAnch[0] != "o/r#2" || s.OK() {
		t.Fatalf("stale %v age %s second %v problems %v", s.Stale, s.Age, s.SecondAnch, s.Problems)
	}
	var out bytes.Buffer
	s.Print(&out)
	if !strings.Contains(out.String(), "STALE: re-plan before the flip") || !strings.Contains(out.String(), "PRs that would get a second anchor: 1") {
		t.Errorf("output:\n%s", out.String())
	}
	// Inside the bound, and with the bound switched off, nothing is stale.
	if s := Summarize(lg, set, ModePlan, 72*time.Hour, now); s.Stale {
		t.Error("stale inside the bound")
	}
	if s := Summarize(lg, set, ModePlan, 0, now.Add(1000*time.Hour)); s.Stale {
		t.Error("stale with no bound")
	}
}

func TestTheApplySummaryPrintsEachPRsResidualAndFailsOnANonZeroOrMissingOne(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		recLine{at: t0, pr: "o/r#1", mode: ModeApply, residual: intp(0)},
		recLine{at: t0, pr: "o/r#2", mode: ModeApply, residual: intp(2)},
		recLine{at: t0, pr: "o/r#3", mode: ModeApply}, // no residual recorded
		recLine{at: t0, pr: "o/r#5", mode: ModePlan},  // only a plan
	)
	set := testSet("o/r#1", "o/r#2", "o/r#3", "o/r#4", "o/r#5")
	s := Summarize(lg, set, ModeApply, 0, t0)
	if s.OK() {
		t.Fatal("the apply summary passed")
	}
	var out bytes.Buffer
	s.Print(&out)
	for _, w := range []string{"o/r#1: 0", "o/r#2: 2", "o/r#3: unknown", "o/r#4: no apply-mode record", "o/r#5: no apply-mode record", "residual 2 after the apply", "checks: FAIL"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
	good := readGolden(t, recLine{at: t0, pr: "o/r#1", mode: ModeApply, residual: intp(0)}, recLine{at: t0, pr: "o/r#2", mode: ModeApply, residual: intp(0)})
	if s := Summarize(good, testSet("o/r#1", "o/r#2"), ModeApply, 0, t0); !s.OK() {
		t.Fatalf("problems %v", s.Problems)
	}
}

func TestMarkersAreIgnoredWhenTakingTheLatestRecordPerPRAndMode(t *testing.T) {
	p := tmpRecord(t)
	t0 := baseTime
	writeLog(t, p, recLine{at: t0, pr: "o/r#1", mode: ModePlan}.json(t))
	if _, err := WriteFlipMarker(t.Context(), FlipMarkerOptions{RunRecord: p, Now: func() time.Time { return t0.Add(time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if err := AppendLine(p, Marker{Contract: PassStartContract, Kind: "pass-start", At: t0.Add(2 * time.Hour).Format(time.RFC3339), Mode: ModePlan}); err != nil {
		t.Fatal(err)
	}
	lg, err := ReadLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(lg.Records) != 1 || len(lg.FlipMarkers) != 1 || len(lg.PassStarts) != 1 || lg.Ignored != 0 {
		t.Fatalf("%+v", lg)
	}
	s := Summarize(lg, testSet("o/r#1"), ModePlan, 0, t0)
	if !s.OK() || s.WithRecord != 1 {
		t.Fatalf("the markers disturbed the summary: %+v", s)
	}
	if got := lg.Latest(ModePlan, nil); len(got) != 1 {
		t.Fatalf("latest %v", got)
	}
}

func TestTheApplySummaryFailsOnATrackedPRAbsentFromTheSetWithAResidual(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		recLine{at: t0, pr: "o/r#1", mode: ModeApply, residual: intp(0)},
		// Followed by the router after enumeration: not in the set.
		recLine{at: t0, pr: "o/r#7", mode: ModeApply, residual: intp(1), eventID: "o/r#7@0123456789ab"},
		// An older residual of #8 was cleared by a later apply.
		recLine{at: t0, pr: "o/r#8", mode: ModeApply, residual: intp(3)},
		recLine{at: t0.Add(time.Hour), pr: "o/r#8", mode: ModeApply, residual: intp(0)},
		// A plan-mode record is not an apply residual.
		recLine{at: t0, pr: "o/r#9", mode: ModePlan},
	)
	s := Summarize(lg, testSet("o/r#1"), ModeApply, 0, t0)
	if s.OK() {
		t.Fatal("the apply summary passed with a residual outside the set")
	}
	if len(s.OutsideSet) != 1 || s.OutsideSet[0] != "o/r#7" {
		t.Fatalf("outside the set: %v", s.OutsideSet)
	}
	var out bytes.Buffer
	s.Print(&out)
	for _, w := range []string{"o/r#7: residual 1", "outside the persisted set", "checks: FAIL"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
	if strings.Contains(out.String(), "o/r#8") || strings.Contains(out.String(), "o/r#9") {
		t.Errorf("output names a PR with no live residual:\n%s", out.String())
	}
}

func TestTheApplySummaryPassesWhenATrackedPRAbsentFromTheSetHasNoResidual(t *testing.T) {
	t0 := baseTime
	lg := readGolden(
		t,
		recLine{at: t0, pr: "o/r#1", mode: ModeApply, residual: intp(0)},
		recLine{at: t0, pr: "o/r#7", mode: ModeApply, residual: intp(0)},
	)
	s := Summarize(lg, testSet("o/r#1"), ModeApply, 0, t0)
	if !s.OK() || len(s.OutsideSet) != 0 {
		t.Fatalf("problems %v outside %v", s.Problems, s.OutsideSet)
	}
	var out bytes.Buffer
	s.Print(&out)
	if !strings.Contains(out.String(), "checks: PASS") {
		t.Errorf("output:\n%s", out.String())
	}
}
