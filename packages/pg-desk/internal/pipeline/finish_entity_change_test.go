package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// finishInterp is a minimal valid interpretation row for the entity.
func finishInterp(id, asOf string) store.Interpretation {
	return store.Interpretation{
		Repo: ecRepo, EntityType: "issue", EntityID: id,
		Ownership: "mine", Enrichment: "{}", Urgency: "low", Category: "c",
		Dispositions: "[]", Approvals: "{}", GateState: "g", MatchReasons: "[]", Panel: "p", AsOf: asOf,
	}
}

func finishLink(id string) store.XrefLink {
	return store.XrefLink{
		Repo: ecRepo, FromType: "issue", FromID: id, ToType: "pr", ToID: ecRepo + "#7",
		Origin: "derived:test", Relation: "work", Evidence: "e",
		FirstSeen: "2026-09-16T00:00:00Z", LastConfirmed: "2026-09-16T00:00:00Z",
	}
}

// finishHash is the content_hash of the entity row finishPipeline seeds; a
// postCommit carries it so the interpretation write is not skipped.
const finishHash = "finish-hash"

func finishPipeline(t *testing.T) *Pipeline {
	t.Helper()
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	p.store.SeedEntityForTest(t, store.Entity{
		Repo: ecRepo, EntityType: "issue", EntityID: "i-1",
		Facts: "{}", AsOf: "2026-09-16T00:00:00Z", ContentHash: finishHash,
	})
	return p
}

// The pinned order is: local records, then the interpretation, then the
// derived links LAST. A full success leaves all three written.
func TestFinishEntityChange_SuccessWritesInterpretationThenLinks(t *testing.T) {
	p := finishPipeline(t)
	err := p.finishEntityChange(ecRepo, "issue", "i-1", postCommit{
		interp: finishInterp("i-1", "A"), contentHash: finishHash, haveLinks: true, newLinks: []store.XrefLink{finishLink("i-1")},
	})
	if err != nil {
		t.Fatalf("finishEntityChange: %v", err)
	}
	got, found, err := p.store.GetInterpretation(ecRepo, "issue", "i-1")
	if err != nil || !found || got.AsOf != "A" || got.Ownership != "mine" {
		t.Fatalf("interpretation = %+v found=%v err=%v", got, found, err)
	}
	links, err := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")
	if err != nil || len(links) != 1 || links[0].ToID != ecRepo+"#7" {
		t.Fatalf("links = %+v err=%v", links, err)
	}
}

// haveLinks=false leaves the stored derived links alone.
func TestFinishEntityChange_NoLinksFlagLeavesStoredLinksAlone(t *testing.T) {
	p := finishPipeline(t)
	if err := p.store.ReplaceDerivedXrefs(ecRepo, "issue", "i-1", []store.XrefLink{finishLink("i-1")}); err != nil {
		t.Fatal(err)
	}
	if err := p.finishEntityChange(ecRepo, "issue", "i-1", postCommit{interp: finishInterp("i-1", "A"), contentHash: finishHash}); err != nil {
		t.Fatal(err)
	}
	if links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); len(links) != 1 {
		t.Fatalf("links = %+v, want the stored link kept", links)
	}
}

// An interpretation write failure returns a wrapped error, and the derived
// links (the LAST step) are not replaced.
func TestFinishEntityChange_InterpretationWriteFailureSkipsTheLinks(t *testing.T) {
	p := finishPipeline(t)
	bad := finishInterp("i-1", "A")
	bad.SyncError = "boom" // refused on the new schema: a real store failure
	err := p.finishEntityChange(ecRepo, "issue", "i-1", postCommit{
		interp: bad, contentHash: finishHash, haveLinks: true, newLinks: []store.XrefLink{finishLink("i-1")},
	})
	if err == nil || !strings.Contains(err.Error(), "persist interpretation issue i-1") {
		t.Fatalf("err = %v, want a persist interpretation error", err)
	}
	if links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); len(links) != 0 {
		t.Fatalf("links = %+v, want none: the links step runs only after the interpretation is stored", links)
	}
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); found {
		t.Fatal("a failed interpretation write left a row")
	}
}

// A link-replacement failure is wrapped, and the interpretation row (written
// before it) stays: the order is pinned.
func TestFinishEntityChange_LinkFailureKeepsTheInterpretation(t *testing.T) {
	p := finishPipeline(t)
	wrong := finishLink("another-entity") // does not start from i-1: the store refuses it
	err := p.finishEntityChange(ecRepo, "issue", "i-1", postCommit{
		interp: finishInterp("i-1", "A"), contentHash: finishHash, haveLinks: true, newLinks: []store.XrefLink{wrong},
	})
	if err == nil || !strings.Contains(err.Error(), "links issue i-1") {
		t.Fatalf("err = %v, want a links error", err)
	}
	if got, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); !found || got.AsOf != "A" {
		t.Fatalf("interpretation = %+v found=%v, want it stored before the links step", got, found)
	}
}

// A local-records failure (the appendLocalRecords test seam) stops
// finishEntityChange before the interpretation and links are touched.
func TestFinishEntityChange_LocalRecordsFailureStopsTheLaterSteps(t *testing.T) {
	p := finishPipeline(t)
	boom := errors.New("injected local records failure")
	p.seams.appendLocalRecords = func() error { return boom }
	err := p.finishEntityChange(ecRepo, "issue", "i-1", postCommit{
		observed: true, interp: finishInterp("i-1", "A"), contentHash: finishHash, haveLinks: true, newLinks: []store.XrefLink{finishLink("i-1")},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); found {
		t.Fatal("the interpretation was written after a local-records failure")
	}
	if links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); len(links) != 0 {
		t.Fatalf("links = %+v, want none", links)
	}
}

// The beforeFinish seam sits in the gap after the entity commit: an error
// there leaves the committed entity but no interpretation, from either caller.
func TestFinishEntityChange_GapFailureLeavesTheCommittedEntityWithoutAnInterpretation(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	boom := errors.New("injected gap failure")
	p.seams.beforeFinish = func() error { return boom }

	_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("RunEntityChange err = %v, want the injected failure", err)
	}
	if e, found, _ := p.store.GetEntity(ecRepo, "issue", "i-1"); !found || e.Version == 0 {
		t.Fatalf("entity = %+v found=%v, want the commit to stand", e, found)
	}
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); found {
		t.Fatal("an interpretation was written despite the gap failure")
	}

	// The same seam is reached from the feed-row path.
	fp, _ := frPipeline(t)
	fp.seams.beforeFinish = func() error { return boom }
	if _, err := fp.IngestFeedRow(context.Background(), frRow(1, "i-1", "title_changed")); !errors.Is(err, boom) {
		t.Fatalf("IngestFeedRow err = %v, want the injected failure", err)
	}
}

// A failing local-records step in a real second hydration keeps the previous
// interpretation row and does not replace the derived links.
func TestRunEntityChange_LocalRecordsFailureKeepsThePreviousInterpretation(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	before, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1")
	if !found {
		t.Fatal("no interpretation after the first hydration")
	}

	boom := errors.New("injected local records failure")
	p.seams.appendLocalRecords = func() error { return boom }
	res = ecIssue(t, "open", "me", "2026-09-16T05:00:00Z", workMeta("8"))
	if _, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	after, _, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1")
	if after != before {
		t.Fatalf("interpretation changed after a failed finish: before=%+v after=%+v", before, after)
	}
	links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")
	for _, l := range links {
		if strings.HasSuffix(l.ToID, "#8") {
			t.Fatalf("links = %+v, want the old link set (the replacement runs last)", links)
		}
	}
}
