package groupsvc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

const ent1 = "ent-1"

func TestSerializeWriteNeverOverlapsWritesToOneEntity(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := r.svc.SerializeWrite(context.Background(), ent1, func(context.Context) error {
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
		t.Fatalf("%d writes to one entity overlapped, want 1 at a time", peak.Load())
	}
}

func TestSerializeWriteRunsWritesToDifferentEntitiesInParallel(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	aIn, bIn := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	// Each body waits for the other to have started: they can only both finish if
	// they run at the same time.
	run := func(id string, mine, other chan struct{}) {
		defer wg.Done()
		err := r.svc.SerializeWrite(context.Background(), id, func(context.Context) error {
			close(mine)
			select {
			case <-other:
				return nil
			case <-time.After(10 * time.Second):
				return errors.New("the other entity's write never started")
			}
		})
		if err != nil {
			t.Error(err)
		}
	}
	wg.Add(2)
	go run("ent-a", aIn, bIn)
	go run("ent-b", bIn, aIn)
	wg.Wait()
}

func TestSerializeWriteCanonicalizesReferencesThroughTheAliasTable(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	if err := r.aliases.Put(ctx, ent1, "Zone-A/Item-B~1"); err != nil {
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.SerializeWrite(ctx, ent1, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// The same entity by reference (any case) waits for the lock held under its id.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	ran := false
	err := r.svc.SerializeWrite(short, "zone-a/ITEM-b~1", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("by reference: err = %v, ran = %v, want the deadline and no run", err, ran)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SerializeWrite(ctx, "Zone-A/Item-B~1", func(context.Context) error { return nil }); err != nil {
		t.Errorf("after the release: %v", err)
	}
}

func TestSerializeWriteKeysAnUnknownReferenceBySpecFoldRef(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.SerializeWrite(ctx, "Nobody/Nowhere~5", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := r.svc.SerializeWrite(short, "nobody/nowhere~5", func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the two spellings to share one lock", err)
	}
	close(release)
	<-done
}

func TestSerializeWriteKeysAnUnknownReferenceWithTheIdentityWhenTheSpecHasNoFold(t *testing.T) {
	spec := specAlpha
	spec.FoldRef = nil
	r := newRig(t, spec, nil, nil)
	ctx := context.Background()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.SerializeWrite(ctx, "Nobody/Nowhere~5", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// Another spelling is another key: it does not wait.
	if err := r.svc.SerializeWrite(ctx, "nobody/nowhere~5", func(context.Context) error { return nil }); err != nil {
		t.Errorf("another spelling waited or failed: %v", err)
	}
	close(release)
	<-done
}

func TestSerializeWriteGivesUpWhenItsContextEndsWhileWaiting(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.svc.SerializeWrite(context.Background(), ent1, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	err := r.svc.SerializeWrite(ctx, ent1, func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("err = %v, ran = %v", err, ran)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDrainWritesBlocksWhileABodyRunsAndReturnsWhenItFinishes(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	if err := r.svc.DrainWrites(context.Background()); err != nil {
		t.Fatalf("DrainWrites with nothing in flight = %v", err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = r.svc.SerializeWrite(context.Background(), ent1, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// The context ends first: its error.
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.svc.DrainWrites(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DrainWrites with a body running = %v, want the context's error", err)
	}

	drained := make(chan error, 1)
	go func() { drained <- r.svc.DrainWrites(context.Background()) }()
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

func TestInvalidateAndQueueBumpsTheGenerationAndQueuesOneClass2Task(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	r.seedPrimary(ent1, "p")
	r.seedGroup(freshGroup(r, ent1, groupSide, "d"))

	for i := 0; i < 2; i++ { // twice: still exactly one queued task
		if err := r.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
			t.Fatal(err)
		}
	}
	if d := r.depth(governor.ClassInvalidated); d != 1 {
		t.Fatalf("class 2 queue depth = %d, want exactly 1", d)
	}
	if c, queued, running, err := r.svc.Inspect(ctx, ent1, groupSide); err != nil || c != governor.ClassInvalidated || !queued || running {
		t.Fatalf("Inspect = %v %v %v %v", c, queued, running, err)
	}
	row, _ := r.group(ent1, groupSide)
	if row.Generation != 3 || string(row.Content) != "d" || !row.FreshUntil.Equal(r.clk.Now()) {
		t.Errorf("invalidated row = %+v, want the content kept, generation bumped twice and fresh_until now", row)
	}
	if got := r.hints.all(); len(got) != 2 || got[0] != [3]string{ent1, groupSide, OriginWrite} {
		t.Errorf("origin hints = %v, want one write hint per call", got)
	}
}

func TestInvalidateAndQueueAsNotesTheGivenOrigin(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	r.seedPrimary(ent1, "p")
	if err := r.svc.InvalidateAndQueueAs(context.Background(), ent1, groupSide, OriginRefresh); err != nil {
		t.Fatal(err)
	}
	if got := r.hints.all(); len(got) != 1 || got[0] != [3]string{ent1, groupSide, OriginRefresh} {
		t.Errorf("origin hints = %v, want the refresh origin", got)
	}
	// With no hints receiver the call still works.
	r.svc.hints = nil
	if err := r.svc.InvalidateAndQueueAs(context.Background(), ent1, groupPassive, OriginWrite); err != nil {
		t.Errorf("without hints: %v", err)
	}
}

func TestInvalidateAndQueueIgnoresATombstonedEntity(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	r.seedPrimary(ent1, "p")
	ent, _, err := r.st.GetEntity(ctx, r.kind(), ent1)
	if err != nil {
		t.Fatal(err)
	}
	ent.TombstonedAt = r.clk.Now()
	if err := r.st.PutEntity(ctx, ent); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.group(ent1, groupSide); ok {
		t.Error("a tombstoned entity got a group row")
	}
	if _, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); queued || len(r.hints.all()) != 0 {
		t.Error("a tombstoned entity was queued or noted")
	}
}

func TestInvalidateAndQueueRejectsWhatItCannotQueueAndIgnoresAnUnseenEntity(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	if err := r.svc.InvalidateAndQueue(ctx, "nobody/nowhere~3", groupSide); err != nil {
		t.Errorf("unseen reference: %v", err)
	}
	if err := r.svc.InvalidateAndQueue(ctx, "ent-unseen", groupSide); err != nil {
		t.Errorf("unseen id: %v", err)
	}
	if _, ok := r.group("ent-unseen", groupSide); ok {
		t.Error("an unseen entity got a group row")
	}
	if n := r.depth(governor.ClassInvalidated); n != 0 {
		t.Errorf("%d tasks queued for unseen entities", n)
	}
	r.seedPrimary(ent1, "p")
	err := r.svc.InvalidateAndQueue(ctx, ent1, "bogus")
	if !errors.Is(err, ErrUnknownGroup) || err.Error() != `groupsvc: unknown group "bogus"` {
		t.Errorf("an unknown group: %v", err)
	}
	delete(r.svc.fetchers, groupStamped)
	if err := r.svc.InvalidateAndQueue(ctx, ent1, groupStamped); !errors.Is(err, ErrNoFetcher) {
		t.Errorf("a group with no fetcher: %v", err)
	}
	// The primary group needs no fetcher: its executor is the connector's own.
	if err := r.svc.InvalidateAndQueue(ctx, ent1, "core"); err != nil {
		t.Errorf("the primary group: %v", err)
	}
}

func TestQueueGroupMergesTheBetterClassAndTheEarlierDue(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	r.seedPrimary(ent1, "p")
	later := r.clk.Now().Add(time.Hour)
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassKeepAlive, later); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassKeepAlive, later); err != nil {
		t.Fatal(err) // a worse class and a later due do not undo it
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassInvalidated {
		t.Errorf("task = class %v queued %v, want class 2", c, queued)
	}
	if err := r.svc.QueueGroup(ctx, ent1, "core", governor.ClassInvalidated, time.Time{}); err != nil {
		t.Errorf("QueueGroup of the primary group: %v", err)
	}
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.Class(9), time.Time{}); err == nil {
		t.Error("an invalid class was accepted")
	}
	err := r.svc.QueueGroup(ctx, "nobody/nowhere~3", groupSide, governor.ClassKeepAlive, time.Time{})
	if !errors.Is(err, errTestUnknown) {
		t.Errorf("unknown reference: %v, want Config.UnknownEntity", err)
	}
}

func TestRescheduleGroupQueuesClass3ForAMemberAndClass4OtherwiseAndNeverDowngradesClass2(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	r.putQuery("mine", "ent-member")
	r.seedPrimary(ent1, "p")
	r.seedPrimary("ent-member", "p")
	hour := r.clk.Now().Add(time.Hour)

	if err := r.svc.RescheduleGroup(ctx, ent1, groupSide, hour); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RescheduleGroup(ctx, "ent-member", groupSide, hour); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassKeepAlive {
		t.Errorf("class %v queued %v, want class 4", c, queued)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, "ent-member", groupSide); !queued || c != governor.ClassQuery {
		t.Errorf("member: class %v queued %v, want class 3", c, queued)
	}

	// A queued class 2 task is neither downgraded nor postponed.
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RescheduleGroup(ctx, ent1, groupSide, r.clk.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassInvalidated {
		t.Fatalf("class %v queued %v, want the class 2 task kept", c, queued)
	}
	if err := r.svc.RescheduleGroup(ctx, ent1, "bogus", hour); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("an unknown group: %v", err)
	}
	if err := r.svc.RescheduleGroup(ctx, "nobody/nowhere~3", groupSide, hour); !errors.Is(err, errTestUnknown) {
		t.Errorf("an unknown reference: %v", err)
	}
}

func TestTheEntityKindComesFromTheSpec(t *testing.T) {
	st := openStore(t)
	alpha := newRig(t, specAlpha, st, nil)
	beta := newRig(t, specBeta, st, alpha.clk)
	ctx := context.Background()
	alpha.seedPrimary(ent1, "p")

	// The same id names nothing of kind beta: it is an unseen entity there.
	if err := beta.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
		t.Fatal(err)
	}
	if _, queued, _, _ := beta.svc.Inspect(ctx, ent1, groupSide); queued {
		t.Error("an entity of kind alpha was queued by the kind beta service")
	}
	if err := alpha.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
		t.Fatal(err)
	}
	if _, queued, _, _ := alpha.svc.Inspect(ctx, ent1, groupSide); !queued {
		t.Error("the kind alpha service did not queue its own entity")
	}
	if _, ok, err := st.GetGroup(ctx, "beta", ent1, groupSide); err != nil || ok {
		t.Errorf("a kind beta group row exists (%v, %v)", ok, err)
	}
	if row, ok := alpha.group(ent1, groupSide); !ok || row.Generation != 1 {
		t.Errorf("kind alpha group row = %+v (%v)", row, ok)
	}
}
