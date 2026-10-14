package groupsvc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// events records the order of hook calls.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	e.log = append(e.log, s)
	e.mu.Unlock()
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

// runInteractive queues the group as class 1, runs the scheduler once and
// returns the task's result (what the executor reported for it).
func runInteractive(t *testing.T, r *groupsvctest.Rig, id, group string) scheduler.Result {
	t.Helper()
	ctx := context.Background()
	f, err := r.Sched.SubmitInteractive(scheduler.Task{Kind: group, Entity: id, Group: group})
	if err != nil {
		t.Fatal(err)
	}
	r.Sched.Step(ctx)
	r.Sched.Wait()
	select {
	case <-f.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the task never finished")
	}
	return f.Result()
}

func TestOnGroupStoringRunsOnceInsideTheTransactionBeforeOnGroupRefreshed(t *testing.T) {
	ev := &events{}
	var gotPrev *store.GroupRow
	var gotNext store.GroupRow
	var gotInfo groupsvc.RefreshInfo
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{
		OnGroupRefreshed: func(*store.GroupRow, store.GroupRow, groupsvc.RefreshInfo) { ev.add("refreshed") },
		OnGroupStoring: func(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo) error {
			ev.add("storing")
			gotPrev, gotNext, gotInfo = prev, next, info
			// Only the transaction's own methods: this would deadlock on the store.
			row, ok, err := tx.GetGroup(ctx, entity.KindPR, next.ID, next.Group)
			if err != nil || !ok || row.Generation != next.Generation || string(row.Content) != string(next.Content) {
				t.Errorf("inside the transaction the row = %+v ok=%v err=%v, want next", row, ok, err)
			}
			if _, err := tx.BumpEntityVersion(ctx, entity.KindPR, next.ID); err != nil {
				t.Errorf("a hook writing through tx: %v", err)
			}
			return nil
		},
	})
	register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))

	if res := runInteractive(t, r, node, groups.Detail); res.Err != nil {
		t.Fatal(res.Err)
	}
	if got := ev.all(); len(got) != 2 || got[0] != "storing" || got[1] != "refreshed" {
		t.Fatalf("events = %v, want storing once, then refreshed once", got)
	}
	if gotPrev == nil || string(gotPrev.Content) != "old" || gotNext.Generation != 2 || gotNext.Group != groups.Detail {
		t.Errorf("prev = %+v, next = %+v", gotPrev, gotNext)
	}
	if gotInfo.Class != governor.ClassInteractive || gotInfo.Purpose != governor.PurposeInteractive {
		t.Errorf("info = %+v", gotInfo)
	}
	// What the hook wrote through tx committed with the group.
	if e, _ := r.Entity(node); e.Version != 1 {
		t.Errorf("entity version = %d, want the hook's bump to have committed", e.Version)
	}
}

func TestASupersededRefreshCallsNeitherOnGroupStoringNorOnGroupRefreshed(t *testing.T) {
	ctx := context.Background()
	ev := &events{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{
		OnGroupRefreshed: func(*store.GroupRow, store.GroupRow, groupsvc.RefreshInfo) { ev.add("refreshed") },
		OnGroupStoring: func(context.Context, *store.Tx, *store.GroupRow, store.GroupRow, groupsvc.RefreshInfo) error {
			ev.add("storing")
			return nil
		},
	})
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	release := f[groups.Detail].Block()
	t.Cleanup(release)

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Step(ctx)
	if !f[groups.Detail].WaitStarted(1, 10*time.Second) {
		t.Fatal("the refresh never started")
	}
	if err := groups.Invalidate(ctx, r.Store, entity.KindPR, node, groups.Detail, r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	release()
	r.Sched.Wait()

	if got := ev.all(); len(got) != 0 {
		t.Fatalf("hooks ran for a superseded refresh: %v", got)
	}
	if row, _ := r.Group(node, groups.Detail); string(row.Content) != "old" {
		t.Errorf("row = %+v, want the old content", row)
	}
}

func TestAnOnGroupStoringErrorRollsTheWriteBackAndFailsTheTask(t *testing.T) {
	boom := errors.New("boom")
	ev := &events{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{
		OnGroupRefreshed: func(*store.GroupRow, store.GroupRow, groupsvc.RefreshInfo) { ev.add("refreshed") },
		OnGroupStoring: func(ctx context.Context, tx *store.Tx, _ *store.GroupRow, next store.GroupRow, _ groupsvc.RefreshInfo) error {
			// A write through tx before failing: it must roll back with the group.
			if _, err := tx.BumpEntityVersion(ctx, entity.KindPR, next.ID); err != nil {
				return err
			}
			return boom
		},
	})
	register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	before, _ := r.Group(node, groups.Detail)

	res := runInteractive(t, r, node, groups.Detail)
	if !errors.Is(res.Err, boom) {
		t.Fatalf("task result error = %v, want the hook's error", res.Err)
	}
	after, _ := r.Group(node, groups.Detail)
	if string(after.Content) != "old" || after.Generation != before.Generation {
		t.Errorf("row = %+v, want the previous row in place (was %+v)", after, before)
	}
	if e, _ := r.Entity(node); e.Version != 0 {
		t.Errorf("entity version = %d: the hook's own write must roll back too", e.Version)
	}
	if got := ev.all(); len(got) != 0 {
		t.Errorf("OnGroupRefreshed ran after a failed store: %v", got)
	}
}

// goneCall is one OnGone call.
type goneCall struct {
	node    string
	purpose governor.Purpose
}

func TestAGroupFetchThatSaysGoneCallsOnGoneOnceWithItsPurposeAndNotAgain(t *testing.T) {
	var mu sync.Mutex
	var calls []goneCall
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{
		OnGone: func(ctx context.Context, tx *store.Tx, n string, p governor.Purpose) error {
			// Inside the transaction the entity is already tombstoned.
			e, ok, err := tx.GetEntity(ctx, entity.KindPR, n)
			if err != nil || !ok || e.TombstonedAt.IsZero() {
				t.Errorf("inside the transaction the entity = %+v ok=%v err=%v", e, ok, err)
			}
			mu.Lock()
			calls = append(calls, goneCall{n, p})
			mu.Unlock()
			return nil
		},
	})
	f := register(r, groups.Detail)
	seed(r, node)
	f[groups.Detail].SetGone(node, true)

	if res := runInteractive(t, r, node, groups.Detail); res.Err != nil {
		t.Fatal(res.Err)
	}
	if e, _ := r.Entity(node); e.TombstonedAt.IsZero() {
		t.Fatal("the entity was not tombstoned")
	}
	mu.Lock()
	if len(calls) != 1 || calls[0] != (goneCall{node, governor.PurposeInteractive}) {
		t.Fatalf("OnGone calls = %+v, want one for the node with the interactive purpose", calls)
	}
	mu.Unlock()

	// A second tombstone of the same entity does not call the hook.
	if res := runInteractive(t, r, node, groups.Detail); res.Err != nil {
		t.Fatal(res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Errorf("OnGone calls after a repeated tombstone = %+v, want still one", calls)
	}
}

func TestAnOnGoneErrorLeavesTheEntityVisibleAndFailsTheTask(t *testing.T) {
	boom := errors.New("boom")
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{
		OnGone: func(context.Context, *store.Tx, string, governor.Purpose) error { return boom },
	})
	f := register(r, groups.Detail)
	seed(r, node)
	f[groups.Detail].SetGone(node, true)

	res := runInteractive(t, r, node, groups.Detail)
	if !errors.Is(res.Err, boom) {
		t.Fatalf("task result error = %v, want the hook's error", res.Err)
	}
	if e, ok := r.Entity(node); !ok || !e.TombstonedAt.IsZero() {
		t.Errorf("entity = %+v ok=%v, want it still visible", e, ok)
	}
}

// hintLog records the origin hints.
type hintLog struct {
	mu    sync.Mutex
	notes [][3]string
}

func (h *hintLog) NoteOrigin(node, group, origin string) {
	h.mu.Lock()
	h.notes = append(h.notes, [3]string{node, group, origin})
	h.mu.Unlock()
}

func (h *hintLog) all() [][3]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][3]string(nil), h.notes...)
}

func TestOriginConstantsHaveTheSpecsValues(t *testing.T) {
	for got, want := range map[string]string{
		groupsvc.OriginQuery: "query", groupsvc.OriginRefresh: "refresh",
		groupsvc.OriginWrite: "write", groupsvc.OriginRead: "read",
	} {
		if got != want {
			t.Errorf("origin constant = %q, want %q", got, want)
		}
	}
}

func TestInvalidateAndQueueNotesWriteAndInvalidateAndQueueAsNotesTheGivenOrigin(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r, groups.Detail, groups.Runs)
	seed(r, node, freshRow(r, node, groups.Detail, "d"), freshRow(r, node, groups.Runs, "r"))
	h := &hintLog{}
	r.Svc.SetOriginHints(h)

	if err := r.Svc.InvalidateAndQueue(ctx, node, groups.Detail); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.InvalidateAndQueueAs(ctx, node, groups.Runs, groupsvc.OriginRefresh); err != nil {
		t.Fatal(err)
	}
	want := [][3]string{{node, groups.Detail, "write"}, {node, groups.Runs, "refresh"}}
	got := h.all()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("notes = %v, want %v", got, want)
	}
	if !r.Sched.Queued(node, groups.Detail) || !r.Sched.Queued(node, groups.Runs) {
		t.Error("InvalidateAndQueueAs must still invalidate and queue")
	}
}

func TestOriginHintsAreNotNotedForAnUnknownEntityOrReference(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r, groups.Detail)
	h := &hintLog{}
	r.Svc.SetOriginHints(h)

	// A node id the store has no row for, and a reference the alias table does not know.
	if err := r.Svc.InvalidateAndQueue(ctx, "PR_unseen", groups.Detail); err != nil {
		t.Fatal(err)
	}
	ref := entity.Ref{Owner: "acme-org", Repo: "widget", Number: 99}
	if err := r.Svc.InvalidateAndQueueAs(ctx, ref.String(), groups.Detail, groupsvc.OriginRefresh); err != nil {
		t.Fatal(err)
	}
	if got := h.all(); len(got) != 0 {
		t.Errorf("notes = %v, want none for an unknown entity", got)
	}

	// Without hints installed the call is the plain one.
	r.Svc.SetOriginHints(nil)
	seed(r, node)
	if err := r.Svc.InvalidateAndQueue(ctx, node, groups.Summary); err != nil {
		t.Fatal(err)
	}
}
