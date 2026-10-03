package main

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Tests for runRun's graceful shutdown (bead pg2-euh4f): SIGTERM must not
// cancel in-flight dispatches; they get up to the drain timeout to finish, and
// only then is the dispatch context cancelled and the session sweep run.

// shutdownRunner is a wireclient.Runner whose dispatch blocks until released or
// its context is cancelled, recording what happened and in what order.
type shutdownRunner struct {
	started chan struct{} // closed on the first dispatch call
	release chan struct{} // close to let a blocked dispatch finish normally

	mu          sync.Mutex
	once        sync.Once
	dispatches  int
	ctxErr      error // the dispatch ctx's Err() when the dispatch ended
	steps       []string
	dispatchCtx context.Context
}

func newShutdownRunner() *shutdownRunner {
	return &shutdownRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *shutdownRunner) record(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
}

func (r *shutdownRunner) Run(ctx context.Context, argv []string, _ []byte) ([]byte, int, error) {
	if argv[len(argv)-1] != "dispatch" {
		r.record(argv[len(argv)-1])
		return []byte(`{"schemaVersion":"1","id":"x","outcome":"ok"}`), 0, nil
	}
	r.mu.Lock()
	r.dispatches++
	r.dispatchCtx = ctx
	r.mu.Unlock()
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		r.mu.Lock()
		r.ctxErr = ctx.Err()
		r.mu.Unlock()
		r.record("dispatch-finished")
		return []byte(`{"schemaVersion":"1","id":"x","outcome":"delivered"}`), 0, nil
	case <-ctx.Done():
		r.mu.Lock()
		r.ctxErr = ctx.Err()
		r.mu.Unlock()
		r.record("dispatch-cancelled")
		return nil, 0, ctx.Err()
	}
}

func (r *shutdownRunner) snapshot() (dispatches int, steps []string, ctxErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dispatches, append([]string(nil), r.steps...), r.ctxErr
}

// startShutdownRun boots runLongRunningBody over a timer source and one role
// backed by r, returning the signal-ctx cancel func and the body's exit code.
func startShutdownRun(t *testing.T, r *shutdownRunner, drainTimeout time.Duration) (signal context.CancelFunc, done <-chan int) {
	t.Helper()
	cfg := config.Config{
		LogDir:       shortDir(t),
		PollInterval: 20 * time.Millisecond,
		Roles:        roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
		Queries:      query.SourceSet{{Name: "tick", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}}}},
	}
	h := &wireclient.Client{Runner: r, Command: func(_ roles.Role, sub string) ([]string, error) { return []string{"fake-handler", sub}, nil }}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: h, Bindings: core.NewBindings("t1")}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := make(chan int, 1)
	go func() {
		ch <- runLongRunningBody(ctx, preparedRun{cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles}, drainTimeout)
	}()
	select {
	case <-r.started:
	case <-time.After(20 * time.Second):
		t.Fatal("no dispatch started")
	}
	return cancel, ch
}

// (a) Cancelling the signal ctx must NOT cancel the in-flight handler: the run
// waits, the offer completes, and only then does the preShutdown sweep run.
func TestRunLongRunning_signalDoesNotCancelInFlightDispatch(t *testing.T) {
	r := newShutdownRunner()
	signal, done := startShutdownRun(t, r, time.Minute)
	signal()

	select {
	case code := <-done:
		t.Fatalf("run exited (%d) while a dispatch was still in flight: it must wait for the drain", code)
	case <-time.After(300 * time.Millisecond):
	}
	r.mu.Lock()
	ctxErr := r.dispatchCtx.Err()
	r.mu.Unlock()
	if ctxErr != nil {
		t.Fatalf("the signal cancelled the in-flight handler's context: %v", ctxErr)
	}

	close(r.release)
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, want %d", code, exitOK)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not exit after the in-flight dispatch finished")
	}
	n, steps, endErr := r.snapshot()
	if endErr != nil {
		t.Fatalf("handler ctx at completion = %v, want nil (the offer completed undisturbed)", endErr)
	}
	if n != 1 {
		t.Fatalf("dispatches = %d, want 1: no new offer may start after the signal", n)
	}
	// The session sweep must not run while a dispatch is in flight.
	if got := withoutStep(steps, "postStartup"); len(got) < 2 || got[0] != "dispatch-finished" || got[1] != "preShutdown" {
		t.Fatalf("steps = %v, want dispatch-finished BEFORE preShutdown", steps)
	}
}

// (c) A dispatch that outlives the drain timeout has its context cancelled, and
// the run then exits.
func TestRunLongRunning_drainTimeoutCancelsDispatchContext(t *testing.T) {
	r := newShutdownRunner()
	signal, done := startShutdownRun(t, r, 200*time.Millisecond)
	signal()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit = %d, want %d", code, exitOK)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not exit after the drain timeout")
	}
	// The handler observes the cancellation asynchronously to the run's exit.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, _, endErr := r.snapshot(); endErr != nil {
			if endErr != context.Canceled {
				t.Fatalf("handler ctx err = %v, want context.Canceled", endErr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the handler never saw its context cancelled after the drain timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func withoutStep(steps []string, drop string) []string {
	var out []string
	for _, s := range steps {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

// (b) A tick whose signal ctx is already done must not launch a new offer.
func TestRunOneTick_noKickAfterShutdownRequested(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	took := make(chan string, 4)
	q.Register(&takeListener{id: "r1", typ: "t1", took: took})
	if _, err := q.Enqueue(eventqueue.Event{ID: "queued", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runOneTick(ctx, config.Config{Queries: o.Cfg.Queries}, o, &core.Service{}, q, "", io.Discard)
	waitInFlight(t, q)
	select {
	case id := <-took:
		t.Fatalf("offer %q launched after shutdown was requested", id)
	default:
	}

	// Control: the same tick with a live ctx does Kick the queued event.
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, &core.Service{}, q, "", io.Discard)
	select {
	case <-took:
	case <-time.After(20 * time.Second):
		t.Fatal("control tick did not dispatch the queued event")
	}
	waitInFlight(t, q)
}
