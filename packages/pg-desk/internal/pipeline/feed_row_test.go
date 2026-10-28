package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	frInstance = "owning-demo"
	frIdentity = "store-1"
)

// scriptedBackendGatherer is an EntityGatherer AND BackendEntityGatherer
// whose answer is whatever its fields hold; it asserts every hydration is
// pinned to the instance.
type scriptedBackendGatherer struct {
	t      *testing.T
	result gather.GatherResult
	err    error
	calls  int
}

func (g *scriptedBackendGatherer) GatherEntity(context.Context, string, gather.ChangeKind) (gather.GatherResult, error) {
	g.t.Errorf("GatherEntity called: a feed row must hydrate through GatherEntityOn")
	return g.result, g.err
}

func (g *scriptedBackendGatherer) GatherEntityOn(_ context.Context, _, backend string, _ gather.ChangeKind) (gather.GatherResult, error) {
	g.calls++
	if backend != frInstance {
		g.t.Errorf("GatherEntityOn backend = %q, want %q", backend, frInstance)
	}
	return g.result, g.err
}

func (g *scriptedBackendGatherer) GatherEntityWatch(ctx context.Context, id, backend string) (gather.GatherResult, error) {
	return g.GatherEntityOn(ctx, id, backend, "")
}

// frPipeline is a pipeline over a fresh new-schema store with the delivery
// production gate installed (every issue entity).
func frPipeline(t *testing.T) (*Pipeline, *scriptedBackendGatherer) {
	t.Helper()
	g := &scriptedBackendGatherer{t: t, result: ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)}
	p := newGenericPipeline(t, nil, map[string]gather.EntityGatherer{"issue": g})
	InstallIssueGate(p.store, nil)
	return p, g
}

func frRow(seq int64, id string, kinds ...string) FeedRowInput {
	return FeedRowInput{
		Instance: frInstance, StoreIdentity: frIdentity,
		Row: gather.FeedRow{Seq: seq, EntityID: id, Kinds: kinds},
	}
}

func frIngest(t *testing.T, p *Pipeline, in FeedRowInput) FeedRowResult {
	t.Helper()
	r, err := p.IngestFeedRow(context.Background(), in)
	if err != nil {
		t.Fatalf("IngestFeedRow: %v", err)
	}
	return r
}

func frKinds(rows []store.ChangeRecord) [][]string {
	var out [][]string
	for _, r := range rows {
		out = append(out, r.Kinds)
	}
	return out
}

func frWatched(w *bool) string {
	if w == nil {
		return "NULL"
	}
	return fmt.Sprint(*w)
}

func TestIngestFeedRow_StoresTheRowsOwnKinds(t *testing.T) {
	p, _ := frPipeline(t)
	// entered_query is dropped defensively, the daemon's own kinds are kept.
	r := frIngest(t, p, frRow(1, "i-1", "labels_changed", "entered_query", "title_changed"))
	if !r.Written || r.Version != 1 || r.Skipped || r.Failed {
		t.Fatalf("result = %+v", r)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Kinds, []string{"labels_changed", "title_changed"}) || rows[0].Origin != "pg-connector" {
		t.Fatalf("rows = %+v", rows)
	}
	e, found, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !found || e.Backend != frInstance || e.Inactive || e.Watched == nil || !*e.Watched {
		t.Fatalf("entity = %+v found=%v watched=%s", e, found, frWatched(e.Watched))
	}
	m, ok, _ := p.store.FeedMarker(frInstance)
	if !ok || m.Seq != 1 || m.StoreIdentity != frIdentity {
		t.Fatalf("marker = %+v ok=%v", m, ok)
	}
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); !found {
		t.Fatal("interpretation row missing")
	}
}

// TestIngestFeedRow_RedeliveryAppendsNothing is the T-12 case: one row, the
// same row again, and no further rows all leave exactly one change row.
func TestIngestFeedRow_RedeliveryAppendsNothing(t *testing.T) {
	p, g := frPipeline(t)
	in := frRow(1, "i-1", "title_changed")
	frIngest(t, p, in)
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("after first ingest: %+v", rows)
	}
	calls := g.calls

	r := frIngest(t, p, in)
	if !r.Skipped || r.Written || r.Version != 0 {
		t.Fatalf("redelivery result = %+v", r)
	}
	if g.calls != calls {
		t.Fatal("a skipped row hydrated")
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 1 {
		t.Fatalf("after redelivery: %+v", rows)
	}
	e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Version != 1 {
		t.Fatalf("version = %d, want 1", e.Version)
	}
	// A row at a LOWER seq is a redelivery too; a different store identity is
	// a new sequence and is ingested.
	if r := frIngest(t, p, frRow(1, "i-1", "title_changed")); !r.Skipped {
		t.Fatalf("lower/equal seq not skipped: %+v", r)
	}
	other := frRow(1, "i-1", "labels_changed")
	other.StoreIdentity = "store-2"
	if r := frIngest(t, p, other); !r.Written {
		t.Fatalf("new store identity not ingested: %+v", r)
	}
}

func TestIngestFeedRow_HydrationErrorRecordsAndRetryWritesRecordedKinds(t *testing.T) {
	p, g := frPipeline(t)
	g.err = fmt.Errorf("gather: fetch issue i-1: %w", gather.ErrUnavailable)

	in := frRow(5, "i-1", "description_changed", "labels_changed")
	r := frIngest(t, p, in)
	if !r.Failed || r.Written || r.Skipped || r.Version != 0 {
		t.Fatalf("result = %+v", r)
	}
	fails, err := p.store.ListIngestFailures(frInstance)
	if err != nil || len(fails) != 1 {
		t.Fatalf("failures = %+v err=%v", fails, err)
	}
	f := fails[0]
	if f.EntityID != "i-1" || f.Seq != 5 || f.StoreIdentity != frIdentity ||
		!reflect.DeepEqual(f.Kinds, []string{"description_changed", "labels_changed"}) || f.LastError == "" {
		t.Fatalf("failure = %+v", f)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, "issue", "i-1"); found {
		t.Fatal("a failed row wrote an entity")
	}
	// The same row redelivered is skipped (the failed-ingest entry).
	if r := frIngest(t, p, in); !r.Skipped {
		t.Fatalf("redelivered failed row = %+v", r)
	}

	// Still failing: the entry stays.
	retried, still, err := p.RetryIngestFailures(context.Background(), frInstance)
	if err != nil || retried != 1 || still != 1 {
		t.Fatalf("retry (failing) = %d, %d, %v", retried, still, err)
	}
	if n, _ := p.store.CountIngestFailures(frInstance); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}

	// A working gatherer: the recorded kinds are written and the entry cleared.
	g.err = nil
	retried, still, err = p.RetryIngestFailures(context.Background(), frInstance)
	if err != nil || retried != 1 || still != 0 {
		t.Fatalf("retry (working) = %d, %d, %v", retried, still, err)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Kinds, []string{"description_changed", "labels_changed"}) {
		t.Fatalf("rows = %+v", rows)
	}
	if n, _ := p.store.CountIngestFailures(frInstance); n != 0 {
		t.Fatalf("entries = %d, want 0", n)
	}
	if _, ok, _ := p.store.FeedMarker(frInstance); ok {
		t.Fatal("a retry wrote a feed marker")
	}
	// Nothing left to retry.
	if retried, still, err := p.RetryIngestFailures(context.Background(), frInstance); err != nil || retried != 0 || still != 0 {
		t.Fatalf("retry (empty) = %d, %d, %v", retried, still, err)
	}
}

func TestIngestFeedRow_DegradedAnswerWritesNothing(t *testing.T) {
	p, g := frPipeline(t)
	g.result.Degraded = "issue answer is stale"

	r := frIngest(t, p, frRow(1, "i-1", "title_changed"))
	if !r.Failed || !r.Degraded || r.Written {
		t.Fatalf("result = %+v", r)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, "issue", "i-1"); found {
		t.Fatal("a degraded answer wrote an entity")
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if _, ok, _ := p.store.FeedMarker(frInstance); ok {
		t.Fatal("a degraded answer advanced the marker")
	}
	if n, _ := p.store.CountIngestFailures(frInstance); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
}

func TestIngestFeedRow_ContextCancelIsAnErrorNotAFailure(t *testing.T) {
	p, g := frPipeline(t)
	g.err = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.IngestFeedRow(ctx, frRow(1, "i-1", "title_changed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n, _ := p.store.CountIngestFailures(frInstance); n != 0 {
		t.Fatalf("a cancelled read recorded %d failures", n)
	}
}

func TestIngestFeedRow_ClosedRowAppendsDeactivatesAndClearsWatched(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))

	r := frIngest(t, p, frRow(2, "i-1", "closed", "status_changed"))
	if !r.Written {
		t.Fatalf("result = %+v", r)
	}
	e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if !e.Inactive || e.Watched == nil || *e.Watched {
		t.Fatalf("entity inactive=%v watched=%s, want inactive and watched=false", e.Inactive, frWatched(e.Watched))
	}
	// The delivered closed row is readable.
	read, err := p.store.ReadChanges("c1", "issue", 100, time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"title_changed"}, {"closed", "status_changed"}}
	if got := frKinds(read); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadChanges kinds = %v, want %v", got, want)
	}

	// The next feed row sets the flag again and reactivates the entity.
	frIngest(t, p, frRow(3, "i-1", "reopened"))
	e, _, _ = p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Inactive || e.Watched == nil || !*e.Watched {
		t.Fatalf("after next row: inactive=%v watched=%s", e.Inactive, frWatched(e.Watched))
	}
}

func TestIngestFeedRow_WatchedMirrorsEntityMeta(t *testing.T) {
	p, g := frPipeline(t)

	// NOT STATED is written as true.
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Watched == nil || !*e.Watched {
		t.Fatalf("not stated: watched = %s, want true", frWatched(e.Watched))
	}

	// A stated false is written as false, and the delivered row still appends.
	f := false
	g.result.Watched = &f
	frIngest(t, p, frRow(2, "i-1", "labels_changed"))
	e, _, _ = p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Watched == nil || *e.Watched {
		t.Fatalf("stated false: watched = %s, want false", frWatched(e.Watched))
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 2 {
		t.Fatalf("rows = %+v, want the stated-false row delivered", rows)
	}

	// A stated true is written as true.
	tr := true
	g.result.Watched = &tr
	frIngest(t, p, frRow(3, "i-1", "title_changed"))
	e, _, _ = p.store.GetEntity(ecRepo, "issue", "i-1")
	if e.Watched == nil || !*e.Watched {
		t.Fatalf("stated true: watched = %s, want true", frWatched(e.Watched))
	}
}

func TestIngestFeedRow_RemovedRowOfAHeldEntityKeepsFactsAndDeactivates(t *testing.T) {
	p, g := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	before, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")

	g.result = gather.GatherResult{RemovedState: "not_found"}
	r := frIngest(t, p, frRow(2, "i-1", "removed"))
	if !r.Written || r.Failed {
		t.Fatalf("result = %+v", r)
	}
	after, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
	if after.Facts != before.Facts || after.ContentHash != before.ContentHash || !after.Inactive ||
		after.Watched == nil || *after.Watched || after.Version != before.Version+1 {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 2 || !reflect.DeepEqual(rows[1].Kinds, []string{"removed"}) {
		t.Fatalf("rows = %+v", rows)
	}
	if m, _, _ := p.store.FeedMarker(frInstance); m.Seq != 2 {
		t.Fatalf("marker = %+v", m)
	}
}

func TestIngestFeedRow_RemovedRowOfAnUnheldEntityOnlyAdvancesTheMarker(t *testing.T) {
	p, g := frPipeline(t)
	g.result = gather.GatherResult{RemovedState: "not_found"}

	r := frIngest(t, p, frRow(4, "i-9", "removed"))
	if r.Written || r.Skipped || r.Failed {
		t.Fatalf("result = %+v", r)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, "issue", "i-9"); found {
		t.Fatal("an unheld removed row created an entity")
	}
	if m, ok, _ := p.store.FeedMarker(frInstance); !ok || m.Seq != 4 {
		t.Fatalf("marker = %+v ok=%v", m, ok)
	}
}

func TestIngestFeedRow_NotFoundWithoutRemovedIsAFailure(t *testing.T) {
	p, g := frPipeline(t)
	g.result = gather.GatherResult{RemovedState: "not_found"}

	r := frIngest(t, p, frRow(1, "i-1", "title_changed"))
	if !r.Failed || r.Written {
		t.Fatalf("result = %+v", r)
	}
	if n, _ := p.store.CountIngestFailures(frInstance); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
}

func TestIngestFeedRow_OnlyMembershipKindsAdvancesMarkerWithoutARow(t *testing.T) {
	p, _ := frPipeline(t)
	r := frIngest(t, p, frRow(1, "i-1", "entered_query", "left_query"))
	if !r.Written {
		t.Fatalf("result = %+v", r)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
	if m, ok, _ := p.store.FeedMarker(frInstance); !ok || m.Seq != 1 {
		t.Fatalf("marker = %+v ok=%v", m, ok)
	}
}

func TestIngestFeedRow_ReplayRowIsAddedWithResetOriginAndNoMarker(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(3, "i-1", "title_changed"))

	// A replay row is never skipped, whatever the marker says, and carries no
	// kinds on the wire.
	r := frIngest(t, p, frRow(0, "i-1"))
	if !r.Written || r.Skipped {
		t.Fatalf("result = %+v", r)
	}
	rows := p.changeRows(t, "issue", "i-1")
	if len(rows) != 2 || !reflect.DeepEqual(rows[1].Kinds, []string{"added"}) || rows[1].Origin != "reset" {
		t.Fatalf("rows = %+v", rows)
	}
	if m, _, _ := p.store.FeedMarker(frInstance); m.Seq != 3 {
		t.Fatalf("marker = %+v, a replay row must not move it", m)
	}
	// Replaying again appends again (no skip check).
	if r := frIngest(t, p, frRow(0, "i-1")); !r.Written {
		t.Fatalf("second replay = %+v", r)
	}
}

// TestIngestFeedRow_PropagationIsDerivedFromTheRowsKinds seeds an issue
// linked to a PR through its metadata and checks a row's kinds propagate to
// the linked PR, while a reconcile-only row does not.
func TestIngestFeedRow_PropagationIsDerivedFromTheRowsKinds(t *testing.T) {
	p, g := frPipeline(t)
	g.result = ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", workMeta("7"))
	frIngest(t, p, frRow(1, "i-1", "added"))
	// The PR entity exists so the propagated record has a row to land on.
	const prID = ecRepo + "#7"
	if _, err := p.store.WriteEntityStateWithLogFP(store.Entity{
		Repo: ecRepo, EntityType: "pr", EntityID: prID, Facts: `{}`, AsOf: "2026-09-16T00:00:00Z", ContentHash: "h",
	}, 0, "2026-09-16T00:00:00Z", true, nil, nil, "test", "2026-09-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	frIngest(t, p, frRow(2, "i-1", "reconcile"))
	if rows := p.changeRows(t, "pr", prID); len(rows) != 0 {
		t.Fatalf("a reconcile-only row propagated: %+v", rows)
	}
	frIngest(t, p, frRow(3, "i-1", "title_changed"))
	rows := p.changeRows(t, "pr", prID)
	if len(rows) != 1 || rows[0].Origin != "pg-desk" {
		t.Fatalf("propagated rows = %+v", rows)
	}
}

// TestPropagationToALinkedIssueIsGatedByWatched is the T-20 propagation case:
// a changed PR propagates link_changed to a linked issue only when the issue
// is watched; the gate inside the store withholds the other.
func TestPropagationToALinkedIssueIsGatedByWatched(t *testing.T) {
	p, g := frPipeline(t)
	frIngest(t, p, frRow(1, "i-watched", "title_changed"))
	f := false
	g.result.Watched = &f
	frIngest(t, p, frRow(2, "i-unwatched", "title_changed"))

	const prID = ecRepo + "#9"
	for _, id := range []string{"i-watched", "i-unwatched"} {
		if err := p.store.UpsertXref(store.Xref{
			Repo: ecRepo, FromType: "pr", FromID: prID, ToType: "issue", ToID: id,
			Evidence: "branch", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The PR's own change record (kinds include link_changed) propagates.
	own := classify.NewStoreWorkLookup(p.store, ecRepo)
	if err := p.appendLocalRecords(ecRepo, "pr", prID, true, true, nil, own, "2026-09-16T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	w := p.changeRows(t, "issue", "i-watched")
	if len(w) != 2 || !reflect.DeepEqual(w[1].Kinds, []string{"link_changed"}) {
		t.Fatalf("watched issue rows = %+v", w)
	}
	u := p.changeRows(t, "issue", "i-unwatched")
	if len(u) != 1 || !reflect.DeepEqual(u[0].Kinds, []string{"title_changed"}) {
		t.Fatalf("unwatched issue rows = %+v, want only its delivered row", u)
	}
}
