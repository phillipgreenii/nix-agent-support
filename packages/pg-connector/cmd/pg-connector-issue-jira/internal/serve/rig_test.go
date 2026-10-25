package serve_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // reads the daemon's store file

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The daemon is assembled in-process: serve.Run on a real unix socket under a
// short temp path, its origin a fake pjira shell script in a temp directory, its
// state in a temp directory. No environment variable is read by the daemon.

// fakePjiraOK answers every search with one open issue, ALPHA-1.
const fakePjiraOK = `#!/bin/sh
case "$1" in
search)
  printf '%s' '{"items":[{"key":"ALPHA-1","summary":"title of ALPHA-1","status":"Some Status","status_category":"indeterminate","issuetype":"Task","project":"ALPHA","updated":"2026-10-05T10:11:12.000+0000","comments":[]}],"truncated":false}'
  ;;
*)
  echo "fake pjira: unexpected args $*" >&2
  exit 1
  ;;
esac
`

// fakePjiraDown fails every call, like an unreachable origin.
const fakePjiraDown = `#!/bin/sh
echo "fake pjira: the origin is unreachable" >&2
exit 1
`

// syncBuf is a goroutine-safe stderr.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is one daemon directory: a config file, a fake pjira and a state
// directory, under a short path (a unix socket path is limited to about 100
// bytes).
type rig struct {
	t     *testing.T
	root  string
	state string
	cfg   *config.Config
}

// newRig writes the config and the fake pjira. before runs after the state
// directory exists and before the config is loaded.
func newRig(t *testing.T, pjira string, queries string, before func(r *rig)) *rig {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "jx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	r := &rig{t: t, root: root, state: filepath.Join(root, "state")}
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "pjira")
	if err := os.WriteFile(script, []byte(pjira), 0o755); err != nil {
		t.Fatal(err)
	}
	if queries == "" {
		queries = "{}"
	}
	doc := `{"state_dir":` + quote(r.state) + `,"pjira_path":` + quote(script) +
		`,"socket_path":` + quote(filepath.Join(root, "s")) + `,"queries":` + queries + `}`
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if before != nil {
		before(r)
	}
	cfg, err := config.Load(path, nil)
	if err != nil {
		t.Fatalf("load the test config: %v", err)
	}
	r.cfg = cfg
	return r
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (r *rig) storePath() string { return filepath.Join(r.state, config.StoreFileName) }

// daemonRun is one running serve.Run.
type daemonRun struct {
	r      *rig
	cancel context.CancelFunc
	code   chan int
	ready  chan *router.Router
	stderr *syncBuf
	router *router.Router
}

// start runs the daemon; the caller waits for it with waitReady and ends it with
// stop (a test cleanup ends it too).
func (r *rig) start(hooks serve.Hooks) *daemonRun {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemonRun{r: r, cancel: cancel, code: make(chan int, 1), ready: make(chan *router.Router, 1), stderr: &syncBuf{}}
	go func() {
		d.code <- serve.Run(ctx, serve.Options{
			Config: r.cfg, Version: "test", Stderr: d.stderr, Hooks: hooks,
			OnReady: func(rt *router.Router) { d.ready <- rt },
		})
	}()
	r.t.Cleanup(func() {
		cancel()
		select {
		case c := <-d.code:
			d.code <- c
		case <-time.After(30 * time.Second):
		}
	})
	return d
}

// waitReady blocks until the gate opened.
func (d *daemonRun) waitReady() *router.Router {
	d.r.t.Helper()
	select {
	case rt := <-d.ready:
		d.router = rt
		return rt
	case c := <-d.code:
		d.code <- c
		d.r.t.Fatalf("the daemon exited %d before it was ready; stderr: %s", c, d.stderr.String())
	case <-time.After(60 * time.Second):
		d.r.t.Fatalf("the daemon was not ready in time; stderr: %s", d.stderr.String())
	}
	return nil
}

// stop cancels the context (what SIGTERM does) and returns the exit code.
func (d *daemonRun) stop() int {
	d.r.t.Helper()
	d.cancel()
	return d.wait(30 * time.Second)
}

// wait returns the exit code of Run.
func (d *daemonRun) wait(limit time.Duration) int {
	d.r.t.Helper()
	select {
	case c := <-d.code:
		d.code <- c
		return c
	case <-time.After(limit):
		d.r.t.Fatalf("the daemon did not exit within %s; stderr: %s", limit, d.stderr.String())
	}
	return -1
}

// call sends one request frame to instance over the daemon's socket.
func (r *rig) call(instance, op string, args any) (scriptout.Response, error) {
	r.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		r.t.Fatal(err)
	}
	req, err := json.Marshal(scriptout.Request{Op: op, Args: raw})
	if err != nil {
		r.t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", r.cfg.SocketPath, 2*time.Second)
	if err != nil {
		return scriptout.Response{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := socket.WriteFrame(conn, socket.RequestFrame{
		SocketProtocol: socket.ProtocolVersion, ClientBuild: "test", Instance: instance,
		DeadlineMS: (30 * time.Second).Milliseconds(), Request: req,
	}); err != nil {
		return scriptout.Response{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	frame, err := socket.ReadFrame(bufio.NewReader(conn), 64<<20)
	if err != nil {
		return scriptout.Response{}, err
	}
	var reply socket.ReplyFrame
	if err := json.Unmarshal(frame, &reply); err != nil {
		r.t.Fatalf("decode the reply frame %q: %v", frame, err)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		r.t.Fatalf("decode the response %q: %v", reply.Response, err)
	}
	return resp, nil
}

// mustCall is call that fails the test on a transport error.
func (r *rig) mustCall(instance, op string, args any) scriptout.Response {
	r.t.Helper()
	resp, err := r.call(instance, op, args)
	if err != nil {
		r.t.Fatalf("%s %s: %v", instance, op, err)
	}
	return resp
}

// until polls cond every 10 ms until it holds or the timeout passes.
func until(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// daemonLog reads the structured log of state changes.
func (r *rig) daemonLog() []map[string]any {
	r.t.Helper()
	raw, err := os.ReadFile(logs.DaemonLogPath(r.state))
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			r.t.Fatalf("daemon log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func hasChange(lines []map[string]any, change string) bool {
	for _, l := range lines {
		if l["change"] == change {
			return true
		}
	}
	return false
}

// count runs a COUNT query against the store file, read-only.
func (r *rig) count(query string) int {
	r.t.Helper()
	db, err := sql.Open("sqlite", "file:"+r.storePath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
	return n
}
