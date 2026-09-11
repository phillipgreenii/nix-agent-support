package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/ccpool"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/dtest"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/usage"
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
func TestRoleListener_OfferAlwaysAccepts(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w1": {"closed"}}}
	ext1 := "pg-router-worker-zr-w1-" + dtest.TestStamp
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: ext1, Live: true, State: ccpool.StateWorking},
	}}}
	o := newOrch(cc, bd, cfg)
	ctx := context.Background()
	l := o.NewListener(ctx, workerRole(o))

	evt := discover.ToQueueEvent(event.NewItemEvent(roles.EventWorkReady, "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if len(cc.Sent) != 1 || cc.Sent[0] != ext1 {
		t.Fatalf("Offer did not dispatch the worker session; sent=%v", cc.Sent)
	}
}

// fakeExitCommander is a query.Commander test double returning a fixed error
// from every call — used to hand a command role's backing command a
// fabricated *exec.ExitError (Task 2.3, pg2-84o3m.22's "test doubles
// fabricate an *exec.ExitError").
type fakeExitCommander struct{ err error }

func (f fakeExitCommander) Run(_ context.Context, _ []string) ([]byte, error) { return nil, f.err }

// fabricateExitError runs a trivial subprocess that exits with code so the
// test gets back a REAL *exec.ExitError — os/exec.ExitError has no exported
// constructor, so a genuine short-lived process is the only portable way to
// produce one.
func fabricateExitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("fabricateExitError(%d): did not produce *exec.ExitError; err=%v", code, err)
	}
	return exitErr
}

// TestListenerOffer_CommandExitBusyMapsToDeclineBusy is Task 2.3's required
// RED test (Step 2.3.2): a command role whose backing command exits busy
// (code 9, executor.ErrBusy) must make Offer report a pre-accept DeclineBusy
// rather than treating the dispatch as completed.
func TestListenerOffer_CommandExitBusyMapsToDeclineBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{}
	o := newOrch(cc, bd, cfg)
	o.Cmd = fakeExitCommander{err: fabricateExitError(t, 9)}
	role := roles.Role{
		Name: "cmdrole", Type: "command", Binds: []string{"work-ready"},
		Command: &roles.CommandConfig{Argv: []string{"noop"}},
	}
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
// must make Offer decline BEFORE doing any dispatch work at all (no session
// sent), never reaching the executor.
func TestListenerOffer_UnavailableSelfStatusDeclines(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{}
	o := newOrch(cc, bd, cfg)
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

	evt := discover.ToQueueEvent(event.NewItemEvent(roles.EventWorkReady, "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineUnavailable}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if len(cc.Sent) != 0 {
		t.Fatalf("Offer must not dispatch while self-status is unavailable; sent=%v", cc.Sent)
	}
}

// fakeResourceLimitObserver is a test double for ResourceLimitObserver
// (this bead, pg2-fm2gw): mutex-guarded since it is exercised from the
// watchdog/waitDone race (workerWaitWithWatchdog runs both concurrently).
type fakeResourceLimitObserver struct {
	mu    sync.Mutex
	calls [][2]string // {eventID, evtType} per call, in order
}

func (f *fakeResourceLimitObserver) OnResourceLimit(eventID, evtType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{eventID, evtType})
}

// TestRoleListener_Offer_ResourceLimitHitFiresHookAndStillAccepts is this
// bead's (pg2-fm2gw) required RED test for acceptance criterion 1:
// roleListener.Offer exposes a hook that reports a resource-limit outcome.
// It replays executor/ccpool_test.go's TestDispatch_watchdogHardStop_unclaimed
// recipe (a finite BudgetTokens cap + a RampReader immediately over 100%,
// with the SAME Tick-wraps-a-real-sleep fix that test documents, so the
// watchdog reliably wins its race against waitDone) through Offer rather
// than the bare executor, and asserts TWO things at once: Offer still
// reports Accepted=true (a budget hard-stop is a genuine accept — the
// dispatch ran to completion inline, INV-EVT-1 — never a decline), and
// o.ResourceLimitObserver.OnResourceLimit fired exactly once, carrying the
// offered event's own id and type.
func TestRoleListener_Offer_ResourceLimitHitFiresHookAndStillAccepts(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the ramp trips it
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w1": {"in_progress"}}}
	ext1 := "pg-router-worker-zr-w1-" + dtest.TestStamp
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: ext1, Live: true, TranscriptPath: "/t", CWD: "/repo"},
	}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately >100%

	// Same wall-clock-vs-manual-clock fix as
	// executor/ccpool_test.go's TestDispatch_watchdogHardStop_unclaimed: o.tick
	// advances a manual clock with NO real sleep while the watchdog's own Poll
	// wait is a real time.After, so without this the two racers can run on
	// mismatched clocks and waitDone occasionally wins instead.
	origTick := o.tick
	o.tick = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return origTick(ctx, d)
	}

	fake := &fakeResourceLimitObserver{}
	o.ResourceLimitObserver = fake

	ctx := context.Background()
	l := o.NewListener(ctx, workerRole(o))
	evt := discover.ToQueueEvent(event.NewItemEvent(roles.EventWorkReady, "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt})

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v (a budget hard-stop is a genuine accept, never a decline)", got, want)
	}

	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("OnResourceLimit calls = %d, want exactly 1; calls=%v", len(calls), calls)
	}
	if want := [2]string{evt.ID, evt.Type}; calls[0] != want {
		t.Fatalf("OnResourceLimit call = %v, want {eventID, evtType} = %v", calls[0], want)
	}
}

// TestRoleListener_Offer_NoResourceLimitObserverIsSafe proves the nil-means-
// no-op idiom this hook follows (matching l.reg's own doc): a role listener
// built with no ResourceLimitObserver configured (every pre-this-bead
// construction site, and most of this file's own tests) must not panic
// when the SAME budget hard-stop that would otherwise fire the hook occurs.
func TestRoleListener_Offer_NoResourceLimitObserverIsSafe(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w1": {"in_progress"}}}
	ext1 := "pg-router-worker-zr-w1-" + dtest.TestStamp
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: ext1, Live: true, TranscriptPath: "/t", CWD: "/repo"},
	}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}}
	origTick := o.tick
	o.tick = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return origTick(ctx, d)
	}
	// o.ResourceLimitObserver deliberately left nil.

	ctx := context.Background()
	l := o.NewListener(ctx, workerRole(o))
	evt := discover.ToQueueEvent(event.NewItemEvent(roles.EventWorkReady, "t", item.Item{ID: "zr-w1"}))
	got := l.Offer(eventqueue.Offering{ID: "dsp-000000000000", Event: evt}) // must not panic

	want := eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
	if got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
}
