package emit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// shortDir returns a SHORT temp dir; a unix socket path is capped at ~104 bytes by
// the platform and t.TempDir() embeds the (long) test name.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "prp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// startCore brings up a REAL core (real unix socket, real durable queue) in logDir
// and returns it. Everything below crosses that socket for real; nothing about the
// transport is mocked, which is the only way to prove a forwarded event actually
// lands in the core's queue rather than in a throwaway local one.
func startCore(t *testing.T, logDir string, store eventqueue.Store) *core.Service {
	t.Helper()
	q, err := eventqueue.New(store)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	// The CONFIGURED binding set: the types these tests' fixtures emit. An event of
	// any other type is unknown to the configuration and the core rejects it
	// (INV-DISP-3), so a core under test must declare what its fixtures send.
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
			return svc
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("core never became discoverable")
	return nil
}

// --- the point of the bead: the event reaches a REAL core's queue -------------

// Emit against a DISCOVERED core must put the event in THAT core's durable queue.
// This is the test the old QueueEnqueuer would have passed while delivering
// nothing: it asserts the depth of the CORE's queue, not of a queue the test
// itself holds.
func TestEmit_SocketEnqueuer_DeliversToDiscoveredCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())

	res, err := Emit([]byte(validEvent), Locator{Discover: Discoverer(dir)}, SocketEnqueuer{})
	if err != nil {
		t.Fatalf("emit over the socket: %v", err)
	}
	if !res.Core.Discovered || res.Core.Socket != svc.Ref().Socket {
		t.Fatalf("located core = %+v, want the discovered core at %s", res.Core, svc.Ref().Socket)
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("core queue depth = %d, want 1 — the injected event never reached the core", depth)
	}
}

// The event must arrive INTACT, and be dispatchable from the core's queue to a
// bound listener: forwarding it over the socket must not lose the payload or the
// optional `at` source-stamp.
func TestEmit_SocketEnqueuer_EventArrivesIntactAndDispatches(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())
	l := &acceptListener{}
	svc.Queue().Register(l)

	if _, err := Emit([]byte(validEventWithAt), Locator{Discover: Discoverer(dir)}, SocketEnqueuer{}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if n := svc.Queue().Dispatch(); n != 1 {
		t.Fatalf("core dispatched %d events, want 1", n)
	}
	if len(l.got) != 1 || l.got[0] != "op-at" {
		t.Fatalf("core delivered %v, want [op-at]", l.got)
	}
}

// An INJECTED socket must work the same way (the operator passed --socket/--token).
func TestEmit_SocketEnqueuer_DeliversToInjectedCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())
	ref := svc.Ref()

	loc := Locator{InjectedSocket: ref.Socket, InjectedToken: ref.Token}
	res, err := Emit([]byte(validEvent), loc, SocketEnqueuer{})
	if err != nil {
		t.Fatalf("emit over the injected socket: %v", err)
	}
	if res.Core.Discovered {
		t.Fatalf("core = %+v, want an INJECTED (not discovered) ref", res.Core)
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("core queue depth = %d, want 1", depth)
	}
}

// A re-emit while the event is still RETAINED is absorbed by the core's
// de-duplication (INV-EVT-3) and is still reported as ACCEPTED — the wire reply folds dedupe into `accepted`, so
// this must not surface as a failure.
//
// It also pins the STATUS side of that fold: every emit over the socket reports
// Enqueued, INCLUDING the absorbed re-emit that the in-process path reports as
// Deduped (TestEmit_DedupesReEmitWhileRetained). This is the counterpart to that test
// and to Result.Status's doc: Deduped is unobservable over the wire, so a future
// SocketEnqueuer must not start GUESSING it from an unchanged reply schema — the
// reply has no field to derive it from.
func TestEmit_SocketEnqueuer_ReEmitIsAcceptedNotAnError(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())
	loc := Locator{Discover: Discoverer(dir)}

	for i := range 2 {
		res, err := Emit([]byte(validEvent), loc, SocketEnqueuer{})
		if err != nil {
			t.Fatalf("emit #%d: %v", i+1, err)
		}
		if res.Status != eventqueue.Enqueued {
			t.Fatalf("emit #%d status = %v, want Enqueued (the wire reply cannot express Deduped)", i+1, res.Status)
		}
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("core queue depth = %d, want 1 (the duplicate absorbed)", depth)
	}
}

// --- failures must be REPORTED, never swallowed ------------------------------

// A bad token is a protocol refusal; the event does NOT enter the queue, so the
// enqueue must fail.
func TestSocketEnqueuer_BadTokenIsRefused(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())

	_, err := SocketEnqueuer{}.Enqueue(
		CoreRef{Socket: svc.Ref().Socket, Token: "wrong"},
		eventqueue.Event{ID: "e1", Type: "t"},
	)
	if err == nil {
		t.Fatal("a bad token was reported as a successful injection")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v, want the core's refusal reason", err)
	}
	if depth := svc.Queue().DepthByType()["t"]; depth != 0 {
		t.Fatalf("core queue depth = %d, want 0", depth)
	}
}

// failingStore makes the core's durable append fail, so the core replies with a
// real per-event `rejected` entry. The injection must surface as an error: the
// event is NOT in the queue. It holds an eventqueue.Store in a NAMED field
// rather than an anonymous embed: embedding a nil eventqueue.Store would
// silently "inherit" AppendBatch (and any future Store method) from the nil
// interface value and nil-panic on first call, instead of failing the way every
// other method here does.
type failingStore struct{}

func (failingStore) Append(eventqueue.Record) error        { return errors.New("disk on fire") }
func (failingStore) AppendBatch([]eventqueue.Record) error { return errors.New("disk on fire") }
func (failingStore) Replay() ([]eventqueue.Record, error) {
	return nil, nil
}
func (failingStore) Close() error { return nil }

func TestSocketEnqueuer_CoreRejectionIsAnError(t *testing.T) {
	dir := shortDir(t)
	startCore(t, dir, failingStore{})

	_, err := Emit([]byte(validEvent), Locator{Discover: Discoverer(dir)}, SocketEnqueuer{})
	if err == nil {
		t.Fatal("a core-side rejection was reported as a successful injection")
	}
	if !strings.Contains(err.Error(), "rejected") || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v, want the core's rejection reason", err)
	}
}

// No core at all: ErrNoRunningCore, so the CLI can print the one diagnostic that
// matters and exit non-zero. It never starts one (ADR 0036).
func TestSocketEnqueuer_NoRunningCore(t *testing.T) {
	_, err := SocketEnqueuer{}.Enqueue(
		CoreRef{Socket: filepath.Join(shortDir(t), "gone.sock")},
		eventqueue.Event{ID: "e1", Type: "t"},
	)
	if !errors.Is(err, core.ErrNoRunningCore) {
		t.Fatalf("err = %v, want core.ErrNoRunningCore", err)
	}
}

// A ref that names neither a socket nor the local core locates nothing.
func TestSocketEnqueuer_RefWithNoSocket(t *testing.T) {
	_, err := SocketEnqueuer{}.Enqueue(CoreRef{}, eventqueue.Event{ID: "e1", Type: "t"})
	if !errors.Is(err, core.ErrNoRunningCore) {
		t.Fatalf("err = %v, want core.ErrNoRunningCore", err)
	}
}

// The symmetric refusal: SocketEnqueuer cannot serve the IN-PROCESS core.
func TestSocketEnqueuer_RefusesLocalCore(t *testing.T) {
	_, err := SocketEnqueuer{}.Enqueue(CoreRef{Local: true}, eventqueue.Event{ID: "e1", Type: "t"})
	if !errors.Is(err, ErrWrongEnqueuer) {
		t.Fatalf("err = %v, want ErrWrongEnqueuer", err)
	}
}

// An Event with no valid wire form is reported HERE, not pushed onto the core as an
// opaque "malformed" rejection.
func TestSocketEnqueuer_RejectsUnencodableEvent(t *testing.T) {
	_, err := SocketEnqueuer{}.Enqueue(CoreRef{Socket: "/nope.sock"}, eventqueue.Event{ID: "", Type: "t"})
	if !errors.Is(err, eventqueue.ErrInvalidEvent) {
		t.Fatalf("err = %v, want eventqueue.ErrInvalidEvent", err)
	}
}

// --- Discoverer --------------------------------------------------------------

func TestDiscoverer_FindsRunningCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())
	got, err := Discoverer(dir)()
	if err != nil {
		t.Fatalf("Discoverer: %v", err)
	}
	if got.Socket != svc.Ref().Socket || got.Token != svc.Ref().Token || !got.Discovered {
		t.Fatalf("discovered = %+v, want the running core %+v marked Discovered", got, svc.Ref())
	}
}

// No record under logDir is ErrNoRunningCore, passed through UNWRAPPED so a CLI can
// still key its remedy off the sentinel.
func TestDiscoverer_NoCoreIsErrNoRunningCore(t *testing.T) {
	if _, err := Discoverer(shortDir(t))(); !errors.Is(err, core.ErrNoRunningCore) {
		t.Fatalf("err = %v, want core.ErrNoRunningCore", err)
	}
}

// --- reply interpretation over a hand-rolled peer -----------------------------
//
// These drive the reply shapes a healthy core does not produce. The frames are
// written by hand because core's transport frames are unexported; they are two
// flat JSON objects (`{token,subcommand,payload}` in, `{exitCode,reply}` out), and
// TestFakeCore_FramesMatchTheRealTransport below pins them against the real core so
// this fake cannot drift into testing a protocol nobody speaks.

// fakeCore answers one request on a unix socket with a canned exitCode + reply.
func fakeCore(t *testing.T, exitCode int, reply string) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "fake.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req map[string]any
			_ = json.NewDecoder(conn).Decode(&req)
			frame := map[string]any{"exitCode": exitCode}
			if reply == "" {
				frame["reply"] = nil
			} else {
				frame["reply"] = json.RawMessage(reply)
			}
			_ = json.NewEncoder(conn).Encode(frame)
			_ = conn.Close()
		}
	}()
	return sock
}

func enqueueVia(t *testing.T, sock string) error {
	t.Helper()
	_, err := SocketEnqueuer{}.Enqueue(
		CoreRef{Socket: sock, Token: "tok"},
		eventqueue.Event{ID: "e1", Type: "t"},
	)
	return err
}

func TestInterpretReply_Faults(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		reply    string
		want     string
	}{
		// The busy code is read from conformance, never spelled as a literal: this
		// case is the one that has to move whenever the wire code does, and a
		// literal here would silently start exercising the usage code instead
		// (ADR 0042's Consequences).
		{"busy is never a delivery", conformance.ExitBusy, "", "busy"},
		{"no reply body", 1, "", "no reply body"},
		{"protocol error envelope", 1, `{"schemaVersion":"1","error":"not accepting: core is stopping"}`, "not accepting"},
		{"reply violates the reply schema", 0, `{"schemaVersion":"1","id":"x","accepted":"one","rejected":[]}`, "not a valid cli.ingest-event-reply"},
		{"accepted 0 with nothing rejected", 0, `{"schemaVersion":"1","id":"x","accepted":0,"rejected":[]}`, "unaccounted for"},
		{"accepted but non-zero exit", 1, `{"schemaVersion":"1","id":"x","accepted":1,"rejected":[]}`, "accepted but exited 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := enqueueVia(t, fakeCore(t, c.exitCode, c.reply))
			if err == nil {
				t.Fatalf("%s was reported as a successful injection", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestInterpretReply_AcceptedIsSuccess(t *testing.T) {
	if err := enqueueVia(t, fakeCore(t, 0, `{"schemaVersion":"1","id":"x","accepted":1,"rejected":[]}`)); err != nil {
		t.Fatalf("an accepted reply was reported as a failure: %v", err)
	}
}

// The hand-rolled frames above MUST be the frames the real core speaks. This drives
// the same fake-shaped request at a REAL core and requires it to be understood, so
// a transport change breaks this test rather than silently making the fault tests
// vacuous.
func TestFakeCore_FramesMatchTheRealTransport(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir, eventqueue.NewMemStore())

	conn, err := net.DialTimeout("unix", svc.Ref().Socket, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req := map[string]any{
		"token":      svc.Ref().Token,
		"subcommand": core.SubcommandIngestEvent,
		"payload":    json.RawMessage(`{"schemaVersion":"1","id":"trk","events":[{"id":"e1","type":"t"}]}`),
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request frame: %v", err)
	}
	var resp struct {
		ExitCode int             `json:"exitCode"`
		Reply    json.RawMessage `json:"reply"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response frame: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("real core exit = %d with reply %s, want 0 — the hand-rolled frame shape has drifted", resp.ExitCode, resp.Reply)
	}
	if !strings.Contains(string(resp.Reply), `"accepted":1`) {
		t.Fatalf("real core reply = %s, want accepted:1", resp.Reply)
	}
}
