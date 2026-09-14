package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// deferringListener blocks inside Offer until told to proceed -- standing in
// for "a handler role configured to defer" (ADR 0065's Acceptance section;
// [design: Task 5.11 Test, Step 1]). It mirrors
// internal/eventqueue's own Task 2.2 double (concurrency_test.go's
// blockingListener, exercised by TestDispatch_CustodyPinnedDuringBlockingOffer):
// closing `entered` the instant it starts blocking gives the test a
// provably-outstanding offer to read status-shaped state against, exactly
// as that pinned acceptance test does one package down.
type deferringListener struct {
	id      string
	typ     string
	proceed chan struct{}
	entered chan struct{}
}

func (l *deferringListener) ID() string                      { return l.id }
func (l *deferringListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }
func (l *deferringListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	close(l.entered)
	<-l.proceed
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

// TestBanner_GatedWithLiveSessionInFlightReadsHaltedNotQuiescent is this
// docket's Step 1 acceptance test (ADR 0065's Acceptance section: "A gated
// core with one deferred session outstanding reports sessionsInFlight >= 1
// and the status/TUI banner reads halted, not quiescent"; [design: Task
// 5.11 Test, Step 1]).
//
// Task 4.6's own TestBanner_MutuallyExclusiveHeaderVsPaused (banner_test.go)
// already pins gated-wins-over-quiescing against entirely SYNTHETIC
// StatusReply/Delivery values. This test is the docket's live-dispatch
// proof of the same rule: a REAL eventqueue.Queue dispatches one event to a
// handler role that defers its reply (blocks inside Offer, never settling
// custody until told to proceed); while that session is outstanding,
// Queue.SessionsInFlight() -- "the eventual 'N in flight' the status banner
// surfaces" (queue.go's own doc) -- reports >= 1, and a StatusReply carrying
// a set gate plus that SAME live count renders the "PAUSED -- dispatch
// halted" banner via renderTopZone, never the quiescing wording, even when
// quiescing is ALSO true (INV-LIFE-2's mutual exclusivity: gated always
// wins).
func TestBanner_GatedWithLiveSessionInFlightReadsHaltedNotQuiescent(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	l := &deferringListener{id: "h", typ: "T", proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "T", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Dispatch()
	}()

	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("deferring handler never entered its blocking wait (timeout)")
	}

	inFlight := q.SessionsInFlight()
	if inFlight < 1 {
		t.Fatalf("SessionsInFlight() = %d while the deferring handler's session is outstanding, want >= 1", inFlight)
	}

	reply := StatusReply{
		Core:       CoreInfo{State: "started"},
		Gates:      []Gate{{Name: core.GateOperatorPaused, Set: true}},
		Deliveries: make([]Delivery, inFlight),
	}
	got := renderTopZone(topZoneData{
		reply:     reply,
		quiescing: true, // both conditions hold at once: gated must still win.
		width:     120,
		theme:     render.NewTheme(false),
	})
	if !strings.Contains(got, "dispatch halted") {
		t.Errorf("expected the halted banner with a real session in flight; got:\n%s", got)
	}
	if strings.Contains(got, "quiescing —") {
		t.Errorf("halted must win over quiescing even with a real session in flight; got:\n%s", got)
	}
	if want := fmt.Sprintf("%d in flight", inFlight); !strings.Contains(got, want) {
		t.Errorf("expected the banner's in-flight count (%q) to reflect the live SessionsInFlight() value; got:\n%s", want, got)
	}

	close(l.proceed)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch did not return after the deferring handler proceeded (timeout)")
	}
	if got := q.SessionsInFlight(); got != 0 {
		t.Fatalf("SessionsInFlight() = %d after Dispatch returned, want 0 (custody settled)", got)
	}
}
