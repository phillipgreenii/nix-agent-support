package focus

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateDraftGolden = flag.Bool("update-draft-golden", false, "rewrite testdata/draft golden files")

func kp(id string) Key { return Key{Type: "pr", ID: id} }
func kb(id string) Key { return Key{Type: "issue", ID: id} }

// rrow is a ranked row for a key, in the given tier.
func rrow(k Key, kind Kind, tier string, finished bool) RankedRow {
	return RankedRow{Candidate: Candidate{Key: k, Kind: kind, Seed: true}, Tier: tier, Finished: finished}
}

func rankingOf(rows ...RankedRow) Ranking {
	for i := range rows {
		rows[i].Position = i + 1
	}
	return Ranking{Rows: rows, Cap: DefaultCap}
}

func plusKeys(d DraftDoc) []Key {
	var out []Key
	for _, r := range d.Rows {
		if r.Plus {
			out = append(out, r.Key)
		}
	}
	return out
}

func draftRowKeys(d DraftDoc) []Key {
	var out []Key
	for _, r := range d.Rows {
		out = append(out, r.Key)
	}
	return out
}

func tierPtr(s string) *string { return &s }

func TestInitialDraftProposesTopCapUnfinishedRows(t *testing.T) {
	rk := rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, true), // finished: never +
		rrow(kp("2"), KindPR, TierOverdue, false),
		rrow(kp("3"), KindPR, TierStarted, false),
		rrow(kb("bd-4"), KindBead, TierNotStarted, false),
	)
	rk.Rows[0].DecidingKey = "due"
	rk.Inputs = RankInputs{AgeFallback: 3}
	rk.EpicsInPlay = []EpicEntry{{Key: kb("bd-9"), InPlay: 1, Total: 4}}
	d := NewInitialDraft(rk, 2, CoverageState{})

	if d.Contract != DraftContract || d.Mode != DraftMode {
		t.Fatalf("contract/mode = %q/%q", d.Contract, d.Mode)
	}
	if got, want := draftRowKeys(d), []Key{kp("1"), kp("2"), kp("3"), kb("bd-4")}; !reflect.DeepEqual(got, want) {
		t.Errorf("row order = %v, want %v (rank order, finished row kept)", got, want)
	}
	if got, want := plusKeys(d), []Key{kp("2"), kp("3")}; !reflect.DeepEqual(got, want) {
		t.Errorf("+ rows = %v, want %v (top cap unfinished; the finished row is skipped and not counted)", got, want)
	}
	if !d.Rows[0].Finished || d.Rows[0].Plus {
		t.Errorf("finished row = %+v, want finished and not +", d.Rows[0])
	}
	if d.Rows[0].Tier != TierOverdue || d.Rows[0].DecidingKey != "due" || d.Rows[0].Kind != KindPR {
		t.Errorf("row carries tier/deciding key/kind: %+v", d.Rows[0])
	}
	if d.Inputs.AgeFallback != 3 || len(d.Epics) != 1 || d.Epics[0].Total != 4 {
		t.Errorf("inputs/epics not carried: %+v %+v", d.Inputs, d.Epics)
	}
}

func TestInitialDraftCapLineWithFewerCandidatesThanCap(t *testing.T) {
	rk := rankingOf(rrow(kp("1"), KindPR, TierStarted, false), rrow(kp("2"), KindPR, TierStarted, false))
	d := NewInitialDraft(rk, 6, CoverageState{})
	if got := plusKeys(d); len(got) != 2 {
		t.Fatalf("+ rows = %v, want both candidates", got)
	}
	if got, want := d.ProposedCapLine(6).String(), "cap 6: 2 in plan, 4 open slots"; got != want {
		t.Errorf("cap line = %q, want %q", got, want)
	}
	// An empty ranking still yields a well-formed draft.
	e := NewInitialDraft(Ranking{}, 0, CoverageState{})
	if len(e.Rows) != 0 || e.ProposedCapLine(0).String() != "cap 6: 0 in plan, 6 open slots" {
		t.Errorf("empty draft = %+v, cap line %q", e, e.ProposedCapLine(0))
	}
}

func TestFinishedRowsDoNotCountTowardCap(t *testing.T) {
	rk := rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, true),
		rrow(kp("2"), KindPR, TierOverdue, true),
		rrow(kp("3"), KindPR, TierStarted, false),
		rrow(kp("4"), KindPR, TierStarted, false),
		rrow(kp("5"), KindPR, TierStarted, false),
	)
	d := NewInitialDraft(rk, 2, CoverageState{})
	if got, want := plusKeys(d), []Key{kp("3"), kp("4")}; !reflect.DeepEqual(got, want) {
		t.Errorf("+ rows = %v, want %v", got, want)
	}
}

func replanFixture() (plan []PlanRow, rk Ranking, firstSeen func(Key) string) {
	plan = []PlanRow{
		{Key: kp("1"), RankPosition: 1, Tier: tierPtr(TierOverdue), SelectedAt: "2026-10-10T08:00:00Z"},
		{Key: kp("2"), RankPosition: 2, Tier: tierPtr(TierStarted), SelectedAt: "2026-10-10T08:00:00Z"},
	}
	rk = rankingOf(
		rrow(kb("bd-9"), KindBead, TierOverdue, false), // new since the plan
		rrow(kp("1"), KindPR, TierOverdue, false),
		rrow(kp("3"), KindPR, TierStarted, false),
		rrow(kp("2"), KindPR, TierStarted, false),
	)
	seen := map[Key]string{
		kb("bd-9"): "2026-10-10T09:30:00Z",
		kp("1"):    "2026-10-01T00:00:00Z",
		kp("2"):    "2026-10-01T00:00:00Z",
		kp("3"):    "2026-10-09T00:00:00Z",
	}
	return plan, rk, func(k Key) string { return seen[k] }
}

func TestReplanKeepsPlanFixedAndProposesNothing(t *testing.T) {
	plan, rk, firstSeen := replanFixture()
	d := NewReplanDraft(plan, rk, CoverageState{}, 6, firstSeen)

	if got := plusKeys(d); len(got) != 0 {
		t.Errorf("a replan proposes nothing, got + on %v", got)
	}
	for _, r := range d.Rows {
		if r.Key == kp("1") || r.Key == kp("2") {
			t.Errorf("plan row %v stored in the draft document", r.Key)
		}
	}
	// New and non-selected candidates are ordered afresh (rank order).
	if got, want := draftRowKeys(d), []Key{kb("bd-9"), kp("3")}; !reflect.DeepEqual(got, want) {
		t.Errorf("row order = %v, want %v", got, want)
	}
	if !d.Rows[0].New || d.Rows[1].New {
		t.Errorf("New marks = %v, %v; want true (arrived after the plan), false (seen before it)", d.Rows[0].New, d.Rows[1].New)
	}
	if d.Mode != DraftMode || d.Contract != DraftContract {
		t.Errorf("mode/contract = %q/%q", d.Mode, d.Contract)
	}
	// The plan is untouched by building a draft.
	if *plan[0].Tier != TierOverdue || plan[1].RankPosition != 2 {
		t.Errorf("plan mutated: %+v", plan)
	}
	// A nil firstSeen marks nothing new.
	if r := NewReplanDraft(plan, rk, CoverageState{}, 6, nil); r.Rows[0].New {
		t.Errorf("New set without a first-seen source")
	}
}

func TestReplanOnEmptyPeriodEqualsShow(t *testing.T) {
	_, rk, firstSeen := replanFixture()
	cov := CoverageState{Incomplete: true, Reasons: []string{"listing failed"}}
	got := NewReplanDraft(nil, rk, cov, 3, firstSeen)
	want := NewInitialDraft(rk, 3, cov)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replan over an empty plan = %+v, want the initial draft %+v", got, want)
	}
	if len(plusKeys(got)) != 3 {
		t.Errorf("first plan is the degenerate replan and proposes the top cap, got %v", plusKeys(got))
	}
}

func TestDraftStoresNoPlanRowsAndNoFacts(t *testing.T) {
	plan, rk, firstSeen := replanFixture()
	body, err := EncodeDraft(NewReplanDraft(plan, rk, CoverageState{}, 6, firstSeen))
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"plan", "plan_rows", "facts", "title", "due", "state"} {
		if _, ok := generic[banned]; ok {
			t.Errorf("stored draft carries top-level %q", banned)
		}
	}
	for _, row := range generic["rows"].([]any) {
		for _, banned := range []string{"title", "due", "priority", "state", "facts"} {
			if _, ok := row.(map[string]any)[banned]; ok {
				t.Errorf("stored draft row carries %q", banned)
			}
		}
	}
}

func richDraft() DraftDoc {
	rk := rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, true),
		rrow(kp("2"), KindPR, TierOverdue, false),
		rrow(kb("bd-3"), KindBead, TierNotStarted, false),
	)
	rk.Rows[0].DecidingKey = "started"
	rk.Rows[2].Via = &Via{SeedKey: kp("2"), Relation: "work", More: 1}
	rk.Rows[2].Seed = false
	rk.Inputs = RankInputs{UnparseableDue: 1, UnmappedPriority: 2, AgeFallback: 3, UnblocksUnavailable: 4, StatusCategoryAbsent: 5}
	rk.EpicsInPlay = []EpicEntry{{Key: kb("bd-epic"), InPlay: 2, Total: 5}}
	cov := CoverageState{
		PerType: map[string]TypeCoverage{
			"pr":    {Active: 7, DueBacklog: 1, OldestHydratedAt: "2026-10-09T06:00:00Z", Listings: []Listing{{Query: "mine", Status: "ok"}}},
			"issue": {Active: 12, Listings: []Listing{{Query: "assigned", Status: "failed", Reason: "timeout"}}},
		},
		Incomplete: true,
		Reasons:    []string{"issue listing assigned failed: timeout"},
	}
	return NewInitialDraft(rk, 2, cov)
}

func TestDraftRoundTrip(t *testing.T) {
	d := richDraft()
	body, err := EncodeDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeDraft(body)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, d) {
		t.Errorf("round trip changed the document:\n got %+v\nwant %+v", got, d)
	}
	again, err := EncodeDraft(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(body) {
		t.Errorf("re-encoding is not byte-stable")
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil || generic["contract"] != DraftContract {
		t.Errorf("contract member = %v (%v)", generic["contract"], err)
	}
}

func TestEncodeDraftEmptyListsAreArraysAndContractEnforced(t *testing.T) {
	body, err := EncodeDraft(DraftDoc{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rows": []`, `"epics": []`, `"contract": "pg-desk.focus.draft/v1"`, `"mode": "draft"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("encoded empty draft lacks %s:\n%s", want, body)
		}
	}
	if _, err := EncodeDraft(DraftDoc{Contract: "other/v2"}); err == nil {
		t.Error("EncodeDraft accepted a foreign contract")
	}
	if _, err := EncodeDraft(DraftDoc{Mode: "plan"}); err == nil {
		t.Error("EncodeDraft accepted mode plan")
	}
}

func TestUnreadableDraftIsTreatedAsAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"empty":             ``,
		"undecodable":       `{"contract": "pg-desk.focus.draft/v1", "rows": [`,
		"not an object":     `[1,2,3]`,
		"missing contract":  `{"mode":"draft","rows":[]}`,
		"unknown contract":  `{"contract":"pg-desk.focus.draft/v2","mode":"draft","rows":[]}`,
		"prefix contract":   `{"contract":"pg-desk.focus.draft/v1 ","rows":[]}`,
		"wrong type member": `{"contract":"pg-desk.focus.draft/v1","rows":"x"}`,
		"trailing garbage":  `{"contract":"pg-desk.focus.draft/v1"} {`,
	} {
		_, err := DecodeDraft([]byte(body))
		if !errors.Is(err, ErrDraftUnreadable) {
			t.Errorf("%s: err = %v, want ErrDraftUnreadable", name, err)
		}
	}
	if _, err := DecodeDraft([]byte(`{"contract":"pg-desk.focus.draft/v1","mode":"draft"}`)); err != nil {
		t.Errorf("a minimal valid draft is unreadable: %v", err)
	}
}

func TestDraftGolden(t *testing.T) {
	body, err := EncodeDraft(richDraft())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "draft", "initial_draft.json")
	if *updateDraftGolden {
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Compared as JSON values: the formatter reflows the checked-in file.
	var wantV, gotV any
	if err := json.Unmarshal(want, &wantV); err != nil {
		t.Fatalf("golden %s: %v", path, err)
	}
	if err := json.Unmarshal(body, &gotV); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantV, gotV) {
		t.Errorf("encoded draft differs from %s (re-run with -update-draft-golden to accept):\n%s", path, body)
	}
}
