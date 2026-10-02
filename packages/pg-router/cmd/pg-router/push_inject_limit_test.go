package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/emit"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// startLimitedCore is startCore over a queue whose reported log size the test
// pins (soft 900, hard 1000), so the hard limit can be hit without megabytes.
func startLimitedCore(t *testing.T, logDir string) (*core.Service, *eventqueue.MemStore) {
	t.Helper()
	mem := eventqueue.NewMemStore()
	q, err := eventqueue.New(mem, eventqueue.WithLogLimits(900, 1000))
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	svc, err := core.Listen(core.Options{LogDir: logDir, Queue: q, Bindings: core.NewBindings("review-requested", "t")})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Accept(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Accept = %v, want nil", err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := core.Discover(logDir); err == nil {
			return svc, mem
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("core never became discoverable")
	return nil, nil
}

// PUSH PATH END TO END (acceptance): at the hard log-size limit `push-inject`
// against a real running core exits 1 and shows the fixed `log_full:` prefix with
// the numbers and remedies — in text on stderr and in --json — the event is NOT in
// the core's queue, and once there is room the same injection succeeds.
func TestPushInject_AtTheHardLogLimitShowsLogFullAndExits1(t *testing.T) {
	dir := shortDir(t)
	svc, mem := startLimitedCore(t, dir)
	loc := injectedLocator(svc.Ref().Socket, svc.Ref().Token)

	mem.SetLogSize(1000)
	var stdout, stderr strings.Builder
	code := pushInject(&stdout, &stderr, false, loc, emit.SocketEnqueuer{}, testPushEvent)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 (the prefix is the contract, not a distinct exit code)", code)
	}
	for _, want := range []string{
		`core rejected event "op-1": log_full: pg-router event log is at 1000 of 1000 bytes; the event was NOT queued; safe to retry later`,
		"PG_ROUTER_MAX_LOG_BYTES",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 0 {
		t.Fatalf("a rejected injection reached the queue: depth %d", depth)
	}
	if got := svc.Queue().LimitStatus().RejectedLogFull; got != 1 {
		t.Fatalf("RejectedLogFull = %d, want 1", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := pushInject(&stdout, &stderr, true, loc, emit.SocketEnqueuer{}, testPushEvent); code != conformance.ExitError {
		t.Fatalf("--json exit = %d, want 1", code)
	}
	var got struct {
		Accepted bool   `json:"accepted"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if got.Accepted || !strings.Contains(got.Error, "log_full: pg-router event log is at") {
		t.Fatalf("report = %+v, want accepted=false carrying the log_full reason", got)
	}

	// Room again: the very same injection is accepted.
	mem.SetLogSize(10)
	stdout.Reset()
	stderr.Reset()
	if code := pushInject(&stdout, &stderr, false, loc, emit.SocketEnqueuer{}, testPushEvent); code != exitOK {
		t.Fatalf("exit = %d after space returned, want 0; stderr=%s", code, stderr.String())
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("depth = %d, want 1 after the retry", depth)
	}
}
