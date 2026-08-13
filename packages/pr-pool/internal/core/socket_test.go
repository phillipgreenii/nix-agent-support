package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// shortDir returns a SHORT temp dir. t.TempDir() nests the test name, and a unix
// socket path is capped at ~104 bytes by the platform, so a long test name would
// make Listen fail for a reason unrelated to what the test asserts.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "prp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func newQueue(t *testing.T) *eventqueue.Queue {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	return q
}

// startService binds a core and runs its accept loop, returning the service and
// its Ref. The loop is torn down by the test's cleanup.
func startService(t *testing.T, logDir string) (*Service, Ref) {
	t.Helper()
	svc, err := Listen(Options{LogDir: logDir, Queue: newQueue(t), Bindings: testBindings(), Command: "pr-pool"})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Accept(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Accept returned %v, want nil on orderly shutdown", err)
		}
	})
	waitStarted(t, svc)
	return svc, svc.Ref()
}

// waitStarted blocks until the accept loop has entered `started`, so a test never
// races the goroutine that flips the state.
func waitStarted(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if svc.State() == conformance.Started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("service never reached started (state=%s)", svc.State())
}

// Listen publishes everything a CLI needs: a bound socket and a 0600 discovery
// record carrying the socket + token.
func TestListen_PublishesDiscoverableCore(t *testing.T) {
	dir := shortDir(t)
	svc, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = svc.Close() }()

	if svc.State() != conformance.Starting {
		t.Fatalf("state after Listen = %s, want starting (messages cross only in started)", svc.State())
	}
	if _, err := os.Stat(SocketPath(dir)); err != nil {
		t.Fatalf("socket not bound: %v", err)
	}
	info, err := os.Stat(RecordPath(dir))
	if err != nil {
		t.Fatalf("discovery record: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("discovery record perm = %o, want 600 (it carries the auth token)", perm)
	}
	ref := svc.Ref()
	if len(ref.Token) != 64 {
		t.Fatalf("token = %q (%d chars), want 64 hex chars of CSPRNG entropy", ref.Token, len(ref.Token))
	}
	if ref.Socket != SocketPath(dir) {
		t.Fatalf("ref socket = %q, want %q", ref.Socket, SocketPath(dir))
	}
}

// Two cores must never share one socket path.
func TestListen_RefusesASecondLiveCore(t *testing.T) {
	dir := shortDir(t)
	svc, _ := startService(t, dir)
	_ = svc

	_, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen err = %v, want ErrAlreadyRunning", err)
	}
}

// A socket file left behind by a crashed core must not block a fresh one.
func TestListen_RebindsOverAStaleSocket(t *testing.T) {
	dir := shortDir(t)
	// Simulate a crash: bind, close the listener WITHOUT unlinking, leaving a dead
	// socket file on disk.
	sock := SocketPath(dir)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close pre-bind: %v", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("stale socket should still exist: %v", err)
	}

	svc, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestListen_RejectsMissingRequirements(t *testing.T) {
	if _, err := Listen(Options{Queue: newQueue(t)}); err == nil {
		t.Fatal("Listen with no LogDir succeeded, want an error")
	}
	if _, err := Listen(Options{LogDir: shortDir(t)}); err == nil {
		t.Fatal("Listen with no Queue succeeded, want an error (the queue IS the delivery guarantee)")
	}
	// Without the configured binding set the core cannot tell an event type unknown
	// to the configuration from one merely inactive this run, and INV-DISP-3 requires
	// opposite outcomes for the two — so a core is refused rather than started with
	// that question unanswerable.
	_, err := Listen(Options{LogDir: shortDir(t), Queue: newQueue(t)})
	if err == nil {
		t.Fatal("Listen with no Bindings succeeded, want an error (INV-DISP-3 is unanswerable without them)")
	}
	if !strings.Contains(err.Error(), "Bindings") {
		t.Fatalf("err = %v, want it to name the missing Bindings", err)
	}
}

// A socket path over the platform limit must fail with an actionable message, not
// net.Listen's bare "invalid argument".
func TestListen_RejectsAnOverlongSocketPath(t *testing.T) {
	dir := filepath.Join(shortDir(t), strings.Repeat("d", maxSocketPathLen))
	_, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if err == nil {
		t.Fatal("Listen with an overlong socket path succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "PR_POOL_LOG_DIR") {
		t.Fatalf("err = %v, want it to name the remedy (PR_POOL_LOG_DIR)", err)
	}
}

// Discover finds a live core and hands back a usable Ref.
func TestDiscover_LiveCore(t *testing.T) {
	dir := shortDir(t)
	_, ref := startService(t, dir)

	got, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != ref {
		t.Fatalf("Discover = %+v, want %+v", got, ref)
	}
}

// Every no-core shape must be ErrNoRunningCore — never a spawn, never a bare I/O
// error the caller cannot classify (ADR 0036).
func TestDiscover_NoRunningCore(t *testing.T) {
	t.Run("no record at all", func(t *testing.T) {
		if _, err := Discover(shortDir(t)); !errors.Is(err, ErrNoRunningCore) {
			t.Fatalf("err = %v, want ErrNoRunningCore", err)
		}
	})
	t.Run("unreadable record", func(t *testing.T) {
		dir := shortDir(t)
		if err := os.WriteFile(RecordPath(dir), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Discover(dir); !errors.Is(err, ErrNoRunningCore) {
			t.Fatalf("err = %v, want ErrNoRunningCore", err)
		}
	})
	t.Run("incomplete record", func(t *testing.T) {
		dir := shortDir(t)
		if err := os.WriteFile(RecordPath(dir), []byte(`{"schemaVersion":"1"}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Discover(dir); !errors.Is(err, ErrNoRunningCore) {
			t.Fatalf("err = %v, want ErrNoRunningCore", err)
		}
	})
	t.Run("stale record left by a crashed core", func(t *testing.T) {
		dir := shortDir(t)
		rec := record{SchemaVersion: "1", Socket: filepath.Join(dir, "gone.sock"), Token: "t", PID: 1}
		if err := writeRecord(RecordPath(dir), rec); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}
		if _, err := Discover(dir); !errors.Is(err, ErrNoRunningCore) {
			t.Fatalf("err = %v, want ErrNoRunningCore for a record whose socket is dead", err)
		}
	})
	t.Run("closed core unpublishes itself", func(t *testing.T) {
		dir := shortDir(t)
		svc, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
		if err != nil {
			t.Fatalf("Listen: %v", err)
		}
		if err := svc.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := Discover(dir); !errors.Is(err, ErrNoRunningCore) {
			t.Fatalf("err = %v, want ErrNoRunningCore after Close", err)
		}
	})
}

func TestDial_NoSocketIsNoRunningCore(t *testing.T) {
	if _, err := Dial(Ref{}); !errors.Is(err, ErrNoRunningCore) {
		t.Fatalf("Dial with an empty ref = %v, want ErrNoRunningCore", err)
	}
	if _, err := Dial(Ref{Socket: filepath.Join(shortDir(t), "nope.sock")}); !errors.Is(err, ErrNoRunningCore) {
		t.Fatalf("Dial at a dead socket = %v, want ErrNoRunningCore", err)
	}
}

// A wrong token must be refused, and refused WITHOUT touching the queue.
func TestServe_RejectsABadToken(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	client, err := Dial(Ref{Socket: ref.Socket, Token: "not-the-token"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(SubcommandIngestEvent, []byte(oneEventRequest))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d for an unauthorized request", code, conformance.ExitError)
	}
	if !strings.Contains(string(reply), "unauthorized") {
		t.Fatalf("reply = %s, want an unauthorized error envelope", reply)
	}
	if depth := svc.Queue().DepthByType(); len(depth) != 0 {
		t.Fatalf("queue depth = %v, want empty: an unauthorized request must not enqueue", depth)
	}
}

func TestAuthorized(t *testing.T) {
	if !authorized("abc", "abc") {
		t.Fatal("matching tokens rejected")
	}
	for _, bad := range []string{"", "ab", "abcd", "abd"} {
		if authorized(bad, "abc") {
			t.Fatalf("token %q accepted against %q", bad, "abc")
		}
	}
}

// A garbage transport frame must be answered, not silently dropped: the caller
// needs an exit code.
func TestServe_MalformedTransportFrame(t *testing.T) {
	dir := shortDir(t)
	_, ref := startService(t, dir)

	conn, err := net.Dial("unix", ref.Socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp wireResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ExitCode != conformance.ExitError {
		t.Fatalf("exit = %d, want %d", resp.ExitCode, conformance.ExitError)
	}
	if !strings.Contains(string(resp.Reply), "malformed transport frame") {
		t.Fatalf("reply = %s, want a malformed-frame error", resp.Reply)
	}
}

// An unknown subcommand — the branch `session-status` now lands in — is an error,
// not a silent no-op.
func TestServe_UnknownSubcommand(t *testing.T) {
	dir := shortDir(t)
	_, ref := startService(t, dir)

	client, err := Dial(ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call("session-status", []byte(`{"schemaVersion":"1","id":"hs-1","state":"running"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d for a dropped/unknown subcommand", code, conformance.ExitError)
	}
	if !strings.Contains(string(reply), "unknown subcommand") {
		t.Fatalf("reply = %s, want an unknown-subcommand error", reply)
	}
}

// Messages cross ONLY while started (INV-INTF-1): before Accept and after Close
// the participant boundary refuses with a diagnostic.
func TestServe_RefusesOutsideStarted(t *testing.T) {
	dir := shortDir(t)
	svc, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	var out strings.Builder
	if code := svc.Serve(SubcommandIngestEvent, strings.NewReader(oneEventRequest), &out); code != conformance.ExitError {
		t.Fatalf("exit = %d while starting, want %d", code, conformance.ExitError)
	}
	if !strings.Contains(out.String(), "core is starting") {
		t.Fatalf("reply = %s, want it to name the lifecycle state", out.String())
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	out.Reset()
	if code := svc.Serve(SubcommandIngestEvent, strings.NewReader(oneEventRequest), &out); code != conformance.ExitError {
		t.Fatalf("exit = %d while stopping, want %d", code, conformance.ExitError)
	}
	if !strings.Contains(out.String(), "core is stopping") {
		t.Fatalf("reply = %s, want it to name the lifecycle state", out.String())
	}
}

// Close is idempotent and Accept ends cleanly (`stopped`) rather than reporting a
// use-of-closed-connection error.
func TestAcceptAndClose_OrderlyShutdown(t *testing.T) {
	dir := shortDir(t)
	svc, err := Listen(Options{LogDir: dir, Queue: newQueue(t), Bindings: testBindings()})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Accept(context.Background()) }()
	waitStarted(t, svc)

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second Close: %v, want idempotent nil", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Accept = %v, want nil after Close", err)
	}
	if svc.State() != conformance.Stopped {
		t.Fatalf("state = %s, want stopped after a drained shutdown", svc.State())
	}
	// A late Accept must not resurrect a closed core.
	svc.setState(conformance.Started)
	if svc.State() != conformance.Stopped {
		t.Fatalf("state = %s, want the shutdown to be one-way", svc.State())
	}
}

func TestCallbackCommand_CarriesSocketAndTokenQuoted(t *testing.T) {
	svc := &Service{command: "pr-pool", ref: Ref{Socket: "/a dir/core.sock", Token: "tok'en"}}
	got := svc.CallbackCommand(SubcommandIngestEvent)
	want := `pr-pool ingest-event --socket '/a dir/core.sock' --token 'tok'\''en'`
	if got != want {
		t.Fatalf("CallbackCommand =\n  %s\nwant\n  %s", got, want)
	}
}

func TestErrorReply_IsTheProtocolEnvelope(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(errorReply("boom"), &got); err != nil {
		t.Fatalf("errorReply is not JSON: %v", err)
	}
	if got["schemaVersion"] != "1" || got["error"] != "boom" {
		t.Fatalf("errorReply = %v, want {schemaVersion:1, error:boom}", got)
	}
}

// An unpublishable discovery record is a hard Listen failure, not a core that
// binds a socket nobody can find.
func TestWriteRecord_ReportsAnUnwritablePath(t *testing.T) {
	missing := filepath.Join(shortDir(t), "no-such-dir", RecordName)
	if err := writeRecord(missing, record{SchemaVersion: "1", Socket: "s", Token: "t"}); err == nil {
		t.Fatal("writeRecord into a missing directory succeeded, want an error")
	}
}

// A read failure that is NOT "absent" (here: the record path is a directory) is
// reported as an I/O error, distinct from ErrNoRunningCore — the caller should not
// conclude "no core" from a broken filesystem.
func TestReadRecord_DistinguishesIOFailureFromAbsence(t *testing.T) {
	dir := shortDir(t)
	if err := os.Mkdir(RecordPath(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := readRecord(RecordPath(dir))
	if err == nil {
		t.Fatal("readRecord of a directory succeeded, want an error")
	}
	if errors.Is(err, ErrNoRunningCore) {
		t.Fatalf("err = %v, want an I/O error rather than ErrNoRunningCore", err)
	}
}

// A Call on a dead connection reports the transport failure instead of hanging or
// returning a bogus exit code.
func TestCall_OnAClosedConnection(t *testing.T) {
	dir := shortDir(t)
	_, ref := startService(t, dir)
	client, err := Dial(ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := client.Call(SubcommandIngestEvent, []byte(oneEventRequest)); err == nil {
		t.Fatal("Call on a closed connection succeeded, want an error")
	}
}

// A reply frame carrying an explicit null body (the busy shape) surfaces as a nil
// reply, not the four bytes "null".
func TestCall_NullReplyIsNoBody(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "busy.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req wireRequest
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(wireResponse{ExitCode: conformance.ExitBusy, Reply: jsonNull})
	}()

	client, err := Dial(Ref{Socket: sock, Token: "t"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(SubcommandIngestEvent, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitBusy)
	}
	if reply != nil {
		t.Fatalf("reply = %q, want nil for a body-less busy reply", reply)
	}
}

// A liveness probe — connect, send nothing, close, exactly what Discover does —
// must be silent: it is not a malformed request, and answering it would write to a
// closed peer. Without this, every discovery logs two warnings.
func TestHandleConn_LivenessProbeIsSilent(t *testing.T) {
	dir := shortDir(t)
	_, ref := startService(t, dir)

	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for i := 0; i < 3; i++ {
		if err := probe(ref.Socket); err != nil {
			t.Fatalf("probe: %v", err)
		}
	}
	// Give the accept goroutines a moment to run the (silent) probe path.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && buf.Len() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if buf.Len() != 0 {
		t.Fatalf("a liveness probe logged at WARN or above:\n%s", buf.String())
	}
}
