package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/dtest"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// readEventLog returns the parsed JSONL records written to path. Mirrors
// internal/executor/ccpool_test.go's helper of the same name (unexported,
// so each package that needs it keeps its own copy).
func readEventLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil // no file ⇒ no records emitted
	}
	defer func() { _ = f.Close() }()
	var recs []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", sc.Text(), err)
		}
		recs = append(recs, m)
	}
	return recs
}

// evOf wraps a dispatch context's role+item into the eventqueue.Event
// RunOne now consumes directly (pg2-oju6w.15 changed RunOne's signature from
// event.Event to eventqueue.Event, matching the queue-driven roleListener.Offer
// path — see discover.DeriveContextFromQueueEvent). Test-only shim so the
// existing DispatchContext-shaped fixtures drive RunOne unchanged.
func evOf(d discover.DispatchContext) eventqueue.Event {
	return discover.ToQueueEvent(event.NewItemEvent("test.event", "test", d.Item))
}

// runOne is the test shim for the eventqueue.Event-taking RunOne(ctx, role, evt).
func runOne(o *Orchestrator, ctx context.Context, d discover.DispatchContext) error {
	return o.RunOne(ctx, d.Role, evOf(d))
}

// dispatchCall records one wireclient.Dispatch invocation a fakeHandler
// observed — the role dispatched to and the eventqueue.Event it carried.
type dispatchCall struct {
	role roles.Role
	evt  eventqueue.Event
}

// fakeHandler is a scripted wireclient.HandlerClient test double (Task 5.4):
// it records every Dispatch call and returns the scripted (reply, err) —
// this package's OWN dispatch mechanics (ccpool session launch, the budget
// watchdog, a bare-command executable) now live entirely in the registered
// handler participant this fake stands in for; this package no longer has
// any of that machinery to exercise directly.
//
// repliesByItem is an OPTIONAL per-item override (Task 5.5), keyed by the
// dispatched item's own ID: a test proving buildResult passes
// reply.Outcome straight through, per item, needs a DIFFERENT opaque
// outcome per call — something the single shared reply field can't
// express. Every call not found in repliesByItem falls back to reply/err.
type fakeHandler struct {
	mu            sync.Mutex
	calls         []dispatchCall
	reply         wireclient.Reply
	err           error
	repliesByItem map[string]wireclient.Reply
	// blockRole/unblock (pg2-qubkf part 2, the tick-loop/Kick decouple
	// design doc): when blockRole matches the dispatched role's name,
	// Dispatch blocks on unblock before recording the call and returning.
	// Both fields are set once, during test setup, strictly before any
	// concurrent goroutine (a Kick()-launched offer, a driving ProduceTick
	// loop) starts — so reading them from Dispatch later needs no lock of
	// its own; only TestOrchestrator_LastTick_hasNoRaceWithConcurrentKickInFlightOffer
	// sets them at all, every other test leaves both at their zero value
	// (no blocking, unchanged behavior).
	blockRole string
	unblock   chan struct{}
}

func (f *fakeHandler) Dispatch(_ context.Context, role roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	if f.blockRole != "" && role.Name == f.blockRole {
		<-f.unblock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dispatchCall{role: role, evt: evt})
	if f.repliesByItem != nil {
		id, _ := evt.Payload["id"].(string)
		if r, ok := f.repliesByItem[id]; ok {
			return r, nil
		}
	}
	return f.reply, f.err
}

// PostStartup/PreShutdown (pg2-oju6w.15) are stubs here: no test in this
// file exercises the lifecycle hooks through *Orchestrator directly (that
// coverage lives in cmd/pg-router's own run_test.go, which drives
// postStartupAll/preShutdownAll end to end) — these only exist to satisfy
// the now-3-method wireclient.HandlerClient interface.
func (f *fakeHandler) PostStartup(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (f *fakeHandler) PreShutdown(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (f *fakeHandler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeQuery is a minimal in-process query.Query test double (pg2-oju6w.15):
// Task 5.8 deleted the beads-backed built-in query set (query.BeadsReady)
// and its role pairing (roles.BuiltinRoleSet/BuiltinQuerySet) entirely — the
// beads-shaped query now lives as a registered participant, reached only
// through config, never as an automatic built-in — so this package's own
// tests can no longer script a fake beads.Runner (query.Env carries no BD
// field at all) and instead script a query directly. It embeds query.Meta
// for Emits/Trigger/FailureBackoff and returns a fixed, scripted event list
// on every Run call.
type fakeQuery struct {
	query.Meta
	events []event.Event
	err    error
}

func (q *fakeQuery) Validate() error        { return nil }
func (q *fakeQuery) BackingCommand() string { return "" }
func (q *fakeQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	return q.events, q.err
}

// newOrch builds an Orchestrator over a hand-rolled feedback+worker role/
// query fixture (testRoleSet/testQuerySet below) — the local replacement for
// the retired roles.BuiltinRoleSet/BuiltinQuerySet (Task 5.8) — with a fresh
// *fakeHandler wired as Handler, a deterministic per-attempt stamp, and a
// manual clock the caller can advance via o.tick.
func newOrch(cfg config.Config, sources query.SourceSet) *Orchestrator {
	cfg.Queries = sources
	o := &Orchestrator{Reg: testRoleSet(), Cfg: cfg, Handler: &fakeHandler{}}
	// Mirrors bootCore's wiring (cmd/pg-router/run.go): the SAME declared-bind-types
	// value the production core.Listen would get, so ProduceTick's undeclared-type
	// rejection (Task 1.1) never rejects a built-in role's own bound types.
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	o.now = clk.Now
	o.tick = clk.TickAdvancing()
	return o
}

// testRoleSet is this package's local feedback+worker role fixture,
// replacing the retired roles.BuiltinRoleSet (Task 5.8). "feedback.ready"/
// "work.ready" are plain literal event types now — the built-in
// roles.EventFeedbackReady/EventWorkReady constants these tests used to
// reference no longer exist (deleted with internal/roles/enums.go).
func testRoleSet() roles.RoleSet {
	return roles.RoleSet{
		{Name: "feedback", Enabled: true, Binds: []string{"feedback.ready"}},
		{Name: "worker", Enabled: true, Binds: []string{"work.ready"}},
	}
}

// testQuerySet builds a feedback-source/worker-source pair of fakeQuery
// producers, scripted with feedbackEvts/workerEvts — this package's local
// replacement for the retired roles.BuiltinQuerySet (Task 5.8). Names match
// exactly what TestOrchestrator_LastTick_mergesForwardWithoutErasingPriorEntries
// asserts on ("feedback-source"/"worker-source").
func testQuerySet(feedbackEvts, workerEvts []event.Event) query.SourceSet {
	return query.SourceSet{
		{Name: "feedback-source", Query: &fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}, events: feedbackEvts}},
		{Name: "worker-source", Query: &fakeQuery{Meta: query.Meta{EmitTypes: []string{"work.ready"}}, events: workerEvts}},
	}
}

func roleByName(o *Orchestrator, name string) roles.Role {
	for _, r := range o.Reg {
		if r.Name == name {
			return r
		}
	}
	panic("test: role not found: " + name)
}

func feedbackRole(o *Orchestrator) roles.Role { return roleByName(o, "feedback") }
func workerRole(o *Orchestrator) roles.Role   { return roleByName(o, "worker") }

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	// SAFETY: never the real ~/.local/state worktree dir — keep worktree.Ensure's
	// MkdirAll inside an isolated throwaway path. os.MkdirTemp is used (not
	// t.TempDir) to preserve fastCfg's no-arg signature; the OS reaps it.
	if d, err := os.MkdirTemp("", "pg-router-orch-wt-"); err == nil {
		c.WorktreeDir = d
	}
	return c
}

// writeTemp creates a sentinel file and returns its path (+ a no-op cleanup;
// t.TempDir() handles removal). Used by the gated-pass test.
func writeTemp(t *testing.T) (string, func()) {
	t.Helper()
	p := t.TempDir() + "/sentinel"
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, func() {}
}

// newTestQueue builds a bare eventqueue.Queue over an in-memory store, for
// tests exercising the queue->handler Listener bridge (orchestrator.NewListener,
// bead pg2-f3mcb.2) directly.
func newTestQueue(t *testing.T) *eventqueue.Queue {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	return q
}

// TestProduceTick_thenDispatch_matchesBuiltinRoles replaces the retired
// discoverViaBus parity test: it drives the SAME built-in config through
// ProduceTick (the discovery->enqueue producer) + a registered NewListener per
// role + one queue Dispatch pass, and checks each enabled role's Listener gets
// ITS OWN head offered in this one pass (no cap, no starvation across roles,
// INV-CONC-1's "one outstanding offer per handler") — dispatched, as of Task
// 5.4, through the wire client rather than in-process ccpool mechanics, so
// this now asserts on the fakeHandler's own recorded calls rather than
// cc.Sent. The worker query returns two ready beads; only the FIRST (the
// per-listener head) is dispatched this pass — the second waits for the next
// Dispatch call, exactly the per-handler serial FIFO DEC-EVENT-2 describes,
// with no core-tracked cap involved.
func TestProduceTick_thenDispatch_matchesBuiltinRoles(t *testing.T) {
	cfg := fastCfg()
	feedbackEvts := []event.Event{event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-c", Type: "task", Title: "process-feedback: x"})}
	workerEvts := []event.Event{
		event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}),
		event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w2"}),
	}
	o := newOrch(cfg, testQuerySet(feedbackEvts, workerEvts))
	handler := o.Handler.(*fakeHandler)
	ctx := context.Background()
	q := newTestQueue(t)
	q.Register(o.NewListener(ctx, feedbackRole(o)))
	q.Register(o.NewListener(ctx, workerRole(o)))

	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()

	if handler.callCount() != 2 {
		t.Fatalf("feedback's and worker's own heads should each be dispatched this pass; calls=%d", handler.callCount())
	}
	var sawFeedback, sawWorker bool
	for _, c := range handler.calls {
		switch c.role.Name {
		case "feedback":
			sawFeedback = true
		case "worker":
			sawWorker = true
		}
	}
	if !sawFeedback || !sawWorker {
		t.Errorf("expected exactly one dispatch to feedback and one to worker; calls=%+v", handler.calls)
	}
}

// TestOrchestrator_LastTick_mergesForwardWithoutErasingPriorEntries is
// pg2-bzb8i's regression coverage for the new LastTick() accessor: a
// ProduceTick call records THIS pass's own real fire time for every
// built-in source it actually attempts, while a name it never touches at
// all (seeded here as if a previous, unrelated pass had recorded it) keeps
// its prior entry untouched — ProduceTick's merge loop (unchanged by this
// bead) only ever ADDS entries present in its own pass's ProduceReport,
// never clears an absent one, which is exactly the persistence
// cmd/pg-router's sourceReportsFor now relies on so a source's LastTick
// survives a later pass that cadence-gates it off (see LastTick's own doc).
func TestOrchestrator_LastTick_mergesForwardWithoutErasingPriorEntries(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	ctx := context.Background()
	q := newTestQueue(t)

	stalePriorFire := time.Unix(1_700_000_000, 0)
	o.lastTick = map[string]time.Time{"other-source": stalePriorFire}

	before := time.Now()
	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatal(err)
	}

	got := o.LastTick()
	if !got["other-source"].Equal(stalePriorFire) {
		t.Fatalf("LastTick()[other-source] = %v, want the preserved prior fire %v -- a pass that never touches a source must not erase its history", got["other-source"], stalePriorFire)
	}
	if got["feedback-source"].Before(before) {
		t.Fatalf("LastTick()[feedback-source] = %v, want this pass's own real fire time (>= %v)", got["feedback-source"], before)
	}
	if got["worker-source"].Before(before) {
		t.Fatalf("LastTick()[worker-source] = %v, want this pass's own real fire time (>= %v)", got["worker-source"], before)
	}

	// The returned map is an independent copy -- mutating it must not
	// corrupt the Orchestrator's own persisted history.
	got["other-source"] = time.Time{}
	if !o.lastTick["other-source"].Equal(stalePriorFire) {
		t.Fatalf("mutating LastTick()'s result corrupted the Orchestrator's own state: got %v", o.lastTick["other-source"])
	}
}

// TestOrchestrator_LastTick_hasNoRaceWithConcurrentKickInFlightOffer is this
// bead's (pg2-qubkf part 2, "decouple pg-router-core's tick loop from
// per-dispatch-pass completion") own regression coverage for the design
// doc's o.lastTick concurrency question (orchestrator.go's own field doc):
// once cmd/pg-router's tick loop switched from eventqueue.Queue.Dispatch to
// Queue.Kick, a NEW ProduceTick call can now occur while a PRIOR pass's
// Kick()-launched offer is still settling in the background, on its own
// detached goroutine -- Dispatch's blocking return used to make that
// impossible.
//
// This proves, under `go test -race`, that o.lastTick (a bare, unlocked
// map) survives that overlap: repeated ProduceTick/LastTick calls run from
// THIS goroutine (the tick loop's own driving goroutine, per INV-LIFE-3)
// while a worker listener's offer sits blocked on handler.unblock -- still
// inside eventqueue's phase 2 (roleListener.Offer), never having reached
// its own phase-3 settlement yet -- and again for a few more calls once
// that offer is unblocked and settling concurrently. If a future change
// ever made Kick's per-offer phase 2 or phase 3 touch o.lastTick (or any
// other Orchestrator field ProduceTick/LastTick reach) without
// synchronization, -race has a real window here to catch it; today it does
// not (see this field's own doc: phase 3 lives entirely inside
// *eventqueue.Queue, and phase 2's own Orchestrator-side call --
// roleListener.Offer -> workOne/emitResult -- never reads or writes
// o.lastTick).
func TestOrchestrator_LastTick_hasNoRaceWithConcurrentKickInFlightOffer(t *testing.T) {
	cfg := fastCfg()
	workerEvts := []event.Event{event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"})}
	// Deliberately NOT testQuerySet (which leaves worker-source's trigger at
	// its zero value, falling back to cfg.PollInterval -- 1ms under
	// fastCfg): this test's own tick loop below calls ProduceTick ~35 times
	// back-to-back with no per-tick real-time wait (unlike a real `run`
	// ticker), and ProduceWithCadence's cadenceDue gate is keyed to genuine
	// wall-clock time.Now() (discover.go), never to this test's injected
	// ManualClock (o.now/newOrch) or the Queue's own clock -- so NEITHER
	// clock seam holds the cadence gate shut. Once real time -- not virtual
	// time -- has advanced 1ms since worker-source's first fire, cadenceDue
	// reopens and ProduceTick re-runs the query, which keeps returning the
	// SAME fixed workerEvts id ("zr-w1") every call.
	//
	// That re-emit is not itself the bug: eventqueue's own retention model
	// (event.go's Resolve/Expired, DEC-EVENT-1 "re-emission, not
	// resurrection") means a born-expired event settles the instant its one
	// bound listener has had its attempt, after which retainedLocked
	// correctly treats a same-id re-emit as fresh work, not a duplicate
	// (Queue.Enqueue's stale-retire branch; see
	// TestEnqueueStaleReplaceCountsTheMissAndRecordsTheEvict for the
	// vacuous/no-listener variant of the identical mechanism). On a fast,
	// idle CPU the whole ~35-call loop finishes in far under 1ms, so
	// cadence never reopens and worker-source fires exactly once; under a
	// loaded/throttled CPU (this bead, pg2-dp12o) enough real time can pass
	// for cadence to reopen WHILE the blocked offer is settling, so the
	// re-emitted "zr-w1" gets legitimately redelivered a second time --
	// callCount=2 -- with no race anywhere near o.lastTick/Kick, which is
	// this test's actual, and only, subject (see its own doc comment
	// above). Pinning worker-source's trigger to an hour-long period keeps
	// cadence shut for the test's entire (sub-second) real run, regardless
	// of how much real time a loaded CPU burns per iteration, without
	// changing any production cadence/clock behavior.
	o := newOrch(cfg, query.SourceSet{
		{Name: "feedback-source", Query: &fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}}},
		{Name: "worker-source", Query: &fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{Every: time.Hour}},
			events: workerEvts,
		}},
	})
	handler := o.Handler.(*fakeHandler)
	handler.blockRole = "worker"
	handler.unblock = make(chan struct{})
	ctx := context.Background()
	q := newTestQueue(t)
	q.Register(o.NewListener(ctx, feedbackRole(o)))
	q.Register(o.NewListener(ctx, workerRole(o)))

	// Enqueue the one worker.ready event, then Kick(): the worker listener's
	// own offer launches on its own goroutine and immediately blocks on
	// handler.unblock -- still inside phase 2, never having reached
	// eventqueue's own phase-3 settlement.
	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatal(err)
	}
	q.Kick()

	// While that offer sits blocked, drive many more ProduceTick/Kick/
	// LastTick calls from THIS goroutine -- proving the driving loop's own
	// progress is unaffected by the stuck offer (INV-LIFE-3), and giving
	// -race a real concurrent window against the blocked offer's own
	// goroutine.
	for range 25 {
		if _, err := o.ProduceTick(ctx, q); err != nil {
			t.Fatal(err)
		}
		q.Kick()
		_ = o.LastTick()
	}

	// Unblock the offer: its own goroutine now proceeds into eventqueue's
	// phase-3 settlement on ITS OWN schedule, concurrently with a few more
	// ProduceTick/LastTick calls below -- the window that would catch a
	// race on o.lastTick from phase 3, were one to exist.
	close(handler.unblock)
	for range 10 {
		if _, err := o.ProduceTick(ctx, q); err != nil {
			t.Fatal(err)
		}
		q.Kick()
		_ = o.LastTick()
	}

	drainCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !q.WaitForInFlightDrain(drainCtx, 5*time.Millisecond) {
		t.Fatal("the blocked offer never settled")
	}

	if got := handler.callCount(); got != 1 {
		t.Fatalf("callCount = %d, want exactly 1 (the worker's one queued event, dispatched exactly once)", got)
	}
	if lt := o.LastTick(); lt["worker-source"].IsZero() {
		t.Errorf("LastTick()[worker-source] is zero, want a real fire time recorded across this test's many ProduceTick calls")
	}
}

// TestRoleListenerOffer_CtxCancellationUnwindsStuckSubprocessOfferWithinBound
// closes the one MUST-FIX gap an independent review left open across both
// prior implementation passes of this bead's design doc ("decouple
// pg-router-core's tick loop from per-dispatch-pass completion"):
// internal/eventqueue's kick_test.go (Scenario 5(b)'s own doc comment,
// TestWaitForInFlightDrainReturnsTrueOnceOfferSettles) explicitly punts
// proving that cancelling the driving ctx while an offer is genuinely stuck
// mid-dispatch actually makes it unwind, in bounded time, to "the listener
// implementation that owns that ctx (roleListener/wireclient in
// internal/orchestrator and cmd/pg-router), not [eventqueue] ...
// Listener.Offer takes no context argument at all." Nothing in this package
// or in cmd/pg-router ever wrote that other half either, until now. This
// matters concretely because cmd/pg-router/run.go's drainThenCloseStore
// gates storeClose() on q.WaitForInFlightDrain succeeding within
// inFlightDrainTimeout (5s) — an assumption that a genuinely-stuck offer
// actually unwinds that fast, which nothing before this test exercised.
//
// This deliberately dispatches through a REAL wireclient.Client backed by
// the REAL wireclient.OSRunner (a real subprocess via exec.CommandContext,
// wireclient.go), never a fake/blocking test double: roleListener.ctx (set
// at NewListener construction, exactly as bootCore/runRunRole wire it) flows
// into workOne -> wireclient.Client.Dispatch -> OSRunner.Run's
// exec.CommandContext call, and THAT specific plumbing — ctx cancellation
// actually reaching, and killing, a real subprocess — is exactly what this
// test needs to exercise. A fake handler double (e.g. this file's own
// fakeHandler, whose Dispatch signature literally discards its ctx
// parameter — see fakeHandler.Dispatch above) would "unwind" the instant its
// own blocking channel is closed regardless of whether real ctx-cancellation
// wiring works at all: it cannot fail the way this test needs to be able to
// fail, so it would prove nothing about the real mechanism.
//
// The handler here is a real subprocess (a temp shell script) that sleeps
// far longer than this test is willing to wait. Sequence: (1) Kick() launches
// the offer and returns without waiting on it — SessionsInFlight reaches 1,
// proving the subprocess has genuinely started and is blocked in its own
// sleep, not already finished; (2) cancelling the SAME ctx NewListener was
// constructed with makes that offer settle — SessionsInFlight returns to 0 —
// well within `bound`, a duration far shorter than the subprocess's own
// sleep, observed via q.WaitForInFlightDrain (the exact primitive
// drainThenCloseStore, cmd/pg-router/run.go, gates storeClose() on). If ctx
// cancellation did not actually propagate through exec.CommandContext, the
// subprocess would run its own full sleep, WaitForInFlightDrain would time
// out at `bound` and return false, and this test would fail.
//
// What this proves: the real ctx-cancellation-through-exec.CommandContext
// path unwinds a genuinely stuck offer well within a bound far shorter than
// the process's own sleep — the load-bearing assumption behind
// inFlightDrainTimeout. What remains inference, NOT exercised by this test:
// this is a package-level (unit) proof of the mechanism, not an end-to-end
// reproduction of run.go's exact call graph (bootCore -> NewListener -> a
// real SIGINT via signal.NotifyContext -> drainThenCloseStore); that
// end-to-end wiring is read, not independently re-verified, by this test.
func TestRoleListenerOffer_CtxCancellationUnwindsStuckSubprocessOfferWithinBound(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-handler.sh")
	const handlerSleepSeconds = 10
	body := "#!/bin/sh\n" +
		"cat > /dev/null\n" +
		"sleep " + fmt.Sprint(handlerSleepSeconds) + "\n" +
		"echo '{\"schemaVersion\":\"1\",\"id\":\"fake\",\"outcome\":\"ok\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write slow handler script: %v", err)
	}

	role := roles.Role{Name: "worker", Enabled: true, Binds: []string{"work.ready"}}
	o := &Orchestrator{
		Reg: roles.RoleSet{role},
		Cfg: fastCfg(),
		Handler: wireclient.New(func(roles.Role, string) ([]string, error) {
			return []string{script}, nil
		}),
	}

	// listenerCtx is the SAME shape NewListener captures at construction in
	// production (bootCore's per-role q.Register(o.NewListener(ctx, r)) call)
	// — cancelling it below is what a real SIGINT/SIGTERM does to `run`'s own
	// long-lived ctx.
	listenerCtx, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()
	q := newTestQueue(t)
	q.Register(o.NewListener(listenerCtx, role))

	// ExpiresAt is set explicitly (unlike this file's other Enqueue calls,
	// which rely on discover.ToQueueEvent's born-expired default): a fresh
	// event resolves expiresAt==at==now absent this, and this test's whole
	// point is an offer that is still outstanding well after "now" — kick_test.go's
	// own evtUntil helper sets this for the identical reason.
	evt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "item-1"}))
	evt.ExpiresAt = time.Now().Add(time.Hour)
	if _, err := q.Enqueue(evt); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick() launched = %d, want 1", launched)
	}

	// Wait for the offer to actually be in flight — i.e. the subprocess has
	// genuinely started and is blocked in its own sleep — before cancelling,
	// so the cancellation below is proven to race a REAL stuck dispatch, not
	// an already-finished one.
	inFlightDeadline := time.Now().Add(2 * time.Second)
	for q.SessionsInFlight() != 1 {
		if time.Now().After(inFlightDeadline) {
			t.Fatalf("offer never reached SessionsInFlight()==1 within 2s -- the subprocess dispatch never even started")
		}
		time.Sleep(time.Millisecond)
	}

	cancelListener()

	// bound is deliberately far shorter than handlerSleepSeconds: if ctx
	// cancellation did not propagate through exec.CommandContext, the
	// subprocess would run its own full sleep and WaitForInFlightDrain below
	// would time out and return false, failing this test.
	const bound = 3 * time.Second
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), bound)
	defer cancelDrain()
	if !q.WaitForInFlightDrain(drainCtx, 5*time.Millisecond) {
		t.Fatalf("offer did not settle within %v of ctx cancellation (handler's own sleep is %ds) -- "+
			"context cancellation did not propagate through exec.CommandContext to kill the real subprocess in time",
			bound, handlerSleepSeconds)
	}
	if got := q.SessionsInFlight(); got != 0 {
		t.Fatalf("SessionsInFlight() = %d after WaitForInFlightDrain returned true, want 0", got)
	}
}

// TestNewListener_perHandlerSerialFIFO_onePerDispatchCall locks in the
// structural replacement for the retired per-role Cap: a Listener's head
// advances by exactly one accepted event per Dispatch() call, regardless of how
// many matching events are queued — there is no core-tracked number gating
// this, only the queue's own per-listener cursor (INV-CONC-1 / DEC-EVENT-2).
func TestNewListener_perHandlerSerialFIFO_onePerDispatchCall(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	handler := o.Handler.(*fakeHandler)
	ctx := context.Background()
	q := newTestQueue(t)
	q.Register(o.NewListener(ctx, workerRole(o)))
	if _, err := q.Enqueue(discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w1"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w2"}))); err != nil {
		t.Fatal(err)
	}

	q.Dispatch()
	if handler.callCount() != 1 {
		t.Fatalf("after ONE Dispatch call, exactly the head (zr-w1) should be worked; calls=%d", handler.callCount())
	}
	q.Dispatch()
	if handler.callCount() != 2 {
		t.Fatalf("after a SECOND Dispatch call, the next head (zr-w2) should be worked; calls=%d", handler.callCount())
	}
}

// TestGated_operatorPausedAndCICDDown locks the Gated() predicate `run` /
// `run-until-idle` consult before registering any Listener or running a
// producer tick (the exported form of the retired DrainOnce's own gate check).
func TestGated_operatorPausedAndCICDDown(t *testing.T) {
	o := newOrch(fastCfg(), testQuerySet(nil, nil))
	if o.Gated() {
		t.Fatal("an ungated config must report Gated() == false")
	}
	f, _ := writeTemp(t)
	o.Cfg.OperatorPaused = f
	if !o.Gated() {
		t.Fatal("OperatorPaused sentinel present must report Gated() == true")
	}
	o.Cfg.OperatorPaused = ""
	o.Cfg.CICDDown = f
	if !o.Gated() {
		t.Fatal("CICDDown sentinel present must report Gated() == true")
	}
}

// TestWorkOne_dispatchesToWireClientForRole locks Task 5.4's own core
// replacement: workOne no longer runs any in-process ccpool/command
// mechanics at all (that business — Ensure/Send/wait, the budget watchdog,
// stuck-bead escalation labeling — moved entirely to the registered
// handler participant, packages/pg-router-ccpool-handler, which now owns
// its own equivalent test coverage) — it just sends the event to
// o.Handler.Dispatch for d.Role and returns its error unmodified.
func TestWorkOne_dispatchesToWireClientForRole(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	handler := o.Handler.(*fakeHandler)
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	qevt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", d.Item))
	if _, err := o.workOne(context.Background(), d, qevt); err != nil {
		t.Fatalf("workOne: %v", err)
	}
	if handler.callCount() != 1 {
		t.Fatalf("workOne must dispatch exactly once via the wire client; calls=%d", handler.callCount())
	}
	if handler.calls[0].role.Name != "worker" {
		t.Fatalf("dispatched role = %q, want %q", handler.calls[0].role.Name, "worker")
	}
}

// TestWorkOne_propagatesDispatchError proves a Handler error is returned
// unmodified — workOneWithID's own doc comment: Outcome interpretation and
// created/closed/handed-back branching are docket pg2-oju6w's Task 5.5's
// job, not this one's.
func TestWorkOne_propagatesDispatchError(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: fmt.Errorf("boom")}
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	qevt := discover.ToQueueEvent(event.NewItemEvent("work.ready", "t", d.Item))
	if _, err := o.workOne(context.Background(), d, qevt); err == nil {
		t.Fatal("expected the Handler's error to propagate")
	}
}

// TestRunOne_feedbackClosesSession and TestRunOne_dispatchErrorStillTearsDownSession
// (session-teardown assertions against RunOne itself) were DELETED here
// (pg2-oju6w.15): their premise — that RunOne closes or preserves the
// ccpool session it launched — no longer holds. RunOne has no CC field and
// no session-inspection logic left at all (ccpool session lifecycle is now
// entirely the registered handler participant's own process); its caller
// (cmd/pg-router's run-role) instead brackets the call with its own
// Handler.PreShutdown, exactly mirroring run.go's per-role preShutdown sweep
// at daemon/run-until-idle shutdown. That behavior is covered in
// cmd/pg-router's own tests, not here.

// TestRunOne_launchFailureSurfacesRealErrorInEventLog is a regression pin for
// pg2-an65v's secondary (defense-in-depth) fix: before it, emitResult accepted
// dispatchErr only to decide branching (errors.Is(err, wireclient.ErrBusy) in
// the queue-bridge path) and then discarded it entirely -- a failed dispatch's
// event-log record carried nothing but the bare verb the executor applied
// (escalated/unclaimed/...), never the underlying error message. Now a failed
// dispatch's "dispatch" record must carry level "warn" (not "info") and an
// "error" field holding dispatchErr's message.
func TestRunOne_launchFailureSurfacesRealErrorInEventLog(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{err: fmt.Errorf("did not reach ready")}
	o.Log = lw
	d := discover.DispatchContext{Role: workerRole(o), Item: item.Item{ID: "zr-w"}}
	if err := runOne(o, context.Background(), d); err == nil {
		t.Fatal("dispatch failure should return an error")
	}

	var rec map[string]any
	for _, r := range readEventLog(t, logPath) {
		if r["kind"] == "dispatch" {
			rec = r
		}
	}
	if rec == nil {
		t.Fatalf("expected a dispatch record in the event log; got %v", readEventLog(t, logPath))
	}
	if lvl, _ := rec["level"].(string); lvl != "warn" {
		t.Errorf("dispatch record level = %q, want %q on a failed dispatch", lvl, "warn")
	}
	errMsg, _ := rec["error"].(string)
	if errMsg == "" {
		t.Errorf("dispatch record must carry an \"error\" field with the underlying failure; rec=%v", rec)
	} else if !strings.Contains(errMsg, "did not reach ready") {
		t.Errorf("dispatch record error field = %q, want it to contain the underlying dispatch error", errMsg)
	}
}

// --- progress-marker support: computed values behind the new slog.Info markers ---

// TestNewListener_mixedOutcomesAcrossPasses locks buildResult's own Task 5.5
// rewrite: reply.Outcome is stored VERBATIM on the dispatch record, per
// item, with no created/closed/handed-back interpretation left in this
// package — two items dispatched to the SAME role, each scripted with a
// DIFFERENT opaque outcome string, must each surface exactly that string in
// the event log, unchanged. (Before Task 5.5 this locked a bead-status-based
// closed/handed-back branch in buildResult itself; that branch is gone.)
func TestNewListener_mixedOutcomesAcrossPasses(t *testing.T) {
	cfg := fastCfg()
	o := newOrch(cfg, testQuerySet(nil, nil))
	o.Handler = &fakeHandler{repliesByItem: map[string]wireclient.Reply{
		"zr-ok":  {Outcome: "closed"},
		"zr-bad": {Outcome: "handed-back"},
	}}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	o.Log = lw
	ctx := context.Background()
	q := newTestQueue(t)
	q.Register(o.NewListener(ctx, feedbackRole(o)))
	if _, err := q.Enqueue(discover.ToQueueEvent(event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-ok"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(discover.ToQueueEvent(event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-bad"}))); err != nil {
		t.Fatal(err)
	}

	q.Dispatch() // works the head, zr-ok
	q.Dispatch() // works the next head, zr-bad

	verbFor := func(bead string) string {
		for _, r := range readEventLog(t, logPath) {
			if r["bead"] == bead {
				if actions, ok := r["actions"].([]any); ok && len(actions) > 0 {
					if a, ok := actions[0].(map[string]any); ok {
						v, _ := a["verb"].(string)
						return v
					}
				}
			}
		}
		return ""
	}
	if got := verbFor("zr-ok"); got != "closed" {
		t.Errorf("zr-ok's reported verb = %q, want %q (a closed bead)", got, "closed")
	}
	if got := verbFor("zr-bad"); got != "handed-back" {
		t.Errorf("zr-bad's reported verb = %q, want %q (a still-open bead)", got, "handed-back")
	}
}

// TestTeardownAll_purges/_returnsClosedCount/_preservesNeedsInput and
// TestRunOne_preservesNeedsInputSession were DELETED here (pg2-oju6w.15):
// teardownAll/closeUnlessNeedsInput/sessionStateByID no longer exist in this
// package at all — that sweep logic was PORTED to
// packages/pg-router-ccpool-handler's own preShutdown subcommand
// (cmd/pg-router-ccpool-handler/preshutdown.go), which carries the
// equivalent test coverage (needs_input preservation included) in its own
// preshutdown_test.go.
