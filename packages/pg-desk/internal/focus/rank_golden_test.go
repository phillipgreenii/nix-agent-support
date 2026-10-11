package focus

// Rank goldens (bead pg2-2j5ac.44.13, the daily-focus design's "Rank regression" and test plan).
//
// This file holds the fixture format and the harness both the goldens
// (testdata/rank/*.json, TestRankGoldens) and the standing gate over recorded
// snapshots (testdata/snapshots/*/*.json, TestFocusRankGate in
// rank_gate_test.go) run through: a fixture is loaded into a real new-schema
// store, read back by Load, and ranked by Candidates and Rank (which applies
// the slot rule and the correlation groups). The expectations are written by
// hand from the operator rulings and the df-survey suites, never captured
// from the rank's own output.
//
// Telemetry: this test code emits no OpenTelemetry or Prometheus signal and
// logs nothing; the rank it drives emits none either.
//
// Fixture format (one JSON object per file; unknown fields are an error):
//
//	name         the file's base name without ".json"
//	provenance   free text: where the shapes come from (optional)
//	now          RFC3339 reading of the injected clock
//	time_zone    IANA name of focus.time_zone
//	cap          the cap line (optional; 0 or absent is the rank's default)
//	config       {self_login, operator_identities, priority_map, bead_id_pattern}
//	entities     [{type, id, facts, interpretation, annotations, active,
//	               first_seen_at}]; facts is the stored facts JSON verbatim
//	               ({"pr_show": ...} or {"issue_show": ..., "issue_deps": ...})
//	links        [{from, to, relation, external, reason}]; from and to are
//	               canonical keys "<type>:<id>"; a link is DERIVED unless
//	               external is true
//	plan_keys    canonical keys currently in a plan (the in-plan counts of the
//	               epics block)
//	expected     see rankCaseExpected
//
// first_seen_at is the one field beyond the packet's minimal entity list: the
// store stamps it at insert time, and the age key reads it for a PR and for an
// issue with no creation time, so a fixture must be able to pin it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// rankCase is one fixture file.
type rankCase struct {
	Name       string           `json:"name"`
	Provenance string           `json:"provenance,omitempty"`
	Now        string           `json:"now"`
	TimeZone   string           `json:"time_zone"`
	Cap        int              `json:"cap,omitempty"`
	Config     rankCaseConfig   `json:"config"`
	Entities   []rankCaseEntity `json:"entities"`
	Links      []rankCaseLink   `json:"links,omitempty"`
	PlanKeys   []string         `json:"plan_keys,omitempty"`
	Expected   rankCaseExpected `json:"expected"`
}

type rankCaseConfig struct {
	SelfLogin          string            `json:"self_login,omitempty"`
	OperatorIdentities []string          `json:"operator_identities,omitempty"`
	PriorityMap        map[string]string `json:"priority_map,omitempty"`
	BeadIDPattern      string            `json:"bead_id_pattern,omitempty"`
}

type rankCaseEntity struct {
	Type           string                     `json:"type"`
	ID             string                     `json:"id"`
	Facts          json.RawMessage            `json:"facts"`
	Interpretation *rankCaseInterpretation    `json:"interpretation,omitempty"`
	Annotations    map[string]json.RawMessage `json:"annotations,omitempty"`
	Active         *bool                      `json:"active,omitempty"`
	FirstSeenAt    string                     `json:"first_seen_at,omitempty"`
}

type rankCaseInterpretation struct {
	Ownership string `json:"ownership"`
}

type rankCaseLink struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	External bool   `json:"external,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// rankCaseExpected is what a fixture asserts. Order, Tiers, Deciding and
// RankInputs are required: Tiers must name every row and Deciding every row
// but the last, so an expectation cannot silently cover only part of the
// ranking. EpicsInPlay is compared in its stored order (absent means none).
// Priorities, Due and InPlan are optional extra assertions: the effective
// priority and due day a row shows, and the keys the cap line marks.
type rankCaseExpected struct {
	Order       []string          `json:"order"`
	Tiers       map[string]string `json:"tiers"`
	Deciding    map[string]string `json:"deciding"`
	EpicsInPlay []string          `json:"epics_in_play,omitempty"`
	RankInputs  *rankCaseInputs   `json:"rank_inputs"`
	Priorities  map[string]string `json:"priorities,omitempty"`
	Due         map[string]string `json:"due,omitempty"`
	InPlan      *[]string         `json:"in_plan,omitempty"`
}

type rankCaseInputs struct {
	UnparseableDue       int `json:"unparseable_due"`
	UnmappedPriority     int `json:"unmapped_priority"`
	AgeFallback          int `json:"age_fallback"`
	UnblocksUnavailable  int `json:"unblocks_unavailable"`
	StatusCategoryAbsent int `json:"status_category_absent"`
}

func (c *rankCaseInputs) asRankInputs() RankInputs {
	return RankInputs{
		UnparseableDue:       c.UnparseableDue,
		UnmappedPriority:     c.UnmappedPriority,
		AgeFallback:          c.AgeFallback,
		UnblocksUnavailable:  c.UnblocksUnavailable,
		StatusCategoryAbsent: c.StatusCategoryAbsent,
	}
}

// canonKey is the canonical text form of a key: "<type>:<id>".
func canonKey(k Key) string { return k.Type + ":" + k.ID }

func parseCanonKey(s string) (Key, error) {
	typ, id, ok := strings.Cut(s, ":")
	if !ok || typ == "" || id == "" {
		return Key{}, fmt.Errorf("%q is not a canonical key <type>:<id>", s)
	}
	return Key{typ, id}, nil
}

// readRankCase decodes one fixture file strictly and validates the parts of
// the expectation that must be complete.
func readRankCase(t *testing.T, path string) *rankCase {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c rankCase
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	if dec.More() {
		t.Fatalf("fixture %s has trailing content after the object", path)
	}
	if want := strings.TrimSuffix(filepath.Base(path), ".json"); c.Name != want {
		t.Fatalf("fixture %s has name %q, want %q (the file's base name)", path, c.Name, want)
	}
	if problems := c.validateExpectation(); len(problems) > 0 {
		t.Fatalf("fixture %s: incomplete expectation: %s", path, strings.Join(problems, "; "))
	}
	return &c
}

// validateExpectation reports an expectation that does not cover the whole
// ranking, so a fixture cannot pass by asserting less than it appears to.
func (c *rankCase) validateExpectation() []string {
	var out []string
	e := c.Expected
	if len(e.Order) == 0 {
		out = append(out, "expected.order is empty")
	}
	if e.RankInputs == nil {
		out = append(out, "expected.rank_inputs is missing (zero counts are an assertion too)")
	}
	if len(e.Tiers) != len(e.Order) {
		out = append(out, fmt.Sprintf("expected.tiers names %d keys, order has %d", len(e.Tiers), len(e.Order)))
	}
	if want := len(e.Order) - 1; len(e.Order) > 0 && len(e.Deciding) != want {
		out = append(out, fmt.Sprintf("expected.deciding names %d keys, want %d (every row but the last)", len(e.Deciding), want))
	}
	return out
}

// rankResult is the observable outcome of ranking one fixture, in the same
// shape as the expectation.
type rankResult struct {
	Order       []string
	Tiers       map[string]string
	Deciding    map[string]string
	EpicsInPlay []string
	RankInputs  RankInputs
	Priorities  map[string]string
	Due         map[string]string
	InPlan      []string
}

// runRankCase loads the fixture into a store and ranks it.
func runRankCase(t *testing.T, c *rankCase) rankResult {
	t.Helper()
	now, err := time.Parse(time.RFC3339, c.Now)
	if err != nil {
		t.Fatalf("fixture %s: now %q: %v", c.Name, c.Now, err)
	}
	const repo = testRepo
	const at = "2026-10-10T08:00:00Z"

	cfg := &config.Config{
		SelfLogin:     c.Config.SelfLogin,
		BeadIDPattern: c.Config.BeadIDPattern,
		Focus: config.FocusConfig{
			TimeZone:           c.TimeZone,
			OperatorIdentities: c.Config.OperatorIdentities,
			PriorityMap:        c.Config.PriorityMap,
		},
	}
	st := store.OpenNewSchemaForTest(t)

	firstSeen := map[Key]string{}
	for _, e := range c.Entities {
		k := Key{e.Type, e.ID}
		var facts bytes.Buffer
		if err := json.Compact(&facts, e.Facts); err != nil {
			t.Fatalf("fixture %s: facts of %s: %v", c.Name, canonKey(k), err)
		}
		active := e.Active == nil || *e.Active
		ent := store.Entity{Repo: repo, EntityType: e.Type, EntityID: e.ID, Facts: facts.String(), AsOf: at, ContentHash: "h" + e.ID}
		if _, err := st.WriteEntityStateWithLog(ent, 0, at, active, []string{"created"}, "sync", at); err != nil {
			t.Fatalf("fixture %s: write %s: %v", c.Name, canonKey(k), err)
		}
		if e.Interpretation != nil {
			if err := st.UpsertInterpretation(store.Interpretation{
				Repo: repo, EntityType: e.Type, EntityID: e.ID, Ownership: e.Interpretation.Ownership,
				Enrichment: "{}", Urgency: "{}", Dispositions: "{}", Approvals: "{}", MatchReasons: "[]", AsOf: at,
			}); err != nil {
				t.Fatalf("fixture %s: interpretation of %s: %v", c.Name, canonKey(k), err)
			}
		}
		for name, raw := range e.Annotations {
			var v bytes.Buffer
			if err := json.Compact(&v, raw); err != nil {
				t.Fatalf("fixture %s: annotation %s of %s: %v", c.Name, name, canonKey(k), err)
			}
			if err := st.SetAnnotation(store.KVAnnotation{
				Repo: repo, EntityType: e.Type, EntityID: e.ID, Key: name, Value: v.String(),
				Origin: "pg-desk", SetBy: "operator", SetAt: at,
			}); err != nil {
				t.Fatalf("fixture %s: annotation %s of %s: %v", c.Name, name, canonKey(k), err)
			}
		}
		if e.FirstSeenAt != "" {
			firstSeen[k] = e.FirstSeenAt
		}
	}

	derived := map[Key][]store.XrefLink{}
	for _, l := range c.Links {
		from, err := parseCanonKey(l.From)
		if err != nil {
			t.Fatalf("fixture %s: link from: %v", c.Name, err)
		}
		to, err := parseCanonKey(l.To)
		if err != nil {
			t.Fatalf("fixture %s: link to: %v", c.Name, err)
		}
		x := store.XrefLink{
			Repo: repo, FromType: from.Type, FromID: from.ID, ToType: to.Type, ToID: to.ID,
			Relation: l.Relation, FirstSeen: at, LastConfirmed: at,
		}
		if l.External {
			x.Actor, x.ActedAt, x.Reason = "operator", at, l.Reason
			if err := st.AddExternalXref(x); err != nil {
				t.Fatalf("fixture %s: external link %s -> %s: %v", c.Name, l.From, l.To, err)
			}
			continue
		}
		x.Origin = "derived:fixture"
		derived[from] = append(derived[from], x)
	}
	for from, ls := range derived {
		if err := st.ReplaceDerivedXrefs(repo, from.Type, from.ID, ls); err != nil {
			t.Fatalf("fixture %s: derived links of %s: %v", c.Name, canonKey(from), err)
		}
	}

	in, err := Load(st, cfg, repo, now)
	if err != nil {
		t.Fatalf("fixture %s: Load: %v", c.Name, err)
	}
	for k, ts := range firstSeen {
		ent, ok := in.Entities[k]
		if !ok {
			t.Fatalf("fixture %s: first_seen_at set on %s, which Load did not return", c.Name, canonKey(k))
		}
		ent.FirstSeenAt = ts
		in.Entities[k] = ent
	}
	for _, s := range c.PlanKeys {
		k, err := parseCanonKey(s)
		if err != nil {
			t.Fatalf("fixture %s: plan_keys: %v", c.Name, err)
		}
		in.PlanKeys[k] = true
	}

	r := Rank(in, Candidates(in), RankOptions{Cap: c.Cap, Clock: interpret.FixedClock(now)})

	res := rankResult{
		Tiers: map[string]string{}, Deciding: map[string]string{},
		Priorities: map[string]string{}, Due: map[string]string{},
		RankInputs: r.Inputs, InPlan: []string{},
	}
	for _, row := range r.Rows {
		key := canonKey(row.Key)
		res.Order = append(res.Order, key)
		res.Tiers[key] = row.Tier
		if row.DecidingKey != "" {
			res.Deciding[key] = row.DecidingKey
		}
		res.Priorities[key] = row.Priority
		res.Due[key] = row.Due
		if row.InPlan {
			res.InPlan = append(res.InPlan, key)
		}
	}
	for _, e := range r.EpicsInPlay {
		res.EpicsInPlay = append(res.EpicsInPlay, canonKey(e.Key))
	}
	return res
}

// diffRankCase returns one line per way the result departs from the
// fixture's expectation; empty means it holds. The optional assertions
// (priorities, due, in_plan) apply only when the fixture names them.
func diffRankCase(c *rankCase, got rankResult) []string {
	var out []string
	e := c.Expected
	if !reflect.DeepEqual(got.Order, e.Order) {
		out = append(out, fmt.Sprintf("order = %v, want %v", got.Order, e.Order))
	}
	if !reflect.DeepEqual(got.Tiers, e.Tiers) {
		out = append(out, fmt.Sprintf("tiers = %v, want %v", got.Tiers, e.Tiers))
	}
	if !reflect.DeepEqual(got.Deciding, e.Deciding) {
		out = append(out, fmt.Sprintf("deciding keys = %v, want %v", got.Deciding, e.Deciding))
	}
	if !reflect.DeepEqual(nonNil(got.EpicsInPlay), nonNil(e.EpicsInPlay)) {
		out = append(out, fmt.Sprintf("epics in play = %v, want %v", got.EpicsInPlay, e.EpicsInPlay))
	}
	if e.RankInputs != nil && got.RankInputs != e.RankInputs.asRankInputs() {
		out = append(out, fmt.Sprintf("rank_inputs = %+v, want %+v", got.RankInputs, *e.RankInputs))
	}
	for k, want := range e.Priorities {
		if got.Priorities[k] != want {
			out = append(out, fmt.Sprintf("priority of %s = %q, want %q", k, got.Priorities[k], want))
		}
	}
	for k, want := range e.Due {
		if got.Due[k] != want {
			out = append(out, fmt.Sprintf("due of %s = %q, want %q", k, got.Due[k], want))
		}
	}
	if e.InPlan != nil && !reflect.DeepEqual(nonNil(got.InPlan), nonNil(*e.InPlan)) {
		out = append(out, fmt.Sprintf("in plan = %v, want %v", got.InPlan, *e.InPlan))
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// checkRankCase runs a fixture and fails the test with every departure.
func checkRankCase(t *testing.T, c *rankCase) rankResult {
	t.Helper()
	got := runRankCase(t, c)
	for _, d := range diffRankCase(c, got) {
		t.Errorf("%s: %s", c.Name, d)
	}
	return got
}

// rankFixtureFiles lists the *.json files of a testdata directory, sorted.
func rankFixtureFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	sort.Strings(files)
	return files
}

const rankGoldenDir = "testdata/rank"

// requiredRankGoldens are the goldens the packet and the spec name; the test
// fails when one is missing so a deleted fixture cannot shrink the gate.
var requiredRankGoldens = []string{
	"headtohead",
	"headtohead_reversed",
	"due_horizon",
	"priority_map",
	"priority_inheritance",
	"unblocks_unavailable",
	"age_tiebreak",
	"total_order_tiebreak",
	"cap_exact",
	"cap_under",
	"cap_default",
	"cap_zero",
	"epic_buried_p1",
	"epic_low_leaves",
	"epic_zero_leaf",
	"pr_candidates",
	"bd_due_timestamp",
	"blocked_open",
	"operator_identities",
	"jira_status_category",
}

func TestRankGoldens(t *testing.T) {
	files := rankFixtureFiles(t, rankGoldenDir)
	have := map[string]string{}
	for _, f := range files {
		have[strings.TrimSuffix(filepath.Base(f), ".json")] = f
	}
	for _, name := range requiredRankGoldens {
		if _, ok := have[name]; !ok {
			t.Errorf("required golden %s.json is missing from %s", name, rankGoldenDir)
		}
	}
	if len(files) < len(requiredRankGoldens) {
		t.Fatalf("only %d golden fixtures in %s, want at least %d", len(files), rankGoldenDir, len(requiredRankGoldens))
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) {
			checkRankCase(t, readRankCase(t, f))
		})
	}
}

// profile is the part of a head-to-head fixture entity the evidence table
// talks about.
type profile struct {
	started bool
	prio    string
	due     string
	blocks  int
}

// profileOf reads an issue entity's evidence-table profile from the fixture.
func profileOf(t *testing.T, c *rankCase, id string) profile {
	t.Helper()
	var p profile
	found := false
	for _, e := range c.Entities {
		if e.Type != "issue" {
			continue
		}
		var f struct {
			Show struct {
				State    string `json:"state"`
				Priority string `json:"priority"`
				DueDate  string `json:"due_date"`
				Deps     []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"deps"`
			} `json:"issue_show"`
		}
		if err := json.Unmarshal(e.Facts, &f); err != nil {
			t.Fatalf("facts of issue:%s: %v", e.ID, err)
		}
		if e.ID == id {
			found = true
			p.started = f.Show.State == "in_progress"
			p.prio, p.due = f.Show.Priority, f.Show.DueDate
		}
		for _, d := range f.Show.Deps {
			if d.ID == id && d.Type == "blocks" && f.Show.State == "open" {
				p.blocks++
			}
		}
	}
	if !found {
		t.Fatalf("fixture %s has no issue:%s", c.Name, id)
	}
	return p
}

// evidenceRow is one head-to-head ruling of the spec's evidence table (the rank step,
// 2026-10-05) bound to the ids of the head-to-head fixture that pins it. The
// profile columns are asserted against the fixture, so a fixture edit that no
// longer matches the ruling it names fails here and not silently.
type evidenceRow struct {
	ruling         string
	winner, loser  string
	winnerProfile  profile
	loserProfile   profile
	decidingForAdj string // the deciding key when the pair is adjacent in the list ("" otherwise)
}

const (
	h2hTomorrow = "2026-10-11"
	h2hIn3Days  = "2026-10-13"
	h2hOverdue  = "2026-10-09"
)

// evidenceRows binds the eight rulings to the ids of headtohead.json:
// bd-45 overdue unstarted P1, bd-31 started P3 due tomorrow, bd-38 started P1,
// bd-22 started P2, bd-52 started P3, bd-17 unstarted P3 due tomorrow, bd-61
// unstarted P3 due in 3 days, bd-09 unstarted P2 unblocking 3, bd-26 unstarted
// P1 blocking none.
var evidenceRows = []evidenceRow{
	{
		"started P2 over unstarted P1, same deadline", "bd-22", "bd-26",
		profile{true, "P2", "", 0},
		profile{false, "P1", "", 0},
		"",
	},
	{
		"started, blocks nothing over unstarted unblocker", "bd-22", "bd-09",
		profile{true, "P2", "", 0},
		profile{false, "P2", "", 3},
		"",
	},
	{
		"started P1 with no deadline over unstarted P3 due tomorrow", "bd-38", "bd-17",
		profile{true, "P1", "", 0},
		profile{false, "P3", h2hTomorrow, 0},
		"",
	},
	{
		"overdue unstarted P1 over started P3 with no deadline", "bd-45", "bd-52",
		profile{false, "P1", h2hOverdue, 0},
		profile{true, "P3", "", 0},
		"",
	},
	{
		"unstarted P3 due in 3 days over unstarted P1 with no deadline", "bd-61", "bd-26",
		profile{false, "P3", h2hIn3Days, 0},
		profile{false, "P1", "", 0},
		"",
	},
	{
		"started P3 due tomorrow over started P1 with no deadline", "bd-31", "bd-38",
		profile{true, "P3", h2hTomorrow, 0},
		profile{true, "P1", "", 0},
		KeyDue,
	},
	{
		"unstarted P2 unblocking 3 over unstarted P1 blocking none", "bd-09", "bd-26",
		profile{false, "P2", "", 3},
		profile{false, "P1", "", 0},
		KeyUnblocks,
	},
	{
		"unstarted, due in 3 days over unstarted, no deadline, unblocks 3 others", "bd-61", "bd-09",
		profile{false, "P3", h2hIn3Days, 0},
		profile{false, "P2", "", 3},
		KeyDue,
	},
}

// h2hOrder is the one full ordered list the eight rulings imply, by id.
var h2hOrder = []string{"bd-45", "bd-31", "bd-38", "bd-22", "bd-52", "bd-17", "bd-61", "bd-09", "bd-26"}

func beadKeys(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "issue:" + id
	}
	return out
}

func indexOf(order []string, key string) int {
	for i, k := range order {
		if k == key {
			return i
		}
	}
	return -1
}

// TestHeadToHeadGolden pins every row of the evidence table as ONE full
// ordered list of nine items (not only adjacent pairs): each ruling holds as
// a pair in that list, and the list itself is the golden.
func TestHeadToHeadGolden(t *testing.T) {
	c := readRankCase(t, filepath.Join(rankGoldenDir, "headtohead.json"))
	if want := beadKeys(h2hOrder); !reflect.DeepEqual(c.Expected.Order, want) {
		t.Fatalf("headtohead.json expects order %v, the evidence table implies %v", c.Expected.Order, want)
	}
	got := checkRankCase(t, c)
	for _, row := range evidenceRows {
		if p := profileOf(t, c, row.winner); p != row.winnerProfile {
			t.Errorf("%q: fixture winner %s is %+v, the ruling needs %+v", row.ruling, row.winner, p, row.winnerProfile)
		}
		if p := profileOf(t, c, row.loser); p != row.loserProfile {
			t.Errorf("%q: fixture loser %s is %+v, the ruling needs %+v", row.ruling, row.loser, p, row.loserProfile)
		}
		w, l := indexOf(got.Order, "issue:"+row.winner), indexOf(got.Order, "issue:"+row.loser)
		if w < 0 || l < 0 || w >= l {
			t.Errorf("%q: %s is at %d and %s at %d in %v, want the winner first", row.ruling, row.winner, w, row.loser, l, got.Order)
		}
		if row.decidingForAdj != "" && l == w+1 {
			if d := got.Deciding["issue:"+row.winner]; d != row.decidingForAdj {
				t.Errorf("%q: deciding key under %s = %q, want %q", row.ruling, row.winner, d, row.decidingForAdj)
			}
		}
	}
}

// TestHeadToHeadReversedKeysNegativeControl is the negative control: the
// sibling fixture gives every item the key profile of its mirror position, so
// each key that decided an adjacent pair is inverted. The golden's own
// expectation MUST fail against those inputs, every ruling's pair MUST come
// out the other way round, and the sibling's own expectation (the mirror
// ranking, with the same deciding key at each position) MUST hold, which
// proves the keys and not the id tiebreak decide in both directions.
func TestHeadToHeadReversedKeysNegativeControl(t *testing.T) {
	orig := readRankCase(t, filepath.Join(rankGoldenDir, "headtohead.json"))
	rev := readRankCase(t, filepath.Join(rankGoldenDir, "headtohead_reversed.json"))

	got := checkRankCase(t, rev)

	// The original expectation does not hold on the reversed inputs.
	if diffs := diffRankCase(orig, got); len(diffs) == 0 {
		t.Fatalf("the golden's expectation holds on the reversed-key inputs: the golden does not depend on the keys")
	}
	// Every ruling's pair is inverted: the winner id now ranks AFTER the loser id.
	for _, row := range evidenceRows {
		w, l := indexOf(got.Order, "issue:"+row.winner), indexOf(got.Order, "issue:"+row.loser)
		if w < 0 || l < 0 || w <= l {
			t.Errorf("%q: with the keys reversed %s is at %d and %s at %d in %v, want the loser first", row.ruling, row.winner, w, row.loser, l, got.Order)
		}
	}
	// The mirror: each position holds the id whose profile the original has
	// at the mirrored position, and the deciding key between two positions is
	// the original's, because the profiles are the same, only the ids moved.
	if len(orig.Expected.Order) != len(rev.Expected.Order) {
		t.Fatalf("the sibling ranks %d items, the golden %d", len(rev.Expected.Order), len(orig.Expected.Order))
	}
	origDeciding := make([]string, 0, len(orig.Expected.Order)-1)
	for i := 0; i < len(orig.Expected.Order)-1; i++ {
		origDeciding = append(origDeciding, orig.Expected.Deciding[orig.Expected.Order[i]])
	}
	revDeciding := make([]string, 0, len(rev.Expected.Order)-1)
	for i := 0; i < len(rev.Expected.Order)-1; i++ {
		revDeciding = append(revDeciding, rev.Expected.Deciding[rev.Expected.Order[i]])
	}
	if !reflect.DeepEqual(origDeciding, revDeciding) {
		t.Errorf("deciding keys by position: golden %v, reversed %v, want the same sequence", origDeciding, revDeciding)
	}
	for _, d := range revDeciding {
		if d == KeyTiebreak {
			t.Errorf("a position of the reversed fixture is decided by the tiebreak, not a key: %v", revDeciding)
		}
	}
}

// TestRankGoldenHarnessDetectsDeviations is the harness's own mutation check:
// each way an expectation can be wrong makes diffRankCase report it, so a
// golden cannot pass while asserting something false.
func TestRankGoldenHarnessDetectsDeviations(t *testing.T) {
	base := readRankCase(t, filepath.Join(rankGoldenDir, "headtohead.json"))
	got := runRankCase(t, base)
	if d := diffRankCase(base, got); len(d) != 0 {
		t.Fatalf("the unmutated golden already fails: %v", d)
	}
	mutate := map[string]func(c *rankCase){
		"two adjacent keys swapped in the order": func(c *rankCase) {
			o := append([]string(nil), c.Expected.Order...)
			o[3], o[4] = o[4], o[3]
			c.Expected.Order = o
		},
		"a tier changed": func(c *rankCase) {
			m := copyMap(c.Expected.Tiers)
			m["issue:bd-45"] = TierStarted
			c.Expected.Tiers = m
		},
		"a deciding key changed": func(c *rankCase) {
			m := copyMap(c.Expected.Deciding)
			m["issue:bd-31"] = KeyPriority
			c.Expected.Deciding = m
		},
		"a rank_inputs counter changed": func(c *rankCase) {
			in := *c.Expected.RankInputs
			in.AgeFallback++
			c.Expected.RankInputs = &in
		},
		"an epics-in-play entry added": func(c *rankCase) {
			c.Expected.EpicsInPlay = []string{"issue:bd-45"}
		},
		"an extra priority assertion that is false": func(c *rankCase) {
			c.Expected.Priorities = map[string]string{"issue:bd-45": "P4"}
		},
		"an extra due assertion that is false": func(c *rankCase) {
			c.Expected.Due = map[string]string{"issue:bd-45": "2000-01-01"}
		},
		"an in_plan assertion that is false": func(c *rankCase) {
			none := []string{}
			c.Expected.InPlan = &none
		},
	}
	for name, mut := range mutate {
		t.Run(name, func(t *testing.T) {
			c := *base
			c.Expected = base.Expected
			mut(&c)
			if d := diffRankCase(&c, got); len(d) == 0 {
				t.Errorf("a mutated expectation (%s) was not detected", name)
			}
		})
	}
	t.Run("an incomplete expectation is rejected", func(t *testing.T) {
		c := *base
		c.Expected = base.Expected
		c.Expected.Deciding = map[string]string{}
		c.Expected.RankInputs = nil
		if p := c.validateExpectation(); len(p) < 2 {
			t.Errorf("validateExpectation = %v, want the missing deciding keys and rank_inputs reported", p)
		}
	})
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestRankDiffsDocument checks DIFFS.md next to the goldens: it is a markdown
// table with the four required columns, and one row names each deliberate
// difference from df-survey. The check is on the document's shape and its
// coverage of the named differences, not on its prose.
func TestRankDiffsDocument(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(rankGoldenDir, "DIFFS.md"))
	if err != nil {
		t.Fatalf("read DIFFS.md: %v", err)
	}
	text := string(b)
	// The differences table is the one that opens with the header; it ends at
	// the first line that is not a table row. (prettier realigns the header's
	// padding, so find it by its cells.)
	var rows []string
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if !inTable {
			inTable = strings.HasPrefix(line, "|") && len(cells) == 4 &&
				cells[0] == "Difference" && cells[1] == "df-survey behavior" && cells[2] == "pg-desk behavior" && cells[3] == "Decision"
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		if strings.HasPrefix(cells[0], "---") {
			continue
		}
		if len(cells) != 4 {
			t.Errorf("DIFFS.md row has %d cells, want 4: %s", len(cells), line)
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		t.Fatalf("DIFFS.md differences table has no rows")
	}
	// Each entry is a marker that MUST appear in the row's Decision or
	// Difference cell: the spec's named differences, then the two the spec
	// does not carry (the design's rank-regression text and the packet's decision-encoding text).
	for _, want := range []struct{ name, marker string }{
		{"D-F2 single repository for PRs", "D-F2"},
		{"D-F11 seeds, one-hop linked candidates, epic slot rule", "D-F11"},
		{"RV-C beads are candidates only by the label", "RV-C"},
		{"RV-D linked candidates", "RV-D"},
		{"D-F23 Jira by assignee identity list", "D-F23"},
		{"the Jira status category", "status category"},
		{"the widened blocked open-beads query", "blocked"},
		{"the deployment-specific request-type priority floor", "request type"},
		{"an epic ranked by its top open leaf", "top open leaf"},
	} {
		found := false
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r), strings.ToLower(want.marker)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DIFFS.md has no row for %s (marker %q)", want.name, want.marker)
		}
	}
}
