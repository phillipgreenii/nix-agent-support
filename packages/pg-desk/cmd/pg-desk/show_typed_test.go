package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// viewFixture is a new-schema store seeded with synthetic entities: pr
// o/r#5, issue bd-1 (a work item of the PR) and thread C1/1.5 (referenced
// from the PR by an operator).
type viewFixture struct {
	t    *testing.T
	seed *store.Store
}

const (
	viewPRFacts     = `{"pr_show":{"id":"o/r#5","repo":"o/r","number":5,"title":"Add retry to client","state":"open","url":"https://code.example/o/r/pull/5","draft":false,"head_sha":"9f3c1e2aaaa","node_id":"N5","labels":["bug"]},"ci":{"runs":[{"id":"900","name":"build","status":"completed","conclusion":"failure","url":"https://ci.example/runs/900","head_sha":"9f3c1e2aaaa","attempt":2},{"id":"900","name":"build","status":"completed","conclusion":"success","url":"https://ci.example/runs/900","head_sha":"9f3c1e2aaaa","attempt":1},{"id":"901","name":"lint","status":"in_progress","conclusion":"","url":"https://ci.example/runs/901"},{"id":"800","name":"build","status":"completed","conclusion":"failure","url":"https://ci.example/runs/800","head_sha":"0ldhead0000","attempt":1}]},"head_sha":"9f3c1e2aaaa"}`
	viewIssueFacts  = `{"issue_show":{"id":"bd-1","title":"process feedback","state":"open","url":"https://tracker.example/bd-1","labels":["mine","fbsum:ab12"],"metadata":{"dedup_key":"pr:o/r#5:process-feedback:ab12"}}}`
	viewThreadFacts = `{"thread_show":{"id":"C1/1.5","permalink":"https://chat.example/archives/C1/p15","text":"look at this"}}`
)

func newViewFixture(t *testing.T) *viewFixture {
	t.Helper()
	open, seed := seedLinkStore(t)
	withOpenSeams(t, openTestConfig("o/r"), open)
	f := &viewFixture{t: t, seed: seed}
	f.entity("pr", "o/r#5", viewPRFacts, "2026-09-29T14:03:10Z", "9f3c1e2aaaa")
	f.entity("issue", "bd-1", viewIssueFacts, "2026-09-29T14:00:00Z", "")
	f.entity("thread", "C1/1.5", viewThreadFacts, "2026-09-29T13:00:00Z", "")
	if err := seed.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#5", Ownership: "mine", Category: "feature",
		Urgency: `{"level":"medium","score":1}`, Dispositions: `[{"comment_id":"c-881","verdict":"open"},{"comment_id":"c-882","verdict":"open"}]`,
		Approvals: `{}`, MatchReasons: `[]`, AsOf: "2026-09-29T14:03:10Z",
	}); err != nil {
		t.Fatal(err)
	}
	// The work item records the link; the PR's own view must show it too.
	if err := seed.ReplaceDerivedXrefs("o/r", "issue", "bd-1", []store.XrefLink{{
		Repo: "o/r", FromType: "issue", FromID: "bd-1", ToType: "pr", ToID: "o/r#5",
		Relation: "work", Origin: "derived:work-item", FirstSeen: "2026-09-29T14:00:00Z", LastConfirmed: "2026-09-29T14:01:02Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "thread", ToID: "C1/1.5", Relation: "references",
		FirstSeen: "2026-09-29T13:00:00Z", LastConfirmed: "2026-09-29T13:00:00Z",
		Actor: "operator", ActedAt: "2026-09-29T13:00:00Z", Reason: "same incident",
	}); err != nil {
		t.Fatal(err)
	}
	f.annotate("pr", "o/r#5", "hidden", `{"value":false,"reason":null}`)
	f.annotate("pr", "o/r#5", "wip", "false")
	f.annotate("pr", "o/r#5", "suppress.fix-ci", "true")
	f.annotate("pr", "o/r#5", "disposition.c-881", "will-fix")
	f.annotate("pr", "o/r#5", "decider.pr-decider.fail.review.head-advanced", "1")
	return f
}

func (f *viewFixture) entity(typ, id, facts, asOf, head string) {
	f.t.Helper()
	if _, err := f.seed.WriteEntityWithLog(store.Entity{Repo: "o/r", EntityType: typ, EntityID: id, Facts: facts, AsOf: asOf, HeadSHA: head},
		0, []string{"reconcile"}, "pg-connector", asOf); err != nil {
		f.t.Fatalf("seed %s %s: %v", typ, id, err)
	}
}

func (f *viewFixture) annotate(typ, id, key, value string) {
	f.t.Helper()
	if err := f.seed.SetAnnotation(store.KVAnnotation{
		Repo: "o/r", EntityType: typ, EntityID: id, Key: key, Value: value,
		Origin: "pg-desk", SetBy: "operator", SetAt: "2026-09-29T14:05:00Z",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func runTypedShowCmd(t *testing.T, entityType string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newTypedShowCmd(entityType)
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestTypedShowIsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		c, _, err := rootCmd.Find([]string{typ, "show"})
		if err != nil || c.Name() != "show" || c.Parent() != typeGroup(typ) {
			t.Errorf("%s show not registered: %v, %v", typ, c, err)
		}
		o, _, err := rootCmd.Find([]string{typ, "open"})
		if err != nil || o.Name() != "open" || o.Parent() != typeGroup(typ) {
			t.Errorf("%s open not registered: %v, %v", typ, o, err)
		}
	}
	// The old top-level verbs stay.
	for _, name := range []string{"show", "open"} {
		if c, _, err := rootCmd.Find([]string{name}); err != nil || c.Parent() != rootCmd {
			t.Errorf("top-level %s missing: %v, %v", name, c, err)
		}
	}
}

// assertGolden compares got with testdata/<name>; PG_DESK_UPDATE_GOLDEN=1
// rewrites it (run the formatter over the result afterwards).
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("PG_DESK_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Compared as decoded JSON: the formatter lays the golden's arrays out
	// differently from the command's indented output.
	var a, b any
	if err := json.Unmarshal(want, &a); err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	if err := json.Unmarshal([]byte(got), &b); err != nil {
		t.Fatalf("output for %s: %v", name, err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s drifted from the golden:\n got: %s\nwant: %s", name, got, want)
	}
}

func TestTypedShowJSONGolden(t *testing.T) {
	newViewFixture(t)
	for _, tc := range []struct{ typ, id, golden string }{
		{"pr", "5", "show_pr.golden.json"},
		{"issue", "bd-1", "show_issue.golden.json"},
		{"thread", "C1/1.5", "show_thread.golden.json"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			out, _, err := runTypedShowCmd(t, tc.typ, tc.id, "--json")
			if err != nil {
				t.Fatalf("show: %v", err)
			}
			assertGolden(t, tc.golden, out)
			var v map[string]any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatal(err)
			}
			if v["contract"] != "pg-desk.view/v1" || v["type"] != tc.typ {
				t.Errorf("contract/type = %v/%v", v["contract"], v["type"])
			}
		})
	}
}

func TestTypedShowLinkRecordedOnOneEntityAppearsOnBoth(t *testing.T) {
	newViewFixture(t)
	view := func(typ, id string) viewJSON {
		out, _, err := runTypedShowCmd(t, typ, id, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var v viewJSON
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	find := func(v viewJSON, typ, id string) *viewLink {
		for i := range v.Links {
			if v.Links[i].Type == typ && v.Links[i].ID == id {
				return &v.Links[i]
			}
		}
		return nil
	}
	// Recorded on the issue (derived, issue -> pr) ...
	onIssue := find(view("issue", "bd-1"), "pr", "o/r#5")
	// ... and visible from the PR as well, with the same origin.
	onPR := find(view("pr", "5"), "issue", "bd-1")
	for name, l := range map[string]*viewLink{"issue view": onIssue, "pr view": onPR} {
		if l == nil || l.Relation != "work" || len(l.Origins) != 1 || l.Origins[0].Origin != "derived:work-item" {
			t.Fatalf("%s link = %+v, want a work link with origin derived:work-item", name, l)
		}
	}
	if onPR.State != "open" || onPR.Assignee == nil || *onPR.Assignee != "" || onPR.Labels == nil || len(*onPR.Labels) != 2 ||
		(*onPR.Metadata)["dedup_key"] == "" || onPR.URL != "https://tracker.example/bd-1" {
		t.Errorf("pr view of the work item = %+v", onPR)
	}
	// An external claim carries its actor, time and reason.
	th := find(view("pr", "5"), "thread", "C1/1.5")
	if th == nil || len(th.Origins) != 1 || th.Origins[0] != (viewOrigin{Origin: "external:operator", At: "2026-09-29T13:00:00Z", Reason: "same incident"}) {
		t.Errorf("thread link = %+v", th)
	}
}

func TestTypedShowLinkToUnstoredEntityOmitsSnapshotFields(t *testing.T) {
	f := newViewFixture(t)
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "gone-9", Relation: "references",
		FirstSeen: "2026-09-29T13:00:00Z", LastConfirmed: "2026-09-29T13:00:00Z", Actor: "operator", ActedAt: "2026-09-29T13:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTypedShowCmd(t, "pr", "5", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	for _, l := range raw.Links {
		if l["id"] != "gone-9" {
			continue
		}
		for _, k := range []string{"state", "labels", "metadata", "assignee", "url"} {
			if _, has := l[k]; has {
				t.Errorf("unstored link carries %q: %v", k, l)
			}
		}
		return
	}
	t.Fatal("gone-9 link missing")
}

// links[].priority is the linked work item's stored priority (pg-decider's
// anchor.priority rule reads it from the raw link JSON). It is omitted when the
// snapshot carries none, for a PR or thread link, and for an unstored entity.
func TestTypedShowLinkPriority(t *testing.T) {
	f := newViewFixture(t)
	if err := f.seed.UpsertEntity(store.Entity{
		Repo: "o/r", EntityType: "issue", EntityID: "bd-1", AsOf: "2026-09-29T14:10:00Z",
		Facts: `{"issue_show":{"id":"bd-1","title":"process feedback","state":"open","priority":"P3","labels":[],"metadata":{}}}`,
	}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTypedShowCmd(t, "pr", "5", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range raw.Links {
		id, _ := l["id"].(string)
		seen[id] = true
		p, has := l["priority"]
		if id == "bd-1" {
			if p != "P3" {
				t.Errorf("work item link priority = %v, want P3: %v", p, l)
			}
		} else if has {
			t.Errorf("link %s carries priority %v, want none", id, p)
		}
	}
	if !seen["bd-1"] || !seen["C1/1.5"] {
		t.Fatalf("links = %v, want the work item and the thread", raw.Links)
	}
}

func TestTypedShowLinkWithoutStoredPriorityOmitsIt(t *testing.T) {
	newViewFixture(t)
	out, _, err := runTypedShowCmd(t, "pr", "5", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	for _, l := range raw.Links {
		if _, has := l["priority"]; has {
			t.Errorf("link %v carries priority though no snapshot has one", l)
		}
	}
}

func TestTypedShowNeverCarriesClosedBy(t *testing.T) {
	newViewFixture(t)
	for _, typ := range [][2]string{{"pr", "5"}, {"issue", "bd-1"}, {"thread", "C1/1.5"}} {
		out, _, err := runTypedShowCmd(t, typ[0], typ[1], "--json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "closed_by") {
			t.Errorf("%s view carries closed_by:\n%s", typ[0], out)
		}
	}
}

func TestTypedShowHumanOutput(t *testing.T) {
	newViewFixture(t)
	out, _, err := runTypedShowCmd(t, "pr", "o/r#5")
	if err != nil {
		t.Fatal(err)
	}
	want := "pr o/r#5  Add retry to client\n" +
		"mine  open  ready  head=9f3c1e2  ci=failure  as_of=2026-09-29T14:03:10Z (fresh)\n" +
		"annotations: hidden=no  wip=no  suppress=[fix-ci]\n" +
		"review: pending=unknown (pending-review state was not looked up; run show --refresh)  escalation=unknown (escalations were not looked up; run show --refresh)\n" +
		"links: bd-1 (work, open)  C1/1.5 (references)\n"
	if out != want {
		t.Errorf("human output:\n got: %q\nwant: %q", out, want)
	}
}

func TestTypedShowUnknownEntityFails(t *testing.T) {
	newViewFixture(t)
	if _, _, err := runTypedShowCmd(t, "issue", "nope"); err == nil || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
}

func TestTypedShowRefusesOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(path) })
	out, _, err := runTypedShowCmd(t, "issue", "bd-1")
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Fatalf("err = %v, want the old-schema refusal naming pg-desk migrate --cutover", err)
	}
	if out != "" || exitCodeFor(err) != 1 {
		t.Errorf("stdout %q, exit %d", out, exitCodeFor(err))
	}
}

// ---- --refresh with a fake pg-connector ----

func TestTypedShowRefreshHydratesEntityAndLinkedEntity(t *testing.T) {
	f := newRefreshFixture(t)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showThread("C1/1.5")
	for _, e := range [][3]string{{"issue", "bd-1", `{}`}, {"thread", "C1/1.5", `{}`}} {
		if _, err := f.seed.WriteEntityWithLog(store.Entity{Repo: "o/r", EntityType: e[0], EntityID: e[1], Facts: e[2], AsOf: "2026-09-01T00:00:00Z"},
			0, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "issue", FromID: "bd-1", ToType: "thread", ToID: "C1/1.5",
		Relation: "references", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z", Actor: "operator", ActedAt: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// A linked entity with no stored row is not hydrated.
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "issue", FromID: "bd-1", ToType: "issue", ToID: "unstored-2",
		Relation: "references", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z", Actor: "operator", ActedAt: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runTypedShowCmd(t, "issue", "bd-1", "--refresh", "--json")
	if err != nil {
		t.Fatalf("show --refresh: %v (stderr %q)", err, stderr)
	}
	var v viewJSON
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Stale || !strings.Contains(string(v.Snapshot), "title of bd-1") {
		t.Errorf("view after refresh: stale=%v snapshot=%s", v.Stale, v.Snapshot)
	}
	if got := f.entity("thread", "C1/1.5").Facts; !strings.Contains(got, "hello") {
		t.Errorf("linked thread was not hydrated: %s", got)
	}
	calls, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if strings.Contains(string(calls), "unstored-2") || !strings.Contains(string(calls), "issue show bd-1") || !strings.Contains(string(calls), "thread show C1/1.5") {
		t.Errorf("pg-connector calls:\n%s", calls)
	}
}

func TestTypedShowRefreshLinkedFailureDoesNotFailShow(t *testing.T) {
	f := newRefreshFixture(t)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z") // the thread has no fixture: its show fails
	for _, e := range [][2]string{{"issue", "bd-1"}, {"thread", "C1/1.5"}} {
		if _, err := f.seed.WriteEntityWithLog(store.Entity{Repo: "o/r", EntityType: e[0], EntityID: e[1], Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"},
			0, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "issue", FromID: "bd-1", ToType: "thread", ToID: "C1/1.5",
		Relation: "references", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z", Actor: "operator", ActedAt: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runTypedShowCmd(t, "issue", "bd-1", "--refresh", "--json")
	if err != nil {
		t.Fatalf("a failed linked refresh failed the show: %v", err)
	}
	if !strings.Contains(stderr, "refresh linked thread C1/1.5") || !strings.Contains(out, `"stale": false`) {
		t.Errorf("stderr %q, stdout %s", stderr, out)
	}
}

func TestTypedShowRefreshPrimaryFailurePrintsStaleViewThenExitsThree(t *testing.T) {
	f := newRefreshFixture(t)
	if _, err := f.seed.WriteEntityWithLog(store.Entity{
		Repo: "o/r", EntityType: "issue", EntityID: "bd-1",
		Facts: `{"issue_show":{"id":"bd-1","title":"old title","state":"open"}}`, AsOf: "2026-09-01T00:00:00Z",
	},
		0, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTypedShowCmd(t, "issue", "bd-1", "--refresh", "--json") // no fixture for bd-1: the show call fails
	if exitCodeFor(err) != exitTotal {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitTotal)
	}
	var v viewJSON
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("stored view not printed: %v\n%q", jerr, out)
	}
	if !v.Stale || !strings.Contains(string(v.Snapshot), "old title") {
		t.Errorf("stale=%v snapshot=%s, want the stored view marked stale", v.Stale, v.Snapshot)
	}
}

func TestTypedShowRefreshDegradedExitsTwoWithStaleView(t *testing.T) {
	f := newRefreshFixture(t)
	if _, err := f.seed.WriteEntityWithLog(store.Entity{
		Repo: "o/r", EntityType: "issue", EntityID: "bd-1",
		Facts: `{"issue_show":{"id":"bd-1","title":"old title","state":"open"}}`, AsOf: "2026-09-01T00:00:00Z",
	},
		0, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	withFakeHydrator(t, &fakeHydrator{res: pipeline.EntityChangeResult{Degraded: "ci list"}})
	out, _, err := runTypedShowCmd(t, "issue", "bd-1", "--refresh", "--json")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitPartial)
	}
	if !strings.Contains(out, `"stale": true`) {
		t.Errorf("view not marked stale:\n%s", out)
	}
}

// ---- ci section (additive to pg-desk.view/v1) ----

// ciSection decodes the view's ci member, distinguishing absent from null.
type ciSection struct {
	CI *struct {
		Runs *[]map[string]any `json:"runs"`
	} `json:"ci"`
}

func showCI(t *testing.T, typ, id string) (ciSection, []string) {
	t.Helper()
	out, _, err := runTypedShowCmd(t, typ, id, "--json")
	if err != nil {
		t.Fatalf("show %s %s: %v", typ, id, err)
	}
	var c ciSection
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	// Top-level member order, to pin that ci comes after the existing members.
	dec := json.NewDecoder(strings.NewReader(out))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return c, keys
}

func TestTypedShowPRCarriesHeadCommitCIRunsAfterExistingMembers(t *testing.T) {
	newViewFixture(t)
	c, keys := showCI(t, "pr", "5")
	if c.CI == nil || c.CI.Runs == nil {
		t.Fatalf("ci = %+v, want an object with a runs array", c.CI)
	}
	want := []map[string]any{
		{"id": "900", "attempt": float64(2), "name": "build", "status": "completed", "conclusion": "failure", "url": "https://ci.example/runs/900"},
		{"id": "900", "attempt": float64(1), "name": "build", "status": "completed", "conclusion": "success", "url": "https://ci.example/runs/900"},
		// No recorded attempt: 0. No recorded head_sha: included.
		{"id": "901", "attempt": float64(0), "name": "lint", "status": "in_progress", "conclusion": "", "url": "https://ci.example/runs/901"},
	}
	if !reflect.DeepEqual(*c.CI.Runs, want) {
		t.Errorf("ci.runs = %v\nwant     %v", *c.CI.Runs, want)
	}
	if last := keys[len(keys)-1]; last != "ci" || keys[len(keys)-2] != "links_as_of" {
		t.Errorf("top-level members = %v, want ci after links_as_of", keys)
	}
}

func TestTypedShowPRWithoutCIFactsHasEmptyRunsArray(t *testing.T) {
	f := newViewFixture(t)
	f.entity("pr", "o/r#6", `{"pr_show":{"id":"o/r#6","repo":"o/r","number":6,"title":"No ci","state":"open","head_sha":"abc1234"}}`, "2026-09-29T14:03:10Z", "abc1234")
	f.entity("pr", "o/r#7", `{"pr_show":{"id":"o/r#7","repo":"o/r","number":7,"title":"Null runs","state":"open","head_sha":"abc1234"},"ci":{"runs":null}}`, "2026-09-29T14:03:10Z", "abc1234")
	for _, id := range []string{"6", "7"} {
		out, _, err := runTypedShowCmd(t, "pr", id, "--json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"ci": {
    "runs": []
  }`) {
			t.Errorf("pr %s: ci is not present with an empty runs array:\n%s", id, out)
		}
	}
}

func TestTypedShowCIExcludesRunsOfAnotherHead(t *testing.T) {
	f := newViewFixture(t)
	f.entity("pr", "o/r#8", `{"pr_show":{"id":"o/r#8","repo":"o/r","number":8,"title":"Moved on","state":"open","head_sha":"newhead"},"ci":{"runs":[{"id":"1","name":"a","status":"completed","conclusion":"failure","url":"u1","head_sha":"oldhead","attempt":1},{"id":"2","name":"b","status":"completed","conclusion":"failure","url":"u2","head_sha":"newhead","attempt":1},{"id":"3","name":"c","status":"completed","conclusion":"success","url":"u3"}]}}`, "2026-09-29T14:03:10Z", "newhead")
	c, _ := showCI(t, "pr", "8")
	if c.CI == nil || c.CI.Runs == nil || len(*c.CI.Runs) != 2 || (*c.CI.Runs)[0]["id"] != "2" || (*c.CI.Runs)[1]["id"] != "3" {
		t.Errorf("ci.runs = %+v, want only runs 2 (head matches) and 3 (no head_sha)", c.CI)
	}
}

func TestTypedShowIssueAndThreadCarryNoCI(t *testing.T) {
	newViewFixture(t)
	for _, tc := range [][2]string{{"issue", "bd-1"}, {"thread", "C1/1.5"}} {
		out, _, err := runTypedShowCmd(t, tc[0], tc[1], "--json")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["ci"]; has {
			t.Errorf("%s view carries a ci member", tc[0])
		}
	}
}

func TestTypedShowCIHasNoPrecomputedVerdict(t *testing.T) {
	newViewFixture(t)
	out, _, err := runTypedShowCmd(t, "pr", "5", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"failing_builds", "failing"} {
		if strings.Contains(out, banned) {
			t.Errorf("view carries %q:\n%s", banned, out)
		}
	}
}

// annotationsOf runs `show --json` and returns the raw annotations object.
func annotationsOf(t *testing.T, typ, id string) map[string]any {
	t.Helper()
	out, _, err := runTypedShowCmd(t, typ, id, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Contract    string         `json:"contract"`
		Annotations map[string]any `json:"annotations"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Contract != "pg-desk.view/v1" {
		t.Errorf("contract = %q, want pg-desk.view/v1", raw.Contract)
	}
	return raw.Annotations
}

func TestTypedShowForceReviewSHAUnsetIsNull(t *testing.T) {
	newViewFixture(t)
	a := annotationsOf(t, "pr", "5")
	if got, has := a["force_review"]; !has || got != false {
		t.Errorf("force_review = %v (present %v), want false", got, has)
	}
	if got, has := a["force_review_sha"]; !has || got != nil {
		t.Errorf("force_review_sha = %v (present %v), want an explicit null", got, has)
	}
}

func TestTypedShowForceReviewSHAReportsTheRequestedHead(t *testing.T) {
	f := newViewFixture(t)
	f.annotate("pr", "o/r#5", "force_review", "9f3c1e2aaaa")
	a := annotationsOf(t, "pr", "5")
	if a["force_review"] != true || a["force_review_sha"] != "9f3c1e2aaaa" {
		t.Errorf("annotations = force_review %v, force_review_sha %v; want true, 9f3c1e2aaaa", a["force_review"], a["force_review_sha"])
	}
}

func TestTypedShowForceReviewSetClearSetShowsTheNewSHA(t *testing.T) {
	f := newViewFixture(t)
	f.annotate("pr", "o/r#5", "force_review", "9f3c1e2aaaa")
	if _, err := f.seed.DeleteAnnotation("o/r", "pr", "o/r#5", "force_review", "pr-decider", "2026-09-29T15:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if a := annotationsOf(t, "pr", "5"); a["force_review"] != false || a["force_review_sha"] != nil {
		t.Errorf("after clear: force_review %v, force_review_sha %v; want false, null", a["force_review"], a["force_review_sha"])
	}
	f.annotate("pr", "o/r#5", "force_review", "bbbb2222")
	if a := annotationsOf(t, "pr", "5"); a["force_review"] != true || a["force_review_sha"] != "bbbb2222" {
		t.Errorf("after re-set: force_review %v, force_review_sha %v; want true, bbbb2222", a["force_review"], a["force_review_sha"])
	}
}

// linksByID runs `show --json` and returns the raw links[] objects by id.
func linksByID(t *testing.T, typ, id string) map[string]map[string]any {
	t.Helper()
	out, _, err := runTypedShowCmd(t, typ, id, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	m := map[string]map[string]any{}
	for _, l := range raw.Links {
		m[l["id"].(string)] = l
	}
	return m
}

func TestTypedShowLinkTitleIsTheStoredTitleOrEmpty(t *testing.T) {
	f := newViewFixture(t)
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "gone-9", Relation: "references",
		FirstSeen: "2026-09-29T13:00:00Z", LastConfirmed: "2026-09-29T13:00:00Z", Actor: "operator", ActedAt: "2026-09-29T13:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// A stored issue whose snapshot carries no title.
	f.entity("issue", "bd-2", `{"issue_show":{"id":"bd-2","state":"open"}}`, "2026-09-29T14:00:00Z", "")
	if err := f.seed.AddExternalXref(store.XrefLink{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "bd-2", Relation: "references",
		FirstSeen: "2026-09-29T13:00:00Z", LastConfirmed: "2026-09-29T13:00:00Z", Actor: "operator", ActedAt: "2026-09-29T13:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	links := linksByID(t, "pr", "5")
	for id, want := range map[string]string{"bd-1": "process feedback", "bd-2": "", "gone-9": "", "C1/1.5": ""} {
		l, ok := links[id]
		if !ok {
			t.Fatalf("link %s missing: %v", id, links)
		}
		if got, has := l["title"]; !has || got != want {
			t.Errorf("link %s title = %v (present %v), want %q", id, got, has, want)
		}
	}
	// The PR end of the same link carries the PR's stored title.
	if got := linksByID(t, "issue", "bd-1")["o/r#5"]["title"]; got != "Add retry to client" {
		t.Errorf("pr link title = %v, want the stored PR title", got)
	}
}

func TestTypedShowReadyToLandIsTrueFalseOrNull(t *testing.T) {
	f := newViewFixture(t)
	a := annotationsOf(t, "pr", "5")
	if got, has := a["ready_to_land"]; !has || got != nil {
		t.Errorf("unset: ready_to_land = %v (present %v), want an explicit null", got, has)
	}
	f.annotate("pr", "o/r#5", "ready_to_land", "true")
	if got := annotationsOf(t, "pr", "5")["ready_to_land"]; got != true {
		t.Errorf("after true: ready_to_land = %v, want JSON true", got)
	}
	f.annotate("pr", "o/r#5", "ready_to_land", "false")
	if got := annotationsOf(t, "pr", "5")["ready_to_land"]; got != false {
		t.Errorf("after false: ready_to_land = %v, want JSON false", got)
	}
}
