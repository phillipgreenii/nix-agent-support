package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Offer feeds RoleProgressObserver for the per-role progress gauges (docket
// pg2-12gr4.8, design sections 5 and 10). internal/metrics classifies what each
// call counts as (and tests it); these tests pin WHEN Offer makes which call.

type progressCall struct {
	kind string // "succeeded" | "failed" | "declined"
	role string
	err  error
	dk   string
}

type fakeProgressObserver struct {
	mu    sync.Mutex
	calls []progressCall
}

func (f *fakeProgressObserver) OnRoleSucceeded(role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, progressCall{kind: "succeeded", role: role})
}

func (f *fakeProgressObserver) OnRoleFailed(role string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, progressCall{kind: "failed", role: role, err: err})
}

func (f *fakeProgressObserver) OnRoleDeclined(role, kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, progressCall{kind: "declined", role: role, dk: kind})
}

func (f *fakeProgressObserver) snapshot() []progressCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]progressCall(nil), f.calls...)
}

func offerWithProgress(t *testing.T, h wireclient.HandlerClient, maxRetries int, ctx context.Context) (eventqueue.Listener, *fakeProgressObserver) {
	t.Helper()
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	obs := &fakeProgressObserver{}
	o.RoleProgressObserver = obs
	role := roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: maxRetries}
	return o.NewListener(ctx, role), obs
}

func TestRoleProgress_OfferCleanFinishIsProgress(t *testing.T) {
	l, obs := offerWithProgress(t, &seqHandler{}, 0, context.Background())
	l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	got := obs.snapshot()
	if len(got) != 1 || got[0].kind != "succeeded" || got[0].role != "desk-pr" {
		t.Fatalf("calls = %+v, want one OnRoleSucceeded(desk-pr)", got)
	}
}

func TestRoleProgress_OfferHandlerErrorIsReportedWithTheError(t *testing.T) {
	boom := errors.New("wireclient: role desk-pr exited 1: boom")
	l, obs := offerWithProgress(t, &seqHandler{errs: []error{boom}}, 0, context.Background())
	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")}); !got.Accepted {
		t.Fatalf("a handler error is still an accept, got %+v", got)
	}
	got := obs.snapshot()
	if len(got) != 1 || got[0].kind != "failed" || !errors.Is(got[0].err, boom) {
		t.Fatalf("calls = %+v, want one OnRoleFailed carrying the error", got)
	}
}

func TestRoleProgress_OfferDeclineReportsTheRawKind(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		kind string
	}{
		"fault":        {&wireclient.BusyDecline{Reason: "launch-failed", Kind: "fault"}, "fault"},
		"neutral":      {&wireclient.BusyDecline{Kind: "neutral"}, "neutral"},
		"busy":         {&wireclient.BusyDecline{Kind: "busy"}, "busy"},
		"unknown kind": {&wireclient.BusyDecline{Kind: "explode"}, "explode"},
		"absent kind":  {&wireclient.BusyDecline{Reason: "r"}, ""},
		"bare busy":    {wireclient.ErrBusy, ""},
	} {
		t.Run(name, func(t *testing.T) {
			l, obs := offerWithProgress(t, &seqHandler{errs: []error{tc.err}}, 0, context.Background())
			l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
			got := obs.snapshot()
			if len(got) != 1 || got[0].kind != "declined" || got[0].dk != tc.kind || got[0].role != "desk-pr" {
				t.Fatalf("calls = %+v, want one OnRoleDeclined(desk-pr, %q)", got, tc.kind)
			}
		})
	}
}

// A shutdown hand-back is not counted at all.
func TestRoleProgress_OfferShutdownHandBackIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l, obs := offerWithProgress(t, &seqHandler{errs: []error{killedErr}}, 3, ctx)
	cancel()
	got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	if got.DeclineDetail != DeclineDetailShutdown {
		t.Fatalf("Offer() = %+v, want the shutdown hand-back", got)
	}
	if calls := obs.snapshot(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none for a shutdown hand-back", calls)
	}
}

// A dispatch-retry hand-back is counted once: the failed attempt is one
// OnRoleFailed, and the hand-back adds nothing.
func TestRoleProgress_OfferDispatchRetryHandBackIsCountedOnce(t *testing.T) {
	l, obs := offerWithProgress(t, &seqHandler{errs: []error{killedErr}}, 2, context.Background())
	got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	if got.DeclineDetail != DeclineDetailDispatchRetry {
		t.Fatalf("Offer() = %+v, want the dispatch-retry hand-back", got)
	}
	calls := obs.snapshot()
	if len(calls) != 1 || calls[0].kind != "failed" {
		t.Fatalf("calls = %+v, want exactly one OnRoleFailed for the hand-back", calls)
	}
	// The retried attempt then succeeds: one more call, progress.
	l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	calls = obs.snapshot()
	if len(calls) != 2 || calls[1].kind != "succeeded" {
		t.Fatalf("calls = %+v, want the retry's success as the second call", calls)
	}
}

// The unavailable path calls no handler and moves no counter (a stated gap in
// the design, recorded in RoleProgressObserver's doc).
func TestRoleProgress_OfferUnavailableMovesNothing(t *testing.T) {
	cfg := fastCfg(t)
	o := newOrch(cfg, testQuerySet(nil, nil))
	obs := &fakeProgressObserver{}
	o.RoleProgressObserver = obs
	role := workerRole(o)
	o.Registry = unavailableRegistry(t, role.Name)
	l := o.NewListener(context.Background(), role)
	evt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}))
	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}); got.Decline != eventqueue.DeclineUnavailable {
		t.Fatalf("Offer() = %+v, want an unavailable decline", got)
	}
	if calls := obs.snapshot(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none for an unavailable role", calls)
	}
}

func TestRoleProgress_NoObserverIsSafe(t *testing.T) {
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = &seqHandler{errs: []error{errors.New("boom"), &wireclient.BusyDecline{Kind: "fault"}}}
	l := o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}})
	l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e2")})
	l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e3")})
}
