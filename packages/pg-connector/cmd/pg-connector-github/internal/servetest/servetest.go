// Package servetest is the test support for the assembled pg-connector-github
// daemon: it builds the REAL binary, starts it as a child process on a short
// temp socket path with an empty-but-explicit environment, points it at a
// counting fake gh (internal/serve/testdata/fake-gh.sh) and a temp state
// directory, and gives a test the handles it needs: the state directory, the
// socket, the rendered config, the shim's call log, a socket client and
// readers for the store and the logs.
//
// It is reused by the assembly's own tests (internal/serve), the wire
// conformance tests and the platform gates, so the daemon is always started the
// same way: StartDaemon, work, StopDaemon (SIGTERM, then the exit code).
//
// Everything in it is generic: fixture owners, repositories and logins are
// invented placeholders, and the token a test supplies is a made-up string.
package servetest

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	gobuild "go/build"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite" // read-only store inspection

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Fixture names a test can rely on. They are placeholders, not real accounts.
const (
	// Host is the GitHub host the config names; the shim records it as GH_HOST.
	Host = "github.example.test"
	// QueryMine and QueryTeam are the configured queries of the default
	// config. MineExpr uses the viewer token @me (identity-scoped), TeamExpr does not.
	QueryMine = "mine"
	QueryTeam = "team"
	MineExpr  = "is:open author:@me"
	TeamExpr  = "is:open label:needs-review"
	// DefaultToken is the fixture token the shim hands out and accepts.
	DefaultToken = "fixture-token-0123456789abcdef"
	// DefaultViewer is the login the shim's viewer query answers.
	DefaultViewer = "fixture-viewer"
)

// Options configure a daemon.
type Options struct {
	// Config keys replace the defaults of the rendered config (top level).
	Config map[string]any
	// Env is extra environment of the daemon process. The process otherwise
	// gets only PATH and the shim's own variables.
	Env map[string]string
	// Hooks is the link-time serve hooks string of the binary under test (for
	// example "viewer_interval=300ms"); an empty string is the production
	// build.
	Hooks string
	// ClientBudget, when non-empty, is main.clientBudgetOverride for the binary
	// (the real client modes only).
	ClientBudget string
	// Token is what the shim hands out as the GitHub token; empty uses
	// DefaultToken.
	Token string
	// NoWait skips waiting for the daemon to answer: for a start that is
	// expected to refuse, or to observe the migrating window.
	NoWait bool
	// Before runs with the state directory and the shim ready and the daemon not
	// yet started, for a test that plants a store file or fixtures.
	Before func(d *Daemon)
}

// Daemon is one daemon under test. Its state directory, shim and config
// outlive a stop, so Start after StopDaemon is a restart.
type Daemon struct {
	// StateDir is the daemon's state directory; Socket its socket path;
	// ConfigPath the rendered config file.
	StateDir   string
	Socket     string
	ConfigPath string
	// Shim is the fake gh.
	Shim *Shim
	// Token is the fixture token the shim holds.
	Token string

	t    *testing.T
	opts Options
	bin  string

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	exit   int
	stderr *safeBuffer
}

// Binary returns the path of the daemon binary built for hooks (and
// clientBudget), building it on first use.
func Binary(t testing.TB, hooks, clientBudget string) string {
	t.Helper()
	var flags []string
	if hooks != "" {
		flags = append(flags, "-X main.serveHooks="+hooks)
	}
	if clientBudget != "" {
		flags = append(flags, "-X main.clientBudgetOverride="+clientBudget)
	}
	return build(t, strings.Join(flags, " "))
}

// NewDaemon prepares a daemon (state directory, shim, config) without starting
// it.
func NewDaemon(t *testing.T, o Options) *Daemon {
	t.Helper()
	root := shortDir(t)
	d := &Daemon{
		StateDir: filepath.Join(root, "state"),
		Socket:   filepath.Join(root, "state", "sock"),
		t:        t,
		opts:     o,
		Token:    o.Token,
	}
	if d.Token == "" {
		d.Token = DefaultToken
	}
	d.Shim = newShim(t, filepath.Join(root, "gh"), d.Token)
	d.bin = Binary(t, o.Hooks, o.ClientBudget)
	d.ConfigPath = filepath.Join(root, "config.json")
	d.writeConfig()
	if o.Before != nil {
		o.Before(d)
	}
	return d
}

// StartDaemon prepares and starts a daemon and (unless NoWait) waits until it
// answers requests. The daemon is stopped at the end of the test if still running.
func StartDaemon(t *testing.T, o Options) *Daemon {
	t.Helper()
	d := NewDaemon(t, o)
	d.Start(t)
	return d
}

func (d *Daemon) writeConfig() {
	cfg := map[string]any{
		"host":        Host,
		"gh_path":     d.Shim.Path,
		"state_dir":   d.StateDir,
		"socket_path": d.Socket,
		"pr": map[string]any{"queries": map[string]any{
			QueryMine: MineExpr,
			QueryTeam: TeamExpr,
		}},
		// A long freshness keeps a test's data fresh unless it chooses otherwise.
		"summary_ttl":     "1h",
		"terminal_ttl":    "1h",
		"adhoc_query_ttl": "1h",
		"query_ttl":       map[string]any{QueryMine: "1h", QueryTeam: "1h"},
		"config_hash":     "servetest-config-hash",
	}
	for k, v := range d.opts.Config {
		cfg[k] = v
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		d.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(d.ConfigPath), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(d.ConfigPath, raw, 0o600); err != nil {
		d.t.Fatal(err)
	}
}

// Rewrite re-renders the config with extra top-level keys (for the next Start).
func (d *Daemon) Rewrite(extra map[string]any) {
	d.t.Helper()
	merged := map[string]any{}
	for k, v := range d.opts.Config {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	d.opts.Config = merged
	d.writeConfig()
}

// SetEnv sets or replaces a daemon environment variable for the next Start.
func (d *Daemon) SetEnv(k, v string) {
	if d.opts.Env == nil {
		d.opts.Env = map[string]string{}
	}
	d.opts.Env[k] = v
}

// Start starts the daemon process; unless NoWait it returns once the daemon
// answers requests (its gate is open).
func (d *Daemon) Start(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	if d.cmd != nil {
		select {
		case <-d.done:
		default:
			d.mu.Unlock()
			t.Fatal("servetest: the daemon is already running")
		}
	}
	cmd := exec.Command(d.bin, "--config", d.ConfigPath, "serve")
	cmd.Dir = "/"
	cmd.Env = []string{
		"PATH=" + DaemonPath(),
		"FAKE_GH_DIR=" + d.Shim.Dir,
	}
	for k, v := range d.opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	buf := &safeBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		t.Fatalf("servetest: start the daemon: %v", err)
	}
	d.cmd, d.stderr, d.done, d.exit = cmd, buf, make(chan struct{}), -1
	done := d.done
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			code = -2
		}
		d.mu.Lock()
		d.exit = code
		d.mu.Unlock()
		close(done)
	}()
	d.mu.Unlock()
	t.Cleanup(func() { d.kill() })
	if !d.opts.NoWait {
		d.WaitReady(t)
	}
}

// kill ends a still-running daemon without ceremony (test cleanup).
func (d *Daemon) kill() {
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.mu.Unlock()
	if cmd == nil {
		return
	}
	select {
	case <-done:
	default:
		_ = cmd.Process.Kill()
		<-done
	}
}

// Stderr is what the daemon wrote to stdout and stderr so far.
func (d *Daemon) Stderr() string {
	d.mu.Lock()
	b := d.stderr
	d.mu.Unlock()
	if b == nil {
		return ""
	}
	return b.String()
}

// Exited reports whether the process ended, and its exit code.
func (d *Daemon) Exited() (bool, int) {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		return false, 0
	}
	select {
	case <-done:
		d.mu.Lock()
		defer d.mu.Unlock()
		return true, d.exit
	default:
		return false, 0
	}
}

// Wait waits for the process to end (up to timeout) and returns its exit code.
func (d *Daemon) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		t.Fatal("servetest: the daemon was never started")
	}
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("servetest: the daemon did not exit within %s; output: %s", timeout, d.Stderr())
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exit
}

// StopDaemon sends SIGTERM, waits for the drain and returns the exit code.
func StopDaemon(t *testing.T, d *Daemon) int {
	t.Helper()
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.mu.Unlock()
	if cmd == nil {
		t.Fatal("servetest: the daemon was never started")
	}
	select {
	case <-done:
	default:
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("servetest: SIGTERM: %v", err)
		}
	}
	return d.Wait(t, 30*time.Second)
}

// Term sends SIGTERM and returns at once; Wait collects the exit code. A test
// uses it to stop the daemon while a request is still in flight.
func (d *Daemon) Term(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.mu.Unlock()
	if cmd == nil {
		t.Fatal("servetest: the daemon was never started")
	}
	select {
	case <-done:
	default:
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("servetest: SIGTERM: %v", err)
		}
	}
}

// Kill ends the daemon with SIGKILL (a crash) and returns once it is gone: the
// process is reaped and its single-instance lock can be taken again. The second
// matters on a loaded host. A child the daemon forked just before the kill (a gh
// call) holds a copy of the lock's descriptor until its exec closes it, so the
// lock outlives the daemon for as long as the host takes to run that exec, and
// a start straight after the kill would meet "another instance holds the lock".
func (d *Daemon) Kill(t *testing.T) {
	t.Helper()
	d.kill()
	if !awaitLockFree(filepath.Join(d.StateDir, config.LockFileName), 30*time.Second) {
		t.Fatalf("servetest: the killed daemon's lock %s is still held after 30s", filepath.Join(d.StateDir, config.LockFileName))
	}
}

// awaitLockFree waits until the flock at path can be taken (or does not exist)
// and reports whether that happened within max. It releases the lock again.
func awaitLockFree(path string, max time.Duration) bool {
	lk := flock.New(path)
	deadline := time.Now().Add(max)
	for {
		locked, err := lk.TryLock()
		if err == nil && locked {
			_ = lk.Unlock()
			return true
		}
		if err != nil && os.IsNotExist(err) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WaitReady waits until the daemon answers an auth_status request (its gate is
// open), failing if the process ends first.
func (d *Daemon) WaitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ended, code := d.Exited(); ended {
			t.Fatalf("servetest: the daemon exited (%d) before it was ready; output: %s", code, d.Stderr())
		}
		resp, err := d.Call("pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 5*time.Second)
		if err == nil && resp.Error == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("servetest: the daemon never became ready; output: %s", d.Stderr())
}

// Call sends one request for an instance with the client's deadline and returns
// the daemon's answer.
func (d *Daemon) Call(instance string, req scriptout.Request, deadline time.Duration) (scriptout.Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return scriptout.Response{}, err
	}
	reply, err := d.Raw(socket.RequestFrame{
		SocketProtocol: socket.ProtocolVersion,
		ClientBuild:    "servetest",
		Instance:       instance,
		DeadlineMS:     deadline.Milliseconds(),
		Request:        raw,
	})
	if err != nil {
		return scriptout.Response{}, err
	}
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		return scriptout.Response{}, fmt.Errorf("decode the response %q: %w", reply.Response, err)
	}
	return resp, nil
}

// MustCall is Call that fails the test on a transport error.
func (d *Daemon) MustCall(t *testing.T, instance string, req scriptout.Request, deadline time.Duration) scriptout.Response {
	t.Helper()
	resp, err := d.Call(instance, req, deadline)
	if err != nil {
		t.Fatalf("servetest: %s %s: %v", instance, req.Op, err)
	}
	return resp
}

// Raw sends one frame as given and returns the reply frame.
func (d *Daemon) Raw(frame socket.RequestFrame) (socket.ReplyFrame, error) {
	conn, err := net.DialTimeout("unix", d.Socket, 5*time.Second)
	if err != nil {
		return socket.ReplyFrame{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := socket.WriteFrame(conn, frame); err != nil {
		return socket.ReplyFrame{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	raw, err := socket.ReadFrame(bufio.NewReader(conn), 64<<20)
	if err != nil {
		return socket.ReplyFrame{}, err
	}
	var reply socket.ReplyFrame
	if err := json.Unmarshal(raw, &reply); err != nil {
		return socket.ReplyFrame{}, fmt.Errorf("decode the reply frame %q: %w", raw, err)
	}
	return reply, nil
}

// OpenStore opens the daemon's store file read-only. The daemon may be running
// (the store is a WAL database); the handle is closed at the end of the test.
func (d *Daemon) OpenStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(d.StateDir, "store.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// StorePath is the store file's path.
func (d *Daemon) StorePath() string { return filepath.Join(d.StateDir, "store.sqlite") }

// EventLog returns the rows of events.jsonl.
func (d *Daemon) EventLog(t *testing.T) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(d.StateDir, "events.jsonl"))
}

// DaemonLog returns the lines of daemon.log.jsonl.
func (d *Daemon) DaemonLog(t *testing.T) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(d.StateDir, "daemon.log.jsonl"))
}

// HasChange reports whether the daemon log has a line for the named change.
func HasChange(lines []map[string]any, change string) bool {
	for _, l := range lines {
		if l["change"] == change {
			return true
		}
	}
	return false
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("%s: line %q is not JSON: %v", path, line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

// ---- building the binary ------------------------------------------------------

var (
	buildMu   sync.Mutex
	buildRoot string
	builds    = map[string]string{}
)

// RemoveBuilds deletes the built binaries; a package's TestMain calls it after
// the run.
func RemoveBuilds() {
	buildMu.Lock()
	defer buildMu.Unlock()
	if buildRoot != "" {
		_ = os.RemoveAll(buildRoot)
		buildRoot = ""
		builds = map[string]string{}
	}
}

func build(t testing.TB, ldflags string) string {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if bin, ok := builds[ldflags]; ok {
		return bin
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		cand := filepath.Join(gobuild.Default.GOROOT, "bin", "go")
		if _, serr := os.Stat(cand); serr != nil {
			t.Skipf("servetest: no go toolchain to build the real binary: %v", err)
		}
		goBin = cand
	}
	if buildRoot == "" {
		root, err := os.MkdirTemp("", "pcgsrv")
		if err != nil {
			t.Fatal(err)
		}
		buildRoot = root
	}
	bin := filepath.Join(buildRoot, strconv.Itoa(len(builds)), "pg-connector-github")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "./cmd/pg-connector-github")
	cmd := exec.Command(goBin, args...)
	cmd.Dir = moduleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("servetest: go build: %v\n%s", err, out)
	}
	builds[ldflags] = bin
	return bin
}

// moduleRoot is the directory holding go.mod: the one above this source file.
func moduleRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("servetest: cannot locate its own source")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("servetest: no go.mod above " + file)
		}
		dir = parent
	}
}

// shimSource is the fake gh script's path.
func shimSource(t testing.TB) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "cmd", "pg-connector-github", "internal", "serve", "testdata", "fake-gh.sh")
}

// shortDir returns a fresh directory with a short absolute path (a unix socket
// path is capped near 104 bytes, and t.TempDir can exceed that).
func shortDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcg")
	if err != nil {
		d, err = os.MkdirTemp("", "pcg")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// safeBuffer is a bytes.Buffer safe for a test to read while the child writes.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// LoadConfig loads the rendered config the way the binary does (no environment
// overrides), for an in-process test that runs serve.Run itself.
func (d *Daemon) LoadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(d.ConfigPath, func(string) string { return "" })
	if err != nil {
		t.Fatalf("servetest: load the rendered config: %v", err)
	}
	return cfg
}

// WritableStore opens the store file read-write with plain SQL, for a test that
// edits it while the daemon is stopped (for example to age its due times).
func (d *Daemon) WritableStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.StorePath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// DaemonPath is the PATH of a daemon (and of the fake gh it runs): the
// minimal system dirs a service manager gives it, followed by the test
// process's own PATH. The tail matters in the nix build sandbox, where
// /usr/bin and /bin hold nothing but env and sh, while the coreutils the fake
// gh script needs (tr, cat, sed, ...) live only on the build's PATH.
func DaemonPath() string {
	if p := os.Getenv("PATH"); p != "" {
		return "/usr/bin:/bin:" + p
	}
	return "/usr/bin:/bin"
}
