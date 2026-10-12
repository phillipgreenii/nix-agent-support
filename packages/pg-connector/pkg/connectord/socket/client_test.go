package socket

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeClock is a clock whose Sleep advances it, so a retry schedule that would
// take seconds is checked instantly.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	return nil
}

var listReq = scriptout.Request{Op: "list"}

func TestCallWithNoDaemonRetriesUntilHalfTheBudgetThenNamesPathAndHint(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "s")
	clk := newFakeClock()
	c := &Client{
		SocketPath: missing, Build: "cb", RestartHint: "restart it with: launchctl kickstart -k gui/501/com.example", Now: clk.Now, Sleep: clk.Sleep,
		Backoff: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond},
	}

	reply, err := c.Call(context.Background(), "pr", listReq, 10*time.Second, 9*time.Second)
	var down *DaemonDownError
	if reply != nil || !errors.As(err, &down) {
		t.Fatalf("want DaemonDownError, got %v, %v", reply, err)
	}
	for _, want := range []string{missing, "launchctl kickstart -k gui/501/com.example", "retried for"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q must contain %q", err.Error(), want)
		}
	}
	var total time.Duration
	for _, s := range clk.sleeps {
		total += s
	}
	if total > 5*time.Second || total < 4*time.Second {
		t.Errorf("slept %v in total (%v): retries run until half the 10s budget has passed, never past it", total, clk.sleeps)
	}
	if len(clk.sleeps) < 4 || clk.sleeps[0] != 100*time.Millisecond || clk.sleeps[2] != 400*time.Millisecond || clk.sleeps[3] != 400*time.Millisecond {
		t.Errorf("backoff schedule %v: it steps through the schedule and repeats the last entry", clk.sleeps)
	}
}

func TestCallRetriesARefusedConnection(t *testing.T) {
	// A socket file nothing listens on: connect is refused, not absent.
	dir := shortTempDir(t)
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	clk := newFakeClock()
	c := &Client{SocketPath: path, Now: clk.Now, Sleep: clk.Sleep}
	_, err = c.Call(context.Background(), "pr", listReq, 4*time.Second, 3*time.Second)
	var down *DaemonDownError
	if !errors.As(err, &down) || len(clk.sleeps) == 0 {
		t.Fatalf("a refused connection is retried: err=%v sleeps=%v", err, clk.sleeps)
	}
}

func TestCallDoesNotRetryAPermissionRefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	dir := shortTempDir(t)
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock()
	c := &Client{SocketPath: path, RestartHint: "the hint", Now: clk.Now, Sleep: clk.Sleep}
	_, err = c.Call(context.Background(), "pr", listReq, 10*time.Second, 9*time.Second)
	var down *DaemonDownError
	if !errors.As(err, &down) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "the hint") {
		t.Fatalf("err = %v: a permission refusal answers at once, naming the socket and the hint", err)
	}
	if len(clk.sleeps) != 0 {
		t.Errorf("a permission refusal is final, it slept %v", clk.sleeps)
	}
}

func TestCallRefusesASocketPathOverThePlatformLimit(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", proc.MaxSocketPathBytes()) + "/sock"
	c := &Client{SocketPath: long, RestartHint: "hint"}
	_, err := c.Call(context.Background(), "pr", listReq, time.Second, time.Second)
	var down *DaemonDownError
	if !errors.As(err, &down) || !errors.Is(err, proc.ErrSocketPathTooLong) || !strings.Contains(err.Error(), long) {
		t.Fatalf("err = %v: the over-long path is refused, and the message names it", err)
	}
	// The limit is injectable, so the test does not depend on the platform.
	short := filepath.Join(shortTempDir(t), "s")
	c = &Client{SocketPath: short, MaxSocketPathBytes: len(short)}
	if _, err := c.Call(context.Background(), "pr", listReq, time.Second, time.Second); !errors.Is(err, proc.ErrSocketPathTooLong) {
		t.Errorf("a path of %d bytes against a limit of %d (NUL included): %v", len(short), len(short), err)
	}
}

func TestCallReachesADaemonThatStartsDuringTheRetryWindow(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "s")
	d := &stubDispatcher{}
	started := make(chan struct{})
	go func() {
		time.Sleep(400 * time.Millisecond)
		srv, err := proc.Start(proc.Config{StateDir: dir, SocketPath: path})
		if err != nil {
			t.Error(err)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		close(started)
		_ = srv.Serve(ctx, Handler(d, ServerConfig{Build: "d"}), nil)
	}()
	c := &Client{SocketPath: path, Build: "cb"}
	reply, err := c.Call(context.Background(), "pr", listReq, 6*time.Second, 5*time.Second)
	if err != nil || reply == nil || reply.DaemonBuild != "d" {
		t.Fatalf("the retry must reach the late daemon: %v %+v", err, reply)
	}
	<-started
}

func TestCallSendsTheDeadlineLessTheTimeSpentConnecting(t *testing.T) {
	d := &stubDispatcher{}
	path := startDaemon(t, Handler(d, ServerConfig{Build: "d"}))
	c := &Client{SocketPath: path, Build: "client-b"}
	if _, err := c.Call(context.Background(), "ci", scriptout.Request{Op: "list_runs"}, 30*time.Second, 29*time.Second); err != nil {
		t.Fatal(err)
	}
	got := d.last(t)
	if got.Instance != "ci" || got.ClientBuild != "client-b" {
		t.Errorf("%+v", got)
	}
	if got.Deadline <= 28*time.Second || got.Deadline > 29*time.Second {
		t.Errorf("deadline %v: want just under the 29s stated", got.Deadline)
	}
	// A zero deadline states none.
	if _, err := c.Call(context.Background(), "ci", listReq, 5*time.Second, 0); err != nil {
		t.Fatal(err)
	}
	if got := d.last(t); got.Deadline != 0 {
		t.Errorf("deadline %v: none was stated", got.Deadline)
	}
}

func TestCallVersionMismatchOnlyOutsideTheRange(t *testing.T) {
	for _, tc := range []struct {
		daemon   int
		mismatch bool
	}{{1, false}, {2, false}, {3, true}, {7, true}} {
		d := &stubDispatcher{}
		path := startDaemon(t, Handler(d, ServerConfig{Build: "dbuild", Protocol: tc.daemon}))
		c := &Client{SocketPath: path, Build: "cb", Protocol: 1}
		reply, err := c.Call(context.Background(), "pr", listReq, 3*time.Second, 2*time.Second)
		var vm *VersionMismatchError
		if tc.mismatch {
			if !errors.As(err, &vm) || vm.DaemonProtocol != tc.daemon || vm.DaemonBuild != "dbuild" || !strings.Contains(err.Error(), path) {
				t.Errorf("daemon protocol %d against client 1: want VersionMismatchError, got %v", tc.daemon, err)
			}
			continue
		}
		if err != nil || reply == nil {
			t.Errorf("daemon protocol %d against client 1 is inside the range: %v", tc.daemon, err)
		}
	}
}

func TestCallRelaysADaemonErrorAnswerAsAReply(t *testing.T) {
	// A refusal from a daemon the client can talk to (here unknown_op from the
	// router) is a reply to relay, not a failure of the call.
	r := router.New(router.Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil }})
	path := startDaemon(t, Handler(r, ServerConfig{Build: "d"}))
	c := &Client{SocketPath: path}
	reply, err := c.Call(context.Background(), "pr", scriptout.Request{Op: "nothing_registered"}, 3*time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply.Response), `"code":"unknown_op"`) {
		t.Errorf("the daemon's error body is the reply, unmodified: %s", reply.Response)
	}
}

func TestCallWithADaemonThatClosesWithoutAReply(t *testing.T) {
	// The peer-uid rejection looks like this to a client: accepted, then closed.
	path := startDaemon(t, func(_ context.Context, conn net.Conn, _ proc.Peer) { conn.Close() })
	c := &Client{SocketPath: path, RestartHint: "the hint"}
	_, err := c.Call(context.Background(), "pr", listReq, 3*time.Second, 2*time.Second)
	var down *DaemonDownError
	if !errors.As(err, &down) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "the hint") {
		t.Fatalf("err = %v: a daemon that never answers is unavailable, naming the socket", err)
	}
}
