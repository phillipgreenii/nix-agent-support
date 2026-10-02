package links

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	testRepo     = "acme/api"
	testPR       = "acme/api#123"
	testTemplate = "https://tracker.example.invalid/browse/%s"
)

var testPatterns = []string{"ABC-[0-9]+"}

func deps(st *store.Store) Deps {
	return Deps{Store: st, Repo: testRepo, Patterns: testPatterns, IssueURLTemplate: testTemplate}
}

func putEntity(t *testing.T, st *store.Store, typ, id, asOf, facts string) {
	t.Helper()
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: typ, EntityID: id, Facts: facts, AsOf: asOf}); err != nil {
		t.Fatalf("seed entity %s %s: %v", typ, id, err)
	}
}

func putDerived(t *testing.T, st *store.Store, fromType, fromID, toType, toID, relation, extractor string) {
	t.Helper()
	err := st.ReplaceDerivedXrefs(testRepo, fromType, fromID, []store.XrefLink{{
		Repo: testRepo, FromType: fromType, FromID: fromID, ToType: toType, ToID: toID,
		Relation: relation, Origin: "derived:" + extractor, Evidence: "test",
		FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
	}})
	if err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

func resolve(t *testing.T, d Deps, refs ...string) Result {
	t.Helper()
	r, err := Resolve(d, refs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

func TestPRLinks_SelfIssueAndThread(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123"}}`)
	putEntity(t, st, "thread", "C1/100.1", "2026-10-01T09:00:00Z", `{"thread_show":{"permalink":"https://chat.example.invalid/archives/C1/p1001"}}`)
	putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")
	putDerived(t, st, "thread", "C1/100.1", "pr", testPR, "mentions", "thread-refs")

	r := resolve(t, deps(st), "pr:"+testPR)
	item := r.Items["pr:"+testPR]
	if !item.Known {
		t.Fatalf("known = false; item = %+v", item)
	}
	want := []Link{
		{Kind: KindPR, Relation: "self", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
		{Kind: KindIssue, Relation: "jira", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"},
		{Kind: KindThread, Relation: "mentions", Label: "Thread", URL: "https://chat.example.invalid/archives/C1/p1001"},
	}
	if !reflect.DeepEqual(item.Links, want) {
		t.Fatalf("links =\n%+v\nwant\n%+v", item.Links, want)
	}
	if r.SchemaVersion != 1 || r.Degraded {
		t.Errorf("schemaVersion=%d degraded=%v; want 1,false", r.SchemaVersion, r.Degraded)
	}
	// as_of is the oldest as_of among the known entities of the refs: only the PR here.
	if r.AsOf != "2026-10-01T10:00:00Z" {
		t.Errorf("as_of = %q", r.AsOf)
	}
}

// INV-LINKS-3: a link whose URL cannot be determined is omitted, never empty.
func TestIssueURLFallbackAndOmission(t *testing.T) {
	cases := []struct {
		name     string
		tmpl     string
		seed     func(t *testing.T, st *store.Store)
		wantURLs []string // issue-kind link URLs, in order
	}{
		{"template wins over snapshot", testTemplate, func(t *testing.T, st *store.Store) {
			putEntity(t, st, "issue", "ABC-42", "2026-10-01T10:00:00Z", `{"issue_show":{"url":"https://snap.example.invalid/ABC-42"}}`)
		}, []string{"https://tracker.example.invalid/browse/ABC-42"}},
		{"no template falls back to the issue entity snapshot", "", func(t *testing.T, st *store.Store) {
			putEntity(t, st, "issue", "ABC-42", "2026-10-01T10:00:00Z", `{"issue_show":{"url":"https://snap.example.invalid/ABC-42"}}`)
		}, []string{"https://snap.example.invalid/ABC-42"}},
		{"no template falls back to the PR's stored jira_issues snapshot", "", func(t *testing.T, st *store.Store) {
			putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{"jira_issues":{"ABC-42":{"id":"ABC-42","url":"https://snap2.example.invalid/ABC-42"}}}`)
		}, []string{"https://snap2.example.invalid/ABC-42"}},
		{"no template and no snapshot omits the link", "", func(t *testing.T, st *store.Store) {}, nil},
		{"a non-web snapshot URL is omitted", "", func(t *testing.T, st *store.Store) {
			putEntity(t, st, "issue", "ABC-42", "2026-10-01T10:00:00Z", `{"issue_show":{"url":"javascript:alert(1)"}}`)
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{}`)
			putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")
			c.seed(t, st)
			d := deps(st)
			d.IssueURLTemplate = c.tmpl
			var got []string
			for _, l := range resolve(t, d, "pr:"+testPR).Items["pr:"+testPR].Links {
				if l.URL == "" {
					t.Errorf("link with empty URL emitted: %+v", l)
				}
				if l.Kind == KindIssue {
					got = append(got, l.URL)
				}
			}
			if !reflect.DeepEqual(got, c.wantURLs) {
				t.Errorf("issue URLs = %v; want %v", got, c.wantURLs)
			}
		})
	}
}

// INV-LINKS-2: unknown refs (including unsupported types and malformed refs)
// are not errors.
func TestUnknownRefsAreKnownFalseWithEmptyLinks(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	r := resolve(t, deps(st), "pr:acme/api#999", "alert:grafana:abc", "garbage", "pr:")
	for _, ref := range []string{"pr:acme/api#999", "alert:grafana:abc", "garbage", "pr:"} {
		item, ok := r.Items[ref]
		if !ok {
			t.Fatalf("ref %q missing from items: %+v", ref, r.Items)
		}
		if item.Known || item.Links == nil || len(item.Links) != 0 {
			t.Errorf("%q = %+v; want known=false and a non-nil empty links", ref, item)
		}
	}
	if r.AsOf != "" {
		t.Errorf("as_of = %q with no known entity; want empty", r.AsOf)
	}
	b, _ := json.Marshal(r.Items["alert:grafana:abc"])
	if string(b) != `{"known":false,"links":[]}` {
		t.Errorf("wire shape = %s", b)
	}
}

func TestIssueLinks_BeadWorkParentAndJiraReverse(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123"}}`)
	putEntity(t, st, "issue", "bd-1", "2026-09-30T08:00:00Z", `{"issue_show":{"url":"https://beads.example.invalid/bd-1"}}`)
	putEntity(t, st, "issue", "bd-0", "2026-09-30T08:30:00Z", `{"issue_show":{"url":"https://beads.example.invalid/bd-0"}}`)
	putDerived(t, st, "issue", "bd-1", "pr", testPR, "work", "work-item")
	// A second source for the same bead's parent edge: ReplaceDerivedXrefs replaces
	// per (entity, extractor-set), so write both edges in one call.
	err := st.ReplaceDerivedXrefs(testRepo, "issue", "bd-1", []store.XrefLink{
		{Repo: testRepo, FromType: "issue", FromID: "bd-1", ToType: "pr", ToID: testPR, Relation: "work", Origin: "derived:work-item", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"},
		{Repo: testRepo, FromType: "issue", FromID: "bd-1", ToType: "issue", ToID: "bd-0", Relation: "parent", Origin: "derived:work-item", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")

	r := resolve(t, deps(st), "issue:bd-1", "issue:ABC-42")

	bead := r.Items["issue:bd-1"]
	wantBead := []Link{
		{Kind: KindIssue, Relation: "self", Label: "bd-1", URL: "https://beads.example.invalid/bd-1"},
		{Kind: KindPR, Relation: "work", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
		{Kind: KindIssue, Relation: "parent", Label: "bd-0", URL: "https://beads.example.invalid/bd-0"},
	}
	if !bead.Known || !reflect.DeepEqual(bead.Links, wantBead) {
		t.Fatalf("bead = %+v\nwant links %+v", bead, wantBead)
	}

	// A Jira key has no entity row but is known through the PR edge; its self link
	// comes from the template (the key matches ticket_patterns) and the PR is the
	// reverse jira edge.
	jira := r.Items["issue:ABC-42"]
	wantJira := []Link{
		{Kind: KindIssue, Relation: "self", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"},
		{Kind: KindPR, Relation: "jira", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
	}
	if !jira.Known || !reflect.DeepEqual(jira.Links, wantJira) {
		t.Fatalf("jira = %+v\nwant links %+v", jira, wantJira)
	}
	// A bead id never goes through the Jira URL template, even when the snapshot URL is absent.
	putEntity(t, st, "issue", "bd-9", "2026-09-30T08:00:00Z", `{"issue_show":{}}`)
	if got := resolve(t, deps(st), "issue:bd-9").Items["issue:bd-9"]; !got.Known || len(got.Links) != 0 {
		t.Errorf("bd-9 = %+v; want known with no links", got)
	}
	// as_of is the oldest among the refs' entity rows: bd-1 (the Jira key has none).
	if r.AsOf != "2026-09-30T08:00:00Z" {
		t.Errorf("as_of = %q", r.AsOf)
	}
}

func TestThreadLinks_SelfAndMentionedPRs(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123"}}`)
	putEntity(t, st, "thread", "C1/100.1", "2026-10-01T09:00:00Z", `{"thread_show":{"permalink":"https://chat.example.invalid/archives/C1/p1001"}}`)
	putDerived(t, st, "thread", "C1/100.1", "pr", testPR, "mentions", "thread-refs")

	got := resolve(t, deps(st), "thread:C1/100.1").Items["thread:C1/100.1"]
	want := []Link{
		{Kind: KindThread, Relation: "self", Label: "Thread", URL: "https://chat.example.invalid/archives/C1/p1001"},
		{Kind: KindPR, Relation: "mentions", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
	}
	if !got.Known || !reflect.DeepEqual(got.Links, want) {
		t.Fatalf("thread = %+v\nwant links %+v", got, want)
	}
}

// A migrated store still carries the legacy pr-to-issue rows next to the
// derived:jira-key ones; the same key MUST appear once.
func TestLegacyAndDerivedJiraEdgesDedup(t *testing.T) {
	st := store.OpenForTest(t)
	if err := st.UpsertXref(store.Xref{Repo: testRepo, FromType: "pr", FromID: testPR, ToType: "issue", ToID: "ABC-42", Evidence: "title", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{}`)
	if err := st.Cutover(); err != nil {
		t.Fatal(err)
	}
	putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")

	r := resolve(t, deps(st), "pr:"+testPR)
	var issues int
	for _, l := range r.Items["pr:"+testPR].Links {
		if l.Kind == KindIssue {
			issues++
		}
	}
	if issues != 1 {
		t.Errorf("issue links = %d; want 1: %+v", issues, r.Items["pr:"+testPR].Links)
	}
	if r.Degraded {
		t.Error("a migrated store must not report degraded")
	}
}

// On an unmigrated store the verb degrades to the legacy pr-to-issue rows and
// says so (design: links exist only on the migrated schema).
func TestUnmigratedStoreDegradesToLegacyJiraLinks(t *testing.T) {
	st := store.OpenForTest(t) // schema version 1
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123"}}`)
	if err := st.UpsertXref(store.Xref{Repo: testRepo, FromType: "pr", FromID: testPR, ToType: "issue", ToID: "ABC-42", Evidence: "title", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	r := resolve(t, deps(st), "pr:"+testPR, "issue:ABC-42")
	if !r.Degraded {
		t.Fatal("degraded = false on an unmigrated store")
	}
	wantPR := []Link{
		{Kind: KindPR, Relation: "self", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
		{Kind: KindIssue, Relation: "jira", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"},
	}
	if got := r.Items["pr:"+testPR]; !got.Known || !reflect.DeepEqual(got.Links, wantPR) {
		t.Fatalf("pr = %+v\nwant links %+v", got, wantPR)
	}
	wantIssue := []Link{
		{Kind: KindIssue, Relation: "self", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"},
		{Kind: KindPR, Relation: "jira", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
	}
	if got := r.Items["issue:ABC-42"]; !got.Known || !reflect.DeepEqual(got.Links, wantIssue) {
		t.Fatalf("issue = %+v\nwant links %+v", got, wantIssue)
	}
}

// The registry is one entry per type: only pr, issue and thread are registered.
func TestRegistryTypes(t *testing.T) {
	var got []string
	for k := range NewRegistry() {
		got = append(got, k)
	}
	if len(got) != 3 {
		t.Fatalf("registry types = %v; want exactly pr, issue, thread", got)
	}
	for _, k := range []string{"pr", "issue", "thread"} {
		if _, ok := NewRegistry()[k]; !ok {
			t.Errorf("registry lacks %q", k)
		}
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in      string
		typ, id string
		ok      bool
	}{
		{"pr:acme/api#1", "pr", "acme/api#1", true},
		{"alert:grafana:abc", "alert", "grafana:abc", true},
		{" issue:ABC-1 ", "issue", "ABC-1", true},
		{"nocolon", "", "", false},
		{":x", "", "", false},
		{"pr:", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		typ, id, ok := ParseRef(c.in)
		if typ != c.typ || id != c.id || ok != c.ok {
			t.Errorf("ParseRef(%q) = %q,%q,%v; want %q,%q,%v", c.in, typ, id, ok, c.typ, c.id, c.ok)
		}
	}
}

func TestUnmigratedStoreIsDegradedEvenWhenNoRefIsSupported(t *testing.T) {
	st := store.OpenForTest(t)
	r := resolve(t, deps(st), "alert:grafana:abc")
	if !r.Degraded || r.Items["alert:grafana:abc"].Known {
		t.Errorf("result = %+v; want degraded and unknown", r)
	}
}

// prFactsWithCI is a stored PR fact set: the PR snapshot (head "h") plus the
// `ci list` fan-out payload.
func prFactsWithCI(t *testing.T, headSHA string, runs ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"pr_show": map[string]any{"url": "https://scm.example.invalid/acme/api/pull/123", "head_sha": headSHA},
		"ci":      map[string]any{"runs": runs},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ciRun(name, conclusion, sha, id, runURL string) map[string]any {
	return map[string]any{"name": name, "status": "completed", "conclusion": conclusion, "head_sha": sha, "id": id, "attempt": 1, "url": runURL}
}

func buildKindLinks(r Result, ref string) []Link {
	var out []Link
	for _, l := range r.Items[ref].Links {
		if l.Kind == KindBuild {
			out = append(out, l)
		}
	}
	return out
}

// A failing run on the current head becomes one build link (relation ci) with
// its run URL and state, right after the PR's own link.
func TestPRLinks_BuildLinkPerFailingRunOnCurrentHead(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", prFactsWithCI(
		t, "h",
		ciRun("lint", "success", "h", "1", "https://ci.example.invalid/run/1"),
		ciRun("unit-tests", "failure", "h", "2", "https://ci.example.invalid/run/2"),
		ciRun("e2e", "cancelled", "h", "3", "https://ci.example.invalid/run/3"),
		map[string]any{"name": "slow", "status": "in_progress", "head_sha": "h", "id": "4", "attempt": 1, "url": "https://ci.example.invalid/run/4"},
	))
	putDerived(t, st, "pr", testPR, "issue", "ABC-42", "jira", "jira-key")

	links := resolve(t, deps(st), "pr:"+testPR).Items["pr:"+testPR].Links
	want := []Link{
		{Kind: KindPR, Relation: "self", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"},
		{Kind: KindBuild, Relation: "ci", Label: "unit-tests (failure)", URL: "https://ci.example.invalid/run/2", State: "failure"},
		{Kind: KindBuild, Relation: "ci", Label: "e2e (cancelled)", URL: "https://ci.example.invalid/run/3", State: "cancelled"},
		{Kind: KindIssue, Relation: "jira", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("links =\n%+v\nwant\n%+v", links, want)
	}
}

// No failing run -> no build link: green, pending-only, empty and absent CI
// facts, and failures that only exist on an older head.
func TestPRLinks_NoFailingRunsEmitsNoBuildLink(t *testing.T) {
	cases := []struct {
		name  string
		facts func(t *testing.T) string
	}{
		{"all green", func(t *testing.T) string {
			return prFactsWithCI(t, "h", ciRun("lint", "success", "h", "1", "https://ci.example.invalid/run/1"), ciRun("t", "skipped", "h", "2", "https://ci.example.invalid/run/2"))
		}},
		{"pending only", func(t *testing.T) string {
			return prFactsWithCI(t, "h", map[string]any{"name": "slow", "status": "queued", "head_sha": "h", "id": "4", "attempt": 1, "url": "https://ci.example.invalid/run/4"})
		}},
		{"no runs", func(t *testing.T) string { return prFactsWithCI(t, "h") }},
		{"no ci facts at all", func(t *testing.T) string {
			return `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123","head_sha":"h"}}`
		}},
		{"failure only on an older head", func(t *testing.T) string {
			return prFactsWithCI(t, "h", ciRun("unit", "failure", "old", "1", "https://ci.example.invalid/run/1"), ciRun("unit", "success", "h", "2", "https://ci.example.invalid/run/2"))
		}},
		{"failed run superseded by a green rerun on the head", func(t *testing.T) string {
			return prFactsWithCI(t, "h", ciRun("unit", "failure", "h", "1", "https://ci.example.invalid/run/1"), ciRun("unit", "success", "h", "2", "https://ci.example.invalid/run/2"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", c.facts(t))
			r := resolve(t, deps(st), "pr:"+testPR)
			if got := buildKindLinks(r, "pr:"+testPR); len(got) != 0 {
				t.Errorf("build links = %+v; want none", got)
			}
			if !r.Items["pr:"+testPR].Known || len(r.Items["pr:"+testPR].Links) != 1 {
				t.Errorf("item = %+v; want known with only the PR's own link", r.Items["pr:"+testPR])
			}
		})
	}
}

// INV-LINKS-4: a failing run whose name a check_interpreters pattern excludes
// is not a build link, exactly as the dashboard's CI rollup ignores it.
func TestPRLinks_BuildLinksHonorCheckInterpreterExclusions(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", prFactsWithCI(
		t, "h",
		ciRun("policy-bot: approvals", "failure", "h", "1", "https://ci.example.invalid/run/1"),
		ciRun("unit-tests", "failure", "h", "2", "https://ci.example.invalid/run/2"),
	))
	d := deps(st)
	d.CheckInterpreters = []config.CheckInterpreterConfig{{Patterns: []string{"^policy-bot"}, Type: "approval"}}
	got := buildKindLinks(resolve(t, d, "pr:"+testPR), "pr:"+testPR)
	want := []Link{{Kind: KindBuild, Relation: "ci", Label: "unit-tests (failure)", URL: "https://ci.example.invalid/run/2", State: "failure"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("build links = %+v; want %+v", got, want)
	}
	// Without the interpreter entry both fail, so the exclusion is what hid it.
	if n := len(buildKindLinks(resolve(t, deps(st), "pr:"+testPR), "pr:"+testPR)); n != 2 {
		t.Errorf("without exclusions got %d build links; want 2", n)
	}
}

// The menu agrees with the dashboard: the build links are non-empty exactly
// when the CI rollup (cirun-derived) says failure, with a matching head
// fallback to the stored entity head_sha when pr_show carries none.
func TestPRLinks_HeadFallsBackToEntityHeadSHA(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	facts, _ := json.Marshal(map[string]any{
		"pr_show": map[string]any{"url": "https://scm.example.invalid/acme/api/pull/123"},
		"ci": map[string]any{"runs": []map[string]any{
			ciRun("unit", "failure", "old", "1", "https://ci.example.invalid/run/1"),
			ciRun("unit", "success", "h", "2", "https://ci.example.invalid/run/2"),
		}},
	})
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: "pr", EntityID: testPR, Facts: string(facts), AsOf: "2026-10-01T10:00:00Z", HeadSHA: "h"}); err != nil {
		t.Fatal(err)
	}
	if got := buildKindLinks(resolve(t, deps(st), "pr:"+testPR), "pr:"+testPR); len(got) != 0 {
		t.Errorf("build links = %+v; want none (the failure is on an older head)", got)
	}
}

// INV-LINKS-3: a failing run without a usable URL is omitted, not emitted empty.
func TestPRLinks_FailingRunWithoutUsableURLIsOmitted(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putEntity(t, st, "pr", testPR, "2026-10-01T10:00:00Z", prFactsWithCI(
		t, "h",
		ciRun("no-url", "failure", "h", "1", ""),
		ciRun("bad-url", "failure", "h", "2", "javascript:alert(1)"),
		ciRun("good", "timed_out", "h", "3", "https://ci.example.invalid/run/3"),
	))
	got := buildKindLinks(resolve(t, deps(st), "pr:"+testPR), "pr:"+testPR)
	want := []Link{{Kind: KindBuild, Relation: "ci", Label: "good (timed_out)", URL: "https://ci.example.invalid/run/3", State: "timed_out"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("build links = %+v; want %+v", got, want)
	}
}
