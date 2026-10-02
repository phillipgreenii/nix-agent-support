package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const ecRepo = "acme/widgets"

// funcEntityGatherer is a gatherer whose result can change between calls.
type funcEntityGatherer func(change gather.ChangeKind) (gather.GatherResult, error)

func (f funcEntityGatherer) GatherEntity(_ context.Context, _ string, change gather.ChangeKind) (gather.GatherResult, error) {
	return f(change)
}

// ecIssue builds an issue gather result. metadata may be nil.
func ecIssue(t *testing.T, state, assignee, asOf string, metadata map[string]string) gather.GatherResult {
	t.Helper()
	show := map[string]any{"id": "i-1", "owner": "me", "issue_type": "task", "state": state, "assignee": assignee}
	if metadata != nil {
		show["metadata"] = metadata
	}
	showJSON, err := json.Marshal(show)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(gather.IssueFacts{IssueShow: showJSON})
	if err != nil {
		t.Fatal(err)
	}
	return gather.GatherResult{Payload: payload, AsOf: asOf}
}

func workMeta(n string) map[string]string {
	return map[string]string{"repo": ecRepo, "pr_number": n}
}

// ecPipeline is a pipeline over a fresh new-schema store whose "issue"
// gatherer returns whatever *res / *gerr currently hold.
func ecPipeline(t *testing.T, res *gather.GatherResult, gerr *error) *Pipeline {
	t.Helper()
	return newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"issue": funcEntityGatherer(func(gather.ChangeKind) (gather.GatherResult, error) {
			if *gerr != nil {
				return gather.GatherResult{}, *gerr
			}
			return *res, nil
		}),
	})
}

func (p *Pipeline) changeRows(t *testing.T, entityType, entityID string) []store.ChangeRecord {
	t.Helper()
	all, err := p.store.ListChangesAfter(entityType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.ChangeRecord
	for _, r := range all {
		if r.EntityID == entityID {
			out = append(out, r)
		}
	}
	return out
}

func mustRun(t *testing.T, p *Pipeline, typ, id string, change gather.ChangeKind, opts EntityChangeOptions) EntityChangeResult {
	t.Helper()
	r, err := p.RunEntityChange(context.Background(), typ, id, change, opts)
	if err != nil {
		t.Fatalf("RunEntityChange: %v", err)
	}
	return r
}

func TestRunEntityChange_FirstObservationIsReconcileOnly(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)

	r := mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{Origin: "sweep"})
	if !r.Written || r.Version != 1 || r.Retries != 0 || !reflect.DeepEqual(r.Kinds, []string{"reconcile"}) {
		t.Fatalf("result = %+v", r)
	}
	e, found, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !found || e.Version != 1 || e.Inactive || e.HydratedAt != "2026-09-16T12:00:00Z" || e.AsOf != "2026-09-16T00:00:00Z" {
		t.Fatalf("entity = %+v found=%v", e, found)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 1 || rows[0].Origin != "sweep" || !reflect.DeepEqual(rows[0].Kinds, []string{"reconcile"}) || rows[0].Version != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	// The interpretation and the derived links are written too (links LAST).
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); !found {
		t.Fatal("interpretation row missing")
	}
	links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")
	if len(links) != 1 || links[0].ToID != ecRepo+"#7" {
		t.Fatalf("links = %+v", links)
	}
}

func TestRunEntityChange_UnchangedRewritesNothingToTheLog(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})

	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !r.Written || r.Version != 2 || len(r.Kinds) != 0 {
		t.Fatalf("result = %+v, want version 2 and no kinds", r)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("an unchanged re-hydration logged: %+v", rows)
	}
}

func TestRunEntityChange_ClassifiesTransitionAndOrdersKinds(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})

	res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", nil)
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{Origin: "pg-connector"})
	want := []string{"assignee_changed", "closed", "status_changed"}
	if !reflect.DeepEqual(r.Kinds, want) || r.Version != 2 {
		t.Fatalf("result = %+v, want kinds %v", r, want)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 2 || !reflect.DeepEqual(rows[1].Kinds, want) || rows[1].Version != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunEntityChange_FailedReadKeepsPreviousSnapshotAndLogsNothing(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")

	gerr = errors.New("boom")
	r, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if err == nil || !strings.Contains(err.Error(), "pipeline: gather issue i-1") || r.Written {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot moved: %+v -> %+v", before, after)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("a failed read logged: %+v", rows)
	}
}

func TestRunEntityChange_DegradedReadIsNotWrittenAndEntityStaysActive(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")

	res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", workMeta("8"))
	res.Degraded = "deps read failed"
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if r.Written || r.Degraded != "deps read failed" || r.NotFound || len(r.Kinds) != 0 || r.Version != 0 {
		t.Fatalf("result = %+v", r)
	}
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !reflect.DeepEqual(before, after) || after.Inactive {
		t.Fatalf("degraded read moved the snapshot or deactivated it: %+v -> %+v", before, after)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("a degraded read logged: %+v", rows)
	}
	links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")
	if len(links) != 1 || links[0].ToID != ecRepo+"#7" {
		t.Fatalf("a degraded read changed the links: %+v", links)
	}

	// The next healthy read is judged against the good snapshot, not a
	// partial one.
	res = ecIssue(t, "open", "me", "2026-09-16T02:00:00Z", workMeta("7"))
	r = mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if len(r.Kinds) != 0 {
		t.Fatalf("a healthy read after a degraded one faked a transition: %+v", r)
	}
}

func TestRunEntityChange_NotFound(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")

	res = gather.GatherResult{RemovedState: "not_found"}
	if _, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	r := mustRun(t, p, "issue", "i-1", gather.ChangeRemoved, EntityChangeOptions{})
	if !r.NotFound || r.Written {
		t.Fatalf("result = %+v", r)
	}
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !reflect.DeepEqual(before, after) || after.Facts == "{}" || after.Inactive {
		t.Fatalf("not_found overwrote or deactivated the snapshot: %+v -> %+v", before, after)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("not_found logged: %+v", rows)
	}
	if links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); len(links) != 1 {
		t.Fatalf("not_found cleared the derived links: %+v", links)
	}

	// An entity that was never stored stays absent.
	if r, err := p.RunEntityChange(context.Background(), "issue", "i-new", gather.ChangeRemoved, EntityChangeOptions{}); err != nil || !r.NotFound {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, "issue", "i-new"); found {
		t.Fatal("not_found created a row")
	}
}

func TestRunEntityChange_RemovedObservedStateWritesAndClearsLinks(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})

	res = gather.GatherResult{RemovedState: "closed", Payload: res.Payload, AsOf: "2026-09-16T03:00:00Z"}
	r := mustRun(t, p, "issue", "i-1", gather.ChangeRemoved, EntityChangeOptions{})
	if !r.Written || r.NotFound {
		t.Fatalf("result = %+v", r)
	}
	e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Version != 2 || e.AsOf != "2026-09-16T03:00:00Z" || e.Inactive {
		t.Fatalf("entity = %+v", e)
	}
	if links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); len(links) != 0 {
		t.Fatalf("an empty derived set should clear the links: %+v", links)
	}
	// The removed link is a local change source on the entity's own row.
	if !reflect.DeepEqual(r.Kinds, []string{"link_changed"}) {
		t.Fatalf("kinds = %v, want [link_changed]", r.Kinds)
	}
}

func TestRunEntityChange_OldSchemaStoreRefused(t *testing.T) {
	p := newTestPipeline(t, nil, nil)
	p.entityGatherers = map[string]gather.EntityGatherer{
		"issue": funcEntityGatherer(func(gather.ChangeKind) (gather.GatherResult, error) {
			t.Fatal("gather must not run on an old-schema store")
			return gather.GatherResult{}, nil
		}),
	}
	_, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	if !errors.Is(err, store.ErrOldSchema) {
		t.Fatalf("err = %v, want ErrOldSchema", err)
	}
}

func TestRunEntityChange_ForceReconcileSuppressesLocalSources(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	seedPR(t, p, ecRepo+"#7")
	seedPR(t, p, ecRepo+"#8")

	// A real status change AND a link change, but forced to reconcile.
	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", workMeta("8"))
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ForceReconcile: true, Origin: "reset"})
	if !reflect.DeepEqual(r.Kinds, []string{"reconcile"}) {
		t.Fatalf("kinds = %v, want exactly [reconcile]", r.Kinds)
	}
	for _, id := range []string{ecRepo + "#7", ecRepo + "#8"} {
		if rows := p.changeRows(t, "pr", id); len(rows) != 1 { // the seed row only
			t.Fatalf("forced reconcile appended to %s: %+v", id, rows)
		}
	}
	// The derived links are still replaced.
	links, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")
	if len(links) != 1 || links[0].ToID != ecRepo+"#8" {
		t.Fatalf("links = %+v", links)
	}
}

func TestRunEntityChange_CutoverRowAndReturnFromInactiveAreReconcile(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)

	// A row the cutover left at version 0 with no hydrated_at is unobserved.
	if err := p.store.UpsertEntity(store.Entity{Repo: ecRepo, EntityType: "issue", EntityID: "i-1", Facts: `{"old":true}`, AsOf: "2026-09-01T00:00:00Z", ContentHash: "h"}); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"reconcile"}) || r.Version != 1 {
		t.Fatalf("cutover row: %+v", r)
	}

	// Mark it inactive (what Phase 6's removal does), then hydrate again.
	e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if _, err := p.store.WriteEntityStateWithLog(e, e.Version, e.HydratedAt, false, []string{"removed"}, "sync", "t"); err != nil {
		t.Fatal(err)
	}
	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", workMeta("7"))
	r = mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"reconcile"}) {
		t.Fatalf("return from inactive: %+v", r)
	}
	e, _, _ = p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Inactive {
		t.Fatal("a hydration must write active = true")
	}
}

func seedPR(t *testing.T, p *Pipeline, id string) {
	t.Helper()
	if _, err := p.store.WriteEntityWithLog(store.Entity{Repo: ecRepo, EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: "2026-09-01T00:00:00Z", ContentHash: "h"}, 0, []string{"reconcile"}, "sync", "t"); err != nil {
		t.Fatal(err)
	}
}

func TestRunEntityChange_LinkChangeAppendsToOtherEntityAndSkipsMissing(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	seedPR(t, p, ecRepo+"#7") // #8 deliberately has no row

	res = ecIssue(t, "open", "me", "2026-09-16T01:00:00Z", workMeta("8"))
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"link_changed"}) {
		t.Fatalf("own kinds = %v", r.Kinds)
	}
	rows := p.changeRows(t, "pr", ecRepo+"#7")
	// seed row + the removed work link, at origin pg-desk and the PR's version.
	if len(rows) != 2 || rows[1].Origin != "pg-desk" || !reflect.DeepEqual(rows[1].Kinds, []string{"work_changed"}) || rows[1].Version != 1 {
		t.Fatalf("pr rows = %+v", rows)
	}
	if rows := p.changeRows(t, "pr", ecRepo+"#8"); len(rows) != 0 {
		t.Fatalf("a missing entity got a row: %+v", rows)
	}
	// Unchanged links produce no more local records.
	mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if rows := p.changeRows(t, "pr", ecRepo+"#7"); len(rows) != 2 {
		t.Fatalf("re-hydration re-logged the link change: %+v", rows)
	}
}

func TestRunEntityChange_PropagatesOneHopOncePerCall(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	seedPR(t, p, ecRepo+"#7")

	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", workMeta("7"))
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"closed", "status_changed"}) {
		t.Fatalf("own kinds = %v", r.Kinds)
	}
	rows := p.changeRows(t, "pr", ecRepo+"#7")
	if len(rows) != 2 || rows[1].Origin != "pg-desk" || !reflect.DeepEqual(rows[1].Kinds, []string{"work_changed"}) {
		t.Fatalf("propagation rows = %+v (want exactly one propagated record)", rows)
	}
	// A pure reconcile never propagates.
	mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{ForceReconcile: true})
	if rows := p.changeRows(t, "pr", ecRepo+"#7"); len(rows) != 2 {
		t.Fatalf("reconcile propagated: %+v", rows)
	}
}

func TestRunEntityChange_ThreadResolvedOnOwnRow(t *testing.T) {
	var gerr error
	var tres gather.GatherResult
	thread := func(asOf string) gather.GatherResult {
		b, _ := json.Marshal(gather.ThreadFacts{ThreadShow: json.RawMessage(`{"text":"hi","last_reply_at":"2026-09-01T00:00:00Z"}`)})
		return gather.GatherResult{Payload: b, AsOf: asOf}
	}
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{
		"thread": funcEntityGatherer(func(gather.ChangeKind) (gather.GatherResult, error) { return tres, gerr }),
	})
	// First seen 5 days after the last reply, inside the 7d window.
	tres = thread("2026-09-06T00:00:00Z")
	mustRun(t, p, "thread", "t-1", gather.ChangeAdded, EntityChangeOptions{})
	// The fixed clock (2026-09-16T12:00Z) is now past the window.
	tres = thread("2026-09-16T00:00:00Z")
	r := mustRun(t, p, "thread", "t-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"resolved"}) {
		t.Fatalf("kinds = %v, want [resolved]", r.Kinds)
	}
	// It fires once, on the transition.
	tres = thread("2026-09-16T06:00:00Z")
	r = mustRun(t, p, "thread", "t-1", gather.ChangeChanged, EntityChangeOptions{})
	if len(r.Kinds) != 0 {
		t.Fatalf("resolved fired again: %v", r.Kinds)
	}
}

func TestRunEntityChange_LostRaceRetriesReclassifiesAndCounts(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})

	// Our read says open -> closed. A concurrent writer commits the very same
	// closed snapshot after we read the old one; the retry must re-read,
	// re-classify against it and find nothing left to say.
	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", nil)
	winner := store.Entity{Repo: ecRepo, EntityType: "issue", EntityID: "i-1", Facts: string(res.Payload), AsOf: res.AsOf, ContentHash: contentHash(res.Payload)}
	var attempts []int
	opts := EntityChangeOptions{beforeWrite: func(attempt int) {
		attempts = append(attempts, attempt)
		if attempt == 0 {
			if _, err := p.store.WriteEntityWithLog(winner, 1, []string{"closed", "status_changed"}, "other", "t"); err != nil {
				t.Errorf("winner write: %v", err)
			}
		}
	}}
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, opts)
	if r.Retries != 1 || !r.Written || r.Version != 3 || len(r.Kinds) != 0 || !reflect.DeepEqual(attempts, []int{0, 1}) {
		t.Fatalf("result = %+v attempts=%v", r, attempts)
	}
	if p.store.ConflictCount() != 1 {
		t.Fatalf("ConflictCount = %d, want 1", p.store.ConflictCount())
	}
	// The loser did not duplicate the winner's log row.
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 2 || rows[1].Origin != "other" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunEntityChange_RetriesAreBounded(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})

	res = ecIssue(t, "closed", "me", "2026-09-16T01:00:00Z", nil)
	opts := EntityChangeOptions{MaxRetries: 2, beforeWrite: func(int) {
		e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
		if _, err := p.store.WriteEntityWithLog(e, e.Version, nil, "other", "t"); err != nil {
			t.Errorf("interloper: %v", err)
		}
	}}
	r, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, opts)
	if !errors.Is(err, store.ErrVersionConflict) || r.Retries != 2 || r.Written {
		t.Fatalf("r=%+v err=%v, want a bounded ErrVersionConflict after 2 retries", r, err)
	}
}

func TestRunEntityChange_FailureBetweenBumpAndAppendLeavesNothingHalfWritten(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	logBefore := p.changeRows(t, "issue", "i-1")
	linksBefore, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1")

	p.store.SetBetweenBumpAndAppendHook(func() error { return errors.New("injected crash") })
	res = ecIssue(t, "closed", "you", "2026-09-16T01:00:00Z", workMeta("8"))
	r, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	p.store.SetBetweenBumpAndAppendHook(nil)
	if err == nil || !strings.Contains(err.Error(), "injected crash") || r.Written {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("half-written snapshot: %+v -> %+v", before, after)
	}
	if got := p.changeRows(t, "issue", "i-1"); !reflect.DeepEqual(got, logBefore) {
		t.Fatalf("half-written log: %+v", got)
	}
	if linksAfter, _ := p.store.ListXrefLinksFrom(ecRepo, "issue", "i-1"); !reflect.DeepEqual(linksAfter, linksBefore) {
		t.Fatalf("links replaced despite the failed write: %+v", linksAfter)
	}
}

// TestRunEntityChange_ConcurrentCallsNeverRegressVersion races whole
// RunEntityChange calls on separate handles to ONE real SQLite file.
func TestRunEntityChange_ConcurrentCallsNeverRegressVersion(t *testing.T) {
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "race.db")
	open := func() *store.Store {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	first := open()
	if err := first.Cutover(); err != nil {
		t.Fatal(err)
	}

	const writers, rounds = 3, 8
	pipes := make([]*Pipeline, writers)
	for i := range pipes {
		w := i
		st := first
		if i > 0 {
			st = open()
		}
		round := 0
		p := New(testCfg(), st)
		p.clock = interpret.FixedClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
		p.extractors = NewExtractorRegistry(p.cfg, st, p.repo())
		p.entityGatherers = map[string]gather.EntityGatherer{
			"issue": funcEntityGatherer(func(gather.ChangeKind) (gather.GatherResult, error) {
				round++
				return ecIssue(t, fmt.Sprintf("s-%d-%d", w, round), "me", "2026-09-16T00:00:00Z", nil), nil
			}),
		}
		pipes[i] = p
	}

	var wg sync.WaitGroup
	retries := make([]int, writers)
	for i, p := range pipes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				res, err := p.RunEntityChange(context.Background(), "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{MaxRetries: 200})
				if err != nil {
					t.Errorf("writer %d: %v", i, err)
					return
				}
				retries[i] += res.Retries
			}
		}()
	}
	wg.Wait()

	e, _, _ := first.GetEntity(ecRepo, "issue", "i-1")
	if e.Version != writers*rounds {
		t.Fatalf("final version %d, want %d (one bump per call)", e.Version, writers*rounds)
	}
	var sumRetries int
	var conflicts int64
	for i, p := range pipes {
		sumRetries += retries[i]
		conflicts += p.store.ConflictCount()
	}
	if int64(sumRetries) != conflicts {
		t.Fatalf("retries %d != lost races %d", sumRetries, conflicts)
	}
	all, err := first.ListChangesAfter("issue", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := int64(0)
	for _, r := range all {
		if r.Version <= last {
			t.Fatalf("change_log version regressed or repeated: %d after %d", r.Version, last)
		}
		last = r.Version
	}
}
