package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// statusRunner swaps the package runner for one that answers `pg-router
// status --json` with res and records the Call it saw.
func statusRunner(t *testing.T, res Result) *Call {
	t.Helper()
	var seen Call
	orig := runner
	runner = func(_ context.Context, c Call) Result {
		seen = c
		return res
	}
	t.Cleanup(func() { runner = orig })
	return &seen
}

func TestFetchBacklogFromStatusSumsQueueDepths(t *testing.T) {
	seen := statusRunner(t, Result{Stdout: `{"queues":[{"depth":2},{"depth":5}]}`})
	opts := baseOpts(t)
	opts.statusTimeout = 5 * time.Second
	opts.pgRouterPath = "/x/pg-router"
	got, err := fetchBacklogFromStatus(context.Background(), opts)
	if err != nil || got != 7 {
		t.Fatalf("got %d, %v; want 7, nil", got, err)
	}
	if seen.Name != "/x/pg-router" || strings.Join(seen.Args, " ") != "status --json" {
		t.Fatalf("called %+v, want /x/pg-router status --json", *seen)
	}
}

func TestFetchBacklogFromStatusNonZeroExitIsAnErrorNotATimeout(t *testing.T) {
	statusRunner(t, Result{Code: 2, Stderr: "boom"})
	_, err := fetchBacklogFromStatus(context.Background(), statusOpts(t))
	if err == nil || !strings.Contains(err.Error(), "exit status 2") || isTimeout(err) {
		t.Fatalf("got %v, want a non-timeout error naming exit status 2", err)
	}
}

func TestFetchBacklogFromStatusStartErrorIsReported(t *testing.T) {
	start := errors.New("exec: not found")
	statusRunner(t, Result{Code: -1, StartErr: start})
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
