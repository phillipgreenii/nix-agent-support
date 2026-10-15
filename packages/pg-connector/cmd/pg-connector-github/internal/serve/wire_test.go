package serve_test

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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Error table rows for the wire: protocol outside N and N-1, socket path,
// permission and peer uid, unknown query and config hash mismatch.

func frameFor(t *testing.T, protocol int, req scriptout.Request) socket.RequestFrame {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return socket.RequestFrame{SocketProtocol: protocol, ClientBuild: "servetest", Instance: "pr", DeadlineMS: 5000, Request: raw}
}

func replyError(t *testing.T, reply socket.ReplyFrame) *scriptout.Error {
	t.Helper()
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		t.Fatalf("decode %s: %v", reply.Response, err)
	}
	return resp.Error
}

func TestErrorTable_ProtocolOutsideNAndNMinus1_IsVersionMismatch(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})
	n := socket.ProtocolVersion
	req := scriptout.Request{Op: scriptout.OpAuthStatus}

	// N is served.
	reply, err := d.Raw(frameFor(t, n, req))
	if err != nil || replyError(t, reply) != nil {
		t.Fatalf("protocol N must be served: %v %v", err, reply)
	}
	// Anything outside N and N-1 is version_mismatch, naming both builds, in
	// the reply's own protocol number.
	for _, got := range []int{n + 1, n + 5, 0, -1} {
		reply, err := d.Raw(frameFor(t, got, req))
		if err != nil {
			t.Fatalf("protocol %d: %v", got, err)
		}
		e := replyError(t, reply)
		if e == nil || e.Code != "version_mismatch" {
			t.Errorf("protocol %d: got %+v, want version_mismatch", got, e)
		}
		if e != nil && !strings.Contains(e.Message, "servetest") {
			t.Errorf("protocol %d: the message must name the client build: %q", got, e.Message)
		}
		if reply.SocketProtocol != n {
			t.Errorf("the reply must carry the daemon's protocol %d, got %d", n, reply.SocketProtocol)
		}
	}
	// A frame that is not JSON is invalid_argument, not a crash.
	conn, err := net.Dial("unix", d.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("not a frame\n"))
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	k, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:k]), "invalid_argument") {
		t.Errorf("a malformed frame must be invalid_argument: %q", buf[:k])
	}
}

func TestErrorTable_UnknownQuery_IsQueryNotRecognizedWhateverTheRequestCarries(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{Before: seed})
	// The daemon's own config decides: a query the request's config block
	// defines but the daemon does not is still not recognized.
	awaitFirstSightingFetches(t, d, 3)
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second) // the configured queries' own first runs
	before := len(d.Shim.OriginCalls())
	req := listReq("not-a-configured-query", nil)
	req.Config = json.RawMessage(`{"queries":{"not-a-configured-query":"is:open"}}`)
	resp := d.MustCall(t, "pr", req, 10*time.Second)
	if resp.Error == nil || resp.Error.Code != "query_not_recognized" {
		t.Fatalf("got %+v, want query_not_recognized", resp.Error)
	}
	if got := len(d.Shim.OriginCalls()); got != before {
		t.Errorf("an unknown query made %d origin calls", got-before)
	}
}

func TestErrorTable_ConfigHashMismatch_IsInvalidArgumentNamingBothConfigs(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{Before: seed})

	// A block rendered from another config is refused.
	req := scriptout.Request{Op: scriptout.OpAuthStatus, Config: json.RawMessage(`{"config_hash":"some-other-hash","config_path":"/etc/example/other-config.json"}`)}
	resp := d.MustCall(t, "pr", req, 10*time.Second)
	if resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Fatalf("got %+v, want invalid_argument", resp.Error)
	}
	for _, want := range []string{"/etc/example/other-config.json", d.ConfigPath, "some-other-hash"} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Errorf("the message must name %q: %s", want, resp.Error.Message)
		}
	}
	// A block with the daemon's own hash, one with no hash and no block at all
	// pass.
	for _, cfg := range []string{`{"config_hash":"servetest-config-hash"}`, `{"queries":{}}`, ``} {
		req := scriptout.Request{Op: scriptout.OpAuthStatus}
		if cfg != "" {
			req.Config = json.RawMessage(cfg)
		}
		if resp := d.MustCall(t, "pr", req, 10*time.Second); resp.Error != nil {
			t.Errorf("config %q must pass: %+v", cfg, resp.Error)
		}
	}
}

func TestErrorTable_SocketPathTooLong_TheDaemonRefusesAndSaysSo(t *testing.T) {
	limit := proc.MaxSocketPathBytes()
	d := servetest.NewDaemon(t, servetest.Options{NoWait: true, Before: func(d *servetest.Daemon) {
		long := filepath.Join(d.StateDir, strings.Repeat("s", limit))
		d.Socket = long
		d.Rewrite(map[string]any{"socket_path": long})
	}})
	d.Start(t)
	if code := d.Wait(t, 20*time.Second); code != 5 {
		t.Fatalf("exit = %d, want 5; output: %s", code, d.Stderr())
	}
	if !strings.Contains(d.Stderr(), "sun_path limit") {
		t.Errorf("the refusal must name the limit: %q", d.Stderr())
	}
	if !servetest.HasChange(d.DaemonLog(t), "socket_path_too_long") && !strings.Contains(d.Stderr(), "socket path") {
		t.Error("the refusal must be on record")
	}
}

func TestErrorTable_SocketPermission_StateDirIs0700AndTheSocket0600(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{})
	if fi, err := os.Stat(d.StateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode: %v %v, want 0700", fi, err)
	}
	if fi, err := os.Stat(d.Socket); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket: %v %v, want a 0600 socket", fi, err)
	}
	// A looser state directory is tightened at the next start.
	servetest.StopDaemon(t, d)
	if err := os.Chmod(d.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.Start(t)
	if fi, err := os.Stat(d.StateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode after restart: %v %v, want 0700", fi, err)
	}
}

// runInProcess runs serve.Run in this process (the peer uid of a connection
// cannot be faked against a child process by a test that is not root), with
// the shim of d, and returns the router once the gate has opened.
type inProcess struct {
	rt     *router.Router
	cancel context.CancelFunc
	done   chan int
}

func runInProcess(t *testing.T, d *servetest.Daemon, tweak func(*serve.Options)) *inProcess {
	t.Helper()
	t.Setenv("FAKE_GH_DIR", d.Shim.Dir)
	cfg := d.LoadConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := &inProcess{cancel: cancel, done: make(chan int, 1)}
	var mu sync.Mutex
	ready := make(chan struct{})
	o := serve.Options{
		Config: cfg, Version: "in-process-test", Stderr: os.Stderr,
		OnReady: func(rt *router.Router) {
			mu.Lock()
			p.rt = rt
			mu.Unlock()
			close(ready)
		},
	}
	if tweak != nil {
		tweak(&o)
	}
	go func() { p.done <- serve.Run(ctx, o) }()
	select {
	case <-ready:
	case code := <-p.done:
		t.Fatalf("serve.Run exited (%d) before it was ready", code)
	case <-time.After(30 * time.Second):
		t.Fatal("serve.Run never became ready")
	}
	t.Cleanup(func() { cancel(); <-p.done })
	return p
}

func (p *inProcess) stop(t *testing.T) int {
	t.Helper()
	p.cancel()
	select {
	case code := <-p.done:
		p.done <- code
		return code
	case <-time.After(30 * time.Second):
		t.Fatal("serve.Run did not drain")
		return -1
	}
}

func TestErrorTable_PeerUID_AConnectionFromAnotherUserIsRejectedWithALogLine(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{})
	const daemonUID, otherUID = 4242, 4243
	var mu sync.Mutex
	peer := uint32(daemonUID)
	p := runInProcess(t, d, func(o *serve.Options) {
		o.OwnUID = func() uint32 { return daemonUID }
		o.PeerUID = func(net.Conn) (uint32, error) {
			mu.Lock()
			defer mu.Unlock()
			return peer, nil
		}
	})

	// The daemon's own uid is served.
	if resp := d.MustCall(t, "pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 10*time.Second); resp.Error != nil {
		t.Fatalf("a same-uid peer must be served: %+v", resp.Error)
	}
	// Another uid is rejected: the connection is closed with no reply.
	mu.Lock()
	peer = otherUID
	mu.Unlock()
	if resp, err := d.Call("pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 5*time.Second); err == nil {
		t.Errorf("a peer with another uid must get no answer, got %+v", resp)
	}
	until(t, 10*time.Second, "the peer_uid_rejected log line", func() bool {
		for _, l := range d.DaemonLog(t) {
			if l["change"] == "peer_uid_rejected" && l["peer_uid"] == float64(otherUID) && l["daemon_uid"] == float64(daemonUID) {
				return true
			}
		}
		return false
	})
	// The daemon is unharmed.
	mu.Lock()
	peer = daemonUID
	mu.Unlock()
	if resp := d.MustCall(t, "pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 10*time.Second); resp.Error != nil {
		t.Errorf("after the rejection: %+v", resp.Error)
	}
	if code := p.stop(t); code != 0 {
		t.Errorf("exit = %d", code)
	}
	if _, err := os.Lstat(d.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the drained daemon must remove its socket: %v", err)
	}
}
