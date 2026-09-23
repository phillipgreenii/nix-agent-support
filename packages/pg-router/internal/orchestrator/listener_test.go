package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// TestRoleListener_RetryBackoff_roleOverrideSelected is Task 1.3's first
// required RED test: a role carrying its OWN non-zero backoff.Policy (a
// [role.retry] override — config decode already merges it onto the pool
// default, so a config-decoded role's RetryBackoff field is never zero even
// unconfigured; a hand-built Role literal here stands in for that) is
// selected over the pool-wide default injected at construction.
func TestRoleListener_RetryBackoff_roleOverrideSelected(t *testing.T) {
	pool := backoff.Policy{Initial: time.Second, Factor: 2, Max: time.Minute}
	override := backoff.Policy{Initial: 3 * time.Second, Factor: 3, Max: 5 * time.Minute}
	o := &Orchestrator{Cfg: config.Config{RetryBackoff: pool}}
	role := roles.Role{Name: "custom", RetryBackoff: override}

	l := o.NewListener(context.Background(), role)
	bl, ok := l.(eventqueue.BackoffListener)
	if !ok {
		t.Fatalf("roleListener returned by NewListener does not implement eventqueue.BackoffListener")
	}
	if got := bl.RetryBackoff(); got != override {
		t.Fatalf("RetryBackoff() = %+v, want the role's own override %+v", got, override)
	}
}

// TestRoleListener_RetryBackoff_builtinKeepsPoolCadence is Task 1.3's second
// required RED test: a role carrying the ZERO backoff.Policy — every built-in
// role, per roles.BuiltinRoleSet, which never sets RetryBackoff at all — MUST
// keep the injected pool-wide [pool.retry] cadence, never fall through to
// backoff.Default() via backoff.Policy.Duration's own sanitized(). pool is
// deliberately chosen to differ from backoff.Default()'s own values, so a
// silent fall-through to the package default would be observable.
func TestRoleListener_RetryBackoff_builtinKeepsPoolCadence(t *testing.T) {
	pool := backoff.Policy{Initial: 3 * time.Second, Factor: 3, Max: 5 * time.Minute}
	if pool == backoff.Default() {
		t.Fatalf("test fixture bug: pool %+v must differ from backoff.Default() %+v", pool, backoff.Default())
	}
	o := &Orchestrator{Cfg: config.Config{RetryBackoff: pool}}
	role := roles.Role{Name: "feedback"} // zero-value RetryBackoff, as every built-in role carries

	l := o.NewListener(context.Background(), role)
	bl, ok := l.(eventqueue.BackoffListener)
	if !ok {
		t.Fatalf("roleListener returned by NewListener does not implement eventqueue.BackoffListener")
	}
	if got := bl.RetryBackoff(); got != pool {
		t.Fatalf("RetryBackoff() = %+v, want the injected pool default %+v", got, pool)
	}
}

// TestRoleListener_OfferAlwaysAccepts is Task 2.2's required RED test for the
// production roleListener's Offer rewrite: today (this task) it always
// reports OfferResult{Accepted: true, Decline: eventqueue.DeclineNone} — the
// same "always accepts" behavior as before Task 2.2, just through the new
// Offering/OfferResult signature. Task 2.3 is what adds a genuine decline.
// As of docket pg2-oju6w's Task 5.4, Offer's own dispatch call goes through
// the wire client (fakeHandler here) rather than the retired in-process
// executor, so this asserts on the fake's own recorded call instead of
// cc.Sent.
func TestRoleListener_OfferAlwaysAccepts(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	handler := o.Handler.(*fakeHandler)
	ctx := context.Background()
	l := o.NewListener(ctx, workerRole(o))

	evt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if handler.callCount() != 1 {
		t.Fatalf("Offer must dispatch the worker event via the wire client exactly once; calls=%d", handler.callCount())
	}
	if handler.calls[0].role.Name != "worker" || handler.calls[0].evt.ID != evt.ID {
		t.Fatalf("dispatched call = %+v, want role=worker evt.ID=%q", handler.calls[0], evt.ID)
	}
}

// TestListenerOffer_HandlerBusyMapsToDeclineBusy is Task 2.3's required RED
// test (Step 2.3.2), updated for Task 5.4's wire client: a registered
// handler participant whose dispatch subcommand exits busy (DEC-WIRE-1's
// exit code 9, wireclient.ErrBusy — the wire successor to the retired
// in-process executor.ErrBusy) must make Offer report a pre-accept
// DeclineBusy rather than treating the dispatch as completed.
func TestListenerOffer_HandlerBusyMapsToDeclineBusy(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: wireclient.ErrBusy}
	role := roles.Role{Name: "cmdrole", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
}

// TestListenerOffer_HandlerBusyWithReasonForwardsDeclineDetail proves Offer
// forwards a *wireclient.BusyDecline's Reason verbatim into
// OfferResult.DeclineDetail (bead pg2-j4uwg), while Decline itself stays
// eventqueue.DeclineBusy exactly as the bare-ErrBusy case above
// (TestListenerOffer_HandlerBusyMapsToDeclineBusy) — this package interprets
// nothing about what the reason string means.
func TestListenerOffer_HandlerBusyWithReasonForwardsDeclineDetail(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: &wireclient.BusyDecline{Reason: "capacity-unknown"}}
	role := roles.Role{Name: "cmdrole", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy, DeclineDetail: "capacity-unknown"}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
}

// TestListenerOffer_UnavailableSelfStatusDeclines is Task 2.3's required RED
// test (Step 2.3.3): a role whose registry entry self-reports `unavailable`
// must make Offer decline BEFORE doing any dispatch work at all (no wire
// dispatch sent), never reaching the handler.
func TestListenerOffer_UnavailableSelfStatusDeclines(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	handler := o.Handler.(*fakeHandler)
	role := workerRole(o)

	reg := core.NewRegistry(nil)
	if _, err := reg.RegisterInProcess(role.Name, core.KindHandler); err != nil {
		t.Fatalf("RegisterInProcess: %v", err)
	}
	if err := reg.SetLifecycle(role.Name, conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if err := reg.SetSelfStatus(role.Name, core.SelfUnavailable); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	o.Registry = reg

	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineUnavailable}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if handler.callCount() != 0 {
		t.Fatalf("Offer must not dispatch while self-status is unavailable; calls=%d", handler.callCount())
	}
}

// fakeHandlerFailureObserver is a scripted orchestrator.HandlerFailureObserver
// test double recording every OnHandlerFailure call.
type fakeHandlerFailureObserver struct {
	calls []struct{ eventID, evtType, listenerID string }
}

func (f *fakeHandlerFailureObserver) OnHandlerFailure(eventID, evtType, listenerID string) {
	f.calls = append(f.calls, struct{ eventID, evtType, listenerID string }{eventID, evtType, listenerID})
}

// TestRoleListener_Offer_NonBusyHandlerErrorNotifiesHandlerFailureObserver is
// bead pg2-97539's required RED test: a registered handler participant whose
// dispatch returns a genuine, non-panic, non-busy error (e.g. wireclient:
// role "review" exited 1: ...) must notify o.HandlerFailureObserver — the
// gap this bead closes, since neither eventqueue.Observer.OnDeclined (a
// pre-accept decline, never reached here) nor OnDispatchFailure (fed only
// from a recovered panic, never a plain error return) can ever observe this
// case. Offer must still report Accepted: true (ADR 0056's "always reports
// acceptance" is unchanged by this bead).
func TestRoleListener_Offer_NonBusyHandlerErrorNotifiesHandlerFailureObserver(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	dispatchErr := fmt.Errorf(`wireclient: role "review" exited 1: bead zr-w1: session exited before completing`)
	o.Handler = &fakeHandler{err: dispatchErr}
	obs := &fakeHandlerFailureObserver{}
	o.HandlerFailureObserver = obs
	role := roles.Role{Name: "cmdrole", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v (a non-busy handler error is still an ACCEPT)", got, want)
	}
	if len(obs.calls) != 1 {
		t.Fatalf("OnHandlerFailure call count = %d, want 1", len(obs.calls))
	}
	if obs.calls[0].eventID != evt.ID || obs.calls[0].evtType != evt.Type {
		t.Fatalf("OnHandlerFailure(%+v), want eventID=%q evtType=%q", obs.calls[0], evt.ID, evt.Type)
	}
}

// TestRoleListener_Offer_HandlerFailurePassesListenerID is this task's
// required RED test: OnHandlerFailure's newly widened listenerID parameter
// (mirroring eventqueue.Observer.OnDeclined's own listenerID) must carry the
// role name that produced the failure, so pg-router can attribute a handler
// failure to the listener that dispatched it.
func TestRoleListener_Offer_HandlerFailurePassesListenerID(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: fmt.Errorf("boom")}
	obs := &fakeHandlerFailureObserver{}
	o.HandlerFailureObserver = obs
	role := roles.Role{Name: "df-feedback", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	_ = l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	if len(obs.calls) != 1 {
		t.Fatalf("OnHandlerFailure call count = %d, want 1", len(obs.calls))
	}
	if obs.calls[0].listenerID != "df-feedback" {
		t.Fatalf("OnHandlerFailure listenerID = %q, want %q", obs.calls[0].listenerID, "df-feedback")
	}
}

// TestRoleListener_Offer_BusyHandlerErrorDoesNotNotifyHandlerFailureObserver
// proves wireclient.ErrBusy is deliberately EXCLUDED from
// HandlerFailureObserver (HandlerFailureObserver's own doc comment): that
// case already lands on FailureClassDeclined via the pre-accept
// eventqueue.Observer.OnDeclined path, so notifying HandlerFailureObserver
// too would double-count it under a second class.
func TestRoleListener_Offer_BusyHandlerErrorDoesNotNotifyHandlerFailureObserver(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: wireclient.ErrBusy}
	obs := &fakeHandlerFailureObserver{}
	o.HandlerFailureObserver = obs
	role := roles.Role{Name: "cmdrole", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if len(obs.calls) != 0 {
		t.Fatalf("OnHandlerFailure must not fire for wireclient.ErrBusy (already counted as FailureClassDeclined); calls=%+v", obs.calls)
	}
}

// TestRoleListener_Offer_NoHandlerFailureObserverIsSafe proves the
// nil-means-no-op idiom this hook follows (matching
// TestRoleListener_Offer_NoResourceLimitObserverIsSafe's sibling pattern): a
// role listener built with no HandlerFailureObserver configured must not
// panic on a non-busy handler error.
func TestRoleListener_Offer_NoHandlerFailureObserverIsSafe(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: fmt.Errorf("boom")}
	// o.HandlerFailureObserver deliberately left nil.
	role := roles.Role{Name: "cmdrole", Binds: []string{"work-ready"}}
	ctx := context.Background()
	l := o.NewListener(ctx, role)

	evt := discover.ToQueueEvent(event.NewItemEvent("work-ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt}) // must not panic

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
}

// TestRoleListener_Offer_NoResourceLimitObserverIsSafe proves the nil-means-
// no-op idiom this hook follows (matching l.reg's own doc): a role listener
// built with no ResourceLimitObserver configured — every construction site
// today, since the wire protocol has no resource-limit signal for Offer to
// detect at all as of docket pg2-oju6w's Task 5.4 (ResourceLimitObserver's
// own doc comment) — must not panic on an ordinary successful dispatch.
func TestRoleListener_Offer_NoResourceLimitObserverIsSafe(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	// o.ResourceLimitObserver deliberately left nil.

	ctx := context.Background()
	l := o.NewListener(ctx, workerRole(o))
	evt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt}) // must not panic

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
}
