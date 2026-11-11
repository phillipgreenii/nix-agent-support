package gather

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// A connector call that never returns fails at the per-call timeout, as an
// unavailable backend, instead of blocking the caller (and the feed lock)
// forever.
func TestRunFailsAtTheCallTimeoutWhenTheConnectorNeverReturns(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Respond(func(cmdrun.Call) cmdrun.Result {
		<-release // blocks like a hung connector; the fake never sees the context
		return cmdrun.Result{}
	})
	origRunner, origTimeout := runner, callTimeout
	runner, callTimeout = fake.Runner(), 20*time.Millisecond
	t.Cleanup(func() { runner, callTimeout = origRunner, origTimeout })

	start := time.Now()
	_, err := (&Gatherer{}).run(context.Background(), []string{"pr", "show"}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want it to wrap ErrUnavailable", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("run returned after %s, want about the injected timeout", d)
	}
}

// The caller's own cancellation is not reported as a timeout.
func TestRunReportsCallerCancellationAsCancellation(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdrun.Result{Code: -1, CtxErr: context.Canceled})
	origRunner := runner
	runner = fake.Runner()
	t.Cleanup(func() { runner = origRunner })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Gatherer{}).run(ctx, []string{"pr", "show"}, nil)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want context.Canceled and not ErrUnavailable", err)
	}
}
