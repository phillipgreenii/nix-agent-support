package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
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
	r := newShutdownRunner()
	exited := make(chan int, 1)
	// The dispatch never finishes and the drain budget is a minute, so the
	// drain phase outlives the 150ms watchdog.
	signal, done, logs := startWatchdogRun(t, r, r.started, time.Minute, 150*time.Millisecond,
		func(c int) {
			select {
			case exited <- c:
			default:
			}
		})
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
	for _, want := range []string{"shutdown_started", "shutdown_overrun", "phase=drain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
	// Unblock the body so its goroutine ends.
	close(r.release)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run did not exit after release")
	}
	if strings.Contains(logs.String(), "shutdown_done") {
		t.Fatalf("shutdown_done logged after an overrun:\n%s", logs.String())
	}
}

func TestRunLongRunning_emitsShutdownEventsAndBoundsPreShutdown(t *testing.T) {
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
	for _, want := range []string{"shutdown_started", "phase=drain", "phase=metrics_flush", "phase=accept_close", "phase=pre_shutdown", "phase=store_close", "shutdown_done"} {
		i := strings.Index(out, want)
		if i < 0 || i < last {
			t.Fatalf("event %q missing or out of order (after offset %d):\n%s", want, last, out)
		}
		last = i
	}
	if strings.Contains(out, "shutdown_overrun") {
		t.Fatalf("unexpected overrun:\n%s", out)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.preHasDeadline || r.preRemaining <= 0 || r.preRemaining > preShutdownTimeout {
		t.Fatalf("preShutdown ctx deadline = (%v, remaining %v), want one within %v", r.preHasDeadline, r.preRemaining, preShutdownTimeout)
	}
}

func TestShutdownWatchdogBudget(t *testing.T) {
	// launchd's ExitTimeOut clamp is 60s; keep a 7s margin over the deadline,
	// and the deadline over the sum of the bounded phases.
	const exitTimeOut = 60 * time.Second
	const flushAllowance = 3 * time.Second // metrics flush, accept-loop close, store close
	for _, tc := range []struct {
		name  string
		total time.Duration
		limit time.Duration
	}{
		{"deadline plus 7s margin under ExitTimeOut", shutdownWatchdogDeadline + 7*time.Second, exitTimeOut},
		{"bounded phases under the watchdog deadline", shutdownDrainTimeout + preShutdownTimeout + inFlightDrainTimeout, shutdownWatchdogDeadline},
		{"bounded phases plus flush allowance under the watchdog deadline", shutdownDrainTimeout + preShutdownTimeout + inFlightDrainTimeout + flushAllowance, shutdownWatchdogDeadline},
		{"bounded phases plus flush allowance under ExitTimeOut", shutdownDrainTimeout + preShutdownTimeout + inFlightDrainTimeout + flushAllowance, exitTimeOut},
		{"watchdog deadline under ExitTimeOut", shutdownWatchdogDeadline, exitTimeOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.total > tc.limit {
				t.Fatalf("total %v exceeds %v", tc.total, tc.limit)
			}
			if tc.name == "bounded phases under the watchdog deadline" && tc.total >= tc.limit {
				t.Fatalf("total %v must be strictly under %v", tc.total, tc.limit)
			}
		})
	}
	if preShutdownTimeout > 10*time.Second {
		t.Fatalf("preShutdownTimeout %v: the ccpool preShutdown answers at once, so the allowance must stay small", preShutdownTimeout)
	}
}
