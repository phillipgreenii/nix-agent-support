package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// --- tracker ---

func TestTracker_AddResolveDeliversOutcome(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)

	tr.resolve("id-1", true, "")

	select {
	case o := <-ch:
		if !o.ok {
			t.Fatalf("outcome.ok = false, want true")
		}
		if o.err != "" {
			t.Fatalf("outcome.err = %q, want empty", o.err)
		}
	default:
		t.Fatal("expected outcome to be available after resolve")
	}
}

func TestTracker_ResolveWithError(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)

	tr.resolve("id-1", false, "boom")

	o := <-ch
	if o.ok {
		t.Fatalf("outcome.ok = true, want false")
	}
	if o.err != "boom" {
		t.Fatalf("outcome.err = %q, want %q", o.err, "boom")
	}
}

func TestTracker_ResolveUnknownID_NoPanic(t *testing.T) {
	tr := newTracker()
	// Resolving an id that was never added (or already resolved/cancelled)
	// must be a safe no-op, not a panic or a send on a closed channel.
	tr.resolve("nonexistent", true, "")
}

func TestTracker_FailServer_OnlyThatServer(t *testing.T) {
	tr := newTracker()
	chA := tr.add("id-a", 100)
	chB := tr.add("id-b", 200)

	tr.failServer(100)

	select {
	case o := <-chA:
		if o.ok {
			t.Fatalf("failServer(100): id-a outcome.ok = true, want false")
		}
	default:
		t.Fatal("expected id-a to be failed by failServer(100)")
	}

	select {
	case <-chB:
		t.Fatal("id-b belongs to a different server PID and must not be failed")
	default:
		// Still pending, as expected.
	}

	// id-b's server can still be resolved normally afterwards.
	tr.resolve("id-b", true, "")
	o := <-chB
	if !o.ok {
		t.Fatalf("id-b outcome.ok = false, want true")
	}
}

func TestTracker_Cancel_RemovesPendingWithoutSend(t *testing.T) {
	tr := newTracker()
	ch := tr.add("id-1", 100)
	tr.cancel("id-1")

	// A later resolve for the same id must be a no-op (already cancelled).
	tr.resolve("id-1", true, "")

	select {
	case <-ch:
		t.Fatal("cancelled id must not receive an outcome")
	default:
	}
}

func TestTracker_ConcurrentAddResolve(t *testing.T) {
	tr := newTracker()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id := tr.nextID()
			ch := tr.add(id, i%3)
			go tr.resolve(id, true, "")
			<-ch
		}(i)
	}
	wg.Wait()
}

// --- bridgeDeliverer ---

func TestBridgeDeliverer_SuccessRoundTrip(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	const serverPID = 4242
	const bridgePID = 99

	var mu sync.Mutex
	var recorded *pb.Deliver

	tr := newTracker()
	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return serverPID, true
		},
		tr:      tr,
		timeout: time.Second,
	}

	reg.AttachStream(serverPID, bridgePID, func(m *pb.DaemonMsg) error {
		mu.Lock()
		recorded = m.GetDeliver()
		mu.Unlock()
		id := m.GetDeliver().GetId()
		go tr.resolve(id, true, "")
		return nil
	})

	err := d.Deliver(context.Background(), 555, "hello there")
	if err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if recorded == nil {
		t.Fatal("expected a Deliver message to have been recorded")
	}
	if recorded.GetTargetPid() != 555 {
		t.Errorf("recorded TargetPid = %d, want 555", recorded.GetTargetPid())
	}
	if recorded.GetText() != "hello there" {
		t.Errorf("recorded Text = %q, want %q", recorded.GetText(), "hello there")
	}
}

func TestBridgeDeliverer_NoAncestor_ErrNoBridge(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return 0, false
		},
		tr:      newTracker(),
		timeout: time.Second,
	}

	err := d.Deliver(context.Background(), 555, "hi")
	if !errors.Is(err, nudger.ErrNoBridge) {
		t.Fatalf("Deliver error = %v, want ErrNoBridge", err)
	}
}

func TestBridgeDeliverer_NoLiveBridge_ErrNoBridge(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	const serverPID = 4242

	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return serverPID, true
		},
		tr:      newTracker(),
		timeout: time.Second,
	}

	// No AttachStream call: LiveBridge(serverPID) will report false.
	err := d.Deliver(context.Background(), 555, "hi")
	if !errors.Is(err, nudger.ErrNoBridge) {
		t.Fatalf("Deliver error = %v, want ErrNoBridge", err)
	}
}

func TestBridgeDeliverer_SendError_CancelsAndReturnsErr(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	const serverPID = 4242
	const bridgePID = 99
	sendErr := errors.New("queue full")

	tr := newTracker()
	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return serverPID, true
		},
		tr:      tr,
		timeout: time.Second,
	}

	reg.AttachStream(serverPID, bridgePID, func(m *pb.DaemonMsg) error {
		return sendErr
	})

	err := d.Deliver(context.Background(), 555, "hi")
	if !errors.Is(err, sendErr) {
		t.Fatalf("Deliver error = %v, want %v", err, sendErr)
	}

	// The pending id must have been cancelled (removed), not leaked: a
	// stray resolve for it afterwards must be a safe no-op. We can't see
	// the id directly, but failServer for this server should find nothing
	// left over.
	tr.failServer(serverPID)
}

func TestBridgeDeliverer_NeverResolved_TimesOut(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	const serverPID = 4242
	const bridgePID = 99

	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return serverPID, true
		},
		tr:      newTracker(),
		timeout: 20 * time.Millisecond,
	}

	reg.AttachStream(serverPID, bridgePID, func(m *pb.DaemonMsg) error {
		// Never resolves.
		return nil
	})

	start := time.Now()
	err := d.Deliver(context.Background(), 555, "hi")
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed < d.timeout {
		t.Errorf("returned before timeout elapsed: %v < %v", elapsed, d.timeout)
	}
}

func TestBridgeDeliverer_ContextCancelled(t *testing.T) {
	reg := bridge.NewRegistry(time.Minute)
	const serverPID = 4242
	const bridgePID = 99

	d := &bridgeDeliverer{
		reg: reg,
		ancestor: func(pid int) (int, bool) {
			return serverPID, true
		},
		tr:      newTracker(),
		timeout: time.Second,
	}

	reg.AttachStream(serverPID, bridgePID, func(m *pb.DaemonMsg) error {
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := d.Deliver(ctx, 555, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Deliver error = %v, want context.Canceled", err)
	}
}

// --- inDaemonDeliverer ---

type fakeSignaler struct {
	pid  int
	text string
	err  error
}

func (f *fakeSignaler) Send(pid int, text string) error {
	f.pid = pid
	f.text = text
	return f.err
}

func TestInDaemonDeliverer_WrapsSignalerSend(t *testing.T) {
	sig := &fakeSignaler{}
	d := &inDaemonDeliverer{sig: sig}

	if err := d.Deliver(context.Background(), 777, "wake up"); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if sig.pid != 777 || sig.text != "wake up" {
		t.Errorf("signaler got (%d, %q), want (777, %q)", sig.pid, sig.text, "wake up")
	}
}

func TestInDaemonDeliverer_PropagatesError(t *testing.T) {
	wantErr := errors.New("no signaler")
	sig := &fakeSignaler{err: wantErr}
	d := &inDaemonDeliverer{sig: sig}

	if err := d.Deliver(context.Background(), 777, "wake up"); !errors.Is(err, wantErr) {
		t.Fatalf("Deliver error = %v, want %v", err, wantErr)
	}
}

// --- compositeDeliverer ---

type fakeDeliverer struct {
	calledPID  int
	calledText string
	err        error
}

func (f *fakeDeliverer) Deliver(_ context.Context, pid int, text string) error {
	f.calledPID = pid
	f.calledText = text
	return f.err
}

func TestCompositeDeliverer_RoutesToBridgeWhenAncestorOK(t *testing.T) {
	bridgeD := &fakeDeliverer{}
	inDaemonD := &fakeDeliverer{}
	c := &compositeDeliverer{
		ancestor: func(pid int) (int, bool) { return 999, true },
		bridge:   bridgeD,
		inDaemon: inDaemonD,
	}

	if err := c.Deliver(context.Background(), 42, "text"); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if bridgeD.calledPID != 42 || bridgeD.calledText != "text" {
		t.Errorf("bridge deliverer not called with expected args: got (%d, %q)", bridgeD.calledPID, bridgeD.calledText)
	}
	if inDaemonD.calledPID != 0 {
		t.Errorf("inDaemon deliverer should not have been called, got pid %d", inDaemonD.calledPID)
	}
}

func TestCompositeDeliverer_RoutesToInDaemonWhenAncestorNotOK(t *testing.T) {
	bridgeD := &fakeDeliverer{}
	inDaemonD := &fakeDeliverer{}
	c := &compositeDeliverer{
		ancestor: func(pid int) (int, bool) { return 0, false },
		bridge:   bridgeD,
		inDaemon: inDaemonD,
	}

	if err := c.Deliver(context.Background(), 42, "text"); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if inDaemonD.calledPID != 42 || inDaemonD.calledText != "text" {
		t.Errorf("inDaemon deliverer not called with expected args: got (%d, %q)", inDaemonD.calledPID, inDaemonD.calledText)
	}
	if bridgeD.calledPID != 0 {
		t.Errorf("bridge deliverer should not have been called, got pid %d", bridgeD.calledPID)
	}
}

// Compile-time interface satisfaction checks live in delivery.go itself;
// this just double-checks fakeDeliverer/fakeSignaler match the interfaces
// they're standing in for.
var (
	_ nudger.Deliverer = (*fakeDeliverer)(nil)
	_ nudger.Signaler  = (*fakeSignaler)(nil)
)
