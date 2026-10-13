package groupsvc_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestSerializeWriteNeverOverlapsWritesToOnePR(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := r.Svc.SerializeWrite(context.Background(), node, func(context.Context) error {
				n := running.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				running.Add(-1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("%d writes to one PR overlapped, want 1 at a time", peak.Load())
	}
}

func TestSerializeWriteRunsWritesToDifferentPRsInParallel(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	aIn, bIn := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	// Each body waits for the other to have started: they can only both finish if
	// they run at the same time.
	run := func(id string, mine, other chan struct{}) {
		defer wg.Done()
		err := r.Svc.SerializeWrite(context.Background(), id, func(context.Context) error {
			close(mine)
			select {
			case <-other:
				return nil
			case <-time.After(10 * time.Second):
				return errors.New("the other PR's write never started")
			}
		})
		if err != nil {
			t.Error(err)
		}
	}
	wg.Add(2)
	go run("PR_A", aIn, bIn)
	go run("PR_B", bIn, aIn)
	wg.Wait()
}

func TestSerializeWriteCanonicalizesReferencesThroughTheAliasTable(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	ctx := context.Background()
	if _, err := r.Aliases.Put(ctx, node, entity.Ref{Owner: "acme-org", Repo: "widget", Number: 1}); err != nil {
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Svc.SerializeWrite(ctx, node, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// The same PR by reference (any case) waits for the lock held under its node id.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	ran := false
	err := r.Svc.SerializeWrite(short, "Acme-Org/Widget#1", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("by reference: err = %v, ran = %v, want the deadline and no run", err, ran)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.SerializeWrite(ctx, "Acme-Org/Widget#1", func(context.Context) error { return nil }); err != nil {
		t.Errorf("after the release: %v", err)
	}
}

func TestSerializeWriteKeysAnUnknownReferenceByItsLowerCasedForm(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	ctx := context.Background()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Svc.SerializeWrite(ctx, "Nobody/Nowhere#5", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := r.Svc.SerializeWrite(short, "nobody/nowhere#5", func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the two spellings to share one lock", err)
	}
	close(release)
	<-done
	if len(r.Resolver.Calls()) != 0 {
		t.Error("SerializeWrite made an origin call")
	}
}

func TestSerializeWriteGivesUpWhenItsContextEndsWhileWaiting(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Svc.SerializeWrite(context.Background(), node, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	err := r.Svc.SerializeWrite(ctx, node, func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("err = %v, ran = %v", err, ran)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDrainWritesBlocksWhileABodyRunsAndReturnsWhenItFinishes(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	if err := r.Svc.DrainWrites(context.Background()); err != nil {
		t.Fatalf("DrainWrites with nothing in flight = %v", err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = r.Svc.SerializeWrite(context.Background(), node, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// The context ends first: its error.
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Svc.DrainWrites(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DrainWrites with a body running = %v, want the context's error", err)
	}

	drained := make(chan error, 1)
	go func() { drained <- r.Svc.DrainWrites(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("DrainWrites returned (%v) while a body was running", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DrainWrites did not return after the body finished")
	}
}

func TestInvalidateAndQueueMakesTheNextEnsureFetchAndQueuesOneClass2Task(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	seed(r, node, freshRow(r, node, groups.Detail, "d"))
	ctx := context.Background()

	for i := 0; i < 2; i++ { // twice: still exactly one queued task
		if err := r.Svc.InvalidateAndQueue(ctx, node, groups.Detail); err != nil {
			t.Fatal(err)
		}
	}
	if d := r.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; d != 1 {
		t.Fatalf("class 2 queue depth = %d, want exactly 1", d)
	}
	if c, queued, running, err := r.Svc.Inspect(ctx, node, groups.Detail); err != nil || c != governor.ClassInvalidated || !queued || running {
		t.Fatalf("Inspect = %v %v %v %v", c, queued, running, err)
	}
	row, _ := r.Group(node, groups.Detail)
	if row.Generation != 3 || string(row.Content) != "d" || !row.FreshUntil.Equal(r.Clock.Now()) {
		t.Errorf("invalidated row = %+v, want the content kept, generation bumped twice and fresh_until now", row)
	}

	if got := r.Rules.Fresh(row, groups.StampsOf(r.Origin.Summary(node)), r.Clock.Now()); got.IsFresh() {
		t.Error("an invalidated row still reads as fresh")
	}

	// The read after the write goes to origin: either the queued class 2 task is
	// already running (the read joins it) or the read goes first (and takes the
	// queued task over); either way ONE fetch happens and the old content goes.
	r.Start()
	res := ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if g := result(t, res, groups.Detail); g.Status != groupsvc.Fresh || string(g.Row.Content) == "d" {
		t.Errorf("detail = %+v, want fresh content from after the invalidation", g)
	}
	if n := f[groups.Detail].CallCount(); n != 1 {
		t.Errorf("fetcher called %d times, want the queued task and the read to share one", n)
	}
}

func TestInvalidateAndQueueOfSummaryOnlySetsAnExistingRowAndQueues(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	ctx := context.Background()
	// An entity with no summary row: nothing is created, the refresh is queued.
	if err := r.Store.TouchEntity(ctx, entity.KindPR, node, r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.InvalidateAndQueue(ctx, node, summary.GroupName); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Group(node, summary.GroupName); ok {
		t.Error("a summary row was created by an invalidation")
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, node, summary.GroupName); !queued || c != governor.ClassInvalidated {
		t.Errorf("summary task = %v queued %v", c, queued)
	}
	// With a row: fresh_until becomes now.
	r.SeedSummary("PR_2", r.Clock.Now())
	if err := r.Svc.InvalidateAndQueue(ctx, "PR_2", summary.GroupName); err != nil {
		t.Fatal(err)
	}
	row, _ := r.Group("PR_2", summary.GroupName)
	if !row.FreshUntil.Equal(r.Clock.Now()) || len(row.Content) == 0 {
		t.Errorf("summary row = %+v", row)
	}
}

func TestInvalidateAndQueueRejectsWhatItCannotQueueAndIgnoresAnUnseenPR(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	ctx := context.Background()
	register(r, groups.Detail)
	if err := r.Svc.InvalidateAndQueue(ctx, "nobody/nowhere#3", groups.Detail); err != nil {
		t.Errorf("unseen reference: %v", err)
	}
	if err := r.Svc.InvalidateAndQueue(ctx, "PR_unseen", groups.Detail); err != nil {
		t.Errorf("unseen node id: %v", err)
	}
	if _, ok := r.Group("PR_unseen", groups.Detail); ok {
		t.Error("an unseen PR got a group row")
	}
	if n := r.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth; n != 0 {
		t.Errorf("%d tasks queued for unseen PRs", n)
	}
	seed(r, node)
	if err := r.Svc.InvalidateAndQueue(ctx, node, "bogus"); err == nil {
		t.Error("an unknown group was accepted")
	}
	if err := r.Svc.InvalidateAndQueue(ctx, node, groups.Commits); !errors.Is(err, groupsvc.ErrNoFetcher) {
		t.Errorf("a group with no fetcher: %v", err)
	}
}

func TestQueueGroupMergesTheBetterClassAndTheEarlierDue(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	ctx := context.Background()
	register(r, groups.Detail)
	seed(r, node)
	later := r.Clock.Now().Add(time.Hour)
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassKeepAlive, later); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassKeepAlive, later); err != nil {
		t.Fatal(err) // a worse class and a later due do not undo it
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || c != governor.ClassInvalidated {
		t.Errorf("task = class %v queued %v, want class 2", c, queued)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Summary, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Errorf("QueueGroup of summary: %v", err)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.Class(9), time.Time{}); err == nil {
		t.Error("an invalid class was accepted")
	}
	if err := r.Svc.QueueGroup(ctx, "nobody/nowhere#3", groups.Detail, governor.ClassKeepAlive, time.Time{}); !errors.Is(err, summary.ErrUnknownEntity) {
		t.Errorf("unknown reference: %v", err)
	}
}
