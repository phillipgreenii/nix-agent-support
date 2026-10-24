package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Bead pg2-pyewq: a handler that exits 1, as `pg-desk run` does for a failed
// sync stage (pg2-kftf9.1), reaches the listener through the REAL
// wireclient.OSRunner (a real subprocess), not a fake HandlerClient. The
// fakeHandler tests in listener_test.go and retry_test.go cover the listener's
// reaction to an error value; what they cannot cover is that a subprocess exit
// code and its stdout reply body become the error the listener classifies.
// The handler here is a stub shell script standing in for the handler only; it
// does not exercise pg-desk (pg-router and pg-desk are separate Go modules).

// subprocessHandlerScript writes a handler script that counts its runs in a
// file next to it and exits per run: runs 1..failRuns print failBody on stdout
// and exit 1; later runs print a clean delivered reply and exit 0. It returns
// the script path and a func returning how many times it ran.
func subprocessHandlerScript(t *testing.T, failRuns int, failBody string) (script string, runs func() int) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "handler.sh")
	counter := filepath.Join(dir, "runs")
	body := "#!/bin/sh\n" +
		"cat > /dev/null\n" +
		"n=$(cat '" + counter + "' 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"echo $n > '" + counter + "'\n" +
		"if [ $n -le " + strconv.Itoa(failRuns) + " ]; then\n" +
		"  echo '" + failBody + "'\n" +
		"  exit 1\n" +
		"fi\n" +
		"echo '{\"schemaVersion\":\"1\",\"id\":\"fake\",\"outcome\":\"ok\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write handler script: %v", err)
	}
	return script, func() int {
		b, err := os.ReadFile(counter)
		if err != nil {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Errorf("handler run counter %q is not a number: %v", b, err)
		}
		return n
	}
}

// newSubprocessQueue wires a real queue (with a controllable clock and a 5s
// retry backoff) to a listener whose handler is the real wire client running
// script.
func newSubprocessQueue(t *testing.T, script string, maxRetries int) (q *eventqueue.Queue, advance func(time.Duration), robs *fakeRetryObserver, fobs *fakeHandlerFailureObserver) {
	t.Helper()
	now := time.Now() // real time: roleListener judges expiry against time.Now()
	clock := func() time.Time { return now }
	advance = func(d time.Duration) { now = now.Add(d) }
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clock),
		eventqueue.WithRetryBackoff(backoff.Policy{Initial: 5 * time.Second, Factor: 2, Max: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = wireclient.New(func(roles.Role, string) ([]string, error) { return []string{script}, nil })
	robs = &fakeRetryObserver{}
	fobs = &fakeHandlerFailureObserver{}
	o.DispatchRetryObserver = robs
	o.HandlerFailureObserver = fobs
	q.Register(o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: maxRetries}))
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "pr.changed", At: now, ExpiresAt: now.Add(time.Hour), Payload: map[string]any{"id": "pr-1"}}); err != nil {
		t.Fatal(err)
	}
	return q, advance, robs, fobs
}

// A handler subprocess that exits 1 with a reasoned (non-transient) error is a
// counted failure: the failure observer receives the typed exit error carrying
// the exit code and the handler's own message, the event is accepted (settled,
// never re-run), and no retry is scheduled even though the role allows retries.
func TestRealSubprocess_Exit1_IsCountedFailureAndNotRetried(t *testing.T) {
	script, runs := subprocessHandlerScript(t, 5, `{"schemaVersion":"1","id":"fake","error":"invalid_argument: bad PR id"}`)
	q, advance, robs, fobs := newSubprocessQueue(t, script, 3)

	if got := q.Dispatch(); got != 1 {
		t.Fatalf("pass 1: accepted = %d, want 1 (a deterministic failure is accepted, not retried)", got)
	}
	if len(fobs.calls) != 1 {
		t.Fatalf("failure observer calls = %d, want 1", len(fobs.calls))
	}
	var ee *wireclient.ExitError
	if !errors.As(fobs.errs[0], &ee) || ee.Code != 1 || ee.Role != "desk-pr" || ee.Detail != "invalid_argument: bad PR id" {
		t.Fatalf("failure observer error = %#v, want *wireclient.ExitError{Role desk-pr, Code 1, Detail from the reply body}", fobs.errs[0])
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
	advance(time.Hour)
	q.Dispatch()
	if runs() != 1 {
		t.Fatalf("handler subprocess ran %d times, want 1 (the settled event must not be re-offered)", runs())
	}
}

// A handler subprocess that exits 1 with a transient reply body (the connector
// "unavailable" class) is counted, handed back with a backoff the queue
// honours, and re-run once the backoff has elapsed; the second run exits 0 and
// settles the event cleanly with no further failure counted.
func TestRealSubprocess_Exit1Transient_IsCountedBackedOffThenSettlesOnSuccess(t *testing.T) {
	script, runs := subprocessHandlerScript(t, 1, `{"schemaVersion":"1","id":"fake","error":"connector: unavailable"}`)
	q, advance, robs, fobs := newSubprocessQueue(t, script, 2)

	if got := q.Dispatch(); got != 0 || runs() != 1 {
		t.Fatalf("pass 1: accepted=%d runs=%d, want 0 accepted (retry hand-back) and 1 run", got, runs())
	}
	if len(fobs.calls) != 1 {
		t.Fatalf("failure observer calls after the failed run = %d, want 1", len(fobs.calls))
	}
	if got := robs.snapshot(); len(got) != 1 || got[0] != (retryCall{"desk-pr", RetryClassUnavailable}) {
		t.Fatalf("retry observer calls = %+v, want one {desk-pr unavailable}", got)
	}

	if got := q.Dispatch(); got != 0 || runs() != 1 {
		t.Fatalf("pass 2 (inside the backoff): accepted=%d runs=%d, want the re-run withheld", got, runs())
	}

	advance(6 * time.Second)
	if got := q.Dispatch(); got != 1 || runs() != 2 {
		t.Fatalf("pass 3 (after the backoff): accepted=%d runs=%d, want the clean re-run accepted", got, runs())
	}
	if len(fobs.calls) != 1 {
		t.Fatalf("failure observer calls after the clean run = %d, want still 1", len(fobs.calls))
	}
	if n := len(robs.snapshot()); n != 1 {
		t.Fatalf("retry observer calls after the clean run = %d, want still 1", n)
	}
}
