package rpcclient

import (
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeWatchStream is an in-memory stand-in for the WatchState client stream.
// Recv drains a queued event (state or error) or unblocks on ctx.Done, matching
// fakeBridgeStream in cmux_bridge_channel_test.go.
type fakeWatchStream struct {
	ctx    context.Context
	events chan recvEvent
}

type recvEvent struct {
	st  *pb.DaemonState
	err error
}

func newFakeWatchStream(ctx context.Context) *fakeWatchStream {
	return &fakeWatchStream{ctx: ctx, events: make(chan recvEvent, 8)}
}

func (f *fakeWatchStream) Recv() (*pb.DaemonState, error) {
	select {
	case e := <-f.events:
		return e.st, e.err
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func (f *fakeWatchStream) push(st *pb.DaemonState) { f.events <- recvEvent{st: st} }
func (f *fakeWatchStream) fail(err error)          { f.events <- recvEvent{err: err} }

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, msg)
}

// sampleState builds a DaemonState with distinct, non-overlapping values on
// every field the poller mirrors, so a field-swap in apply() fails a test
// rather than passing silently.
func sampleState() *pb.DaemonState {
	return &pb.DaemonState{
		Dirs:                      []*pb.Directory{{Path: "/repo/a", WorkingN: 2}},
		AutoResumeEnabled:         true,
		AutoResumeDelayS:          47,
		CaffeinateActive:          true,
		CaffeinateMode:            false, // distinct from Active so a swap is caught
		CaffeinateProcess:         pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE,
		CaffeinateGraceRemainingS: 12,
		DaemonVersion:             "v-test-123",
		Now:                       timestamppb.New(time.Unix(1_700_000_000, 0)),
	}
}

// TestStreamingPoller_ApplyMirrorsState pins that apply(state) stores the
// translated tree plus every meta field, and that Snapshot then reports the
// poller as connected (nil error) serving that tree.
func TestStreamingPoller_ApplyMirrorsState(t *testing.T) {
	p := newStreamingPoller("", time.Second)
	st := sampleState()

	p.apply(st)

	tree, working, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after apply: unexpected error %v (want nil = connected)", err)
	}
	if !reflect.DeepEqual(tree, pb.ToTree(st)) {
		t.Errorf("Snapshot tree = %+v, want pb.ToTree(state)", tree)
	}
	if !working {
		t.Errorf("anyWorking = false, want true (dir has WorkingN=2)")
	}
	if p.IsOffline() {
		t.Errorf("IsOffline() = true after apply, want false")
	}

	if got := p.LastAutoResumeEnabled(); got != true {
		t.Errorf("LastAutoResumeEnabled = %v, want true", got)
	}
	if got := p.LastAutoResumeDelay(); got != 47*time.Second {
		t.Errorf("LastAutoResumeDelay = %v, want 47s", got)
	}
	if got := p.LastCaffeinateActive(); got != true {
		t.Errorf("LastCaffeinateActive = %v, want true (field swap with Mode?)", got)
	}
	if got := p.LastCaffeinateMode(); got != false {
		t.Errorf("LastCaffeinateMode = %v, want false (field swap with Active?)", got)
	}
	if got := p.LastCaffeinateProcess(); got != pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE {
		t.Errorf("LastCaffeinateProcess = %v, want GRACE", got)
	}
	if got := p.LastCaffeinateGraceRemaining(); got != 12*time.Second {
		t.Errorf("LastCaffeinateGraceRemaining = %v, want 12s", got)
	}
	if got := p.LastDaemonVersion(); got != "v-test-123" {
		t.Errorf("LastDaemonVersion = %q, want v-test-123", got)
	}
	if got := p.LastDaemonNow(); !got.Equal(time.Unix(1_700_000_000, 0)) {
		t.Errorf("LastDaemonNow = %v, want 2023-11-14T...", got)
	}
	if p.LastFreshAt().IsZero() {
		t.Errorf("LastFreshAt is zero after apply, want set")
	}
}

// TestStreamingPoller_OfflineContract pins the property that lets the bubbletea
// Model stay unchanged: Snapshot returns ErrOffline whenever the stream is not
// connected (fresh, or after a drop) while STILL serving the last-known tree,
// exactly as RemotePoller did. update.go derives daemonConnected from this
// error, so getting it wrong latches the offline screen off forever.
func TestStreamingPoller_OfflineContract(t *testing.T) {
	p := newStreamingPoller("", time.Second)

	// Before any push: offline, ErrOffline, empty tree.
	tree, _, err := p.Snapshot(context.Background())
	if !errors.Is(err, ErrOffline) {
		t.Errorf("fresh Snapshot err = %v, want ErrOffline", err)
	}
	if !p.IsOffline() {
		t.Errorf("fresh IsOffline() = false, want true")
	}
	if tree != nil {
		t.Errorf("fresh tree = %+v, want nil", tree)
	}

	// After a push: connected, nil error.
	st := sampleState()
	p.apply(st)
	if _, _, err := p.Snapshot(context.Background()); err != nil {
		t.Errorf("Snapshot after apply err = %v, want nil (connected)", err)
	}

	// After the stream drops: offline again, ErrOffline, but STILL serving the
	// last-known tree.
	p.setDisconnected()
	tree, working, err := p.Snapshot(context.Background())
	if !errors.Is(err, ErrOffline) {
		t.Errorf("Snapshot after disconnect err = %v, want ErrOffline", err)
	}
	if !p.IsOffline() {
		t.Errorf("IsOffline() after disconnect = false, want true")
	}
	if !reflect.DeepEqual(tree, pb.ToTree(st)) {
		t.Errorf("after disconnect tree = %+v, want last-known tree (serve-last-known)", tree)
	}
	if !working {
		t.Errorf("after disconnect anyWorking = false, want true (serving last tree)")
	}
}

// TestStreamingPoller_ConsumesPushesAndReconnects drives the background loop
// through an injected watch seam: it applies pushed states, goes offline when
// the stream errors, and redials.
func TestStreamingPoller_ConsumesPushesAndReconnects(t *testing.T) {
	streams := make(chan *fakeWatchStream, 4)
	var mu sync.Mutex
	var calls int
	watch := func(ctx context.Context, _ uint32) (watchStream, func(), error) {
		mu.Lock()
		calls++
		mu.Unlock()
		fs := newFakeWatchStream(ctx)
		streams <- fs
		return fs, func() {}, nil
	}

	p := newStreamingPoller("", 10*time.Millisecond)
	p.watch = watch
	p.reconnectPause = time.Millisecond
	p.start()
	defer func() { _ = p.Close() }()

	// First stream pushes a state -> poller applies it and reports connected.
	fs1 := <-streams
	st := sampleState()
	fs1.push(st)
	eventually(t, time.Second, func() bool {
		tree, _, err := p.Snapshot(context.Background())
		return err == nil && tree != nil
	}, "poller did not apply first push / connect")

	// Stream errors -> poller goes offline (still serving last tree) and redials.
	fs1.fail(io.EOF)
	eventually(t, time.Second, func() bool { return p.IsOffline() },
		"poller did not go offline after stream error")

	select {
	case <-streams: // second watch call = reconnect
	case <-time.After(time.Second):
		t.Fatal("poller did not reconnect (no second watch call)")
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got < 2 {
		t.Errorf("watch calls = %d, want >= 2 (reconnect)", got)
	}
}

// TestStreamingPoller_WatchdogTripsOnSilence pins that a stream which connects
// then goes silent is treated as hung after the watchdog budget: the poller
// goes offline and redials, rather than sitting on a dead stream forever.
func TestStreamingPoller_WatchdogTripsOnSilence(t *testing.T) {
	streams := make(chan *fakeWatchStream, 4)
	watch := func(ctx context.Context, _ uint32) (watchStream, func(), error) {
		fs := newFakeWatchStream(ctx)
		streams <- fs
		return fs, func() {}, nil
	}

	p := newStreamingPoller("", time.Second)
	p.watch = watch
	p.watchdogBudget = 20 * time.Millisecond
	p.reconnectPause = time.Millisecond
	p.start()
	defer func() { _ = p.Close() }()

	// Connect, then go silent.
	fs1 := <-streams
	fs1.push(sampleState())
	eventually(t, time.Second, func() bool { return !p.IsOffline() },
		"poller never connected after first push")

	// No further pushes -> watchdog trips -> offline + redial.
	eventually(t, time.Second, func() bool { return p.IsOffline() },
		"watchdog did not trip on a silent stream")
	select {
	case <-streams: // first stream already consumed above; this is the redial
	case <-time.After(time.Second):
		t.Fatal("poller did not redial after watchdog trip")
	}
}

// TestStreamingPoller_CloseStopsLoopIdempotently pins that Close joins the
// receiver goroutine (returns promptly), is safe to call twice, and leaves no
// goroutine leak.
func TestStreamingPoller_CloseStopsLoopIdempotently(t *testing.T) {
	base := runtime.NumGoroutine()

	streams := make(chan *fakeWatchStream, 1)
	watch := func(ctx context.Context, _ uint32) (watchStream, func(), error) {
		fs := newFakeWatchStream(ctx) // connects but never pushes
		streams <- fs
		return fs, func() {}, nil
	}
	p := newStreamingPoller("", time.Second)
	p.watch = watch
	p.watchdogBudget = time.Hour // sit in consume; only Close's ctx-cancel exits it
	p.start()
	<-streams // loop has dialed and entered consume

	// Close must return promptly (goroutine joined, not hung).
	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return; receiver goroutine not joined")
	}

	// Second Close is safe (no panic, no block).
	if err := p.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}

	// Receiver + Recv goroutines have exited.
	eventually(t, 2*time.Second, func() bool { return runtime.NumGoroutine() <= base },
		"goroutines did not return to baseline after Close (leak)")
}

// TestCapBackoff_StaysLowForFastRecovery preserves the old RemotePoller backoff
// invariant: the daemon is a local unix socket, so the redial backoff must stay
// small (repeated failures must not grow it past the cap) or the TUI sits on
// stale/empty state long after the daemon is reachable again.
func TestCapBackoff_StaysLowForFastRecovery(t *testing.T) {
	d := time.Second
	for range 25 {
		d = capBackoff(d * 2)
	}
	if d > streamMaxBackoff {
		t.Fatalf("backoff %s exceeded cap %s", d, streamMaxBackoff)
	}
	if streamMaxBackoff > 5*time.Second {
		t.Fatalf("backoff cap %s too large for a local socket; recovery would lag", streamMaxBackoff)
	}
}

// The live daemon-restart E2E (TestStreamingPoller_LiveRecoversFromDaemonRestart)
// is sandbox-hostile (`go build` + daemon subprocess + SIGKILL) and lives in
// streaming_poller_hostile_test.go behind the `hostile` build tag (bead
// pg2-ymi3l). The `eventually` helper above stays untagged so both build modes
// share it.
