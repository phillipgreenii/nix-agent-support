package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// shortTempDir returns a fresh directory with a short absolute path: a unix
// socket path is capped near 104 bytes and t.TempDir can exceed that.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcs")
	if err != nil {
		d, err = os.MkdirTemp("", "pcs")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// startDaemon serves h on a fresh short socket through the real process shell
// and returns the socket path. The daemon is drained when the test ends.
func startDaemon(t *testing.T, h proc.Handler) string {
	t.Helper()
	dir := shortTempDir(t)
	cfg := proc.Config{StateDir: dir, SocketPath: filepath.Join(dir, "s"), DrainTimeout: 3 * time.Second}
	srv, err := proc.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { _ = srv.Serve(ctx, h, nil) })
	t.Cleanup(func() { cancel(); wg.Wait() })
	return cfg.SocketPath
}

// stubDispatcher answers every call with fn and records what it was handed.
type stubDispatcher struct {
	mu    sync.Mutex
	calls []router.Call
	fn    func(ctx context.Context, c router.Call) scriptout.Response
}

func (s *stubDispatcher) Dispatch(ctx context.Context, c router.Call) scriptout.Response {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, c)
	}
	return scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, SchemaVersion: 2, Result: json.RawMessage(`{"ok":true}`)}
}

func (s *stubDispatcher) last(t *testing.T) router.Call {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("the dispatcher was never called")
	}
	return s.calls[len(s.calls)-1]
}

func (s *stubDispatcher) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// exchange sends one raw line to path and returns the decoded reply frame.
func exchange(t *testing.T, path, line string) (ReplyFrame, scriptout.Response) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// A write that fails because the daemon already answered and closed is
	// fine: the reply is what is asserted.
	go func() { _, _ = conn.Write([]byte(line)) }()
	raw, err := ReadFrame(bufio.NewReader(conn), 64<<20)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var rf ReplyFrame
	if err := json.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("reply %q: %v", raw, err)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(rf.Response, &resp); err != nil {
		t.Fatalf("response %q: %v", rf.Response, err)
	}
	return rf, resp
}

func frameLine(protocol int, instance string, deadlineMS int64, request string) string {
	b, _ := json.Marshal(RequestFrame{SocketProtocol: protocol, ClientBuild: "cb", Instance: instance, DeadlineMS: deadlineMS, Request: json.RawMessage(request)})
	return string(b) + "\n"
}

func TestServerRoundTripAndFrameShapes(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "daemon-build-7"}))

	// A literal frame, as a client written from the spec would send it.
	line := `{"socket_protocol":1,"client_build":"cb","instance":"pr","deadline_ms":2500,"request":{"op":"list","args":{"q":"mine"},"config":{"config_hash":"h"}}}` + "\n"
	rf, resp := exchange(t, path, line)
	if rf.SocketProtocol != ProtocolVersion || rf.DaemonBuild != "daemon-build-7" {
		t.Errorf("reply frame %+v", rf)
	}
	if string(rf.Response) != `{"protocolVersion":1,"schemaVersion":2,"result":{"ok":true}}` || resp.Error != nil {
		t.Errorf("the response is the dispatcher's, unmodified: %s", rf.Response)
	}
	c := d.last(t)
	if c.Instance != "pr" || c.Request.Op != "list" || string(c.Request.Args) != `{"q":"mine"}` ||
		string(c.Request.Config) != `{"config_hash":"h"}` || c.Deadline != 2500*time.Millisecond || c.ClientBuild != "cb" || c.Received.IsZero() {
		t.Errorf("call %+v: the request must reach the router unmodified", c)
	}

	// No deadline stated: none assumed.
	exchange(t, path, frameLine(1, "ci", 0, `{"op":"auth_status"}`))
	if c := d.last(t); c.Deadline != 0 || c.Instance != "ci" {
		t.Errorf("call %+v", c)
	}
}

func TestServerProtocolWindow(t *testing.T) {
	d := &stubDispatcher{}
	// A daemon at N=3 serves 3 and 2, and refuses 1 (N-2), 4 and 0.
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d", Protocol: 3}))
	for _, tc := range []struct {
		protocol int
		ok       bool
	}{{3, true}, {2, true}, {1, false}, {4, false}, {0, false}, {99, false}} {
		before := d.count()
		rf, resp := exchange(t, path, frameLine(tc.protocol, "pr", 0, `{"op":"list"}`))
		if rf.SocketProtocol != 3 {
			t.Errorf("protocol %d: the reply is stamped with the daemon's protocol, got %d", tc.protocol, rf.SocketProtocol)
		}
		served := d.count() > before
		switch {
		case tc.ok && (resp.Error != nil || !served):
			t.Errorf("protocol %d must be accepted: %+v", tc.protocol, resp.Error)
		case !tc.ok && (resp.Error == nil || resp.Error.Code != "version_mismatch" || served):
			t.Errorf("protocol %d must be version_mismatch and never dispatched: %+v served=%v", tc.protocol, resp.Error, served)
		}
		if !tc.ok && !strings.Contains(resp.Error.Message, "cb") {
			t.Errorf("protocol %d: message %q should name the client build", tc.protocol, resp.Error.Message)
		}
	}
}

func TestServerBuildDifferenceInsideTheWindowIsNotAnError(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "daemon-new", Protocol: 2}))
	line := `{"socket_protocol":1,"client_build":"a-very-old-build","instance":"pr","deadline_ms":0,"request":{"op":"list"}}` + "\n"
	if _, resp := exchange(t, path, line); resp.Error != nil {
		t.Fatalf("N-1 with a different build must be served: %+v", resp.Error)
	}
}

func TestServerFrameSizeCap(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d"}))

	// A frame whose JSON is exactly size bytes (excluding the newline).
	frameOfSize := func(size int) string {
		base := len(frameLine(1, "pr", 0, `{"op":"list","args":""}`)) - 1
		pad := size - base
		line := frameLine(1, "pr", 0, `{"op":"list","args":"`+strings.Repeat("a", pad)+`"}`)
		if len(line)-1 != size {
			t.Fatalf("built %d bytes, want %d", len(line)-1, size)
		}
		return line
	}

	if _, resp := exchange(t, path, frameOfSize(MaxFrameBytes)); resp.Error != nil {
		t.Errorf("a frame of exactly 1 MiB must be accepted: %+v", resp.Error)
	}
	_, resp := exchange(t, path, frameOfSize(MaxFrameBytes+1))
	if resp.Error == nil || resp.Error.Code != "invalid_argument" || !strings.Contains(resp.Error.Message, "cap") {
		t.Errorf("a frame of 1 MiB + 1 byte must be rejected as invalid_argument naming the cap: %+v", resp.Error)
	}
	n := d.count()
	// Far over the cap, and with no newline at all: still answered, never dispatched.
	_, resp = exchange(t, path, strings.Repeat("x", 3*MaxFrameBytes))
	if resp.Error == nil || resp.Error.Code != "invalid_argument" || d.count() != n {
		t.Errorf("an unterminated 3 MiB frame: %+v (dispatched=%v)", resp.Error, d.count() != n)
	}
}

func TestServerMalformedFrames(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d"}))
	for name, line := range map[string]string{
		"not json":       "this is not json\n",
		"no request":     `{"socket_protocol":1,"client_build":"c","instance":"pr"}` + "\n",
		"null request":   `{"socket_protocol":1,"client_build":"c","instance":"pr","request":null}` + "\n",
		"request no op":  frameLine(1, "pr", 0, `{"args":{}}`),
		"request is str": frameLine(1, "pr", 0, `"list"`),
	} {
		_, resp := exchange(t, path, line)
		if resp.Error == nil || resp.Error.Code != "invalid_argument" {
			t.Errorf("%s: want invalid_argument, got %+v", name, resp.Error)
		}
	}
	if d.count() != 0 {
		t.Errorf("a malformed frame must never reach the router (%d calls)", d.count())
	}
}

func TestServerNeverCancelsAHandlerThatOutlivesTheDeadline(t *testing.T) {
	d := &stubDispatcher{fn: func(ctx context.Context, c router.Call) scriptout.Response {
		time.Sleep(c.Deadline + 150*time.Millisecond) // outlives the stated deadline
		if ctx.Err() != nil {
			t.Errorf("the socket layer cancelled the handler: %v", ctx.Err())
		}
		return scriptout.Response{ProtocolVersion: 1, Result: json.RawMessage(`"late"`)}
	}}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d"}))
	_, resp := exchange(t, path, frameLine(1, "pr", 100, `{"op":"list"}`))
	if resp.Error != nil || string(resp.Result) != `"late"` {
		t.Fatalf("the answer after the deadline is the handler's: %+v", resp)
	}
}

func TestServerWithTheRealRouter(t *testing.T) {
	r := router.New(router.Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 5, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return map[string]string{"config": string(scriptout.ConfigFromContext(ctx))}, nil
	}})
	path := startDaemon(t, Handler(r, ServerConfig{Build: "d"}))

	_, resp := exchange(t, path, frameLine(1, "pr", 0, `{"op":"list","config":{"k":"v"}}`))
	if resp.Error != nil || resp.SchemaVersion != 5 || string(resp.Result) != `{"config":"{\"k\":\"v\"}"}` {
		t.Errorf("list: %+v %s", resp.Error, resp.Result)
	}
	if _, resp := exchange(t, path, frameLine(1, "pr", 0, `{"op":"show"}`)); resp.Error == nil || resp.Error.Code != "unknown_op" {
		t.Errorf("an op nobody registered: %+v", resp.Error)
	}
	if _, resp := exchange(t, path, frameLine(1, "xx", 0, `{"op":"list"}`)); resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Errorf("an unregistered instance: %+v", resp.Error)
	}
	r.SetMigrating(true, "")
	if _, resp := exchange(t, path, frameLine(1, "pr", 0, `{"op":"list"}`)); resp.Error == nil || !strings.HasPrefix(resp.Error.Message, "migrating:") {
		t.Errorf("migrating: %+v", resp.Error)
	}
}

func TestServerClientThatLeavesWithoutAFrameIsHarmless(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d", ReadTimeout: 200 * time.Millisecond}))
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// A client that connects and says nothing is dropped at the read timeout.
	idle, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_ = idle.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := idle.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Errorf("an idle client gets no bytes, only a close (n=%d err=%v)", n, err)
	}
	// The daemon still serves the next client.
	if _, resp := exchange(t, path, frameLine(1, "pr", 0, `{"op":"list"}`)); resp.Error != nil {
		t.Errorf("%+v", resp.Error)
	}
	if d.count() != 1 {
		t.Errorf("dispatched %d times", d.count())
	}
}
