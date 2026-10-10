package rules

// Tests of the focus.item rule (daily-focus design section 8, 8.1, 8.3):
// mint, hold, release and terminal hold for the entity types issue and pr,
// table-driven over the bead states of the section 8.3 table. They run through
// decide.DecideWith (the registered rule, the hidden and suppressed precedence)
// over the views in testdata/focus, patched in place. Every helper here is
// prefixed "focus" because sibling packets add test files to this package.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const focusPeriod = "2026-10-10"

func focusRaw(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "focus", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func focusLoad(t *testing.T, name string) *view.View {
	t.Helper()
	v, err := view.Parse(focusRaw(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

// focusMap decodes fixture name into a mutable object.
func focusMap(t *testing.T, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(focusRaw(t, name), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func focusParse(t *testing.T, m map[string]any) *view.View {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func focusPatch(t *testing.T, name string, mutate func(m map[string]any)) *view.View {
	t.Helper()
	m := focusMap(t, name)
	mutate(m)
	return focusParse(t, m)
}

// focusBead describes the linked focus bead of a view; nil means none.
type focusBead struct {
	state    string
	assignee string
	marker   string // focus_hold metadata value, "" for none
	labels   []string
	extraMD  map[string]string
}

func (b focusBead) link(typ, id string) map[string]any {
	md := map[string]any{
		"source_type": typ, "source_id": id,
		"dedup_key": typ + ":" + id + ":focus-item",
	}
	if b.marker != "" {
		md["focus_hold"] = b.marker
	}
	for k, v := range b.extraMD {
		md[k] = v
	}
	labels := b.labels
	if labels == nil {
		labels = []string{"focus-item"}
	}
	return map[string]any{
		"type": "issue", "id": "bd-focus-1", "relation": "source", "state": b.state,
		"assignee": b.assignee, "title": "Focus " + id, "labels": labels, "metadata": md,
	}
}

// focusSetup is one scenario: a fixture, the annotation value (nil = null),
// an optional bead and an optional source mutation.
type focusSetup struct {
	fixture    string
	annotation any
	bead       *focusBead
	source     func(snap map[string]any)
}

func (s focusSetup) view(t *testing.T) *view.View {
	t.Helper()
	return focusParse(t, s.mapped(t))
}

func (s focusSetup) mapped(t *testing.T) map[string]any {
	t.Helper()
	m := focusMap(t, s.fixture)
	suiteAnnotations(m)["focus_selected"] = s.annotation
	if s.source != nil {
		s.source(m["snapshot"].(map[string]any))
	}
	m["links"] = []any{}
	if s.bead != nil {
		m["links"] = []any{s.bead.link(m["type"].(string), m["id"].(string))}
	}
	return m
}

func (s focusSetup) typ(t *testing.T) string {
	t.Helper()
	return focusMap(t, s.fixture)["type"].(string)
}

// focusOutcome is what focus.item did in a plan: at most one action, or a skip.
type focusOutcome struct {
	act  *action.Action
	skip *action.Skip
}

func focusDecide(t *testing.T, v *view.View, cfg *config.Config) focusOutcome {
	t.Helper()
	res := decide.DecideWith(v, v.Type, cfg)
	var out focusOutcome
	for i := range res.Actions {
		if res.Actions[i].Rule == focusRuleID {
			if out.act != nil {
				t.Fatalf("focus.item planned more than one action: %+v and %+v", *out.act, res.Actions[i])
			}
			out.act = &res.Actions[i]
		}
	}
	for i := range res.Skipped {
		if res.Skipped[i].Rule == focusRuleID {
			out.skip = &res.Skipped[i]
		}
	}
	if (out.act == nil) == (out.skip == nil) {
		t.Fatalf("focus.item must either act or skip, got action %v and skip %v", out.act, out.skip)
	}
	return out
}

func (s focusSetup) decide(t *testing.T) focusOutcome {
	t.Helper()
	return focusDecide(t, s.view(t), nil)
}

func focusCause(o focusOutcome) string {
	if o.skip == nil {
		return ""
	}
	c, _ := o.skip.Facts["cause"].(string)
	return c
}

func focusTransition(o focusOutcome) string {
	if o.act == nil {
		return ""
	}
	tr, _ := o.act.Facts["transition"].(string)
	return tr
}

// focusWant is the expected outcome of one row.
type focusWant struct {
	transition string // "" for a skip
	fields     action.Fields
	reason     string // skip reason
	cause      string // skip cause fact
}

var (
	focusHold = action.Fields{
		Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"},
	}
	focusRelease = action.Fields{
		Status: "open", ClearDefer: true, Metadata: map[string]string{"focus_hold": "released"},
	}
	focusMarkerOnly = action.Fields{Metadata: map[string]string{"focus_hold": "released"}}
)

func (w focusWant) check(t *testing.T, o focusOutcome) {
	t.Helper()
	if w.transition == "" {
		if o.act != nil {
			t.Fatalf("want a skip %q (%s), got action %+v", w.reason, w.cause, *o.act)
		}
		if o.skip.Reason != w.reason {
			t.Errorf("skip reason = %q, want %q (facts %v)", o.skip.Reason, w.reason, o.skip.Facts)
		}
		if got := focusCause(o); got != w.cause {
			t.Errorf("skip cause = %q, want %q", got, w.cause)
		}
		return
	}
	if o.act == nil {
		t.Fatalf("want a %s action, got skip %+v", w.transition, *o.skip)
	}
	a := *o.act
	if w.transition == "mint" { // the bead shape has its own tests
		if a.Op != action.OpCreate || a.Kind != "focus-item" || a.Target != nil || focusTransition(o) != "mint" {
			t.Errorf("mint = op %q kind %q target %v transition %q", a.Op, a.Kind, a.Target, focusTransition(o))
		}
		return
	}
	if a.Op != action.OpUpdate || a.Kind != "focus-item" || a.Target == nil || *a.Target != "bd-focus-1" {
		t.Errorf("action = op %q kind %q target %v, want update focus-item bd-focus-1", a.Op, a.Kind, a.Target)
	}
	if !reflect.DeepEqual(a.Fields, w.fields) {
		t.Errorf("fields = %+v, want %+v", a.Fields, w.fields)
	}
	if got := focusTransition(o); got != w.transition {
		t.Errorf("transition = %q, want %q", got, w.transition)
	}
}

func focusSkipRow(cause string) focusWant {
	return focusWant{reason: action.ReasonAlreadyHandled, cause: cause}
}

// ---- registration -----------------------------------------------------

func TestFocusRuleRegisteredForIssueAndPR(t *testing.T) {
	for _, typ := range []string{decide.EntityTypeIssue, decide.EntityTypePR} {
		var found decide.Rule
		rs := decide.RulesFor(typ)
		for _, r := range rs {
			if r.ID() == "focus.item" {
				found = r
			}
		}
		if found == nil {
			t.Fatalf("%s: focus.item is not registered", typ)
		}
		if found.Kind() != workitem.KindFocusItem {
			t.Errorf("%s: kind = %q, want focus-item (so suppress.focus-item suppresses it)", typ, found.Kind())
		}
		if !decide.HasDecider(typ) {
			t.Errorf("%s: HasDecider = false", typ)
		}
	}
	// It runs after every PR rule, so after land.ready.
	pr := decide.RulesFor(decide.EntityTypePR)
	if last := pr[len(pr)-1].ID(); last != "focus.item" {
		t.Errorf("last PR rule = %q, want focus.item", last)
	}
	if decide.OrdinalFocusItem <= decide.OrdinalLandReady {
		t.Errorf("OrdinalFocusItem %d must follow OrdinalLandReady %d", decide.OrdinalFocusItem, decide.OrdinalLandReady)
	}
	if got := len(decide.RulesFor(decide.EntityTypeIssue)); got != 1 {
		t.Errorf("issue rule set holds %d rules, want focus.item alone", got)
	}
	if decide.HasDecider("thread") {
		t.Error("thread must have no decider")
	}
}

// ---- the bead-state table (section 8.3) ---------------------------------

// focusStateRows is one row per line of the bead-state table that reaches the
// decider, for a LIVE source: what a strike (annotation none) and a select
// (a real period key) plan.
var focusStateRows = []struct {
	name   string
	bead   *focusBead
	strike focusWant
	sel    focusWant
}{
	{
		name:   "no bead yet",
		bead:   nil,
		strike: focusWant{reason: action.ReasonNotMatched, cause: "not-selected"},
		sel:    focusWant{transition: "mint"},
	},
	{
		name:   "open unclaimed no marker",
		bead:   &focusBead{state: "open"},
		strike: focusWant{transition: "hold", fields: focusHold},
		sel:    focusSkipRow("in-play"),
	},
	{
		name:   "open unclaimed marker released",
		bead:   &focusBead{state: "open", marker: "released"},
		strike: focusWant{transition: "hold", fields: focusHold},
		sel:    focusSkipRow("in-play"),
	},
	{
		name:   "open unclaimed marker struck (undeferred by hand)",
		bead:   &focusBead{state: "open", marker: "struck"},
		strike: focusWant{transition: "hold", fields: focusHold},
		sel:    focusWant{transition: "release", fields: focusMarkerOnly},
	},
	{
		name:   "open labelled human",
		bead:   &focusBead{state: "open", labels: []string{"focus-item", "human"}},
		strike: focusWant{transition: "hold", fields: focusHold},
		sel:    focusSkipRow("in-play"),
	},
	{
		name:   "in_progress",
		bead:   &focusBead{state: "in_progress"},
		strike: focusSkipRow("claimed"),
		sel:    focusSkipRow("claimed"),
	},
	{
		name:   "open assigned",
		bead:   &focusBead{state: "open", assignee: "worker-1"},
		strike: focusSkipRow("claimed"),
		sel:    focusSkipRow("claimed"),
	},
	{
		name:   "open assigned marker struck",
		bead:   &focusBead{state: "open", assignee: "worker-1", marker: "struck"},
		strike: focusSkipRow("claimed"),
		sel:    focusSkipRow("claimed"),
	},
	{
		name:   "deferred marker struck assigned",
		bead:   &focusBead{state: "deferred", assignee: "worker-1", marker: "struck"},
		strike: focusSkipRow("claimed"),
		sel:    focusSkipRow("claimed"),
	},
	{
		name:   "held (deferred, struck, unassigned)",
		bead:   &focusBead{state: "deferred", marker: "struck"},
		strike: focusSkipRow("held"),
		sel:    focusWant{transition: "release", fields: focusRelease},
	},
	{
		name:   "deferred no marker (someone else)",
		bead:   &focusBead{state: "deferred"},
		strike: focusSkipRow("deferred-by-someone-else"),
		sel:    focusSkipRow("deferred-by-someone-else"),
	},
	{
		name:   "deferred marker released",
		bead:   &focusBead{state: "deferred", marker: "released"},
		strike: focusSkipRow("deferred-by-someone-else"),
		sel:    focusSkipRow("deferred-by-someone-else"),
	},
	{
		name:   "blocked",
		bead:   &focusBead{state: "blocked"},
		strike: focusSkipRow("status-not-open"),
		sel:    focusSkipRow("status-not-open"),
	},
	{
		name:   "pinned",
		bead:   &focusBead{state: "pinned"},
		strike: focusSkipRow("status-not-open"),
		sel:    focusSkipRow("status-not-open"),
	},
	{
		name:   "hooked",
		bead:   &focusBead{state: "hooked"},
		strike: focusSkipRow("status-not-open"),
		sel:    focusSkipRow("status-not-open"),
	},
	{
		name:   "closed",
		bead:   &focusBead{state: "closed"},
		strike: focusSkipRow("closed"),
		sel:    focusSkipRow("closed"),
	},
	{
		name:   "closed with marker struck",
		bead:   &focusBead{state: "closed", marker: "struck"},
		strike: focusSkipRow("closed"),
		sel:    focusSkipRow("closed"),
	},
}

func TestFocusBeadStateTable(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		for _, row := range focusStateRows {
			for _, mode := range []struct {
				name       string
				annotation any
				want       focusWant
			}{
				{"strike", "none", row.strike},
				{"select", focusPeriod, row.sel},
			} {
				t.Run(fixture+"/"+row.name+"/"+mode.name, func(t *testing.T) {
					s := focusSetup{fixture: fixture, annotation: mode.annotation, bead: row.bead}
					mode.want.check(t, s.decide(t))
				})
			}
		}
	}
}

// Every skip of a source that has a focus bead carries the bead's id and
// state, which the telemetry packet counts; a source without one does not.
func TestFocusSkipsCarryTheBeadFact(t *testing.T) {
	for _, row := range focusStateRows {
		for _, ann := range []any{"none", focusPeriod} {
			s := focusSetup{fixture: "pr_selected", annotation: ann, bead: row.bead}
			o := s.decide(t)
			if o.skip == nil {
				continue
			}
			got, has := o.skip.Facts["bead"]
			if row.bead == nil {
				if has {
					t.Errorf("%s/%v: a skip with no bead carries a bead fact %v", row.name, ann, got)
				}
				continue
			}
			want := map[string]any{"id": "bd-focus-1", "state": row.bead.state}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s/%v: bead fact = %v, want %v", row.name, ann, got, want)
			}
		}
	}
}

// ---- strike spellings -----------------------------------------------------

// none, "" and null behave identically (with a bead and without); a malformed
// key is "not matched" and never holds.
func TestFocusNoneEmptyAndNullBehaveIdentically(t *testing.T) {
	for _, bead := range []*focusBead{nil, {state: "open"}, {state: "deferred", marker: "struck"}, {state: "in_progress"}} {
		var ref *focusOutcome
		for _, ann := range []any{nil, "none", ""} {
			o := focusSetup{fixture: "issue_selected", annotation: ann, bead: bead}.decide(t)
			if ref == nil {
				ref = &o
				continue
			}
			if !reflect.DeepEqual(o, *ref) {
				t.Errorf("bead %+v: annotation %v plans %+v, null plans %+v", bead, ann, o, *ref)
			}
		}
	}
	// With an open bead all three hold it.
	for _, ann := range []any{nil, "none", ""} {
		focusWant{transition: "hold", fields: focusHold}.check(t,
			focusSetup{fixture: "pr_selected", annotation: ann, bead: &focusBead{state: "open"}}.decide(t))
	}
}

func TestFocusMalformedKeyIsNotMatchedAndNeverHolds(t *testing.T) {
	for _, ann := range []string{"2026-9-23", "2026-13-45", "yesterday", "NONE", " none", "2026-10-10T00:00:00Z"} {
		for _, bead := range []*focusBead{nil, {state: "open"}, {state: "deferred", marker: "struck"}} {
			o := focusSetup{fixture: "pr_selected", annotation: ann, bead: bead}.decide(t)
			if o.act != nil || o.skip.Reason != action.ReasonNotMatched || focusCause(o) != "malformed-key" {
				t.Errorf("annotation %q bead %+v: %+v, want not matched (malformed-key)", ann, bead, o)
			}
		}
	}
}

// ---- fail closed on an absent member ---------------------------------------

func TestFocusRuleFailsClosedWhenMemberAbsentFromView(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		for _, bead := range []*focusBead{nil, {state: "open"}, {state: "deferred", marker: "struck"}} {
			s := focusSetup{fixture: fixture, annotation: "none", bead: bead}
			m := s.mapped(t)
			delete(suiteAnnotations(m), "focus_selected")
			v := focusParse(t, m)

			err := CheckFocusView(v, v.Type)
			if !errors.Is(err, ErrFocusSelectedAbsent) || !strings.Contains(err.Error(), "view-lacks-focus_selected") {
				t.Fatalf("%s: CheckFocusView = %v, want ErrFocusSelectedAbsent", fixture, err)
			}
			// Zero writes: whatever evaluates it, the rule plans nothing.
			o := focusDecide(t, v, nil)
			if o.act != nil || o.skip.Reason != action.ReasonNotMatched {
				t.Errorf("%s bead %+v: absent member planned %+v", fixture, bead, o)
			}
		}
	}
}

func TestFocusCheckViewAcceptsPresentNullAndOtherTypes(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		v := focusSetup{fixture: fixture, annotation: nil, bead: &focusBead{state: "open"}}.view(t)
		if !v.Annotations.FocusSelected.Present || v.Annotations.FocusSelected.Set {
			t.Fatalf("setup: want a present null, got %+v", v.Annotations.FocusSelected)
		}
		if err := CheckFocusView(v, v.Type); err != nil {
			t.Errorf("%s: a present null must pass, got %v", fixture, err)
		}
		// Present null with an open bead may hold (it reads as unset).
		focusWant{transition: "hold", fields: focusHold}.check(t, focusDecide(t, v, nil))
	}

	absent := focusPatch(t, "pr_selected", func(m map[string]any) { delete(suiteAnnotations(m), "focus_selected") })
	if err := CheckFocusView(absent, "thread"); err != nil {
		t.Errorf("a type focus.item is not registered for must pass, got %v", err)
	}
	if err := CheckFocusView(nil, "pr"); err != nil {
		t.Errorf("a nil view must pass, got %v", err)
	}
}

// ---- terminal sources ----------------------------------------------------

func focusPRTerminal(state string, merged bool) func(snap map[string]any) {
	return func(snap map[string]any) { snap["state"] = state; snap["merged"] = merged }
}

func focusIssueTerminal(state, category string) func(snap map[string]any) {
	return func(snap map[string]any) {
		snap["state"] = state
		if category == "" {
			delete(snap, "status_category")
		} else {
			snap["status_category"] = category
		}
	}
}

// focusTerminalSources: every source shape and whether it is terminal.
var focusTerminalSources = []struct {
	name     string
	fixture  string
	source   func(snap map[string]any)
	terminal bool
}{
	{"pr merged", "pr_selected", focusPRTerminal("closed", true), true},
	{"pr merged state", "pr_selected", focusPRTerminal("merged", false), true},
	{"pr closed", "pr_selected", focusPRTerminal("closed", false), true},
	{"pr open", "pr_selected", focusPRTerminal("open", false), false},
	{"pr unknown state", "pr_selected", focusPRTerminal("", false), false},
	{"issue category done", "issue_selected", focusIssueTerminal("Shipped", "done"), true},
	{"issue category Done", "issue_selected", focusIssueTerminal("Shipped", "Done"), true},
	{"issue category new", "issue_selected", focusIssueTerminal("To Do", "new"), false},
	{"issue category indeterminate", "issue_selected", focusIssueTerminal("In Progress", "indeterminate"), false},
	// A non-done category wins over a done-looking state.
	{"issue category indeterminate state closed", "issue_selected", focusIssueTerminal("closed", "indeterminate"), false},
	{"issue no category state closed", "issue_selected", focusIssueTerminal("closed", ""), true},
	{"issue no category state done", "issue_selected", focusIssueTerminal("Done", ""), true},
	{"issue no category state resolved", "issue_selected", focusIssueTerminal("resolved", ""), true},
	{"issue no category state cancelled", "issue_selected", focusIssueTerminal("cancelled", ""), true},
	{"issue no category state canceled", "issue_selected", focusIssueTerminal("canceled", ""), true},
	{"issue no category state wontfix", "issue_selected", focusIssueTerminal("wontfix", ""), true},
	{"issue no category state open", "issue_selected", focusIssueTerminal("open", ""), false},
	{"issue no category state in_progress", "issue_selected", focusIssueTerminal("in_progress", ""), false},
	{"issue no category no state", "issue_selected", focusIssueTerminal("", ""), false},
}

func TestFocusTerminalPredicate(t *testing.T) {
	for _, tc := range focusTerminalSources {
		t.Run(tc.name, func(t *testing.T) {
			v := focusSetup{fixture: tc.fixture, annotation: focusPeriod, source: tc.source}.view(t)
			if got := focusSourceTerminal(v); got != tc.terminal {
				t.Errorf("focusSourceTerminal = %v, want %v", got, tc.terminal)
			}
		})
	}
}

// A terminal source mints nothing; it holds an unclaimed bead exactly like a
// strike, and a reselect of it holds and never releases.
func TestFocusTerminalSourceHoldsAndNeverReleases(t *testing.T) {
	for _, tc := range focusTerminalSources {
		if !tc.terminal {
			continue
		}
		for _, ann := range []any{focusPeriod, "none", nil} {
			t.Run(fmt.Sprintf("%s/%v", tc.name, ann), func(t *testing.T) {
				run := func(bead *focusBead) focusOutcome {
					return focusSetup{fixture: tc.fixture, annotation: ann, bead: bead, source: tc.source}.decide(t)
				}
				// No bead: nothing is minted.
				if o := run(nil); o.act != nil || o.skip.Reason != action.ReasonNotMatched {
					t.Errorf("no bead: %+v, want not matched", o)
				}
				if ann == focusPeriod {
					if got := focusCause(run(nil)); got != "source-terminal" {
						t.Errorf("no bead cause = %q, want source-terminal", got)
					}
				}
				// An open unclaimed bead is held, selected or not.
				focusWant{transition: "hold_terminal", fields: focusHold}.check(t, run(&focusBead{state: "open"}))
				// ... also one carrying a stale marker, even for a selected item.
				focusWant{transition: "hold_terminal", fields: focusHold}.check(t, run(&focusBead{state: "open", marker: "struck"}))
				// A held bead stays held: no release, selected or not.
				if o := run(&focusBead{state: "deferred", marker: "struck"}); o.act != nil || focusCause(o) != "held-source-terminal" {
					t.Errorf("held bead: %+v, want a skip (held-source-terminal)", o)
				}
				// A claimed bead is left running; a closed bead stays closed.
				focusSkipRow("claimed").check(t, run(&focusBead{state: "in_progress"}))
				focusSkipRow("closed").check(t, run(&focusBead{state: "closed"}))
				focusSkipRow("deferred-by-someone-else").check(t, run(&focusBead{state: "deferred"}))
			})
		}
	}
}

// ---- hold and release, end to end ---------------------------------------

// focusApplyAction writes an update action into the view the way the apply
// step plus a refresh would: the bead's status and metadata change.
func focusApplyAction(t *testing.T, m map[string]any, a action.Action) {
	t.Helper()
	if a.Op != action.OpUpdate || a.Target == nil {
		t.Fatalf("only an update of an existing bead can be applied here: %+v", a)
	}
	for _, l := range m["links"].([]any) {
		link := l.(map[string]any)
		if link["id"] != *a.Target {
			continue
		}
		if a.Fields.Status != "" {
			link["state"] = a.Fields.Status
		}
		md := link["metadata"].(map[string]any)
		for k, v := range a.Fields.Metadata {
			md[k] = v
		}
		return
	}
	t.Fatalf("no link %q to apply to", *a.Target)
}

// decide, apply into the rebuilt view, decide again: zero writes the second time.
func TestFocusHoldIsIdempotent(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		for _, ann := range []any{"none", nil, ""} {
			s := focusSetup{fixture: fixture, annotation: ann, bead: &focusBead{state: "open"}}
			m := s.mapped(t)
			first := focusDecide(t, focusParse(t, m), nil)
			focusWant{transition: "hold", fields: focusHold}.check(t, first)
			focusApplyAction(t, m, *first.act)

			second := focusDecide(t, focusParse(t, m), nil)
			if second.act != nil {
				t.Fatalf("%s/%v: the second decision wrote %+v", fixture, ann, *second.act)
			}
			if focusCause(second) != "held" {
				t.Errorf("%s/%v: second cause = %q, want held", fixture, ann, focusCause(second))
			}
		}
	}
}

func TestFocusClaimedStrikeLeavesBeadAlone(t *testing.T) {
	for _, bead := range []focusBead{
		{state: "in_progress", assignee: "worker-1"},
		{state: "in_progress"},
		{state: "open", assignee: "worker-1"},
	} {
		s := focusSetup{fixture: "issue_selected", annotation: "none", bead: &bead}
		m := s.mapped(t)
		first := focusDecide(t, focusParse(t, m), nil)
		if first.act != nil || focusCause(first) != "claimed" {
			t.Fatalf("bead %+v: first = %+v, want a claimed skip", bead, first)
		}
		// Nothing was written, so the rebuilt view is the same and so is the plan.
		if second := focusDecide(t, focusParse(t, m), nil); !reflect.DeepEqual(first, second) {
			t.Errorf("bead %+v: second = %+v, want %+v", bead, second, first)
		}
	}
}

// Only a HELD bead (all three conditions) is released, and a release never
// clears an assignee: the action carries no assignee field at all.
func TestFocusReleaseOnlyTouchesHeldBeads(t *testing.T) {
	cases := []struct {
		name    string
		bead    focusBead
		release bool
	}{
		{"deferred with the marker", focusBead{state: "deferred", marker: "struck"}, true},
		{"deferred without the marker", focusBead{state: "deferred"}, false},
		{"deferred marker released", focusBead{state: "deferred", marker: "released"}, false},
		{"marker struck but open", focusBead{state: "open", marker: "struck"}, false},
		{"marker struck but assigned", focusBead{state: "open", marker: "struck", assignee: "worker-1"}, false},
		{"deferred marker struck but assigned", focusBead{state: "deferred", marker: "struck", assignee: "worker-1"}, false},
		{"closed", focusBead{state: "closed", marker: "struck"}, false},
		{"in play", focusBead{state: "open"}, false},
		{"claimed", focusBead{state: "in_progress"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bead := tc.bead
			o := focusSetup{fixture: "pr_selected", annotation: focusPeriod, bead: &bead}.decide(t)
			released := o.act != nil && o.act.Fields.Status == "open" && o.act.Fields.ClearDefer
			if released != tc.release {
				t.Fatalf("released = %v, want %v (%+v)", released, tc.release, o)
			}
			if o.act != nil {
				js, err := json.Marshal(o.act.Fields)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(js), "assignee") {
					t.Errorf("an action must never touch the assignee: %s", js)
				}
			}
		})
	}
}

// strike, release, strike: the marker goes struck, released, struck.
func TestFocusStrikeReleaseStrikeCycle(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		t.Run(fixture, func(t *testing.T) {
			m := focusSetup{fixture: fixture, annotation: focusPeriod, bead: &focusBead{state: "open"}}.mapped(t)
			marker := func() string {
				return m["links"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["focus_hold"].(string)
			}
			step := func(ann any, transition string, fields action.Fields) {
				t.Helper()
				suiteAnnotations(m)["focus_selected"] = ann
				o := focusDecide(t, focusParse(t, m), nil)
				focusWant{transition: transition, fields: fields}.check(t, o)
				focusApplyAction(t, m, *o.act)
			}
			step("none", "hold", focusHold)
			if got := marker(); got != "struck" {
				t.Fatalf("after the strike marker = %q", got)
			}
			step(focusPeriod, "release", focusRelease)
			if got := marker(); got != "released" {
				t.Fatalf("after the release marker = %q", got)
			}
			step("none", "hold", focusHold)
			if got := marker(); got != "struck" {
				t.Fatalf("after the second strike marker = %q", got)
			}
			// And a settled bead plans nothing on every spelling.
			suiteAnnotations(m)["focus_selected"] = "none"
			if o := focusDecide(t, focusParse(t, m), nil); o.act != nil {
				t.Fatalf("a held bead was written again: %+v", *o.act)
			}
		})
	}
}

func TestFocusStaleMarkerOpenBeadIsHeldOnStrikeAndRewrittenOnSelect(t *testing.T) {
	bead := &focusBead{state: "open", marker: "struck"}

	strike := focusSetup{fixture: "issue_selected", annotation: "none", bead: bead}.decide(t)
	focusWant{transition: "hold", fields: focusHold}.check(t, strike)

	sel := focusSetup{fixture: "issue_selected", annotation: focusPeriod, bead: bead}
	m := sel.mapped(t)
	o := focusDecide(t, focusParse(t, m), nil)
	focusWant{transition: "release", fields: focusMarkerOnly}.check(t, o)
	if o.act.Facts["marker_only"] != true {
		t.Errorf("the rewrite is a marker-only update: facts %v", o.act.Facts)
	}
	if o.act.Fields.Status != "" || o.act.Fields.ClearDefer {
		t.Errorf("a marker rewrite must not touch status or deferral: %+v", o.act.Fields)
	}
	// After the rewrite the bead is plain "in play", and a later strike holds it normally.
	focusApplyAction(t, m, *o.act)
	if again := focusDecide(t, focusParse(t, m), nil); again.act != nil || focusCause(again) != "in-play" {
		t.Errorf("after the rewrite: %+v, want an in-play skip", again)
	}
	suiteAnnotations(m)["focus_selected"] = "none"
	focusWant{transition: "hold", fields: focusHold}.check(t, focusDecide(t, focusParse(t, m), nil))
}

// ---- never close, never reopen, never read the close reason ------------

func TestFocusNeverClosesOrReopens(t *testing.T) {
	states := []focusBead{
		{state: "open"},
		{state: "open", marker: "struck"},
		{state: "open", marker: "released"},
		{state: "in_progress"},
		{state: "open", assignee: "w"},
		{state: "deferred"},
		{state: "deferred", marker: "struck"},
		{state: "deferred", marker: "struck", assignee: "w"},
		{state: "blocked"},
		{state: "pinned"},
		{state: "hooked"},
		{state: "closed"},
		{state: "closed", marker: "struck"},
	}
	for _, src := range focusTerminalSources {
		for _, ann := range []any{focusPeriod, "none", nil, "", "garbage"} {
			for i := range states {
				bead := states[i]
				o := focusSetup{fixture: src.fixture, annotation: ann, bead: &bead, source: src.source}.decide(t)
				if o.act == nil {
					continue
				}
				switch o.act.Op {
				case action.OpUpdate:
				default:
					t.Fatalf("%s/%v/%+v: planned op %q", src.name, ann, bead, o.act.Op)
				}
				if o.act.Kind != "focus-item" || o.act.Target == nil {
					t.Fatalf("%s/%v/%+v: %+v", src.name, ann, bead, *o.act)
				}
			}
			// And with no bead the only action is the one create.
			if o := (focusSetup{fixture: src.fixture, annotation: ann, source: src.source}).decide(t); o.act != nil && o.act.Op != action.OpCreate {
				t.Fatalf("%s/%v: planned op %q with no bead", src.name, ann, o.act.Op)
			}
		}
	}
}

// How a bead was closed is not read: done, wontfix and duplicate give
// identical plans (S26), selected or not, in every source state.
func TestFocusRuleIgnoresCloseReason(t *testing.T) {
	reasons := []map[string]string{
		nil,
		{"close_reason": "done"},
		{"close_reason": "wontfix"},
		{"close_reason": "duplicate"},
		{"resolution": "Won't Do", "closed_by": "someone"},
	}
	for _, src := range focusTerminalSources {
		for _, ann := range []any{focusPeriod, "none"} {
			var ref *focusOutcome
			for _, r := range reasons {
				o := focusSetup{
					fixture: src.fixture, annotation: ann, source: src.source,
					bead: &focusBead{state: "closed", extraMD: r},
				}.decide(t)
				if o.act != nil {
					t.Fatalf("%s/%v: a closed bead was written: %+v", src.name, ann, *o.act)
				}
				if ref == nil {
					ref = &o
				} else if !reflect.DeepEqual(o, *ref) {
					t.Errorf("%s/%v: close reason %v plans %+v, others plan %+v", src.name, ann, r, o, *ref)
				}
			}
		}
	}
}

// The decision is a function of the view alone: a re-route (a new version, a
// later as_of, a stale flag) of the same facts plans the same thing, however
// the item that triggered the run was routed.
func TestFocusDecideIsIndependentOfRoutedKind(t *testing.T) {
	for _, ann := range []any{focusPeriod, "none"} {
		for _, bead := range []*focusBead{nil, {state: "open"}, {state: "deferred", marker: "struck"}} {
			s := focusSetup{fixture: "pr_selected", annotation: ann, bead: bead}
			base := focusDecide(t, s.view(t), nil)
			m := s.mapped(t)
			m["version"], m["as_of"], m["stale"] = 99, "2027-01-01T00:00:00Z", true
			if got := focusDecide(t, focusParse(t, m), nil); !reflect.DeepEqual(got, base) {
				t.Errorf("%v/%+v: re-routed plan %+v, want %+v", ann, bead, got, base)
			}
		}
	}
}

// ---- hidden and suppressed ------------------------------------------------

func TestFocusHiddenAndSuppressedSourcesPlanNothing(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		for _, bead := range []*focusBead{nil, {state: "open"}, {state: "deferred", marker: "struck"}} {
			for _, ann := range []any{focusPeriod, "none"} {
				s := focusSetup{fixture: fixture, annotation: ann, bead: bead}

				hidden := s.mapped(t)
				suiteHide(hidden)
				o := focusDecide(t, focusParse(t, hidden), nil)
				if o.act != nil || o.skip.Reason != action.ReasonHidden {
					t.Errorf("%s/%v/%+v hidden: %+v, want a hidden skip", fixture, ann, bead, o)
				}

				supp := s.mapped(t)
				suiteSuppress("focus-item")(supp)
				o = focusDecide(t, focusParse(t, supp), nil)
				if o.act != nil || o.skip.Reason != action.ReasonSuppressed || o.skip.Facts["kind"] != "focus-item" {
					t.Errorf("%s/%v/%+v suppressed: %+v, want a suppressed skip of kind focus-item", fixture, ann, bead, o)
				}

				// Suppressing another kind does not stop the rule.
				other := s.mapped(t)
				suiteSuppress("fix-ci")(other)
				if o := focusDecide(t, focusParse(t, other), nil); o.skip != nil && o.skip.Reason == action.ReasonSuppressed {
					t.Errorf("%s/%v/%+v: suppressing fix-ci suppressed focus.item", fixture, ann, bead)
				}
			}
		}
	}
}

// A selected PR hidden after its bead exists, then struck, keeps the bead: the
// hidden precedence wins, so nothing holds it until the PR is un-hidden.
func TestFocusSelectedPRHiddenAfterBeadExistsThenStruckKeepsTheBead(t *testing.T) {
	bead := &focusBead{state: "open"}
	m := focusSetup{fixture: "pr_selected", annotation: focusPeriod, bead: bead}.mapped(t)
	suiteHide(m)
	if o := focusDecide(t, focusParse(t, m), nil); o.act != nil {
		t.Fatalf("selected and hidden: %+v", *o.act)
	}
	suiteAnnotations(m)["focus_selected"] = "none"
	o := focusDecide(t, focusParse(t, m), nil)
	if o.act != nil || o.skip.Reason != action.ReasonHidden {
		t.Fatalf("struck and hidden: %+v, want a hidden skip", o)
	}
	// Un-hidden, the strike takes effect.
	suiteAnnotations(m)["hidden"] = map[string]any{"value": false, "reason": nil}
	focusWant{transition: "hold", fields: focusHold}.check(t, focusDecide(t, focusParse(t, m), nil))
}

// ---- minting -------------------------------------------------------------

func TestFocusMintShapeForAnIssue(t *testing.T) {
	o := focusSetup{fixture: "issue_selected", annotation: focusPeriod}.decide(t)
	if o.act == nil {
		t.Fatalf("no create: %+v", *o.skip)
	}
	a := *o.act
	if a.Op != action.OpCreate || a.Kind != "focus-item" || a.Target != nil || a.Rule != "focus.item" {
		t.Errorf("action = %+v", a)
	}
	want := action.Fields{
		Title:       "Focus ACME-7 - Crash on startup",
		IssueType:   "bug",
		Description: "Focus item for issue ACME-7 (https://tracker.example/ACME-7)",
		Priority:    "P1",
		Labels:      []string{"focus-item"},
		Metadata: map[string]string{
			"source_type": "issue", "source_id": "ACME-7", "dedup_key": "issue:ACME-7:focus-item",
		},
	}
	if !reflect.DeepEqual(a.Fields, want) {
		t.Errorf("fields = %+v\nwant     %+v", a.Fields, want)
	}
	if a.Fields.Parent != "" {
		t.Errorf("a focus bead has no parent, got %q", a.Fields.Parent)
	}
	wantFacts := map[string]any{"transition": "mint", "source_type": "issue", "source_id": "ACME-7", "period": focusPeriod}
	if !reflect.DeepEqual(a.Facts, wantFacts) {
		t.Errorf("facts = %v, want %v", a.Facts, wantFacts)
	}
}

func TestFocusMintShapeForAPR(t *testing.T) {
	o := focusSetup{fixture: "pr_selected", annotation: focusPeriod}.decide(t)
	if o.act == nil {
		t.Fatalf("no create: %+v", *o.skip)
	}
	want := action.Fields{
		Title:       "Focus acme/widgets#42 - Add retry to client",
		IssueType:   "task",
		Description: "Focus item for pr acme/widgets#42",
		Priority:    "P2",
		Labels:      []string{"focus-item"},
		Metadata: map[string]string{
			"source_type": "pr", "source_id": "acme/widgets#42", "dedup_key": "pr:acme/widgets#42:focus-item",
		},
	}
	if !reflect.DeepEqual(o.act.Fields, want) {
		t.Errorf("fields = %+v\nwant     %+v", o.act.Fields, want)
	}
	// With a URL the description names it.
	withURL := focusSetup{fixture: "pr_selected", annotation: focusPeriod, source: func(s map[string]any) {
		s["url"] = "https://code.example/acme/widgets/pull/42"
	}}.decide(t)
	if got := withURL.act.Fields.Description; got != "Focus item for pr acme/widgets#42 (https://code.example/acme/widgets/pull/42)" {
		t.Errorf("description = %q", got)
	}
	// A PR with no repo or number is referred to by its id.
	noRef := focusSetup{fixture: "pr_selected", annotation: focusPeriod, source: func(s map[string]any) {
		delete(s, "repo")
		delete(s, "number")
	}}.decide(t)
	if got := noRef.act.Fields.Title; got != "Focus acme/widgets#42 - Add retry to client" {
		t.Errorf("title = %q", got)
	}
}

func TestFocusMintIssueTypeAndPriority(t *testing.T) {
	cfgMap := &config.Config{FocusPriorityMap: map[string]string{"Critical": "P0", "High": "P3"}}
	cases := []struct {
		name      string
		source    func(map[string]any)
		cfg       *config.Config
		wantType  string
		wantPrio  string
		wantTitle string
	}{
		{"bug", func(s map[string]any) { s["issue_type"] = "Bug" }, nil, "bug", "P1", ""},
		{"bug lower case", func(s map[string]any) { s["issue_type"] = "bug" }, nil, "bug", "P1", ""},
		{"story", func(s map[string]any) { s["issue_type"] = "Story" }, nil, "task", "P1", ""},
		{"epic", func(s map[string]any) { s["issue_type"] = "Epic" }, nil, "task", "P1", ""},
		{"no type", func(s map[string]any) { delete(s, "issue_type") }, nil, "task", "P1", ""},
		{"Highest", func(s map[string]any) { s["priority"] = "Highest" }, nil, "bug", "P0", ""},
		{"Medium", func(s map[string]any) { s["priority"] = "Medium" }, nil, "bug", "P2", ""},
		{"Low", func(s map[string]any) { s["priority"] = "Low" }, nil, "bug", "P3", ""},
		{"Lowest", func(s map[string]any) { s["priority"] = "Lowest" }, nil, "bug", "P4", ""},
		{"unmapped", func(s map[string]any) { s["priority"] = "Whenever" }, nil, "bug", "P2", ""},
		{"none", func(s map[string]any) { delete(s, "priority") }, nil, "bug", "P2", ""},
		{"configured map replaces the default", func(s map[string]any) { s["priority"] = "Critical" }, cfgMap, "bug", "P0", ""},
		{"configured map replaces the default whole", func(s map[string]any) { s["priority"] = "Highest" }, cfgMap, "bug", "P2", ""},
		{"configured High", func(s map[string]any) {}, cfgMap, "bug", "P3", ""},
		{"blank title", func(s map[string]any) { s["title"] = "  \n " }, nil, "bug", "P1", "Focus ACME-7"},
		{"multi-line title", func(s map[string]any) { s["title"] = "Crash\n\ton  startup\r\n" }, nil, "bug", "P1", "Focus ACME-7 - Crash on startup"},
		{"no title", func(s map[string]any) { delete(s, "title") }, nil, "bug", "P1", "Focus ACME-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := focusSetup{fixture: "issue_selected", annotation: focusPeriod, source: tc.source}.view(t)
			o := focusDecide(t, v, tc.cfg)
			if o.act == nil {
				t.Fatalf("no create: %+v", *o.skip)
			}
			if got := o.act.Fields.IssueType; got != tc.wantType {
				t.Errorf("issue type = %q, want %q", got, tc.wantType)
			}
			if got := o.act.Fields.Priority; got != tc.wantPrio {
				t.Errorf("priority = %q, want %q", got, tc.wantPrio)
			}
			want := tc.wantTitle
			if want == "" {
				want = "Focus ACME-7 - Crash on startup"
			}
			if got := o.act.Fields.Title; got != want {
				t.Errorf("title = %q, want %q", got, want)
			}
			if strings.ContainsAny(o.act.Fields.Title+o.act.Fields.Description, "\n\r") {
				t.Errorf("title and description are one line: %q %q", o.act.Fields.Title, o.act.Fields.Description)
			}
		})
	}
	// A PR has no priority: it is P2 whatever the map holds.
	pr := focusDecide(t, focusLoad(t, "pr_selected"), &config.Config{FocusPriorityMap: map[string]string{"": "P0"}})
	if pr.act.Fields.Priority != "P0" {
		// An empty priority name is looked up like any other; the default map has none.
		t.Errorf("a configured empty name is looked up: %q", pr.act.Fields.Priority)
	}
	if pr := focusDecide(t, focusLoad(t, "pr_selected"), nil); pr.act.Fields.Priority != "P2" {
		t.Errorf("PR priority = %q, want P2", pr.act.Fields.Priority)
	}
}

// The title never starts with "<ref>: ", which the anchor adoption match would
// read as the PR's own anchor bead, and never matches a PR work-item title.
func TestFocusTitleCannotBeAdoptedAsAnAnchorOrChild(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		o := focusSetup{fixture: fixture, annotation: focusPeriod}.decide(t)
		title := o.act.Fields.Title
		v := focusLoad(t, fixture)
		ref := v.ID
		if v.Type == "pr" {
			ref = fmt.Sprintf("%s#%d", v.Snapshot.Repo, v.Snapshot.Number)
		}
		if strings.HasPrefix(title, ref+": ") || !strings.HasPrefix(title, "Focus "+ref) {
			t.Errorf("%s: title %q", fixture, title)
		}
		for _, k := range []string{"review-pr", "fix-ci", "resolve-conflict", "process-feedback"} {
			if strings.HasPrefix(title, k+": ") {
				t.Errorf("%s: title %q reads as a %s item", fixture, title, k)
			}
		}
	}
}

// An epic or a plain bd task (its id matches bead_id_pattern) mints nothing;
// the pattern is unanchored and applies to issues only.
func TestFocusNoBeadForASourceWhoseIdIsABeadId(t *testing.T) {
	cases := []struct {
		pattern string
		mint    bool
	}{
		{"", true},
		{"^ACME-[0-9]+$", false},
		{"ACME", false}, // unanchored
		{"^wb-", true},
		{"^wb-|^ACME-", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			cfg := &config.Config{BeadIDPattern: tc.pattern}
			o := focusDecide(t, focusLoad(t, "issue_selected"), cfg)
			if (o.act != nil) != tc.mint {
				t.Fatalf("pattern %q: %+v, want mint=%v", tc.pattern, o, tc.mint)
			}
			if !tc.mint {
				if o.skip.Reason != action.ReasonNotMatched || focusCause(o) != "source-is-bead" {
					t.Errorf("skip = %+v", *o.skip)
				}
			}
		})
	}
	// A PR is never a bead, whatever the pattern: its id is not a bead id.
	if o := focusDecide(t, focusLoad(t, "pr_selected"), &config.Config{BeadIDPattern: "."}); o.act == nil {
		t.Errorf("a pattern matching every id must not stop a PR from being minted: %+v", o)
	}
	// An unusable pattern (a hand-built config) mints nothing rather than guess.
	if o := focusDecide(t, focusLoad(t, "issue_selected"), &config.Config{BeadIDPattern: "("}); o.act != nil || focusCause(o) != "bead-pattern-invalid" {
		t.Errorf("an invalid pattern: %+v", o)
	}
}

// A source with a bead is never minted again, however the pattern reads.
func TestFocusExistingBeadIsNotRecreatedEvenForAMatchingId(t *testing.T) {
	cfg := &config.Config{BeadIDPattern: "ACME"}
	v := focusSetup{fixture: "issue_selected", annotation: "none", bead: &focusBead{state: "open"}}.view(t)
	focusWant{transition: "hold", fields: focusHold}.check(t, focusDecide(t, v, cfg))
}

// An unmatched, unselected, unlinked source reports "not matched" and is quiet.
func TestFocusNoSelectionNoBeadIsNotMatched(t *testing.T) {
	for _, fixture := range []string{"pr_selected", "issue_selected"} {
		o := focusSetup{fixture: fixture, annotation: nil}.decide(t)
		focusWant{reason: action.ReasonNotMatched, cause: "not-selected"}.check(t, o)
	}
}

// ---- the keyed bead is found whichever way it is linked ---------------------

// A focus bead linked in the node_id key form, or one carrying other metadata,
// is still "the" bead: no second create.
func TestFocusFindsTheBeadByKindNotByState(t *testing.T) {
	m := focusSetup{fixture: "pr_selected", annotation: focusPeriod, bead: &focusBead{state: "closed"}}.mapped(t)
	if o := focusDecide(t, focusParse(t, m), nil); o.act != nil {
		t.Fatalf("a worker-closed bead must not be recreated: %+v", *o.act)
	}
	// An open and a closed bead both linked: the open one is the bead.
	links := m["links"].([]any)
	open := focusBead{state: "open"}.link("pr", "acme/widgets#42")
	open["id"] = "bd-focus-2"
	m["links"] = append(links, open)
	suiteAnnotations(m)["focus_selected"] = "none"
	o := focusDecide(t, focusParse(t, m), nil)
	if o.act == nil || *o.act.Target != "bd-focus-2" {
		t.Fatalf("the open bead is the one held: %+v", o)
	}
}
