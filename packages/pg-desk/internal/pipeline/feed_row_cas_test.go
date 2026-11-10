package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// bumpEntity makes a competing write: it bumps the entity's version.
func bumpEntity(t *testing.T, p *Pipeline, id string) {
	t.Helper()
	cur, found, err := p.store.GetEntity(ecRepo, issueEntityType, id)
	if err != nil || !found {
		t.Fatalf("competing write read: found=%v err=%v", found, err)
	}
	if _, err := p.store.WriteEntityStateWithLog(cur, cur.Version, cur.HydratedAt, true, []string{"reconcile"}, "pg-desk", "2026-09-16T00:00:00Z"); err != nil {
		t.Fatalf("competing write: %v", err)
	}
}

func removedCommit(id string, marker *store.FeedMarker) feedCommit {
	return feedCommit{instance: frInstance, entityID: id, kinds: []string{"removed"}, origin: defaultEntityChangeOrigin, marker: marker}
}

func TestCommitRemovedFeedRow_ReadFailureIsAnError(t *testing.T) {
	p, _ := frPipeline(t)
	if err := p.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := p.commitRemovedFeedRow(removedCommit("i-1", nil), ecRepo, "t", store.WriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "read issue i-1") {
		t.Fatalf("err = %v", err)
	}
}

// An unheld entity creates nothing; a marker still advances; a failing marker
// write is an error naming the instance.
func TestCommitRemovedFeedRow_UnheldEntityMarkerAdvanceFailure(t *testing.T) {
	p, _ := frPipeline(t)
	m := &store.FeedMarker{Instance: frInstance, StoreIdentity: frIdentity, Seq: 9}
	failStoreWrites(t, p.store, "INSERT", "meta")
	failStoreWrites(t, p.store, "UPDATE", "meta")
	_, err := p.commitRemovedFeedRow(removedCommit("i-9", m), ecRepo, "t", store.WriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "advance feed marker "+frInstance) {
		t.Fatalf("err = %v", err)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, issueEntityType, "i-9"); found {
		t.Fatal("an unheld removed row created an entity")
	}
}

func TestCommitRemovedFeedRow_WriteFailureIsAnErrorAndLeavesTheRow(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	before, _, _ := p.store.GetEntity(ecRepo, issueEntityType, "i-1")
	failStoreWrites(t, p.store, "UPDATE", "entity")

	_, err := p.commitRemovedFeedRow(removedCommit("i-1", nil), ecRepo, "t", store.WriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "write issue i-1") {
		t.Fatalf("err = %v", err)
	}
	if after, _, _ := p.store.GetEntity(ecRepo, issueEntityType, "i-1"); after.Version != before.Version || after.Inactive != before.Inactive || after.Facts != before.Facts {
		t.Fatalf("entity changed despite the failure: before=%+v after=%+v", before, after)
	}
}

// A lost race is retried: with one competing write the commit lands on the
// next attempt; with a competing write on every attempt it gives up with the
// version-race failure and writes nothing of its own.
func TestCommitRemovedFeedRow_LostRaceRetriesThenGivesUp(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))

	var attempts []int
	p.seams.beforeCASWrite = func(a int) {
		attempts = append(attempts, a)
		if a == 0 {
			bumpEntity(t, p, "i-1")
		}
	}
	out, err := p.commitRemovedFeedRow(removedCommit("i-1", nil), ecRepo, "t", store.WriteOptions{})
	if err != nil || !out.written || out.failure != "" {
		t.Fatalf("out = %+v err = %v, want the retry to land the write", out, err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %v, want 2 (one lost race, one win)", attempts)
	}
	if e, _, _ := p.store.GetEntity(ecRepo, issueEntityType, "i-1"); !e.Inactive {
		t.Fatalf("entity = %+v, want inactive after the removed commit", e)
	}

	p2, _ := frPipeline(t)
	frIngest(t, p2, frRow(1, "i-1", "title_changed"))
	calls := 0
	p2.seams.beforeCASWrite = func(int) { calls++; bumpEntity(t, p2, "i-1") }
	out, err = p2.commitRemovedFeedRow(removedCommit("i-1", nil), ecRepo, "t", store.WriteOptions{})
	if err != nil || out.written || out.failure != "still losing the version race" {
		t.Fatalf("out = %+v err = %v, want the version-race failure", out, err)
	}
	if calls != defaultEntityChangeRetries+1 {
		t.Fatalf("calls = %d, want %d (the initial attempt plus the retries)", calls, defaultEntityChangeRetries+1)
	}
	if e, _, _ := p2.store.GetEntity(ecRepo, issueEntityType, "i-1"); e.Inactive {
		t.Fatal("the entity was deactivated although every write lost the race")
	}
}

// A failure of the local-records step after a successful write is returned.
func TestCommitRemovedFeedRow_LocalRecordsFailureIsReturned(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	boom := errors.New("injected local records failure")
	p.seams.appendLocalRecords = func() error { return boom }
	_, err := p.commitRemovedFeedRow(removedCommit("i-1", nil), ecRepo, "t", store.WriteOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
}

func casMk(id string) func(store.Entity, bool) (store.Entity, string) {
	return func(store.Entity, bool) (store.Entity, string) {
		return store.Entity{Repo: ecRepo, EntityType: issueEntityType, EntityID: id, Facts: `{}`, AsOf: "2026-09-16T00:00:00Z", ContentHash: "h"}, "2026-09-16T00:00:00Z"
	}
}

func casCommit(id string) feedCommit {
	return feedCommit{instance: frInstance, entityID: id, kinds: []string{"added"}, origin: defaultEntityChangeOrigin}
}

func TestFeedCAS_CancelledContextIsAnErrorBeforeAnyWrite(t *testing.T) {
	p, _ := frPipeline(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, exhausted, err := p.feedCAS(ctx, ecRepo, casCommit("i-2"), "t", true, store.WriteOptions{}, casMk("i-2"))
	if !errors.Is(err, context.Canceled) || exhausted {
		t.Fatalf("err = %v exhausted = %v", err, exhausted)
	}
	if _, found, _ := p.store.GetEntity(ecRepo, issueEntityType, "i-2"); found {
		t.Fatal("a cancelled context still wrote")
	}
}

func TestFeedCAS_ReadFailureIsAnError(t *testing.T) {
	p, _ := frPipeline(t)
	if err := p.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err := p.feedCAS(context.Background(), ecRepo, casCommit("i-2"), "t", true, store.WriteOptions{}, casMk("i-2"))
	if err == nil || !strings.Contains(err.Error(), "read issue i-2") {
		t.Fatalf("err = %v", err)
	}
}

func TestFeedCAS_WriteFailureIsAnErrorNotARetry(t *testing.T) {
	p, _ := frPipeline(t)
	failStoreWrites(t, p.store, "INSERT", "entity")
	calls := 0
	p.seams.beforeCASWrite = func(int) { calls++ }
	_, _, _, exhausted, err := p.feedCAS(context.Background(), ecRepo, casCommit("i-2"), "t", true, store.WriteOptions{}, casMk("i-2"))
	if err == nil || !strings.Contains(err.Error(), "write issue i-2") || exhausted {
		t.Fatalf("err = %v exhausted = %v", err, exhausted)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1: only a version conflict retries", calls)
	}
}

// One lost race returns the winning attempt's row read; a race lost on every
// attempt reports exhausted with no error.
func TestFeedCAS_LostRaceRetriesAndReportsExhaustion(t *testing.T) {
	p, _ := frPipeline(t)
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	p.seams.beforeCASWrite = func(a int) {
		if a == 0 {
			bumpEntity(t, p, "i-1")
		}
	}
	v, cur, found, exhausted, err := p.feedCAS(context.Background(), ecRepo, casCommit("i-1"), "t", true, store.WriteOptions{}, casMk("i-1"))
	if err != nil || exhausted || !found || v != cur.Version+1 || cur.Version < 2 {
		t.Fatalf("v=%d cur=%+v found=%v exhausted=%v err=%v, want a win against the bumped row", v, cur, found, exhausted, err)
	}

	p.seams.beforeCASWrite = func(int) { bumpEntity(t, p, "i-1") }
	_, _, _, exhausted, err = p.feedCAS(context.Background(), ecRepo, casCommit("i-1"), "t", true, store.WriteOptions{}, casMk("i-1"))
	if err != nil || !exhausted {
		t.Fatalf("exhausted = %v err = %v, want exhaustion without an error", exhausted, err)
	}
}
