package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	if _, err := Dial(Ref{}, DefaultProbeTimeout); !errors.Is(err, ErrNoRunningCore) {
		t.Fatalf("Dial with an empty ref = %v, want ErrNoRunningCore", err)
	}
	if _, err := Dial(Ref{Socket: filepath.Join(shortDir(t), "nope.sock")}, DefaultProbeTimeout); !errors.Is(err, ErrNoRunningCore) {
		t.Fatalf("Dial at a dead socket = %v, want ErrNoRunningCore", err)
	}
}

// A wrong token must be refused, and refused WITHOUT touching the queue.
func TestServe_RejectsABadToken(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	client, err := Dial(Ref{Socket: ref.Socket, Token: "not-the-token"}, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), SubcommandIngestEvent, []byte(oneEventRequest), CallOptions{})
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

	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), "session-status", []byte(`{"schemaVersion":"1","id":"hs-1","state":"running"}`), CallOptions{})
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

// TestDiscriminateReply covers DiscriminateReply's own branches directly
// (Task 4.1, Binding Decision 2: promoted from cmd/pr-pool's private
// discriminateReply, previously only exercised indirectly through
// cmd/pr-pool's own integration tests — which no longer counts toward THIS
// package's coverage now that the logic lives here).
func TestDiscriminateReply(t *testing.T) {
	t.Run("empty reply is nil (nothing to check)", func(t *testing.T) {
		if err := DiscriminateReply(nil, StatusReplySchema, nil); err != nil {
			t.Fatalf("DiscriminateReply(empty) = %v, want nil", err)
		}
	})

	t.Run("error envelope detected BEFORE replySchema", func(t *testing.T) {
		reply := []byte(`{"schemaVersion":"1","error":"unauthorized"}`)
		err := DiscriminateReply(reply, StatusReplySchema, nil)
		if err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("DiscriminateReply(error envelope) = %v, want it to name the refusal", err)
		}
	})

	t.Run("empty replySchema skips validation entirely", func(t *testing.T) {
		if err := DiscriminateReply([]byte(`{"anything":"goes"}`), "", nil); err != nil {
			t.Fatalf("DiscriminateReply(no replySchema) = %v, want nil", err)
		}
	})

	t.Run("malformed against replySchema is reported", func(t *testing.T) {
		reply := []byte(`{"schemaVersion":"9"}`) // const mismatch
		err := DiscriminateReply(reply, StatusRequestSchema, nil)
		if err == nil {
			t.Fatal("DiscriminateReply(schema mismatch) = nil, want an error")
		}
	})

	t.Run("valid reply decodes into out", func(t *testing.T) {
		reply := []byte(`{"schemaVersion":"1"}`)
		var out struct {
			SchemaVersion string `json:"schemaVersion"`
		}
		if err := DiscriminateReply(reply, StatusRequestSchema, &out); err != nil {
			t.Fatalf("DiscriminateReply(valid): %v", err)
		}
		if out.SchemaVersion != "1" {
			t.Fatalf("out.SchemaVersion = %q, want 1 (decoded)", out.SchemaVersion)
		}
	})

	t.Run("valid reply with nil out just validates", func(t *testing.T) {
		if err := DiscriminateReply([]byte(`{"schemaVersion":"1"}`), StatusRequestSchema, nil); err != nil {
			t.Fatalf("DiscriminateReply(valid, nil out): %v", err)
		}
	})
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
	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := client.Call(context.Background(), SubcommandIngestEvent, []byte(oneEventRequest), CallOptions{}); err == nil {
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

	client, err := Dial(Ref{Socket: sock, Token: "t"}, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), SubcommandIngestEvent, nil, CallOptions{})
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
		if err := probe(ref.Socket, DefaultProbeTimeout); err != nil {
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

// acquireNReadSlots drives the read-admission semaphore directly (bypassing
// the wire) so a test can put it into a known saturated state without racing
// real concurrent socket calls. Returns a release func.
func acquireNReadSlots(t *testing.T, svc *Service, n int) func() {
	t.Helper()
	for i := 0; i < n; i++ {
		if !svc.acquireReadSlot() {
			t.Fatalf("acquireReadSlot failed acquiring slot %d/%d", i+1, n)
		}
	}
	return func() {
		for i := 0; i < n; i++ {
			svc.releaseReadSlot()
		}
	}
}

// With the read semaphore fully saturated, a concurrent ingest-event (a
// write/lifecycle verb, never admission-gated per the allowlist) must still
// succeed — Task 3.10 Step 4's "every path other than {status, mon.read} ...
// is NEVER refused with exit 9 by this semaphore".
func TestSaturatedReadSemaphoreAllowsIngest(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)
	release := acquireNReadSlots(t, svc, readSemCapacity)
	defer release()

	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), SubcommandIngestEvent, []byte(oneEventRequest), CallOptions{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("ingest-event with the read semaphore fully saturated: exit = %d, want %d", code, conformance.ExitOK)
	}
	if !strings.Contains(string(reply), `"accepted"`) {
		t.Fatalf("reply = %s, want an accepted ingest-event-reply", reply)
	}
}

// An (N+1)th status/mon.read call is refused with exit 9 IMMEDIATELY, not
// after blocking for a slot to free (Task 3.10 Step 2 / Binding decisions):
// a block-then-succeed implementation would defeat admission control's whole
// point, since a poller's backoff ladder depends on a prompt refusal signal.
// The refusal also carries a human-readable message (Step 5), never a bare
// exit code.
func TestSaturatedReadRefusesPromptly(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)
	release := acquireNReadSlots(t, svc, readSemCapacity)
	defer release()

	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	start := time.Now()
	reply, code, err := client.Call(context.Background(), SubcommandStatus, []byte(`{"schemaVersion":"1"}`), CallOptions{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d (busy) for a saturated read call", code, conformance.ExitBusy)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("refusal took %s, want an IMMEDIATE decline (no slot was ever released during this call)", elapsed)
	}
	if !strings.Contains(string(reply), "too many concurrent") {
		t.Fatalf("reply = %s, want the human-readable refusal message", reply)
	}
}

// mon.read is admission-controlled exactly like status -- the allowlist is
// {status, mon.read}, not status alone.
func TestSaturatedReadSemaphoreRefusesMonRead(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)
	if _, err := svc.Register("sink-1", KindMonitor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	release := acquireNReadSlots(t, svc, readSemCapacity)
	defer release()

	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), SubcommandMonRead,
		[]byte(`{"schemaVersion":"1","id":"sink-1","metrics":[]}`), CallOptions{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d (busy) for a saturated mon.read call", code, conformance.ExitBusy)
	}
	if !strings.Contains(string(reply), "too many concurrent") {
		t.Fatalf("reply = %s, want the human-readable refusal message", reply)
	}
}

// TestExitBusy_IsThePollerBackoffSignal documents+tests Task 3.10's
// poller-side contract (Step 6; the poller itself is Task 4.0's, out of
// scope here): a saturated status/mon.read call returns exit 9 -- the exact
// signal a poller's own backoff ladder (capped at PollerBackoffCap) advances
// on -- carrying a cli.error envelope rather than the body-less shape a
// participant's own pre-accept busy decline uses, so a poller (or, today,
// the manual CLI) can render WHY, not just that it was declined.
func TestExitBusy_IsThePollerBackoffSignal(t *testing.T) {
	if PollerBackoffCap <= 0 {
		t.Fatalf("PollerBackoffCap = %s, want a positive cap", PollerBackoffCap)
	}
	dir := shortDir(t)
	svc, ref := startService(t, dir)
	release := acquireNReadSlots(t, svc, readSemCapacity)
	defer release()

	client, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), SubcommandStatus, []byte(`{"schemaVersion":"1"}`), CallOptions{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d -- this IS the signal a poller backs off on", code, conformance.ExitBusy)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &errBody); err != nil || errBody.Error == "" {
		t.Fatalf("reply = %s, want a cli.error envelope (never the body-less busy shape) so a poller/CLI can render why", reply)
	}
}

// No goroutine leak: after a burst of concurrent (including refused) read
// calls AND one ABANDONED call -- a client that cancels its ctx and is never
// Closed, simulating a caller that vanished mid-call -- goroutine counts
// return to baseline within a bounded window (Task 3.10 Step 7: plain
// runtime.NumGoroutine() diffing; this repo has no goleak-style tooling and
// none is added).
func TestNoGoroutineLeak_ConcurrentAndAbandonedCalls(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)
	_ = svc

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const n = readSemCapacity * 3
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := Dial(ref, DefaultProbeTimeout)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()
			_, _, _ = client.Call(context.Background(), SubcommandStatus, []byte(`{"schemaVersion":"1"}`), CallOptions{})
		}()
	}
	wg.Wait()

	// abandoned-mid-call scenario #1 (client side): an already-cancelled ctx
	// means Call must return immediately without ever blocking, and its
	// watcher goroutine must not linger after Call returns.
	abandoned, err := Dial(ref, DefaultProbeTimeout)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := abandoned.Call(cancelledCtx, SubcommandStatus, []byte(`{"schemaVersion":"1"}`), CallOptions{}); err == nil {
		t.Fatal("Call with an already-cancelled ctx succeeded, want an error")
	}
	_ = abandoned.Close()

	// abandoned-mid-call scenario #2 (server side): a raw connection that
	// sends NOTHING and is never closed by this test -- a caller that
	// vanished before ever completing its request frame. The server's own
	// handleConn goroutine has no ctx to watch here; it can only be bounded
	// by serverCallDeadline, so this is what actually proves that bound
	// works rather than trivially passing because nothing was ever stuck.
	stalled, err := net.Dial("unix", ref.Socket)
	if err != nil {
		t.Fatalf("dial stalled conn: %v", err)
	}
	t.Cleanup(func() { _ = stalled.Close() })

	deadline := time.Now().Add(serverCallDeadline + 3*time.Second)
	for {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after the bounded window, want <= baseline %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
