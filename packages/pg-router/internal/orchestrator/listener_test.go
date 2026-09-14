package orchestrator

import (
	"context"
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
