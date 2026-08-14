package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// fakeWaitDaemon is a minimal PaMonitorServer serving WatchState only — the one
// RPC the wait loop uses. It pushes a freshly built DaemonState on a fixed
// cadence so a test can hold the wait open (workingN > 0) or release it
// (workingN == 0) with a HEALTHY push stream, which is precisely the state the
// old deadline-between-reconnects check could never terminate (bead pg2-yzw29).
type fakeWaitDaemon struct {
	pb.UnimplementedPaMonitorServer
	push     time.Duration
	workingN uint32
	pushes   atomic.Int64
}

func (f *fakeWaitDaemon) WatchState(_ *pb.WatchStateRequest, stream pb.PaMonitor_WatchStateServer) error {
	t := time.NewTicker(f.push)
	defer t.Stop()
	for {
		// A new message per push: proto messages are not safe to share across
		// concurrent sends and reads.
		if err := stream.Send(&pb.DaemonState{
			Dirs: []*pb.Directory{{Path: "/fake/repo", WorkingN: f.workingN}},
		}); err != nil {
			return err
		}
		f.pushes.Add(1)
		select {
		case <-stream.Context().Done():
			return nil
		case <-t.C:
		}
	}
}

// waitTestSocket points every path rpcclient.Dial consults at a fresh temp dir
// and returns the socket path it will resolve to. /tmp rather than t.TempDir()
// because macOS caps unix socket paths at 104 bytes and the Go temp root is well
// past that (the same reason internal/daemon's tests use shortTempDir).
//
// Setting all three of XDG_STATE_HOME / XDG_RUNTIME_DIR / HOME is load-bearing:
// ResolvePaths reads XDG_STATE_HOME on darwin, XDG_RUNTIME_DIR first on linux,
// and falls back to HOME, so leaving any of them alone would let these tests see
// a pa-monitor daemon running on the developer's machine.
func waitTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pamwait-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "pa-monitor"), 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "pa-monitor", "daemon.sock")
}

// serveFakeDaemon serves srv on sock until the returned stop func runs (also
// registered as cleanup). Stopping and re-serving the same socket is how the
// reconnect-grace tests simulate a mid-wait daemon disappearance.
func serveFakeDaemon(t *testing.T, sock string, srv pb.PaMonitorServer) (stop func()) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	pb.RegisterPaMonitorServer(gs, srv)
	served := make(chan struct{})
	go func() { defer close(served); _ = gs.Serve(ln) }()

	var stopped atomic.Bool
	stop = func() {
		if stopped.Swap(true) {
			return
		}
		gs.Stop()
		<-served
		_ = os.Remove(sock)
	}
	t.Cleanup(stop)
	return stop
}

// TestWaitUntilAgentsFinishedTimesOutWhileBusy is the pg2-yzw29 regression: a
// HEALTHY daemon pushing a session with WorkingN > 0 used to spin streamLoop
// forever, because the deadline was only re-tested in the outer reconnect loop.
// Exit 1 must now be reached within a bounded margin of --maximum-wait.
func TestWaitUntilAgentsFinishedTimesOutWhileBusy(t *testing.T) {
	sock := waitTestSocket(t)
	serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 200 * time.Millisecond, workingN: 1})

	const maxWait = 2 * time.Second
	var stderr bytes.Buffer
	start := time.Now()
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     maxWait,
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)
	elapsed := time.Since(start)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wait-until-agents-finished: timeout") {
		t.Errorf("stderr = %q, want the documented timeout line", stderr.String())
	}
	if elapsed < maxWait {
		t.Errorf("returned after %s, before --maximum-wait (%s) elapsed", elapsed, maxWait)
	}
	// Generous upper bound: the theoretical overshoot is one push interval plus
	// stream teardown, so anything near maxWait passes under load while the old
	// unbounded behaviour (which never returned) still fails.
	if elapsed > maxWait+10*time.Second {
		t.Errorf("timeout took %s, want within 10s of --maximum-wait (%s)", elapsed, maxWait)
	}
}

// TestWaitUntilAgentsFinishedIdleStreakExitsPromptly guards the success path
// against the deadline change: reaching the consecutive-idle streak must exit 0
// at once and NOT wait out --maximum-wait.
func TestWaitUntilAgentsFinishedIdleStreakExitsPromptly(t *testing.T) {
	sock := waitTestSocket(t)
	serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 100 * time.Millisecond, workingN: 0})

	var stderr bytes.Buffer
	start := time.Now()
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     10 * time.Minute, // must be irrelevant to when this returns
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (idle reached); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "all idle") {
		t.Errorf("stderr = %q, want %q", stderr.String(), "all idle")
	}
	if elapsed > 30*time.Second {
		t.Errorf("idle exit took %s; the deadline must not gate the success path", elapsed)
	}
}

// TestWaitUntilAgentsFinishedDaemonNeverUpFails covers the no-tick-observed
// path: without a streak there is no reconnect grace to apply, so a missing
// daemon is exit 2, not a timeout.
//
// The short --maximum-wait case is the precedence guard. The dial has its own
// 2s handshake budget, so a --maximum-wait UNDER that budget expires while the
// first dial is still in flight. "Never reached the daemon" MUST still win over
// "time ran out" there: the exit-2 message is what
// test-wait-for-agents-real-pa-monitor.bats reads to tell "args accepted, wait
// loop reached" apart from "args rejected in flag parsing", which both exit 2.
func TestWaitUntilAgentsFinishedDaemonNeverUpFails(t *testing.T) {
	for _, maxWait := range []time.Duration{60 * time.Second, 1 * time.Second} {
		t.Run(maxWait.String(), func(t *testing.T) {
			waitTestSocket(t) // no server listens on it

			var stderr bytes.Buffer
			code := waitUntilAgentsFinished(waitParams{
				maxWait:     maxWait,
				consecutive: 3,
				grace:       30 * time.Second,
			}, &stderr)

			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (daemon unavailable); stderr: %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "daemon unreachable") {
				t.Errorf("stderr = %q, want %q", stderr.String(), "daemon unreachable")
			}
		})
	}
}

// TestWaitUntilAgentsFinishedReconnectGracePreserved asserts the deadline change
// did not break the grace path: a daemon that vanishes mid-wait and returns
// inside --reconnect-grace is picked back up and the wait still reaches exit 0.
func TestWaitUntilAgentsFinishedReconnectGracePreserved(t *testing.T) {
	sock := waitTestSocket(t)

	// consecutive is set high enough that phase 1's few idle pushes cannot
	// complete the streak, so the disappearance lands mid-wait with streak > 0
	// (the precondition for grace to apply at all).
	const consecutive = 12
	phase1 := &fakeWaitDaemon{push: 250 * time.Millisecond, workingN: 0}
	stop := serveFakeDaemon(t, sock, phase1)

	type result struct {
		code    int
		stderr  string
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		var stderr bytes.Buffer
		code := waitUntilAgentsFinished(waitParams{
			maxWait:     2 * time.Minute, // must not be the reason this ends
			consecutive: consecutive,
			grace:       60 * time.Second,
		}, &stderr)
		done <- result{code, stderr.String(), time.Since(start)}
	}()

	// Four server-side pushes means pushes 1..3 were written to the socket, so
	// the client has certainly folded at least one idle observation into its
	// streak by the time the daemon disappears.
	waitFor(t, 30*time.Second, func() bool { return phase1.pushes.Load() >= 4 })
	stop()

	// Stay down long enough that the client's 500ms post-teardown pause expires
	// and its redial genuinely fails, which is what routes it through
	// waitForDaemon rather than straight into a fresh stream.
	time.Sleep(2500 * time.Millisecond)
	serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 100 * time.Millisecond, workingN: 0})

	select {
	case got := <-done:
		if got.code != 0 {
			t.Fatalf("exit code = %d, want 0 after the daemon returned inside grace; stderr: %q", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, "all idle") {
			t.Errorf("stderr = %q, want %q", got.stderr, "all idle")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("wait did not finish after the daemon returned inside --reconnect-grace")
	}
}

// TestWaitUntilAgentsFinishedGraceCannotOutlastDeadline is the other half of the
// fix: --reconnect-grace must not become a second unbounded wait. A daemon that
// vanishes for good with a 10-minute grace and a 2s --maximum-wait must report
// the timeout at the deadline, not sit out the grace window.
func TestWaitUntilAgentsFinishedGraceCannotOutlastDeadline(t *testing.T) {
	sock := waitTestSocket(t)
	stop := serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 200 * time.Millisecond, workingN: 0})

	// consecutive is high so the streak cannot complete before the daemon goes
	// away, but is >= 1 by then, which is what arms the grace path.
	const maxWait = 2 * time.Second
	go func() {
		time.Sleep(600 * time.Millisecond)
		stop()
	}()

	var stderr bytes.Buffer
	start := time.Now()
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     maxWait,
		consecutive: 100,
		grace:       10 * time.Minute, // would swallow the deadline if unclamped
	}, &stderr)
	elapsed := time.Since(start)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wait-until-agents-finished: timeout") {
		t.Errorf("stderr = %q, want the documented timeout line", stderr.String())
	}
	if elapsed > 60*time.Second {
		t.Errorf("took %s: the reconnect-grace window outlasted --maximum-wait (%s)", elapsed, maxWait)
	}
}
