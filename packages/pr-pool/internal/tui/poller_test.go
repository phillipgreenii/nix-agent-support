package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
)

// shortTestDir returns a SHORT temp dir: a unix socket path is capped at
// ~104 bytes by the platform (internal/core/socket.go's maxSocketPathLen),
// so t.TempDir()'s test-name-nested path would make Dial/Listen fail for a
// reason unrelated to what a test asserts (internal/core/socket_test.go's
// own shortDir, mirrored here since it is unexported in that package).
func shortTestDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "tuipl")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// writeDiscoveryRecord writes a core discovery record directly (the JSON
// shape internal/core's own unexported `record` type reads back via
// Discover) — the fields/tags are internal/core/socket.go's record struct,
// duplicated here because it is unexported and this package must not reach
// into core's internals to construct a test fixture.
func writeDiscoveryRecord(t *testing.T, logDir, socket, token string) {
	t.Helper()
	rec := map[string]any{
		"schemaVersion": "1",
		"socket":        socket,
		"token":         token,
		"pid":           1,
		"startedAt":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal discovery record: %v", err)
	}
	if err := os.WriteFile(core.RecordPath(logDir), data, 0o600); err != nil {
		t.Fatalf("write discovery record: %v", err)
	}
}

// fakeCoreHandler answers one subcommand call: the reply body (nil for the
// body-less busy shape) and the coarse exit code.
type fakeCoreHandler func(subcommand string, payload json.RawMessage) (reply []byte, exitCode int)

// fakeCore is a minimal unix-socket test double for the wire protocol
// Poller depends on (internal/core/socket.go's wireRequest/wireResponse,
// duplicated locally for the same unexported-type reason as
// writeDiscoveryRecord above): it accepts one connection per request,
// mirroring the real core.Service.handleConn's own one-request-then-close
// contract. A nil handler never replies (simulates a hung core), blocking
// until the test's cleanup closes it.
type fakeCore struct {
	listener net.Listener
	handler  fakeCoreHandler
	stop     chan struct{}
}

func startFakeCore(t *testing.T, socketPath string, handler fakeCoreHandler) *fakeCore {
	t.Helper()
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen %s: %v", socketPath, err)
	}
	fc := &fakeCore{listener: l, handler: handler, stop: make(chan struct{})}
	go fc.acceptLoop()
	t.Cleanup(func() {
		close(fc.stop)
		_ = l.Close()
	})
	return fc
}

func (fc *fakeCore) acceptLoop() {
	for {
		conn, err := fc.listener.Accept()
		if err != nil {
			return
		}
		go fc.handleConn(conn)
	}
}

func (fc *fakeCore) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var req struct {
		Token      string          `json:"token"`
		Subcommand string          `json:"subcommand"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	if fc.handler == nil {
		<-fc.stop // never reply -- the client's own deadline governs the test
		return
	}
	reply, code := fc.handler(req.Subcommand, req.Payload)
	if reply == nil {
		reply = []byte("null")
	}
	resp := struct {
		ExitCode int             `json:"exitCode"`
		Reply    json.RawMessage `json:"reply"`
	}{ExitCode: code, Reply: reply}
	_ = json.NewEncoder(conn).Encode(resp)
}

// validStatusReplyJSON is a minimal cli.status-reply body carrying only the
// schema's required top-level properties.
func validStatusReplyJSON() []byte {
	return []byte(`{"schemaVersion":"1","deliveries":[],"queues":[],"config":{"sources":0,"handlers":0}}`)
}

// Red-first (Task 4.4 Step 2): before this file existed, `go test
// ./internal/tui/... -run TestSnapshot_DialFailureIsErrNoRunningCore -v`
// failed because the package did not exist. It now exercises the "ref
// unknown, Discover itself fails" branch: no discovery record was ever
// published under logDir.
func TestSnapshot_DialFailureIsErrNoRunningCore(t *testing.T) {
	logDir := shortTestDir(t)
	p := NewSocketPoller(logDir, core.Ref{})

	_, err := p.Snapshot(context.Background(), 0)
	if !errors.Is(err, core.ErrNoRunningCore) {
		t.Fatalf("Snapshot() error = %v, want it to wrap core.ErrNoRunningCore", err)
	}
}

func TestSnapshot_MalformedReplyIsPollErrorNotPanic(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	startFakeCore(t, socketPath, func(string, json.RawMessage) ([]byte, int) {
		return []byte(`{"schemaVersion":"1","bogus":true}`), conformance.ExitOK
	})
	writeDiscoveryRecord(t, logDir, socketPath, "tok")

	p := NewSocketPoller(logDir, core.Ref{})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Snapshot panicked on a malformed reply: %v", r)
		}
	}()
	if _, err := p.Snapshot(context.Background(), 0); err == nil {
		t.Fatal("Snapshot() error = nil, want a non-nil error for a malformed reply")
	}
}

func TestSnapshot_Exit9AdvancesBackoffSuppressesAsPollError(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	startFakeCore(t, socketPath, func(string, json.RawMessage) ([]byte, int) {
		return nil, conformance.ExitBusy
	})
	writeDiscoveryRecord(t, logDir, socketPath, "tok")

	p := NewSocketPoller(logDir, core.Ref{})
	got, err := p.Snapshot(context.Background(), 0)

	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Snapshot() error = %v, want errors.Is(err, ErrBusy)", err)
	}
	if !reflect.DeepEqual(got, StatusReply{}) {
		t.Fatalf("Snapshot() reply = %+v, want the zero value", got)
	}

	p.mu.Lock()
	backoff := p.backoff
	p.mu.Unlock()
	if backoff != pollerBackoffInitial {
		t.Fatalf("backoff = %v after one exit-9, want pollerBackoffInitial (%v)", backoff, pollerBackoffInitial)
	}
}

// TestSnapshot_AuthFailureDoesNotTriggerRediscover proves the negative by a
// behavioral trap rather than inspecting a private flag directly: if the
// auth failure wrongly cleared the cached ref, the second call below —
// which deletes the discovery record first — would have no way to recover
// a ref and would fail. It only succeeds if the first call's failure left
// the cached ref untouched.
func TestSnapshot_AuthFailureDoesNotTriggerRediscover(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	var authFail atomic.Bool
	authFail.Store(true)
	startFakeCore(t, socketPath, func(string, json.RawMessage) ([]byte, int) {
		if authFail.Load() {
			return []byte(`{"schemaVersion":"1","error":"unauthorized"}`), conformance.ExitError
		}
		return validStatusReplyJSON(), conformance.ExitOK
	})
	writeDiscoveryRecord(t, logDir, socketPath, "tok")

	p := NewSocketPoller(logDir, core.Ref{})
	if _, err := p.Snapshot(context.Background(), 0); err == nil {
		t.Fatal("first Snapshot() error = nil, want a non-nil error for an unauthorized reply")
	}

	if err := os.Remove(core.RecordPath(logDir)); err != nil {
		t.Fatalf("remove discovery record: %v", err)
	}
	authFail.Store(false)

	if _, err := p.Snapshot(context.Background(), 0); err != nil {
		t.Fatalf("second Snapshot() = %v, want nil: an auth failure must not have invalidated the cached ref", err)
	}
}

func TestSnapshot_DialFailureTriggersRediscoverAtMostOnce(t *testing.T) {
	t.Run("SucceedsAfterOneRediscover", func(t *testing.T) {
		logDir := shortTestDir(t)
		liveSocket := core.SocketPath(logDir)
		startFakeCore(t, liveSocket, func(string, json.RawMessage) ([]byte, int) {
			return validStatusReplyJSON(), conformance.ExitOK
		})
		writeDiscoveryRecord(t, logDir, liveSocket, "tok")

		staleRef := core.Ref{Socket: filepath.Join(logDir, "stale.sock"), Token: "stale"}
		p := NewSocketPoller(logDir, staleRef)

		if _, err := p.Snapshot(context.Background(), 0); err != nil {
			t.Fatalf("Snapshot() = %v, want nil after one bounded re-Discover picks up the live ref", err)
		}

		p.mu.Lock()
		ref := p.ref
		p.mu.Unlock()
		if ref.Socket != liveSocket {
			t.Fatalf("ref.Socket = %q, want the rediscovered live socket %q", ref.Socket, liveSocket)
		}
	})

	t.Run("BoundedWhenRediscoveredRefAlsoFails", func(t *testing.T) {
		logDir := shortTestDir(t)
		staleRef := core.Ref{Socket: filepath.Join(logDir, "stale.sock"), Token: "stale"}
		p := NewSocketPoller(logDir, staleRef) // no discovery record: the one re-Discover attempt fails too

		start := time.Now()
		_, err := p.Snapshot(context.Background(), 0)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Snapshot() took %v against a doubly-dead ref, want a bounded single re-Discover attempt", elapsed)
		}
		if !errors.Is(err, core.ErrNoRunningCore) {
			t.Fatalf("Snapshot() error = %v, want it to wrap core.ErrNoRunningCore", err)
		}
	})
}

func TestSnapshot_DeadlineCutoff(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	startFakeCore(t, socketPath, nil) // never replies
	writeDiscoveryRecord(t, logDir, socketPath, "tok")

	p := NewSocketPoller(logDir, core.Ref{})
	start := time.Now()
	_, err := p.Snapshot(context.Background(), 0)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Snapshot() error = nil, want a non-nil error against a core that never replies")
	}
	if elapsed > pollerRPCDeadline+2*time.Second {
		t.Fatalf("Snapshot() took %v, want it bounded near pollerRPCDeadline (%v)", elapsed, pollerRPCDeadline)
	}
}

// TestPoller_LivelockBoundUnderRace drives Snapshot from N goroutines
// against a fake core that ALWAYS fails Dial (no discovery record is ever
// published, and the seeded ref points at nothing), for M iterations each
// [design: Task 4.4 Step 7 — the "one concurrency experiment"].
func TestPoller_LivelockBoundUnderRace(t *testing.T) {
	logDir := shortTestDir(t)
	deadRef := core.Ref{Socket: filepath.Join(logDir, "dead.sock"), Token: "x"}
	p := NewSocketPoller(logDir, deadRef)

	const goroutines = 8
	const iterations = 20

	var maxInflight int32
	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		for {
			select {
			case <-stopMonitor:
				return
			default:
			}
			if v := atomic.LoadInt32(&p.inflight); v > atomic.LoadInt32(&maxInflight) {
				atomic.StoreInt32(&maxInflight, v)
			}
			runtime.Gosched()
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, err := p.Snapshot(ctx, 0)
				cancel()
				if err == nil {
					t.Errorf("Snapshot() error = nil, want an error against a core that always fails Dial")
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("livelock: goroutines did not terminate within 30s")
	}
	close(stopMonitor)
	<-monitorDone

	if maxInflight > 1 {
		t.Fatalf("observed inflight = %d, want at most 1 Discover+Dial attempt outstanding at a time", maxInflight)
	}

	p.mu.Lock()
	backoff := p.backoff
	p.mu.Unlock()
	if backoff > pollerBackoffCap {
		t.Fatalf("backoff = %v, want at most pollerBackoffCap (%v)", backoff, pollerBackoffCap)
	}
}

func TestToggleGate_ReusesClientDialsIfNeeded(t *testing.T) {
	logDir := shortTestDir(t)
	socketPath := core.SocketPath(logDir)
	startFakeCore(t, socketPath, func(subcommand string, _ json.RawMessage) ([]byte, int) {
		switch subcommand {
		case core.SubcommandPause:
			return []byte(`{"schemaVersion":"1","gate":"quota_paused","set":true}`), conformance.ExitOK
		case core.SubcommandResume:
			return []byte(`{"schemaVersion":"1","gate":"quota_paused","set":false}`), conformance.ExitOK
		default:
			return []byte(`{"schemaVersion":"1","error":"unexpected subcommand"}`), conformance.ExitError
		}
	})
	writeDiscoveryRecord(t, logDir, socketPath, "tok")

	// ref starts unknown: ToggleGate must dial on demand (Discover first).
	p := NewSocketPoller(logDir, core.Ref{})
	effective, err := p.ToggleGate(context.Background(), core.SubcommandPause)
	if err != nil {
		t.Fatalf("ToggleGate(pause) = %v, want nil", err)
	}
	if effective != "paused" {
		t.Fatalf("effective = %q, want %q", effective, "paused")
	}

	// A second call must REUSE the now-cached ref rather than re-Discover:
	// removing the discovery record must not break it.
	if err := os.Remove(core.RecordPath(logDir)); err != nil {
		t.Fatalf("remove discovery record: %v", err)
	}
	effective, err = p.ToggleGate(context.Background(), core.SubcommandResume)
	if err != nil {
		t.Fatalf("second ToggleGate(resume) = %v, want nil (must reuse the cached ref, not re-Discover)", err)
	}
	if effective != "resumed" {
		t.Fatalf("effective = %q, want %q", effective, "resumed")
	}
}

func TestToggleGate_FailureDoesNotAdvanceBackoff(t *testing.T) {
	logDir := shortTestDir(t)
	p := NewSocketPoller(logDir, core.Ref{}) // no discovery record: ToggleGate must fail

	if _, err := p.ToggleGate(context.Background(), core.SubcommandPause); err == nil {
		t.Fatal("ToggleGate() error = nil, want a non-nil error with no core discoverable")
	}

	p.mu.Lock()
	backoff := p.backoff
	p.mu.Unlock()
	if backoff != 0 {
		t.Fatalf("backoff = %v after a ToggleGate failure, want 0 (Snapshot's ladder must be untouched)", backoff)
	}
}

func TestToggleGate_UnknownVerbIsError(t *testing.T) {
	p := NewSocketPoller(shortTestDir(t), core.Ref{})
	if _, err := p.ToggleGate(context.Background(), "quiesce"); err == nil {
		t.Fatal("ToggleGate(\"quiesce\") error = nil, want a non-nil error for an unrecognized verb")
	}
}
