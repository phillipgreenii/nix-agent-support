package summary

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// rowsOf returns the entity and group rows of a node as stored.
func rowsOf(t *testing.T, st *store.Store, node string) (store.EntityRow, store.GroupRow) {
	t.Helper()
	e, ok, err := st.GetEntity(context.Background(), entity.KindPR, node)
	if err != nil || !ok {
		t.Fatalf("entity %s: ok=%v err=%v", node, ok, err)
	}
	g, ok, err := st.GetGroup(context.Background(), entity.KindPR, node, GroupName)
	if err != nil || !ok {
		t.Fatalf("group %s: ok=%v err=%v", node, ok, err)
	}
	return e, g
}

func TestPersistWithAHookThatDoesNothingLeavesTheRowsPersistLeaves(t *testing.T) {
	ctx := context.Background()
	plain, plainStore, plainClk := newPersister(t)
	hooked, hookedStore, hookedClk := newPersister(t)
	hooked.Hook = func(context.Context, *store.Tx, *Summary, Stored, FetchInfo) error { return nil }

	steps := []func(*Summary){
		func(*Summary) {},
		func(s *Summary) { s.CommentCount++ },
		func(s *Summary) { s.Mergeable = "UNKNOWN" },
		func(s *Summary) { s.HeadRefOid = "head-2"; s.Mergeable = "CONFLICTING" },
	}
	for i, mutate := range steps {
		s := sample()
		mutate(&s)
		plainClk.Advance(time.Minute)
		hookedClk.Advance(time.Minute)
		a, err := plain.Persist(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		b, err := hooked.PersistWith(ctx, s, FetchInfo{})
		if err != nil {
			t.Fatal(err)
		}
		if a.Fingerprint != b.Fingerprint || a.First != b.First || a.MergeabilityChanged != b.MergeabilityChanged || a.PrevMergeable != b.PrevMergeable {
			t.Fatalf("step %d: Stored differs: %+v vs %+v", i, a, b)
		}
		ae, ag := rowsOf(t, plainStore, "PR_1")
		be, bg := rowsOf(t, hookedStore, "PR_1")
		if ae != be || string(ag.Content) != string(bg.Content) || ag.Fingerprint != bg.Fingerprint || ag.Generation != bg.Generation ||
			!ag.FetchedAt.Equal(bg.FetchedAt) || !ag.FreshUntil.Equal(bg.FreshUntil) || ag.FetchedForHead != bg.FetchedForHead {
			t.Fatalf("step %d: rows differ:\n%+v %+v\n%+v %+v", i, ae, ag, be, bg)
		}
	}
}

func TestPersistWithAHookErrorKeepsThePreviousRowsAndTheAliasUpdateSurvives(t *testing.T) {
	ctx := context.Background()
	p, st, clk := newPersister(t)
	first, err := p.Persist(ctx, sample())
	if err != nil {
		t.Fatal(err)
	}
	prevEntity, prevGroup := rowsOf(t, st, "PR_1")

	boom := errors.New("boom")
	p.Hook = func(ctx context.Context, tx *store.Tx, _ *Summary, _ Stored, _ FetchInfo) error {
		// A write through tx before failing must roll back with the rest.
		if _, err := tx.BumpEntityVersion(ctx, entity.KindPR, "PR_1"); err != nil {
			return err
		}
		return boom
	}
	clk.Advance(time.Minute)
	changed := sample()
	changed.CommentCount++
	changed.Repo = "Acme-Org/Renamed" // the alias update runs before the transaction
	got, err := p.PersistWith(ctx, changed, FetchInfo{})
	if !errors.Is(err, boom) {
		t.Fatalf("PersistWith error = %v, want the hook's", err)
	}
	if got.Fingerprint != "" || got.Rename != nil {
		t.Errorf("a failed PersistWith must return the zero Stored, got %+v", got)
	}

	e, g := rowsOf(t, st, "PR_1")
	if e != prevEntity || e.Version != 0 {
		t.Errorf("entity = %+v, want the previous row %+v", e, prevEntity)
	}
	if string(g.Content) != string(prevGroup.Content) || g.Generation != prevGroup.Generation || g.Fingerprint != first.Fingerprint || !g.FetchedAt.Equal(prevGroup.FetchedAt) {
		t.Errorf("group = %+v, want the previous row %+v", g, prevGroup)
	}
	var readable Summary
	if err := json.Unmarshal(g.Content, &readable); err != nil || readable.CommentCount != sample().CommentCount || readable.Repo != "Acme-Org/Widget" {
		t.Errorf("the previous summary is not readable: %+v err %v", readable, err)
	}
	if a, ok, _ := st.AliasByNodeID(ctx, "PR_1"); !ok || a.Ref != "Acme-Org/Renamed#7" {
		t.Errorf("alias = %+v ok=%v, want the update that ran before the transaction to survive", a, ok)
	}
}

func TestPersistWithTheHookSeesPrevStoredAndInfoAndMayUseOnlyTheTransaction(t *testing.T) {
	ctx := context.Background()
	p, st, clk := newPersister(t)
	type call struct {
		prev *Summary
		st   Stored
		info FetchInfo
	}
	var calls []call
	p.Hook = func(ctx context.Context, tx *store.Tx, prev *Summary, stored Stored, info FetchInfo) error {
		calls = append(calls, call{prev, stored, info})
		// Reads and writes only through tx must not deadlock on the single connection.
		g, ok, err := tx.GetGroup(ctx, entity.KindPR, "PR_1", GroupName)
		if err != nil || !ok || g.Fingerprint != stored.Fingerprint {
			t.Errorf("inside the transaction the group = %+v ok=%v err=%v, want the new one", g, ok, err)
		}
		_, err = tx.BumpEntityVersion(ctx, entity.KindPR, "PR_1")
		return err
	}
	info := FetchInfo{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh}

	done := make(chan error, 1)
	go func() {
		_, err := p.PersistWith(ctx, sample(), info)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PersistWith deadlocked with a hook that only uses tx")
	}
	if len(calls) != 1 || calls[0].prev != nil || !calls[0].st.First || calls[0].st.Prev != nil || calls[0].info != info {
		t.Fatalf("first sighting hook call = %+v, want no prev, First, and the info passed in", calls)
	}

	clk.Advance(time.Minute)
	next := sample()
	next.Mergeable = "CONFLICTING"
	next.Repo = "Acme-Org/Renamed"
	got, err := p.PersistWith(ctx, next, FetchInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("hook calls = %d", len(calls))
	}
	c := calls[1]
	wantPrev := sample()
	if c.prev == nil || c.prev.Title != wantPrev.Title || c.prev.Mergeable != "MERGEABLE" || c.prev.Repo != "Acme-Org/Widget" {
		t.Errorf("prev = %+v, want the previously stored summary", c.prev)
	}
	if c.st.First || !c.st.MergeabilityChanged || c.st.PrevMergeable != "MERGEABLE" || c.st.Rename == nil || c.st.Rename.Renamed != true {
		t.Errorf("Stored as the hook saw it = %+v", c.st)
	}
	if got.Prev == nil || got.Prev.Mergeable != "MERGEABLE" || got.Rename == nil {
		t.Errorf("returned Stored = %+v, want Prev and Rename set", got)
	}
	if e, _ := rowsOf(t, st, "PR_1"); e.Version != 2 {
		t.Errorf("entity version = %d, want both hook bumps committed", e.Version)
	}
}

func TestPersistWithAGoneSummaryStoresNothingAndRunsNoHook(t *testing.T) {
	ctx := context.Background()
	p, st, _ := newPersister(t)
	p.Hook = func(context.Context, *store.Tx, *Summary, Stored, FetchInfo) error {
		t.Error("the hook ran for a Gone summary")
		return nil
	}
	got, err := p.PersistWith(ctx, Summary{NodeID: "PR_1", Gone: true}, FetchInfo{})
	if err != nil || !got.Summary.Gone {
		t.Fatalf("PersistWith = %+v, %v", got, err)
	}
	if _, ok, _ := st.GetEntity(ctx, entity.KindPR, "PR_1"); ok {
		t.Error("a Gone summary created an entity row")
	}
}

func TestServiceStoresThroughTheHookWithHowTheSummaryWasAdmittedAndFiresTheCallbackAfterTheCommit(t *testing.T) {
	ctx := context.Background()
	var hookInfo []FetchInfo
	var refreshed []*Summary
	var r *rig
	r = newRig(t, rigOpts{
		hook: func(_ context.Context, _ *store.Tx, _ *Summary, _ Stored, info FetchInfo) error {
			hookInfo = append(hookInfo, info)
			return nil
		},
		onRefresh: func(prev *Summary, next Stored) {
			// After the commit: the store is usable from here without deadlock.
			if g, ok := r.group(t, next.Summary.NodeID); !ok || g.Fingerprint != next.Fingerprint {
				t.Errorf("OnSummaryRefreshed ran before the commit: %+v ok=%v", g, ok)
			}
			refreshed = append(refreshed, prev)
		},
	})
	r.putEntity(t, "a", t0)
	f, err := r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "a", Group: GroupName})
	if err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if res := f.Result(); res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(hookInfo) != 1 || hookInfo[0] != (FetchInfo{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}) {
		t.Errorf("hook info = %+v, want the interactive class and purpose", hookInfo)
	}
	if len(refreshed) != 1 || refreshed[0] != nil {
		t.Errorf("OnSummaryRefreshed prev = %+v, want one call with no previous summary", refreshed)
	}

	// A refresh later passes Stored.Prev as the callback's prev.
	r.clk.Advance(time.Hour)
	f, _ = r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "a", Group: GroupName})
	r.sched.Drain(ctx)
	if res := f.Result(); res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(refreshed) != 2 || refreshed[1] == nil || refreshed[1].NodeID != "a" {
		t.Errorf("second OnSummaryRefreshed prev = %+v, want the stored summary", refreshed[1])
	}
}

func TestAServiceHookErrorFailsTheTaskLeavesThePreviousSummaryAndSkipsTheCallback(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	fail := false
	callbacks := 0
	r := newRig(t, rigOpts{
		hook: func(context.Context, *store.Tx, *Summary, Stored, FetchInfo) error {
			if fail {
				return boom
			}
			return nil
		},
		onRefresh: func(*Summary, Stored) { callbacks++ },
	})
	r.putEntity(t, "a", t0)
	run := func() scheduler.Result {
		f, err := r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "a", Group: GroupName})
		if err != nil {
			t.Fatal(err)
		}
		r.sched.Drain(ctx)
		return f.Result()
	}
	if res := run(); res.Err != nil {
		t.Fatal(res.Err)
	}
	before, _ := r.group(t, "a")

	fail = true
	r.clk.Advance(time.Hour)
	if res := run(); !errors.Is(res.Err, boom) {
		t.Fatalf("task error = %v, want the hook's", res.Err)
	}
	after, _ := r.group(t, "a")
	if after.Generation != before.Generation || after.Fingerprint != before.Fingerprint || !after.FetchedAt.Equal(before.FetchedAt) {
		t.Errorf("group = %+v, want the previous row %+v", after, before)
	}
	if callbacks != 1 {
		t.Errorf("OnSummaryRefreshed ran %d times, want only for the stored refresh", callbacks)
	}
}

func TestAGoneSummaryCallsOnGoneOnceWithTheBatchPurposeAndNotForAnAlreadyTombstonedEntity(t *testing.T) {
	ctx := context.Background()
	type call struct {
		node    string
		purpose governor.Purpose
	}
	var calls []call
	r := newRig(t, rigOpts{
		onGone: func(ctx context.Context, tx *store.Tx, node string, purpose governor.Purpose) error {
			e, ok, err := tx.GetEntity(ctx, entity.KindPR, node)
			if err != nil || !ok || e.TombstonedAt.IsZero() {
				t.Errorf("inside the transaction the entity = %+v ok=%v err=%v, want it tombstoned", e, ok, err)
			}
			calls = append(calls, call{node, purpose})
			return nil
		},
	})
	r.fetch.gone = map[string]bool{"a": true, "b": true}
	r.putEntity(t, "a", t0)

	// "a" is found gone by a background refresh, "b" by an interactive one.
	if _, err := r.svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	r.putEntity(t, "b", t0)
	f, _ := r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "b", Group: GroupName})
	r.sched.Drain(ctx)
	if res := f.Result(); res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(calls) != 2 {
		t.Fatalf("OnGone calls = %+v, want one per tombstoned entity", calls)
	}
	byNode := map[string]governor.Purpose{}
	for _, c := range calls {
		byNode[c.node] = c.purpose
	}
	if byNode["a"] != governor.PurposeRefresh || byNode["b"] != governor.PurposeInteractive {
		t.Errorf("purposes = %+v, want refresh for a and interactive for b", byNode)
	}

	// A second tombstone of "b" calls the hook zero times.
	f, _ = r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "b", Group: GroupName})
	r.sched.Drain(ctx)
	if res := f.Result(); res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(calls) != 2 {
		t.Errorf("OnGone calls after a repeated tombstone = %+v, want still two", calls)
	}
	if e, _, _ := r.st.GetEntity(ctx, entity.KindPR, "b"); e.TombstonedAt.IsZero() {
		t.Error("the entity must stay tombstoned")
	}
}

func TestAnOnGoneErrorLeavesTheEntityVisibleAndFailsTheTask(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	r := newRig(t, rigOpts{
		onGone: func(context.Context, *store.Tx, string, governor.Purpose) error { return boom },
	})
	r.fetch.gone = map[string]bool{"a": true}
	r.putEntity(t, "a", t0)
	f, _ := r.sched.SubmitInteractive(scheduler.Task{Kind: GroupName, Entity: "a", Group: GroupName})
	r.sched.Drain(ctx)
	if res := f.Result(); !errors.Is(res.Err, boom) {
		t.Fatalf("task error = %v, want the hook's", res.Err)
	}
	if e, ok, _ := r.st.GetEntity(ctx, entity.KindPR, "a"); !ok || !e.TombstonedAt.IsZero() {
		t.Errorf("entity = %+v ok=%v, want it still visible", e, ok)
	}
}

func TestSweepFlipsKeepAliveWithoutRewindingAVersion(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, rigOpts{})
	r.putEntity(t, "a", t0) // accessed within the window: in the keep-alive set
	if err := r.st.InTx(ctx, func(tx *store.Tx) error {
		_, err := tx.BumpEntityVersion(ctx, entity.KindPR, "a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	e, _, _ := r.st.GetEntity(ctx, entity.KindPR, "a")
	if !e.KeepAlive || e.Version != 1 {
		t.Errorf("entity = %+v, want keep_alive flipped and version 1 kept", e)
	}
}
