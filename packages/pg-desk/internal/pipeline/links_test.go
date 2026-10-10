package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const linkRepo = "acme/widgets"

func linkPipeline(t *testing.T, gs map[string]gather.EntityGatherer) *Pipeline {
	t.Helper()
	p := newGenericPipeline(t, nil, gs)
	p.cfg.TicketPatterns = []string{"PROJ-[0-9]+"}
	p.extractors = NewExtractorRegistry(p.cfg, p.store, p.repo())
	return p
}

func payloadOf(t *testing.T, v any) gather.GatherResult {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return gather.GatherResult{Payload: b, AsOf: "2026-09-16T00:00:00Z"}
}

func issueWith(t *testing.T, show string) gather.GatherResult {
	return payloadOf(t, gather.IssueFacts{IssueShow: json.RawMessage(show)})
}

func hydrate(t *testing.T, p *Pipeline, typ, id string, r gather.GatherResult) {
	t.Helper()
	p.entityGatherers = map[string]gather.EntityGatherer{typ: fakeEntityGatherer{result: r}}
	if err := p.RunGenericEntity(context.Background(), typ, id, gather.ChangeChanged); err != nil {
		t.Fatalf("RunGenericEntity %s %s: %v", typ, id, err)
	}
}

func linksFrom(t *testing.T, p *Pipeline, typ, id string) []store.XrefLink {
	t.Helper()
	ls, err := p.store.ListXrefLinksFrom(linkRepo, typ, id)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

type fakeExt struct{}

func (fakeExt) Name() string { return "fake" }
func (fakeExt) Extract(string, string, json.RawMessage) ([]store.XrefLink, error) {
	return []store.XrefLink{{ToType: "commit", ToID: "abc", Relation: "built"}}, nil
}

func TestRegistry_NewTypeFakeExtractor(t *testing.T) {
	p := linkPipeline(t, nil)
	p.extractors["widget"] = []LinkExtractor{fakeExt{}}
	// No interpreter exists for the fake type, so exercise the hook directly.
	if err := p.extractAndReplace("widget", "w1", json.RawMessage(`{}`), false, "2026-09-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	ls := linksFrom(t, p, "widget", "w1")
	if len(ls) != 1 || ls[0].ToType != "commit" || ls[0].Origin != "derived:fake" {
		t.Fatalf("links = %+v", ls)
	}
}

func TestWorkItemExtractor(t *testing.T) {
	p := linkPipeline(t, nil)
	hydrate(t, p, "issue", "i-1", issueWith(t,
		`{"id":"i-1","parent":"i-0","metadata":{"repo":"acme/widgets","pr_number":"5"},"title":"ignored"}`))
	ls := linksFrom(t, p, "issue", "i-1")
	if len(ls) != 2 {
		t.Fatalf("links = %+v", ls)
	}
	got := map[string]store.XrefLink{}
	for _, l := range ls {
		got[l.Relation] = l
		if l.Origin != "derived:work-item" {
			t.Errorf("origin = %q", l.Origin)
		}
	}
	if w := got["work"]; w.ToType != "pr" || w.ToID != "acme/widgets#5" {
		t.Errorf("work = %+v", w)
	}
	if pa := got["parent"]; pa.ToType != "issue" || pa.ToID != "i-0" {
		t.Errorf("parent = %+v", pa)
	}
}

// TestWorkItemExtractorJiraChildParent: a Jira child (bead pg2-upb9j: the Jira
// backend now maps pjira's parent key onto schema.Issue.Parent) carries no
// work-item metadata, so the extractor derives exactly one issue-to-issue
// "parent" link to its epic, with the same origin as a bd child's.
func TestWorkItemExtractorJiraChildParent(t *testing.T) {
	p := linkPipeline(t, nil)
	hydrate(t, p, "issue", "PROJ-2", issueWith(t,
		`{"id":"PROJ-2","title":"child","issue_type":"Story","tracker":"PROJ","parent":"PROJ-1"}`))
	ls := linksFrom(t, p, "issue", "PROJ-2")
	if len(ls) != 1 {
		t.Fatalf("links = %+v", ls)
	}
	if l := ls[0]; l.Relation != "parent" || l.ToType != "issue" || l.ToID != "PROJ-1" || l.Origin != "derived:work-item" {
		t.Errorf("parent link = %+v", l)
	}
}

func prResult(t *testing.T, branch, title, body string) gather.GatherResult {
	show, _ := json.Marshal(map[string]string{"branch": branch, "title": title, "body": body})
	return payloadOf(t, gather.Facts{PRShow: show, AsOf: "2026-09-16T00:00:00Z"})
}

func TestJiraKeyExtractor(t *testing.T) {
	p := linkPipeline(t, nil)
	hydrate(t, p, "pr", "acme/widgets#1", prResult(t, "feat/PROJ-1", "fix PROJ-2", "refs PROJ-3"))
	ls := linksFrom(t, p, "pr", "acme/widgets#1")
	var ids []string
	for _, l := range ls {
		if l.Origin == "derived:jira-key" && l.ToType == "issue" {
			ids = append(ids, l.ToID)
		}
	}
	if strings.Join(ids, ",") != "PROJ-1,PROJ-2,PROJ-3" {
		t.Fatalf("links = %+v", ls)
	}
}

func threadResult(t *testing.T, text string) gather.GatherResult {
	show, _ := json.Marshal(map[string]string{"text": text})
	return payloadOf(t, gather.ThreadFacts{ThreadShow: show})
}

func TestThreadExtractor(t *testing.T) {
	p := linkPipeline(t, nil)
	// PR 1 is linked to PROJ-1 through the Jira extractor; PROJ-9 has no PR.
	hydrate(t, p, "pr", "acme/widgets#1", prResult(t, "PROJ-1", "", ""))
	hydrate(t, p, "thread", "t1", threadResult(t,
		"see https://github.com/acme/widgets/pull/7 and PROJ-1 and PROJ-9"))
	ls := linksFrom(t, p, "thread", "t1")
	got := map[string]bool{}
	for _, l := range ls {
		if l.Origin != "derived:thread-refs" || l.ToType != "pr" {
			t.Errorf("link = %+v", l)
		}
		got[l.ToID] = true
	}
	if len(got) != 2 || !got["acme/widgets#7"] || !got["acme/widgets#1"] {
		t.Fatalf("links = %+v", ls)
	}
}

func TestRehydrationRebuildsSetKeepsExternal(t *testing.T) {
	p := linkPipeline(t, nil)
	hydrate(t, p, "pr", "acme/widgets#1", prResult(t, "PROJ-1", "PROJ-2", ""))
	if err := p.store.AddExternalXref(store.XrefLink{
		Repo: linkRepo, FromType: "pr", FromID: "acme/widgets#1", ToType: "issue", ToID: "EXT-1",
		Relation: "manual", Actor: "me", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, l := range linksFrom(t, p, "pr", "acme/widgets#1") {
		before[l.ToID] = l.FirstSeen
	}
	p.clock = interpret.FixedClock(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	hydrate(t, p, "pr", "acme/widgets#1", prResult(t, "PROJ-1", "", ""))
	after := map[string]store.XrefLink{}
	for _, l := range linksFrom(t, p, "pr", "acme/widgets#1") {
		after[l.ToID] = l
	}
	if _, ok := after["PROJ-2"]; ok {
		t.Error("vanished link PROJ-2 not removed")
	}
	if l, ok := after["PROJ-1"]; !ok || l.FirstSeen != before["PROJ-1"] || l.LastConfirmed == before["PROJ-1"] {
		t.Errorf("PROJ-1 = %+v (before first_seen %q)", l, before["PROJ-1"])
	}
	if l, ok := after["EXT-1"]; !ok || l.Origin != "external:me" {
		t.Errorf("external link lost: %+v", after)
	}
}

func TestRemovedPayloadClearsDerivedKeepsExternal(t *testing.T) {
	p := linkPipeline(t, nil)
	hydrate(t, p, "pr", "acme/widgets#1", prResult(t, "PROJ-1", "", ""))
	if err := p.store.AddExternalXref(store.XrefLink{
		Repo: linkRepo, FromType: "pr", FromID: "acme/widgets#1", ToType: "issue", ToID: "EXT-1",
		Relation: "manual", Actor: "me", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	hydrate(t, p, "pr", "acme/widgets#1", gather.GatherResult{RemovedState: "closed"})
	ls := linksFrom(t, p, "pr", "acme/widgets#1")
	if len(ls) != 1 || ls[0].Origin != "external:me" {
		t.Fatalf("links = %+v", ls)
	}
}

// TestThreadExtractorCountsLegacyPRToKeyRows pins that a Jira key in a
// thread resolves through a legacy-origin PR-to-key row (written by
// gather.go's scan) even when the derived Jira extractor never ran.
func TestThreadExtractorCountsLegacyPRToKeyRows(t *testing.T) {
	p := linkPipeline(t, nil)
	if err := p.store.UpsertXref(store.Xref{
		Repo: linkRepo, FromType: "pr", FromID: "acme/widgets#3", ToType: "issue", ToID: "PROJ-4",
		Evidence: "branch", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	hydrate(t, p, "thread", "t2", threadResult(t, "tracked in PROJ-4, also PROJ-8"))
	ls := linksFrom(t, p, "thread", "t2")
	if len(ls) != 1 || ls[0].ToType != "pr" || ls[0].ToID != "acme/widgets#3" || ls[0].Origin != "derived:thread-refs" {
		t.Fatalf("links = %+v", ls)
	}
}

// TestExtractionRunsWithoutDeciderAndWritesNoChangeLog pins that hydration
// extracts links with no decider/consumer registered for the type, and that
// extraction itself appends no change_log record.
func TestExtractionRunsWithoutDeciderAndWritesNoChangeLog(t *testing.T) {
	p := linkPipeline(t, nil)
	before, err := p.store.ListChangesAfter("pr", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.extractAndReplace("pr", "acme/widgets#1",
		prResult(t, "PROJ-1", "", "").Payload, false, "2026-09-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if ls := linksFrom(t, p, "pr", "acme/widgets#1"); len(ls) != 1 {
		t.Fatalf("links = %+v", ls)
	}
	after, err := p.store.ListChangesAfter("pr", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("change_log grew: before=%d after=%d", len(before), len(after))
	}
	hist, err := p.store.ListEntityHistory(linkRepo, "pr", "acme/widgets#1", 0)
	if err != nil || len(hist) != 0 {
		t.Fatalf("history = %+v err=%v", hist, err)
	}
}

// beadWithMeta is an issue payload whose metadata is the given JSON object.
func beadWithMeta(t *testing.T, id, title, meta string) gather.GatherResult {
	return issueWith(t, `{"id":"`+id+`","title":"`+title+`","metadata":`+meta+`}`)
}

func TestSourceEntityExtractorEmitsSourceOnly(t *testing.T) {
	for name, meta := range map[string]string{
		"source only":         `{"source_type":"pr","source_id":"acme/widgets#12"}`,
		"with repo+pr_number": `{"source_type":"pr","source_id":"acme/widgets#12","repo":"acme/widgets","pr_number":"12"}`,
		"with a different PR": `{"source_type":"pr","source_id":"acme/widgets#12","repo":"acme/other","pr_number":"99"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := linkPipeline(t, nil)
			hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "focus", meta))
			var source []store.XrefLink
			for _, l := range linksFrom(t, p, "issue", "bd-1") {
				if l.Origin == "derived:source-entity" {
					source = append(source, l)
				}
				if l.Origin == "derived:source-entity" && l.Relation != "source" {
					t.Errorf("source-entity link with relation %q", l.Relation)
				}
			}
			if len(source) != 1 {
				t.Fatalf("source-entity links = %+v", source)
			}
			if l := source[0]; l.ToType != "pr" || l.ToID != "acme/widgets#12" || l.Relation != "source" || l.Evidence != "metadata" {
				t.Errorf("link = %+v", l)
			}
		})
	}
}

func TestSourceEntityExtractorReadsNeitherRepoNorPRNumber(t *testing.T) {
	p := linkPipeline(t, nil)
	// repo+pr_number alone: the work-item extractor derives work; the source
	// extractor derives nothing.
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"repo":"acme/widgets","pr_number":"12"}`))
	for _, l := range linksFrom(t, p, "issue", "bd-1") {
		if l.Origin == "derived:source-entity" {
			t.Errorf("source link from repo+pr_number: %+v", l)
		}
	}
	// A source link never carries the work relation.
	hydrate(t, p, "issue", "bd-2", beadWithMeta(t, "bd-2", "x", `{"source_type":"pr","source_id":"acme/widgets#12"}`))
	for _, l := range linksFrom(t, p, "issue", "bd-2") {
		if l.Relation == "work" {
			t.Errorf("work link from a source-only bead: %+v", l)
		}
	}
}

func TestSourceEntityExtractorRegisteredTypes(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		p := linkPipeline(t, nil)
		hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"source_type":"`+typ+`","source_id":"S-1"}`))
		ls := linksFrom(t, p, "issue", "bd-1")
		if len(ls) != 1 || ls[0].ToType != typ || ls[0].ToID != "S-1" || ls[0].Origin != "derived:source-entity" {
			t.Errorf("type %s: links = %+v", typ, ls)
		}
	}
}

func TestSourceEntityExtractorYieldsNothingOtherwise(t *testing.T) {
	for name, meta := range map[string]string{
		"unknown type":      `{"source_type":"commit","source_id":"abc"}`,
		"empty type":        `{"source_type":"","source_id":"acme/widgets#12"}`,
		"empty id":          `{"source_type":"pr","source_id":""}`,
		"type without id":   `{"source_type":"pr"}`,
		"id without type":   `{"source_id":"acme/widgets#12"}`,
		"no metadata":       `{}`,
		"capitalised type":  `{"source_type":"PR","source_id":"acme/widgets#12"}`,
		"unrelated fields":  `{"focus":"x"}`,
		"whitespace-y type": `{"source_type":" pr","source_id":"acme/widgets#12"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ls, err := sourceEntityExtractor{}.Extract("issue", "bd-1",
				json.RawMessage(`{"issue_show":{"id":"bd-1","metadata":`+meta+`}}`))
			if err != nil || len(ls) != 0 {
				t.Fatalf("links = %+v, err = %v", ls, err)
			}
		})
	}
	// An issue payload without issue_show derives nothing and does not fail.
	if ls, err := (sourceEntityExtractor{}).Extract("issue", "bd-1", json.RawMessage(`{}`)); err != nil || len(ls) != 0 {
		t.Fatalf("empty payload: links = %+v, err = %v", ls, err)
	}
	if _, err := (sourceEntityExtractor{}).Extract("issue", "bd-1", json.RawMessage(`not json`)); err == nil {
		t.Fatal("malformed payload: want an error")
	}
}

func TestSourceEntityExtractorRebuildsAndDropsOnRehydrate(t *testing.T) {
	p := linkPipeline(t, nil)
	srcLinks := func() []store.XrefLink {
		var out []store.XrefLink
		for _, l := range linksFrom(t, p, "issue", "bd-1") {
			if l.Origin == "derived:source-entity" {
				out = append(out, l)
			}
		}
		return out
	}
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"source_type":"pr","source_id":"acme/widgets#12"}`))
	first := srcLinks()
	if len(first) != 1 {
		t.Fatalf("after first hydrate: %+v", first)
	}
	// Re-hydrate with the source moved: the link is rebuilt, the old one gone.
	p.clock = interpret.FixedClock(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"source_type":"thread","source_id":"T-9"}`))
	second := srcLinks()
	if len(second) != 1 || second[0].ToType != "thread" || second[0].ToID != "T-9" {
		t.Fatalf("after move: %+v", second)
	}
	// Re-hydrate unchanged keeps first_seen and refreshes last_confirmed.
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"source_type":"thread","source_id":"T-9"}`))
	third := srcLinks()
	if len(third) != 1 || third[0].FirstSeen != second[0].FirstSeen {
		t.Fatalf("after unchanged re-hydrate: %+v (was %+v)", third, second)
	}
	// Metadata removed: the link drops.
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{}`))
	if got := srcLinks(); len(got) != 0 {
		t.Fatalf("after metadata removed: %+v", got)
	}
	// A removed bead drops it too.
	hydrate(t, p, "issue", "bd-1", beadWithMeta(t, "bd-1", "x", `{"source_type":"pr","source_id":"acme/widgets#12"}`))
	hydrate(t, p, "issue", "bd-1", gather.GatherResult{RemovedState: "closed"})
	if got := srcLinks(); len(got) != 0 {
		t.Fatalf("after removal: %+v", got)
	}
}

// TestLinkedFocusBeadLeavesPRUnchanged: a bead whose source is a PR adds no
// work link and no ownership to that PR, even when the bead's title looks
// like a PR anchor title ("<repo>#<n>: ...", which adoption by title would
// otherwise recognize); the PR's own stored links and everything the
// pg-desk links verb reads for it are the same with and without the bead.
func TestLinkedFocusBeadLeavesPRUnchanged(t *testing.T) {
	const pr = "acme/widgets#12"
	p := linkPipeline(t, nil)
	hydrate(t, p, "pr", pr, prResult(t, "feat/PROJ-1", "PROJ-1 change", ""))
	deps := links.Deps{Store: p.store, Repo: linkRepo, Patterns: []string{"PROJ-[0-9]+"}}
	resolve := func() links.Result {
		r, err := links.Resolve(deps, []string{"pr:" + pr})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	beforeFrom := linksFrom(t, p, "pr", pr)
	beforeResolved := resolve()

	hydrate(t, p, "issue", "bd-focus", beadWithMeta(t, "bd-focus", pr+": review", `{"source_type":"pr","source_id":"`+pr+`"}`))

	if got := linksFrom(t, p, "pr", pr); !reflect.DeepEqual(got, beforeFrom) {
		t.Errorf("PR's own links changed:\n got %+v\nwant %+v", got, beforeFrom)
	}
	if got := resolve(); !reflect.DeepEqual(got, beforeResolved) {
		t.Errorf("links verb result for the PR changed:\n got %+v\nwant %+v", got, beforeResolved)
	}
	if classify.NewStoreWorkLookup(p.store, linkRepo)(pr, "bd-focus") {
		t.Error("a source-linked bead was recognized as the PR's own work item")
	}
	// The one new edge is the bead's own source link, and it is not a work link.
	var toPR []store.XrefLink
	toPR, err := p.store.ListXrefLinksTo(linkRepo, "pr", pr)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range toPR {
		if l.FromID == "bd-focus" && l.Relation != "source" {
			t.Errorf("edge from the bead into the PR: %+v", l)
		}
	}
}
