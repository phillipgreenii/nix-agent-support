package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// A handler reply that completes the event as skipped (the optional `skipped`
// reason of handler.dispatch-reply) is an ordinary ACCEPT for the queue: the event
// settles on its first offer and is never offered again, so a dead event at the head
// of the serial per-handler queue cannot block the events behind it. The router
// counts it apart from a real completion (SkippedObserver) and does not treat it as
// role progress. A decline of the same event, by contrast, is re-offered.

type recordingSkippedObserver struct {
	mu    sync.Mutex
	calls []skippedCall
}

type skippedCall struct{ eventID, evtType, listenerID, reason string }

func (r *recordingSkippedObserver) OnSkipped(eventID, evtType, listenerID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, skippedCall{eventID, evtType, listenerID, reason})
}

func (r *recordingSkippedObserver) snapshot() []skippedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]skippedCall(nil), r.calls...)
}

// scriptedReplyHandler answers each event by id, recording every call in order.
type scriptedReplyHandler struct {
	mu      sync.Mutex
	replies map[string]wireclient.Reply
	calls   []string
}

func (h *scriptedReplyHandler) Dispatch(_ context.Context, _ roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, evt.ID)
	return h.replies[evt.ID], nil
}

func (h *scriptedReplyHandler) PostStartup(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (h *scriptedReplyHandler) PreShutdown(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (h *scriptedReplyHandler) callList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func TestSkippedCompletion_SettlesOnFirstOfferAndTheNextEventIsDispatched(t *testing.T) {
	h := &scriptedReplyHandler{replies: map[string]wireclient.Reply{
		"dead-head": {ID: "dsp-1", Outcome: `{"actions":[{"verb":"skipped","refs":[{"type":"bead","id":"pg2-dead"}]}]}`, Skipped: "skipped-bead-closed"},
		"live-next": {ID: "dsp-2", Outcome: "ok"},
	}}
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	skipObs := &recordingSkippedObserver{}
	o.SkippedObserver = skipObs
	progObs := &fakeProgressObserver{}
	o.RoleProgressObserver = progObs

	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	role := roles.Role{Name: "triager", Binds: []string{"escalated.pg2"}}
	q.Register(o.NewListener(context.Background(), role))
	exp := time.Now().Add(time.Hour)
	for _, id := range []string{"dead-head", "live-next"} {
		if _, err := q.Enqueue(eventqueue.Event{ID: id, Type: "escalated.pg2", ExpiresAt: exp, Payload: map[string]any{"id": id}}); err != nil {
			t.Fatal(err)
		}
	}

	// Pass 1: the closed-bead event at the head is offered once and ACCEPTED.
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("pass 1 accepted %d, want 1 (the moot head settles on its first offer)", n)
	}
	// Pass 2: the head is not re-offered; the next event is the one dispatched.
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("pass 2 accepted %d, want 1 (the event behind the moot head)", n)
	}
	// Pass 3: nothing is left to offer.
	if n := q.Dispatch(); n != 0 {
		t.Fatalf("pass 3 accepted %d, want 0", n)
	}
	if got, want := h.callList(), []string{"dead-head", "live-next"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("handler offers = %v, want exactly %v (one offer per event, no re-offer of the moot head)", got, want)
	}

	// Only the skipped completion reached the SkippedObserver, with its reason.
	sk := skipObs.snapshot()
	if len(sk) != 1 || sk[0] != (skippedCall{"dead-head", "escalated.pg2", "triager", "skipped-bead-closed"}) {
		t.Fatalf("OnSkipped calls = %+v, want one for dead-head with reason skipped-bead-closed", sk)
	}
	// A skipped completion is not role progress; the real one is.
	pc := progObs.snapshot()
	if len(pc) != 1 || pc[0].kind != "succeeded" {
		t.Fatalf("progress calls = %+v, want exactly one OnRoleSucceeded (the live event), none for the skip", pc)
	}
}

// A reply without a skipped reason is not a skipped completion: the observer hears
// nothing.
func TestSkippedCompletion_OnlyACleanReplyWithAReasonCounts(t *testing.T) {
	for name, reply := range map[string]wireclient.Reply{
		"real completion": {ID: "dsp-1", Outcome: "ok"},
		"empty":           {},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOrch(fastCfg(t), testQuerySet(nil, nil))
			o.Handler = &scriptedReplyHandler{replies: map[string]wireclient.Reply{"e1": reply}}
			skipObs := &recordingSkippedObserver{}
			o.SkippedObserver = skipObs
			l := o.NewListener(context.Background(), roles.Role{Name: "triager", Binds: []string{"escalated.pg2"}})
			if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: eventqueue.Event{ID: "e1", Type: "escalated.pg2", Payload: map[string]any{"id": "e1"}}}); !got.Accepted {
				t.Fatalf("Offer = %+v, want accepted", got)
			}
			if sk := skipObs.snapshot(); len(sk) != 0 {
				t.Fatalf("OnSkipped calls = %+v, want none", sk)
			}
		})
	}
}

// No configured SkippedObserver is a no-op, not a crash, and the offer is still an
// accept.
func TestSkippedCompletion_NoObserverIsSafe(t *testing.T) {
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = &scriptedReplyHandler{replies: map[string]wireclient.Reply{"e1": {ID: "dsp-1", Outcome: "x", Skipped: "skipped-pr-merged"}}}
	l := o.NewListener(context.Background(), roles.Role{Name: "triager", Binds: []string{"escalated.pg2"}})
	got := l.Offer(eventqueue.Offering{ID: "dsp", Event: eventqueue.Event{ID: "e1", Type: "escalated.pg2", Payload: map[string]any{"id": "e1"}}})
	if want := (eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}); got != want {
		t.Fatalf("Offer = %+v, want %+v", got, want)
	}
}
