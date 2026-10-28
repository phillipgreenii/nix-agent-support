package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// statusRunner swaps the package runner for a cmdruntest.Fake that answers
// every call (`pg-router status --json`) with res and records it.
func statusRunner(t *testing.T, res cmdrun.Result) *cmdruntest.Fake {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Return(res)
	orig := runner
	runner = f.Runner()
	t.Cleanup(func() { runner = orig })
	return f
}

func TestFetchBacklogFromStatusSumsQueueDepths(t *testing.T) {
	fake := statusRunner(t, cmdruntest.Out(`{"queues":[{"depth":2},{"depth":5}]}`))
	opts := baseOpts(t)
	opts.statusTimeout = 5 * time.Second
	opts.pgRouterPath = "/x/pg-router"
	got, err := fetchBacklogFromStatus(context.Background(), opts)
	if err != nil || got != 7 {
		t.Fatalf("got %d, %v; want 7, nil", got, err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "/x/pg-router" || strings.Join(calls[0].Args, " ") != "status --json" {
		t.Fatalf("called %+v, want /x/pg-router status --json", calls)
	}
}

func TestFetchBacklogFromStatusNonZeroExitIsAnErrorNotATimeout(t *testing.T) {
	statusRunner(t, cmdruntest.Fail(2, "boom"))
	_, err := fetchBacklogFromStatus(context.Background(), statusOpts(t))
	if err == nil || !strings.Contains(err.Error(), "exit status 2") || isTimeout(err) {
		t.Fatalf("got %v, want a non-timeout error naming exit status 2", err)
	}
}

func TestFetchBacklogFromStatusStartErrorIsReported(t *testing.T) {
	start := errors.New("exec: not found")
	statusRunner(t, cmdrun.Result{Code: -1, StartErr: start})
	_, err := fetchBacklogFromStatus(context.Background(), statusOpts(t))
	if !errors.Is(err, start) {
		t.Fatalf("got %v, want it to wrap the start error", err)
	}
}

func statusOpts(t *testing.T) runOptions {
	t.Helper()
	opts := baseOpts(t)
	opts.statusTimeout = 5 * time.Second
	return opts
}
