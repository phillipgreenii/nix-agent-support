package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Tests for the shutdown watchdog and the shutdown_* events of
// runLongRunningBody: an overall deadline once the signal context ends, a
// bounded preShutdown sweep, and the started/phase/done log events.

// init starts the os/signal runtime goroutine outside any synctest bubble: it
// is created by the first signal.Notify, and a bubble that triggers that
// first call aborts with "select on synctest channel from outside bubble".
func init() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGUSR2)
}

// stallRunner is a shutdownRunner whose preShutdown hook ignores context
// cancellation and blocks until released. Every other shutdown phase is
// clamped to the time left before the deadline, so a stall inside one of them
// ends at the same instant as the watchdog (a tie in fake time); only a hook
// that outlives its context can be ended by the watchdog alone.
type stallRunner struct {
	*shutdownRunner
	unstall chan struct{}
}

func (s stallRunner) Run(ctx context.Context, argv []string, in []byte) ([]byte, int, error) {
	if argv[len(argv)-1] == "preShutdown" {
		<-s.unstall
		return []byte(`{"schemaVersion":"1","id":"x","outcome":"ok"}`), 0, nil
	}
	return s.shutdownRunner.Run(ctx, argv, in)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// deadlineRunner delegates to a shutdownRunner and records whether the
// preShutdown call's context carried a deadline.
type deadlineRunner struct {
	*shutdownRunner
	mu             sync.Mutex
	preHasDeadline bool
	preRemaining   time.Duration
}

func (d *deadlineRunner) Run(ctx context.Context, argv []string, in []byte) ([]byte, int, error) {
	if argv[len(argv)-1] == "preShutdown" {
		dl, ok := ctx.Deadline()
		d.mu.Lock()
		d.preHasDeadline, d.preRemaining = ok, time.Until(dl)
		d.mu.Unlock()
	}
	return d.shutdownRunner.Run(ctx, argv, in)
}

func startWatchdogRun(t *testing.T, r wireclient.Runner, started <-chan struct{}, drain, deadline time.Duration, exit func(int)) (signal context.CancelFunc, done <-chan int, logs *lockedBuf) {
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
	logs = &lockedBuf{}
	ch := make(chan int, 1)
	go func() {
		ch <- runLongRunningBody(ctx, preparedRun{
			cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles,
			shutdownLog:      slog.New(slog.NewTextHandler(logs, nil)),
			shutdownDeadline: deadline,
			shutdownExit:     exit,
		}, drain)
	}()
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("no dispatch started")
	}
	return cancel, ch, logs
}

func TestRunLongRunning_watchdogExitsWhenShutdownStalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newShutdownRunner()
		unstall := make(chan struct{})
		exited := make(chan int, 1)
		// The preShutdown hook never returns until released, so the shutdown
		// outlives the 150ms watchdog.
		signal, done, logs := startWatchdogRun(t, stallRunner{r, unstall}, r.started, time.Minute, 150*time.Millisecond,
			func(c int) {
				select {
				case exited <- c:
				default:
				}
			})
		// The dispatch finishes at once, so the drain ends well inside the
		// deadline and the stall is in the preShutdown hook alone.
		close(r.release)
		signal()
		select {
		case code := <-exited:
			if code != 10 {
				t.Fatalf("watchdog exit code = %d, want 10", code)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the watchdog never fired although the shutdown stalled")
		}
		out := logs.String()
		for _, want := range []string{"shutdown_signal", "shutdown_overrun", "phase=pre_shutdown"} {
			if !strings.Contains(out, want) {
				t.Fatalf("log missing %q:\n%s", want, out)
			}
		}
		// Unblock the body so its goroutine ends.
		close(unstall)
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("run did not exit after release")
		}
		if strings.Contains(logs.String(), "shutdown_complete") {
			t.Fatalf("shutdown_complete logged after an overrun:\n%s", logs.String())
		}
	})
}

func TestRunLongRunning_emitsShutdownEventsAndBoundsPreShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sr := newShutdownRunner()
		r := &deadlineRunner{shutdownRunner: sr}
		signal, done, logs := startWatchdogRun(t, r, sr.started, time.Minute, time.Minute, func(int) { t.Error("watchdog must not fire on a prompt shutdown") })
		signal()
		close(sr.release)
		select {
		case code := <-done:
			if code != exitOK {
				t.Fatalf("exit = %d, want %d", code, exitOK)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("run did not exit")
		}
		out := logs.String()
		last := -1
		for _, want := range []string{"shutdown_phase phase=drain", "phase=metrics_flush", "phase=accept_close", "phase=pre_shutdown", "phase=store_close", "shutdown_complete"} {
			i := strings.Index(out, want)
			if i < 0 || i < last {
				t.Fatalf("event %q missing or out of order (after offset %d):\n%s", want, last, out)
			}
			last = i
		}
		// The signal line is emitted by a watcher goroutine concurrently with the
		// drain phase line, so only its presence is asserted, not its order.
		if !strings.Contains(out, "shutdown_signal") {
			t.Fatalf("shutdown_signal missing:\n%s", out)
		}
		if strings.Contains(out, "shutdown_overrun") {
			t.Fatalf("unexpected overrun:\n%s", out)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.preHasDeadline || r.preRemaining <= 0 || r.preRemaining > newShutdownBudget(time.Minute).PreShutdown {
			t.Fatalf("preShutdown ctx deadline = (%v, remaining %v), want one within %v", r.preHasDeadline, r.preRemaining, newShutdownBudget(time.Minute).PreShutdown)
		}
	})
}
