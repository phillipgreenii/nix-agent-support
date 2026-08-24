package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

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

	// pushesPerStream, when > 0, ends each WatchState stream after that many
	// pushes instead of pushing for as long as the client listens. That is how the
	// idle-streak tests break the STREAM while leaving the daemon up and dialable
	// — a daemon that goes away lands on the separate unavailable path, which
	// resets the streak for its own reasons. thenStall picks WHICH break: false
	// returns nil, a clean EOF the client sees on Recv() at once (the real daemon's
	// graceful-shutdown behaviour), true blocks instead so the client's own
	// pushBudget watchdog fires and the unobserved gap exceeds that budget by
	// construction. failErr, when set, takes priority over both: the stream ends
	// with this error instead — a genuine transport failure, as opposed to a clean
	// EOF or a watchdog stall — which is how the Recv-error reporting test forces
	// the "reports the latter" half of that contract. streams counts handler
	// entries, i.e. how many streams the client actually opened.
	pushesPerStream int
	thenStall       bool
	failErr         error
	streams         atomic.Int64

	// lastPushIntervalMs records the PushIntervalMs field of the most recent
	// WatchStateRequest, so a test can confirm the client actually requests the
	// pushIntervalMs constant rather than a bare literal that happens to match it
	// today.
	lastPushIntervalMs atomic.Int64
}

func (f *fakeWaitDaemon) WatchState(req *pb.WatchStateRequest, stream pb.PaMonitor_WatchStateServer) error {
	f.streams.Add(1)
	f.lastPushIntervalMs.Store(int64(req.GetPushIntervalMs()))
	t := time.NewTicker(f.push)
	defer t.Stop()
	sent := 0
	for {
		// A new message per push: proto messages are not safe to share across
		// concurrent sends and reads.
		if err := stream.Send(&pb.DaemonState{
			Dirs: []*pb.Directory{{Path: "/fake/repo", WorkingN: f.workingN}},
		}); err != nil {
			return err
		}
		f.pushes.Add(1)
		sent++
		if f.pushesPerStream > 0 && sent >= f.pushesPerStream {
			if f.failErr != nil {
				return f.failErr
			}
			if !f.thenStall {
				return nil
			}
			<-stream.Context().Done()
			return nil
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-t.C:
		}
	}
}

// connCounter counts accepted client connections at the DAEMON side, which for
// this wait loop is its retry count: every pass through the reconnect loop dials
// a fresh grpc.ClientConn and Closes it again. It rides in as a
// grpc.StatsHandler server option so measuring the rate needs no change to
// serveFakeDaemon's existing callers, and it is independent of anything the loop
// chooses to print.
type connCounter struct{ n atomic.Int64 }

func (c *connCounter) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (c *connCounter) HandleRPC(context.Context, stats.RPCStats)                       {}
func (c *connCounter) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (c *connCounter) HandleConn(_ context.Context, s stats.ConnStats) {
	if _, ok := s.(*stats.ConnBegin); ok {
		c.n.Add(1)
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
// reconnect-grace tests simulate a mid-wait daemon disappearance. opts are
// passed to grpc.NewServer, which is how the refused-stream-open test makes a
// daemon that answers the dial but rejects every stream.
func serveFakeDaemon(t *testing.T, sock string, srv pb.PaMonitorServer, opts ...grpc.ServerOption) (stop func()) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(opts...)
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

// TestWaitUntilAgentsFinishedPacesRefusedStreamOpens is the pg2-2snsq
// regression. A daemon that ANSWERS the dial but REFUSES the WatchState stream
// open sent the loop straight back into a fresh dial with no pause and no
// backoff, and silently: pre-fix, THIS harness counts 6,448 dial/WatchState/Close
// cycles in its 2s window (15,925 without the stats handler below, whose
// bookkeeping is what accounts for the difference). Post-fix the same window
// admits 4 — one per 500ms reconnectPause.
//
// grpc.MaxHeaderListSize(1) is how the fake daemon refuses the OPEN rather than
// the RPC, and the distinction is the whole point: a server-streaming open
// completes CLIENT-side, so a handler that merely returns an error surfaces on
// Recv() instead and lands on the already-paced post-streamLoop path. Advertising
// SETTINGS_MAX_HEADER_LIST_SIZE=1 instead makes the client transport reject its
// own outbound HEADERS, so client.C.WatchState returns an error while the socket
// stays dialable — the exact combination this branch handles, held indefinitely.
func TestWaitUntilAgentsFinishedPacesRefusedStreamOpens(t *testing.T) {
	sock := waitTestSocket(t)
	conns := &connCounter{}
	serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 200 * time.Millisecond, workingN: 0},
		grpc.MaxHeaderListSize(1), grpc.StatsHandler(conns))

	const maxWait = 2 * time.Second
	var stderr bytes.Buffer
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     maxWait,
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)

	// Exit 1 (not 2): every dial SUCCEEDS here, so the daemon-unavailable branch
	// is never reached and the deadline is what ends the wait.
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout); stderr: %q", code, stderr.String())
	}
	retries := conns.n.Load()
	if retries == 0 {
		t.Fatalf("the daemon accepted no connection: the fake never exercised the branch; stderr: %q", stderr.String())
	}
	// THE RATE ASSERTION. At one retry per reconnectPause a 2s wait admits ~4;
	// this bound sits an order of magnitude above that so a loaded machine cannot
	// flake it, and more than two orders below the 6,448 this harness counts
	// pre-fix.
	if retries > 40 {
		t.Errorf("%d refused-open retries within %s: the reconnect is not paced", retries, maxWait)
	}
	// Separately: the branch must SAY something. It retried silently before, which
	// is why the spin presented only as a late exit.
	if n := strings.Count(stderr.String(), "wait: stream refused, reconnecting"); n == 0 {
		t.Errorf("stderr = %q, want a refused-open diagnostic per retry", stderr.String())
	}
}

// TestWaitUntilAgentsFinishedIdleStreakRestartsAfterAStalledStream is the
// pg2-klyz7 regression. `streak` lived outside the reconnect loop with no notion
// of WHEN each observation happened, so idle observations either side of a stream
// break counted as consecutive and --consecutive-idle-checks N could be satisfied
// by observations that were not consecutive in time.
//
// This daemon serves two idle pushes per stream and then goes silent while
// staying dialable, so the client's pushBudget watchdog fires and the gap between
// the second observation of one stream and the first of the next always exceeds
// that budget. With --consecutive-idle-checks 3 the streak must therefore never
// complete and the wait must end at --maximum-wait; pre-decision it exited 0 on
// the first push after the first reconnect.
func TestWaitUntilAgentsFinishedIdleStreakRestartsAfterAStalledStream(t *testing.T) {
	sock := waitTestSocket(t)
	d := &fakeWaitDaemon{push: 50 * time.Millisecond, workingN: 0, pushesPerStream: 2, thenStall: true}
	serveFakeDaemon(t, sock, d)

	// One cycle is ~2.6s (two prompt pushes, the 2s watchdog, the 500ms redial
	// pause, the dial), so this window admits two full cycles with room to spare —
	// enough that the streak provably had the chance to reach 3 across breaks.
	const maxWait = 6 * time.Second
	var stderr bytes.Buffer
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     maxWait,
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout): three idle observations separated by stream breaks are not consecutive; stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wait-until-agents-finished: timeout") {
		t.Errorf("stderr = %q, want the documented timeout line", stderr.String())
	}
	if n := d.streams.Load(); n < 2 {
		t.Fatalf("daemon served %d streams: the fake never broke and re-opened one, so nothing was tested", n)
	}
	// The restart must be REPORTED, not silent: without this line a wait making no
	// progress is indistinguishable from a wait still gathering it.
	if !strings.Contains(stderr.String(), "idle streak restarted") {
		t.Errorf("stderr = %q, want the discarded streak reported", stderr.String())
	}
}

// TestWaitUntilAgentsFinishedIdleStreakSurvivesAPromptStreamDrop guards the
// OVERCORRECTION that the sibling test above invites. Resetting the streak on
// every pass through the reconnect loop would discard it on the reconnects a
// HEALTHY long wait makes too, so a daemon that ends its stream more often than
// every --consecutive-idle-checks pushes could never satisfy the gate at all and
// a satisfiable wait would become a guaranteed timeout.
//
// This daemon serves two idle pushes per stream and then returns — the clean EOF
// the real daemon's WatchState sends on graceful shutdown — which the client
// redials after reconnectPause. Both the real daemon and this fake Send
// immediately on stream open, so the third observation lands well inside
// pushBudget of the second and the streak completes ACROSS the break: exit 0, not
// the --maximum-wait timeout.
func TestWaitUntilAgentsFinishedIdleStreakSurvivesAPromptStreamDrop(t *testing.T) {
	sock := waitTestSocket(t)
	d := &fakeWaitDaemon{push: 50 * time.Millisecond, workingN: 0, pushesPerStream: 2}
	serveFakeDaemon(t, sock, d)

	var stderr bytes.Buffer
	start := time.Now()
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     30 * time.Second, // must not be the reason this ends
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: a stream drop redialed inside pushBudget must not discard the streak; stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "all idle") {
		t.Errorf("stderr = %q, want %q", stderr.String(), "all idle")
	}
	// Without a second stream the streak completed without ever crossing a break,
	// which would make the exit-0 above prove nothing.
	if n := d.streams.Load(); n < 2 {
		t.Fatalf("daemon served %d streams: the streak never crossed a stream break", n)
	}
	if strings.Contains(stderr.String(), "idle streak restarted") {
		t.Errorf("stderr = %q: a %s redial is well inside pushBudget (%s) and must not restart the streak", stderr.String(), reconnectPause, pushBudget)
	}
	// Generous, but far below the 30s --maximum-wait: the expected path is ~0.6s
	// (two prompt pushes, the redial pause, the dial), so anything in this range
	// means the streak survived rather than the deadline having ended the wait.
	if elapsed > 20*time.Second {
		t.Errorf("idle exit took %s: the streak did not survive the stream drop", elapsed)
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

// TestPushBudgetIsTwicePushIntervalMs is bead pg2-lmn5n's compile-time-pin
// guard: pushBudget MUST be derived from pushIntervalMs, not a separate
// literal, so raising the interval alone cannot silently change the ratio.
// Because pushBudget's declaration is already the formula this test checks,
// only a hand-edit back to a bare literal (the exact regression pg2-lmn5n
// describes: 1000 -> 2000 leaving pushBudget at a hardcoded 2s, i.e. 1x
// instead of 2x) can make this fail.
func TestPushBudgetIsTwicePushIntervalMs(t *testing.T) {
	want := 2 * time.Duration(pushIntervalMs) * time.Millisecond
	if pushBudget != want {
		t.Fatalf("pushBudget = %s, want 2x pushIntervalMs (%dms) = %s", pushBudget, pushIntervalMs, want)
	}
}

// TestWaitRequestsPushIntervalMsConstant confirms the client actually sends
// the pushIntervalMs constant on the wire, not a bare literal that merely
// happens to match it today — the other half of pinning the pushBudget
// relationship in code rather than prose.
func TestWaitRequestsPushIntervalMsConstant(t *testing.T) {
	sock := waitTestSocket(t)
	d := &fakeWaitDaemon{push: 20 * time.Millisecond, workingN: 0}
	serveFakeDaemon(t, sock, d)

	var stderr bytes.Buffer
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     10 * time.Second,
		consecutive: 1,
		grace:       30 * time.Second,
	}, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", code, stderr.String())
	}
	if got := d.lastPushIntervalMs.Load(); got != int64(pushIntervalMs) {
		t.Errorf("WatchStateRequest.PushIntervalMs = %d, want the pushIntervalMs constant (%d)", got, pushIntervalMs)
	}
}

// TestWaitUntilAgentsFinishedReportsGenuineStreamError is bead pg2-lmn5n's
// other half: a Recv error that is NEITHER a clean EOF NOR this loop's own
// ctx being canceled is a genuine transport failure and MUST be reported,
// carrying the error, in the sibling reconnect paths' idiom. The fake daemon
// ends the stream with a real RPC error after two pushes instead of the
// clean nil the EOF-path tests use.
func TestWaitUntilAgentsFinishedReportsGenuineStreamError(t *testing.T) {
	sock := waitTestSocket(t)
	d := &fakeWaitDaemon{
		push:            20 * time.Millisecond,
		workingN:        0,
		pushesPerStream: 2,
		failErr:         status.Error(codes.Internal, "boom"),
	}
	serveFakeDaemon(t, sock, d)

	const maxWait = 3 * time.Second
	var stderr bytes.Buffer
	code := waitUntilAgentsFinished(waitParams{
		maxWait: maxWait,
		// Unreachable: only 2 idle pushes land before every stream fails, so
		// the streak can never reach this and the wait must run to the
		// deadline while repeatedly hitting the failure path.
		consecutive: 100,
		grace:       30 * time.Second,
	}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout while redialing past the failure); stderr: %q", code, stderr.String())
	}
	if n := d.streams.Load(); n < 2 {
		t.Fatalf("daemon served %d streams: the fake never failed and reconnected, so nothing was tested", n)
	}
	if !strings.Contains(stderr.String(), "wait: stream error, reconnecting") {
		t.Errorf("stderr = %q, want a genuine transport-failure diagnostic in the sibling paths' idiom", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want the underlying error text carried along", stderr.String())
	}
}

// TestValidateConsecutiveIdleChecks is the pure-function boundary test for
// bead pg2-e05tm: 0 and every negative value must be rejected (a streak
// cannot reach a target below 1), while 1 — the value the DECISION comment
// on waitUntilAgentsFinished (bead pg2-klyz7) singles out as the point where
// the consecutiveness guarantee is vacuous — and every larger value must
// still be accepted. This drives validateConsecutiveIdleChecks directly
// rather than through runWaitUntilAgentsFinished, which os.Exits and so
// cannot be exercised in-process by a normal test.
func TestValidateConsecutiveIdleChecks(t *testing.T) {
	for _, tc := range []struct {
		consecutive int
		wantErr     bool
	}{
		{-100, true},
		{-1, true},
		{0, true},
		{1, false},
		{2, false},
		{3, false},
		{100, false},
	} {
		err := validateConsecutiveIdleChecks(tc.consecutive)
		if tc.wantErr && err == nil {
			t.Errorf("validateConsecutiveIdleChecks(%d) = nil, want an error", tc.consecutive)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateConsecutiveIdleChecks(%d) = %v, want nil", tc.consecutive, err)
		}
	}
}

// TestWaitUntilAgentsFinishedConsecutiveIdleChecksOneExitsOnFirstIdleObservation
// locks in the runtime behavior validateConsecutiveIdleChecks deliberately
// leaves in place for the accepted boundary value 1: the wait must exit 0 as
// soon as a SINGLE idle observation lands, with no debounce. This is
// distinct from the 0/negative case, which never reaches this function at
// all because runWaitUntilAgentsFinished rejects it first.
func TestWaitUntilAgentsFinishedConsecutiveIdleChecksOneExitsOnFirstIdleObservation(t *testing.T) {
	sock := waitTestSocket(t)
	serveFakeDaemon(t, sock, &fakeWaitDaemon{push: 200 * time.Millisecond, workingN: 0})

	var stderr bytes.Buffer
	start := time.Now()
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     30 * time.Second, // must be irrelevant to when this returns
		consecutive: 1,
		grace:       30 * time.Second,
	}, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (idle reached on the first observation); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "all idle") {
		t.Errorf("stderr = %q, want %q", stderr.String(), "all idle")
	}
	// Generous, but far below the 30s --maximum-wait: the expected path is a
	// single push interval, so anything in this range proves the single
	// observation satisfied the streak rather than the deadline ending the
	// wait.
	if elapsed > 10*time.Second {
		t.Errorf("idle exit took %s: a single idle observation should satisfy --consecutive-idle-checks 1 immediately", elapsed)
	}
}

// TestWaitUntilAgentsFinishedMaximumWaitZeroOrNegativeExitsImmediately
// documents --maximum-wait's zero/negative behavior (see its declaration in
// runWaitUntilAgentsFinished): the deadline is already expired at start, so
// the wait must exit 1 (timeout) on its very first pass through the loop —
// before ever dialing the daemon. No daemon is served on the socket at all,
// which is the proof: if the loop dialed first, it would find nothing
// listening and exit 2 (daemon unreachable) instead of 1.
func TestWaitUntilAgentsFinishedMaximumWaitZeroOrNegativeExitsImmediately(t *testing.T) {
	for _, maxWait := range []time.Duration{0, -1 * time.Second, -1 * time.Hour} {
		t.Run(maxWait.String(), func(t *testing.T) {
			waitTestSocket(t) // no server listens on it

			var stderr bytes.Buffer
			start := time.Now()
			code := waitUntilAgentsFinished(waitParams{
				maxWait:     maxWait,
				consecutive: 3,
				grace:       30 * time.Second,
			}, &stderr)
			elapsed := time.Since(start)

			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (timeout: --maximum-wait %s is already expired); stderr: %q", code, maxWait, stderr.String())
			}
			if !strings.Contains(stderr.String(), "wait-until-agents-finished: timeout") {
				t.Errorf("stderr = %q, want the documented timeout line", stderr.String())
			}
			if elapsed > 2*time.Second {
				t.Errorf("took %s to report an already-expired deadline; want near-instant, and certainly before any dial attempt", elapsed)
			}
		})
	}
}

// TestWaitUntilAgentsFinishedReconnectGraceZeroOrNegativeFailsWithoutRetry
// documents --reconnect-grace's zero/negative behavior (see its declaration
// in runWaitUntilAgentsFinished): a grace of zero or less skips the retry
// loop in waitForDaemon entirely, so a daemon that disappears mid-wait must
// fail at once with NO retry. This is deliberately distinct from
// TestWaitUntilAgentsFinishedDaemonNeverUpFails' streak==0 "daemon
// unreachable" path — that path never consults --reconnect-grace at all — so
// this test first drives the streak above zero before killing the daemon,
// and asserts the DIFFERENT ("did not return within grace") message that
// only the grace path emits.
func TestWaitUntilAgentsFinishedReconnectGraceZeroOrNegativeFailsWithoutRetry(t *testing.T) {
	for _, grace := range []time.Duration{0, -1 * time.Second} {
		t.Run(grace.String(), func(t *testing.T) {
			sock := waitTestSocket(t)
			d := &fakeWaitDaemon{push: 50 * time.Millisecond, workingN: 0}
			stop := serveFakeDaemon(t, sock, d)

			const maxWait = 10 * time.Second
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
					maxWait:     maxWait,
					consecutive: 100, // unreachable: keeps the streak from completing
					grace:       grace,
				}, &stderr)
				done <- result{code, stderr.String(), time.Since(start)}
			}()

			// Two pushes means at least one landed and was folded into the
			// streak before the daemon goes away, which is the precondition
			// for the grace path (rather than the streak==0 immediate-fail
			// path) to be the one exercised below.
			waitFor(t, 5*time.Second, func() bool { return d.pushes.Load() >= 2 })
			stop()

			select {
			case got := <-done:
				if got.code != 2 {
					t.Fatalf("exit code = %d, want 2 (daemon unavailable, no retry within a %s grace); stderr: %q", got.code, grace, got.stderr)
				}
				if !strings.Contains(got.stderr, "daemon did not return within grace") {
					t.Errorf("stderr = %q, want the grace-exhausted message", got.stderr)
				}
				if strings.Contains(got.stderr, "daemon unreachable") {
					t.Errorf("stderr = %q: this must go through the grace path, not the streak==0 path", got.stderr)
				}
				if got.elapsed > maxWait {
					t.Errorf("took %s: a %s reconnect-grace must fail fast, well before --maximum-wait (%s)", got.elapsed, grace, maxWait)
				}
			case <-time.After(maxWait + 30*time.Second):
				t.Fatal("wait did not finish after the daemon disappeared with an exhausted reconnect-grace")
			}
		})
	}
}

// TestWaitUntilAgentsFinishedCleanEOFStaysQuiet is the flip side: a clean EOF
// — the daemon's DESIGNED graceful-shutdown behaviour, which a healthy long
// wait redials through routinely — must NOT be reported as a stream error.
// workingN stays 1 so the streak can never complete and the wait keeps
// cycling through clean-EOF redials until --maximum-wait, giving the quiet
// path many chances to (incorrectly) speak up.
func TestWaitUntilAgentsFinishedCleanEOFStaysQuiet(t *testing.T) {
	sock := waitTestSocket(t)
	d := &fakeWaitDaemon{push: 20 * time.Millisecond, workingN: 1, pushesPerStream: 2}
	serveFakeDaemon(t, sock, d)

	const maxWait = 1500 * time.Millisecond
	var stderr bytes.Buffer
	code := waitUntilAgentsFinished(waitParams{
		maxWait:     maxWait,
		consecutive: 3,
		grace:       30 * time.Second,
	}, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (timeout); stderr: %q", code, stderr.String())
	}
	if n := d.streams.Load(); n < 2 {
		t.Fatalf("daemon served %d streams: the clean-EOF redial path was never exercised", n)
	}
	if strings.Contains(stderr.String(), "stream error") {
		t.Errorf("stderr = %q: a clean EOF (the daemon's designed graceful shutdown) must not be reported as a stream error", stderr.String())
	}
}

// TestParseWaitArgsUnknownFlagExitsWithDocumentedInvalidArgsCode pins the fix
// for bead pg2-3rlwm: an unparseable flag (bad name here) must produce
// exit 3, the "invalid args" code the doc comment on runWaitUntilAgentsFinished
// documents. Before the fix, the flag set used flag.ExitOnError, so fs.Parse
// called os.Exit(2) on its own before the code below could ever run --
// `pa-monitor wait-until-agents-finished --bogus` exited 2, never 3. This
// drives parseWaitArgs directly (rather than through runWaitUntilAgentsFinished,
// which os.Exits and so cannot be exercised in-process by a normal test).
func TestParseWaitArgsUnknownFlagExitsWithDocumentedInvalidArgsCode(t *testing.T) {
	var stderr bytes.Buffer
	_, code, ok := parseWaitArgs([]string{"--bogus"}, &stderr)

	if ok {
		t.Fatal("ok = true, want false for an unrecognized flag")
	}
	if code != 3 {
		t.Errorf("code = %d, want 3 (invalid args); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Errorf("stderr = %q, want it to contain the flag package's own parse diagnostic", stderr.String())
	}
}

// TestParseWaitArgsMalformedValueExitsWithDocumentedInvalidArgsCode covers the
// other unparseable-flag shape the doc comment names: a value that fails
// flag.Value.Set (a non-integer for an int flag), as opposed to an unknown
// flag name.
func TestParseWaitArgsMalformedValueExitsWithDocumentedInvalidArgsCode(t *testing.T) {
	var stderr bytes.Buffer
	_, code, ok := parseWaitArgs([]string{"--consecutive-idle-checks", "abc"}, &stderr)

	if ok {
		t.Fatal("ok = true, want false for a non-integer flag value")
	}
	if code != 3 {
		t.Errorf("code = %d, want 3 (invalid args); stderr: %q", code, stderr.String())
	}
}

// TestParseWaitArgsRejectsConsecutiveIdleChecksZero locks in that the OTHER
// exit-3 path -- semantic validation via validateConsecutiveIdleChecks, which
// was already reachable before this bead (pg2-e05tm) -- still works
// unchanged now that flag parsing routes through parseWaitArgs.
func TestParseWaitArgsRejectsConsecutiveIdleChecksZero(t *testing.T) {
	var stderr bytes.Buffer
	_, code, ok := parseWaitArgs([]string{"--consecutive-idle-checks", "0"}, &stderr)

	if ok {
		t.Fatal("ok = true, want false for --consecutive-idle-checks 0")
	}
	if code != 3 {
		t.Errorf("code = %d, want 3 (invalid args); stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be >= 1") {
		t.Errorf("stderr = %q, want validateConsecutiveIdleChecks's diagnostic", stderr.String())
	}
}

// TestParseWaitArgsHelpFlagExitsZeroWithoutError guards the regression this
// bead's fix could otherwise introduce: switching the flag set from
// flag.ExitOnError to flag.ContinueOnError makes fs.Parse RETURN flag.ErrHelp
// for -h/--help instead of exiting the process itself, and that return must
// still be read as "exit 0, print nothing further" -- not folded into the
// same code path as a genuine parse error (which would wrongly turn
// `--help` into exit 3).
func TestParseWaitArgsHelpFlagExitsZeroWithoutError(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			var stderr bytes.Buffer
			_, code, ok := parseWaitArgs([]string{flag}, &stderr)

			if ok {
				t.Fatal("ok = true, want false for a help flag (nothing to run)")
			}
			if code != 0 {
				t.Errorf("code = %d, want 0 for %s; stderr: %q", code, flag, stderr.String())
			}
		})
	}
}

// TestParseWaitArgsAcceptsValidFlags is the happy-path complement to the
// rejection tests above: valid flags must parse into the expected
// waitParams with ok=true and no stderr output.
func TestParseWaitArgsAcceptsValidFlags(t *testing.T) {
	var stderr bytes.Buffer
	p, code, ok := parseWaitArgs([]string{
		"--maximum-wait", "60",
		"--consecutive-idle-checks", "5",
		"--reconnect-grace", "10",
	}, &stderr)

	if !ok {
		t.Fatalf("ok = false, want true; code=%d stderr=%q", code, stderr.String())
	}
	if p.maxWait != 60*time.Second {
		t.Errorf("maxWait = %s, want 60s", p.maxWait)
	}
	if p.consecutive != 5 {
		t.Errorf("consecutive = %d, want 5", p.consecutive)
	}
	if p.grace != 10*time.Second {
		t.Errorf("grace = %s, want 10s", p.grace)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty for valid flags", stderr.String())
	}
}
