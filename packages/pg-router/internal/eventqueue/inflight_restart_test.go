package eventqueue

import (
	"context"
	"sync"
	"testing"
	"time"
)

// An offer that is still outstanding when its process dies (a handler running
// a long dispatch across a daemon restart) writes no accept record, because
// the durable accept is appended only when the offer settles (phase 3). The
// next process therefore replays the event UN-accepted and re-offers it: the
// mechanism ADR 0085 relies on to let a dispatch outlive the daemon that
// started it.
func TestInFlightOfferAtProcessDeathReplaysUnacceptedAndIsReoffered(t *testing.T) {
	mem := NewMemStore()
	q1, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	l1 := newKickSequentialListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))

	entered, release := l1.arm()
	t.Cleanup(release)
	if got := q1.Kick(); got != 1 {
		t.Fatalf("Kick launched %d offers, want 1", got)
	}
	<-entered
	if q1.SessionsInFlight() != 1 {
		t.Fatalf("SessionsInFlight = %d, want 1: the offer must be outstanding when the process dies", q1.SessionsInFlight())
	}

	// "Restart": a fresh queue over the same durable log, while q1's offer is
	// still blocked (its handler is still running).
	q2, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("the in-flight event was lost across the restart: %v", q2.DepthByType())
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	q2.Dispatch()
	if !equal(l2.offered, []string{"e1"}) {
		t.Fatalf("offered after restart = %v, want [e1]: an unsettled offer must be redelivered", l2.offered)
	}
}

// An already-expired event whose offer is in flight is NOT retired by Expire:
// the bound listener is not settled for it yet, so it stays retained and is
// still replayable (the born-expired default makes every event expired).
func TestInFlightOfferKeepsExpiredEventRetainedAcrossExpire(t *testing.T) {
	mem := NewMemStore()
	q1, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	l1 := newKickSequentialListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(-time.Minute)))

	entered, release := l1.arm()
	t.Cleanup(release)
	q1.Kick()
	<-entered
	if dropped := q1.Expire(); dropped != 0 {
		t.Fatalf("Expire dropped %d event(s) with an offer in flight, want 0", dropped)
	}

	q2, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("the in-flight, expired event was not restored: %v", q2.DepthByType())
	}
}

func TestWaitForInFlightDrainExcept(t *testing.T) {
	setup := func(t *testing.T) (q *Queue, survivor, other *kickSequentialListener) {
		t.Helper()
		q, err := New(NewMemStore())
		if err != nil {
			t.Fatal(err)
		}
		survivor = newKickSequentialListener("survivor", "S")
		other = newKickSequentialListener("other", "O")
		q.Register(survivor)
		q.Register(other)
		mustEnqueue(t, q, evtUntil("s1", "S", time.Now().Add(time.Hour)))
		return q, survivor, other
	}
	ignoreSurvivor := func(id string) bool { return id == "survivor" }

	t.Run("ignores an offer that is meant to outlive the process", func(t *testing.T) {
		q, survivor, _ := setup(t)
		entered, release := survivor.arm()
		t.Cleanup(release)
		q.Kick()
		<-entered

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		if !q.WaitForInFlightDrainExcept(ctx, time.Millisecond, ignoreSurvivor) {
			t.Fatal("drain reported outstanding offers although only the ignored listener is in flight")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("drain took %v: it waited on the ignored offer", time.Since(start))
		}
	})

	t.Run("a nil ignore is the plain drain and times out on the same offer", func(t *testing.T) {
		q, survivor, _ := setup(t)
		entered, release := survivor.arm()
		t.Cleanup(release)
		q.Kick()
		<-entered

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if q.WaitForInFlightDrainExcept(ctx, time.Millisecond, nil) {
			t.Fatal("a nil ignore must still wait on every offer")
		}
	})

	t.Run("still waits for an offer that is not ignored", func(t *testing.T) {
		q, survivor, other := setup(t)
		mustEnqueue(t, q, evtUntil("o1", "O", time.Now().Add(time.Hour)))
		sEntered, sRelease0 := survivor.arm()
		sRelease := sync.OnceFunc(sRelease0)
		oEntered, oRelease0 := other.arm()
		oRelease := sync.OnceFunc(oRelease0)
		t.Cleanup(sRelease)
		t.Cleanup(oRelease)
		q.Kick()
		<-sEntered
		<-oEntered

		short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancelShort()
		if q.WaitForInFlightDrainExcept(short, time.Millisecond, ignoreSurvivor) {
			t.Fatal("drain returned while a non-ignored offer was still in flight")
		}

		oRelease()
		long, cancelLong := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelLong()
		if !q.WaitForInFlightDrainExcept(long, time.Millisecond, ignoreSurvivor) {
			t.Fatal("drain did not complete once the non-ignored offer settled")
		}
	})
}
