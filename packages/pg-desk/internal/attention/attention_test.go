package attention

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const testRepo = "acme/api"

var fixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func testClock() Clock { return interpret.FixedClock(fixedNow) }

// prSpec describes one stored PR: its interpretation row and its facts.
type prSpec struct {
	number    int
	ownership string
	panel     string
	approvals interpret.Approvals
	match     []string
	degraded  bool

	state    string // default "open"
	draft    bool
	conflict bool
	thread   bool   // an unresolved review thread
	ci       string // "" (none), "failure", "pending", "success"
	noFacts  bool

	// branch and base are the PR's head and base branches (the stack source
	// of the dependency data reads them); empty omits the field.
	branch, base string
}

func (p prSpec) id() string { return fmt.Sprintf("%s#%d", testRepo, p.number) }

func (p prSpec) facts() string {
	state := p.state
	if state == "" {
		state = "open"
	}
	show := map[string]any{"number": p.number, "state": state, "draft": p.draft, "head_sha": "h1", "author": "me"}
	if p.branch != "" {
		show["branch"] = p.branch
	}
	if p.base != "" {
		show["base"] = p.base
	}
	if p.conflict {
		show["mergeable"] = "CONFLICTING"
	}
	if p.thread {
		show["comments"] = []map[string]any{{"id": "c1", "thread_id": "t1", "resolved": false}}
	}
	facts := map[string]any{"pr_show": show}
	switch p.ci {
	case "failure":
		facts["ci"] = map[string]any{"runs": []map[string]any{{"id": "1", "name": "build", "status": "completed", "conclusion": "failure", "head_sha": "h1", "attempt": 1}}}
	case "pending":
		facts["ci"] = map[string]any{"runs": []map[string]any{{"id": "1", "name": "build", "status": "in_progress", "head_sha": "h1", "attempt": 1}}}
	case "success":
		facts["ci"] = map[string]any{"runs": []map[string]any{{"id": "1", "name": "build", "status": "completed", "conclusion": "success", "head_sha": "h1", "attempt": 1}}}
	}
	b, _ := json.Marshal(facts)
	return string(b)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func put(t *testing.T, st *store.Store, p prSpec) {
	t.Helper()
	if !p.noFacts {
		if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "pr", EntityID: p.id(), Facts: p.facts(), AsOf: "2026-10-06T11:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: testRepo, EntityType: "pr", EntityID: p.id(),
		Ownership: p.ownership, Panel: p.panel, Degraded: p.degraded,
		Approvals: mustJSON(t, p.approvals), MatchReasons: mustJSON(t, p.match),
		Enrichment: "{}", Urgency: "{}", Dispositions: "[]",
		AsOf: "2026-10-06T11:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

func baseConfig() *config.Config {
	return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: testRepo}}}
}

func evaluate(t *testing.T, r Reader, cfg *config.Config) Result {
	t.Helper()
	res, err := Evaluate(Inputs{Store: r, Repo: testRepo, Config: cfg, Clock: testClock()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

func itemFor(res Result, number int) (Item, bool) {
	id := fmt.Sprintf("%s#%d", testRepo, number)
	for _, it := range res.Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

func mustItem(t *testing.T, res Result, number int) Item {
	t.Helper()
	it, ok := itemFor(res, number)
	if !ok {
		t.Fatalf("no item for PR %d; items: %+v", number, res.Items)
	}
	return it
}

func TestReviewRequestedRule(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe, match: []string{"review_requested"}})
	put(t, st, prSpec{number: 2, ownership: "team", panel: interpret.PanelTeamAwaitingTeam})
	put(t, st, prSpec{number: 3, ownership: "team", panel: interpret.PanelTeamAwaitingMe, degraded: true})

	res := evaluate(t, st, baseConfig())

	it := mustItem(t, res, 1)
	if it.Rule != KindReviewRequested || it.Severity != SeverityMedium || it.Type != "pr" {
		t.Errorf("item = %+v, want pr.review-requested at medium", it)
	}
	if _, ok := itemFor(res, 2); ok {
		t.Error("a PR not in team_awaiting_me must not raise")
	}
	if _, ok := itemFor(res, 3); ok {
		t.Error("a degraded interpretation must not raise (INV-ATTNEVAL-6)")
	}
	why := res.Traces[Ref("pr", pid(2))].NotRaised[KindReviewRequested]
	if !strings.Contains(why, "team_awaiting_me") {
		t.Errorf("explain reason for a non-applicable rule = %q, want it to name the panel", why)
	}
}

func TestOwnCIFailingRule(t *testing.T) {
	mine := func(n int, ci string, mod func(*prSpec)) prSpec {
		p := prSpec{number: n, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: ci}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	cases := []struct {
		name  string
		spec  prSpec
		cfg   func(*config.Config)
		fires bool
	}{
		{"failure raises", mine(1, "failure", nil), nil, true},
		{"failure raises even outside the panel", mine(2, "failure", func(p *prSpec) { p.panel = interpret.PanelMineAwaitingTeam }), nil, true},
		{"none rollup never raises", mine(3, "", nil), nil, false},
		{"pending never raises", mine(4, "pending", nil), nil, false},
		{"success never raises", mine(5, "success", nil), nil, false},
		{"draft never raises", mine(6, "failure", func(p *prSpec) { p.draft = true }), nil, false},
		{"closed never raises", mine(7, "failure", func(p *prSpec) { p.state = "closed" }), nil, false},
		{"team PR never raises", mine(8, "failure", func(p *prSpec) { p.ownership = "team" }), nil, false},
		{"co-owned PR raises", mine(9, "failure", func(p *prSpec) { p.ownership = "co-owned" }), nil, true},
		{"check_interpreters exclusion drops the failing run", mine(10, "failure", nil), func(c *config.Config) {
			c.CheckInterpreters = []config.CheckInterpreterConfig{{Type: "x", Patterns: []string{"^build$"}}}
		}, false},
		{"review_exempt_checks does not soften it", mine(11, "failure", nil), func(c *config.Config) {
			c.ReviewExemptChecks = []string{"build"}
		}, true},
		{"no stored facts never raises", mine(12, "failure", func(p *prSpec) { p.noFacts = true }), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			put(t, st, tc.spec)
			cfg := baseConfig()
			if tc.cfg != nil {
				tc.cfg(cfg)
			}
			tr := evaluate(t, st, cfg).Traces[Ref("pr", tc.spec.id())]
			raised := len(tr.Survived)+len(tr.Dropped) > 0
			var gotCI bool
			for _, c := range append(append([]Candidate(nil), tr.Survived...), droppedCandidates(tr)...) {
				if c.Kind == KindOwnCIFailing {
					gotCI = true
					if c.Severity != SeverityHigh {
						t.Errorf("severity = %s, want high", c.Severity)
					}
				}
			}
			if gotCI != tc.fires {
				t.Errorf("pr.own-ci-failing raised = %v, want %v (trace %+v, any=%v)", gotCI, tc.fires, tr, raised)
			}
		})
	}
}

func droppedCandidates(tr Trace) []Candidate {
	var out []Candidate
	for _, d := range tr.Dropped {
		out = append(out, d.Candidate)
	}
	return out
}

func TestOwnNeedsActionRule(t *testing.T) {
	mine := func(mod func(*prSpec)) prSpec {
		p := prSpec{number: 1, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success"}
		mod(&p)
		return p
	}
	cases := []struct {
		name     string
		spec     prSpec
		wantSev  Severity // "" means the rule must not raise
		wantText string
	}{
		{"human changes requested", mine(func(p *prSpec) { p.approvals.HumanChangesRequested = true }), SeverityMedium, "changes requested"},
		{"bot disapproval", mine(func(p *prSpec) { p.approvals.BotVerdict = interpret.BotVerdictDisapproved }), SeverityMedium, "bot disapproval"},
		{"merge conflict", mine(func(p *prSpec) { p.conflict = true }), SeverityMedium, "merge conflict"},
		{"unresolved thread", mine(func(p *prSpec) { p.thread = true }), SeverityMedium, "unresolved review thread"},
		{"approved and ready", mine(func(p *prSpec) { p.approvals.HumanApproved = true }), SeverityLow, "approved and ready"},
		{"approved with CI pending is still ready", mine(func(p *prSpec) { p.approvals.HumanApproved = true; p.ci = "pending" }), SeverityLow, "approved and ready"},
		{"CI failure alone is not this rule's", mine(func(p *prSpec) { p.ci = "failure" }), "", ""},
		{"a none rollup alone is not this rule's", mine(func(p *prSpec) { p.ci = "" }), "", ""},
		{"approved but CI failing is CI-only", mine(func(p *prSpec) { p.approvals.HumanApproved = true; p.ci = "failure" }), "", ""},
		{"CI failure plus a conflict raises for the conflict", mine(func(p *prSpec) { p.ci = "failure"; p.conflict = true }), SeverityMedium, "merge conflict"},
		{"not in the panel", mine(func(p *prSpec) { p.panel = interpret.PanelMineAwaitingTeam; p.conflict = true }), "", ""},
		{"team PR", mine(func(p *prSpec) { p.ownership = "team"; p.conflict = true }), "", ""},
		{"degraded interpretation", mine(func(p *prSpec) { p.degraded = true; p.conflict = true }), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			put(t, st, tc.spec)
			tr := evaluate(t, st, baseConfig()).Traces[Ref("pr", tc.spec.id())]
			var got *Candidate
			for i := range tr.Survived {
				if tr.Survived[i].Kind == KindOwnNeedsAction {
					got = &tr.Survived[i]
				}
			}
			if tc.wantSev == "" {
				if got != nil {
					t.Fatalf("raised %+v, want nothing", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("did not raise; trace %+v", tr)
			}
			if got.Severity != tc.wantSev || !strings.Contains(got.Reason, tc.wantText) {
				t.Errorf("candidate = %+v, want severity %s containing %q", *got, tc.wantSev, tc.wantText)
			}
		})
	}
}

func TestOneItemPerEntityAndPrimaryReason(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "failure", conflict: true})

	res := evaluate(t, st, baseConfig())
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v, want exactly one for one entity (INV-ATTNEVAL-3)", res.Items)
	}
	it := res.Items[0]
	if it.Rule != KindOwnCIFailing || it.Severity != SeverityHigh {
		t.Errorf("primary = %s/%s, want the most severe candidate pr.own-ci-failing/high", it.Rule, it.Severity)
	}
	if !strings.Contains(it.Summary, "(+1 more)") {
		t.Errorf("summary %q must say how many others the entity raised", it.Summary)
	}
	if it.Group != "pr:"+pid(1) {
		t.Errorf("group = %q, want the singleton group keyed by the entity ref", it.Group)
	}
}

func TestOrderingIsSeverityThenID(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 30, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 10, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 20, ownership: "mine", panel: interpret.PanelMineAwaitingTeam, ci: "failure"})
	put(t, st, prSpec{number: 5, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", approvals: interpret.Approvals{HumanApproved: true}})

	res := evaluate(t, st, baseConfig())
	var got []string
	for _, it := range res.Items {
		got = append(got, fmt.Sprintf("%s:%s", it.Severity, it.ID[strings.Index(it.ID, "#"):]))
	}
	want := []string{"high:#20", "medium:#10", "medium:#30", "low:#5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
	if len(res.Groups) != len(res.Items) {
		t.Errorf("groups = %d, want one singleton group per item (%d)", len(res.Groups), len(res.Items))
	}
}

func TestSuppressionChainOrderAndReasons(t *testing.T) {
	// One PR raising two rule kinds: ci-failing (high) and needs-action (medium, conflict).
	type annotate func(t *testing.T, st *store.Store, id string)
	setKV := func(key, value string) annotate {
		return func(t *testing.T, st *store.Store, id string) {
			t.Helper()
			if err := st.SetAnnotation(store.KVAnnotation{Repo: testRepo, EntityType: "pr", EntityID: id, Key: key, Value: value, Origin: "test", SetBy: "test", SetAt: "2026-10-06T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	own := prSpec{number: 1, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "failure", conflict: true}
	team := prSpec{number: 2, ownership: "team", panel: interpret.PanelTeamAwaitingMe}

	cases := []struct {
		name      string
		set       []annotate
		wantDrop  map[string]string // kind -> suppressor, for the own PR
		wantItem  bool              // does the own PR keep an item
		wantTeam  bool              // does the team PR keep an item
		wantRuleN string            // the surviving primary rule, if wantItem
	}{
		{"nothing", nil, nil, true, true, KindOwnCIFailing},
		{
			"hidden drops everything",
			[]annotate{setKV(store.AnnotationHidden, `{"value":true,"reason":null}`)},
			map[string]string{KindOwnCIFailing: "hidden", KindOwnNeedsAction: "hidden"},
			false, true, "",
		},
		{"hidden=false does not", []annotate{setKV(store.AnnotationHidden, `{"value":false,"reason":null}`)}, nil, true, true, KindOwnCIFailing},
		{
			"wip drops pr.own-* only",
			[]annotate{setKV(store.AnnotationWIP, "true")},
			map[string]string{KindOwnCIFailing: "wip", KindOwnNeedsAction: "wip"},
			false, true, "",
		},
		{
			"suppress.attention drops all kinds",
			[]annotate{setKV(store.KeySuppress("attention"), "true")},
			map[string]string{KindOwnCIFailing: "suppress.attention", KindOwnNeedsAction: "suppress.attention"},
			false, true, "",
		},
		{
			"suppress.<kind> drops only that kind",
			[]annotate{setKV(store.KeySuppress(KindOwnCIFailing), "true")},
			map[string]string{KindOwnCIFailing: "suppress." + KindOwnCIFailing},
			true, true, KindOwnNeedsAction,
		},
		{
			"hidden wins over suppress",
			[]annotate{setKV(store.KeySuppress("attention"), "true"), setKV(store.AnnotationHidden, `{"value":true}`)},
			map[string]string{KindOwnCIFailing: "hidden", KindOwnNeedsAction: "hidden"},
			false, true, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			put(t, st, own)
			put(t, st, team)
			for _, a := range tc.set {
				a(t, st, own.id())
			}
			res := evaluate(t, st, baseConfig())

			tr := res.Traces[Ref("pr", own.id())]
			gotDrop := map[string]string{}
			for _, d := range tr.Dropped {
				gotDrop[d.Candidate.Kind] = d.By
			}
			if fmt.Sprint(gotDrop) != fmt.Sprint(orEmpty(tc.wantDrop)) {
				t.Errorf("dropped = %v, want %v (INV-ATTNEVAL-4: the suppressor is retained)", gotDrop, orEmpty(tc.wantDrop))
			}
			it, ok := itemFor(res, 1)
			if ok != tc.wantItem {
				t.Fatalf("own PR has item = %v, want %v", ok, tc.wantItem)
			}
			if ok && it.Rule != tc.wantRuleN {
				t.Errorf("primary rule = %s, want %s", it.Rule, tc.wantRuleN)
			}
			if _, ok := itemFor(res, 2); ok != tc.wantTeam {
				t.Errorf("team PR has item = %v, want %v (a rule of another kind is independent)", ok, tc.wantTeam)
			}
			if res.Degraded {
				t.Error("a migrated store must not report degraded")
			}
		})
	}
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func TestUnmigratedStoreStillRunsAndHonorsOldColumns(t *testing.T) {
	st := store.OpenForTest(t) // version 1
	hide := true
	wip := true
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 2, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 3, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "failure"})
	put(t, st, prSpec{number: 4, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	for id, a := range map[string]store.Annotation{
		pid(2): {Hidden: &hide},
		pid(3): {WIP: &wip},
	} {
		a.Repo, a.EntityType, a.EntityID, a.SetBy, a.SetAt = testRepo, "pr", id, "test", "2026-10-06T00:00:00Z"
		if err := st.UpsertAnnotation(a); err != nil {
			t.Fatal(err)
		}
	}

	res := evaluate(t, st, baseConfig())

	if !res.Degraded {
		t.Error("Degraded must be true on an unmigrated store: suppress.* overrides are unavailable (INV-ATTNEVAL-5)")
	}
	if _, ok := itemFor(res, 1); !ok {
		t.Error("the evaluator must still run on an unmigrated store")
	}
	if _, ok := itemFor(res, 4); !ok {
		t.Error("an unannotated PR must still raise")
	}
	if _, ok := itemFor(res, 2); ok {
		t.Error("the old hidden column must be honored")
	}
	tr := res.Traces[Ref("pr", pid(3))]
	if len(tr.Dropped) == 0 || tr.Dropped[0].By != "wip" {
		t.Errorf("the old wip column must suppress pr.own-* rules, trace %+v", tr)
	}
}

func TestMissingFactsAndUnreadableStoreAreNeverAllClear(t *testing.T) {
	t.Run("rule with missing facts raises nothing", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		put(t, st, prSpec{number: 1, ownership: "mine", panel: interpret.PanelMineAwaitingMe, conflict: true, noFacts: true})
		res := evaluate(t, st, baseConfig())
		if len(res.Items) != 0 {
			t.Errorf("items = %+v, want none: facts are missing (INV-ATTNEVAL-6)", res.Items)
		}
	})
	t.Run("an unreadable store is an error", func(t *testing.T) {
		boom := errors.New("disk on fire")
		_, err := Evaluate(Inputs{Store: failingReader{err: boom}, Repo: testRepo, Config: baseConfig(), Clock: testClock()})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap the read error, never an empty result", err)
		}
	})
	t.Run("an annotation read error is an error", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
		boom := errors.New("annotation read failed")
		_, err := Evaluate(Inputs{Store: annotationFailingReader{Reader: st, err: boom}, Repo: testRepo, Config: baseConfig(), Clock: testClock()})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the annotation read error", err)
		}
	})
}

type failingReader struct{ err error }

func (f failingReader) RequireNewSchema() error { return nil }
func (f failingReader) ListInterpretations() ([]store.Interpretation, error) {
	return nil, f.err
}

func (f failingReader) ListEntities() ([]store.Entity, error) { return nil, f.err }

func (f failingReader) GetEntity(string, string, string) (store.Entity, bool, error) {
	return store.Entity{}, false, f.err
}

func (f failingReader) GetPRAnnotation(string, string, string) (store.Annotation, bool, error) {
	return store.Annotation{}, false, f.err
}

func (f failingReader) ListKVAnnotations(string, string, string) ([]store.KVAnnotation, error) {
	return nil, f.err
}

func (f failingReader) ListXrefLinksFrom(string, string, string) ([]store.XrefLink, error) {
	return nil, f.err
}

func (f failingReader) ListXrefLinksTo(string, string, string) ([]store.XrefLink, error) {
	return nil, f.err
}

func (f failingReader) ListXrefsByFrom(string, string, string, string) ([]store.Xref, error) {
	return nil, f.err
}

func (f failingReader) ListXrefsByTo(string, string, string) ([]store.Xref, error) {
	return nil, f.err
}

type annotationFailingReader struct {
	Reader
	err error
}

func (a annotationFailingReader) ListKVAnnotations(string, string, string) ([]store.KVAnnotation, error) {
	return nil, a.err
}

func TestEvaluateIsDeterministicWithAFixedClock(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	for n := 1; n <= 6; n++ {
		put(t, st, prSpec{number: n, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	}
	put(t, st, prSpec{number: 7, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "failure", conflict: true})

	marshal := func() string {
		res := evaluate(t, st, baseConfig())
		b, err := json.Marshal(res.Document())
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + fmt.Sprintf("%+v", res.Traces)
	}
	first := marshal()
	for i := 0; i < 5; i++ {
		if got := marshal(); got != first {
			t.Fatalf("output differs between runs (INV-ATTNEVAL-1):\n%s\n%s", first, got)
		}
	}
	if !strings.Contains(first, `"now":"2026-10-06T12:00:00Z"`) {
		t.Errorf("Now must come from the injected clock, got %s", first)
	}
}

func TestEvaluateDoesNotWrite(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	before, err := st.ListInterpretations()
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, st, baseConfig())
	after, err := st.ListInterpretations()
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Error("Evaluate changed the store")
	}
	if logs, err := st.ListKVAnnotations(testRepo, "pr", pid(1)); err != nil || len(logs) != 0 {
		t.Errorf("Evaluate wrote annotations: %v %v", logs, err)
	}
}

func TestInactiveEntitiesAreNotProjected(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	views, err := Project(inactiveReader{Reader: st}, testRepo, baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Errorf("views = %d, want an inactive entity skipped", len(views))
	}
}

type inactiveReader struct{ Reader }

func (i inactiveReader) GetEntity(repo, typ, id string) (store.Entity, bool, error) {
	e, ok, err := i.Reader.GetEntity(repo, typ, id)
	e.Inactive = true
	return e, ok, err
}

func TestRepoFilter(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	res, err := Evaluate(Inputs{Store: st, Repo: "other/repo", Config: baseConfig(), Clock: testClock()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 {
		t.Errorf("items = %+v, want rows of other repos ignored", res.Items)
	}
}

// ctxSuppressor claims only the sentinel entity, so registering it globally
// cannot affect any other test.
type ctxSuppressor struct{}

func (ctxSuppressor) Name() string { return "test-context" }
func (ctxSuppressor) Suppress(c Candidate, _ *SuppressEnv) (bool, error) {
	return strings.HasSuffix(c.ID, "#4242"), nil
}

func TestContextSuppressorIsLastInTheChain(t *testing.T) {
	RegisterSuppressor(ctxSuppressor{})
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 4242, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 4243, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	res := evaluate(t, st, baseConfig())
	if _, ok := itemFor(res, 4242); ok {
		t.Error("a registered context suppressor must be able to drop a candidate")
	}
	if _, ok := itemFor(res, 4243); !ok {
		t.Error("the suppressor must not touch other entities")
	}
	tr := res.Traces[Ref("pr", pid(4242))]
	if len(tr.Dropped) != 1 || tr.Dropped[0].By != "test-context" {
		t.Errorf("dropped = %+v, want the context suppressor named", tr.Dropped)
	}
	defer func() {
		if recover() == nil {
			t.Error("RegisterSuppressor must panic on a duplicate name")
		}
	}()
	RegisterSuppressor(ctxSuppressor{})
}

// pid is the entity id of PR number n.
func pid(n int) string { return prSpec{number: n}.id() }
