// Package servetest is the test support for the assembled pg-connector-issue-jira
// daemon and its client: it builds the REAL binary, starts the daemon as a child
// process on a short temp socket path with an EMPTY environment, points it at a
// counting fake pjira (shim.go) and a temp state directory, and gives a test the
// handles it needs: the state directory, the socket, the rendered config, the
// shim's call log, a socket caller, a runner of the client binary and readers
// for the store and the logs.
//
// The daemon is always started the same way: StartDaemon, work, StopDaemon
// (SIGTERM, then the exit code). Everything the daemon needs comes from the
// rendered config, as under its supervisor: the absolute pjira path and the one
// environment (pjira_env) every pjira process gets.
//
// Everything in it is generic: fixture projects, hosts and people are invented
// placeholders.
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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// RouterInstance is the one router instance the daemon registers its ops under.
const RouterInstance = "issue"

// Instance is the registry instance name of the rendered config (the config's
// default), the name the client is started with.
const Instance = config.DefaultInstance

// SupervisorLabel is the label of the rendered config's supervisor entry.
const SupervisorLabel = "com.example.jira-daemon"

// ParentWatchHook is the serve hook every harness-built binary carries: the
// daemon exits when the process that started it is gone.
const ParentWatchHook = "parent_watch=250ms"

// Options configure a daemon.
type Options struct {
	// Config keys replace the defaults of the rendered config (top level).
	Config map[string]any
	// Env is extra environment of the daemon process. The process otherwise gets
	// none at all.
	Env map[string]string
	// Hooks is the link-time serve hooks string of the binary under test (for
	// example "poll_interval=50ms"); an empty string is the production build.
	Hooks string
	// ClientBudget, when non-empty, is main.clientBudgetOverride for the binary
	// (the client and status modes).
	ClientBudget string
	// NoWait skips waiting for the daemon to answer: for a start that is
	// expected to refuse, or to observe the migrating window.
	NoWait bool
	// Before runs with the state directory and the shim ready and the daemon not
	// yet started, for a test that plants a store file or fixtures.
	Before func(d *Daemon)
}

// Daemon is one daemon under test. Its state directory, shim and config outlive
// a stop, so Start after StopDaemon is a restart.
type Daemon struct {
	// StateDir is the daemon's state directory; Socket its socket path;
	// ConfigPath the rendered config file.
	StateDir   string
	Socket     string
	ConfigPath string
	// Shim is the fake pjira.
	Shim *Shim

	t    *testing.T
	opts Options
	bin  string

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	exit   int
	stderr *safeBuffer
}

// Binary returns the path of the binary built for hooks (and clientBudget),
// building it on first use.
func Binary(t testing.TB, hooks, clientBudget string) string {
	t.Helper()
	var flags []string
	// Every daemon the harness starts drains and exits when its parent (the test
	// process) ends, however it ends: t.Cleanup does not run when the test
	// binary is killed (a timeout, SIGKILL), and an orphaned daemon would then
	// live on for days (pg2-krpw80).
	if hooks != "" {
		hooks += ","
	}
	hooks += ParentWatchHook
	flags = append(flags, "-X main.serveHooks="+hooks)
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
	}
	d.Shim = newShim(t, filepath.Join(root, "pjira"))
	d.bin = Binary(t, o.Hooks, o.ClientBudget)
	d.ConfigPath = filepath.Join(root, "config.json")
	d.writeConfig()
	if o.Before != nil {
		o.Before(d)
	}
	return d
}

// StartDaemon prepares and starts a daemon and (unless NoWait) waits until it
// answers requests. The daemon is stopped at the end of the test if still
// running.
func StartDaemon(t *testing.T, o Options) *Daemon {
	t.Helper()
	d := NewDaemon(t, o)
	d.Start(t)
	return d
}

// RenderedConfig is the config a daemon is started with: the defaults of this
// package with extra keys laid over them. state_dir, socket_path and pjira_path
// name this daemon's own directories.
func (d *Daemon) RenderedConfig(extra map[string]any) map[string]any {
	cfg := map[string]any{
		"state_dir":   d.StateDir,
		"socket_path": d.Socket,
		"pjira_path":  d.Shim.Path,
		// The ONLY environment a pjira process gets: where the shim lives, and a
		// PATH for the commands the shim's own script runs.
		"pjira_env":        map[string]string{"FAKE_PJIRA_DIR": d.Shim.Dir, "PATH": DaemonPath()},
		"supervisor_label": SupervisorLabel,
		// MineJQL is watched, as a deployment declares the queries a consumer
		// follows; TeamJQL is not.
		"queries": map[string]any{
			QueryMine: map[string]any{"expr": MineJQL, "watched": true},
			QueryTeam: TeamJQL,
		},
		// A long freshness keeps a test's data fresh unless it chooses otherwise.
		"summary_ttl":          "1h",
		"detail_ttl":           "1h",
		"detail_max_age":       "2h",
		"query_ttl_default":    "1h",
		"terminal_ttl":         "1h",
		"write_notice_timeout": "2s",
		// A generous request rate: a test's own calls and the daemon's must not meet
		// the cap unless the test sets it.
		"request_rate_per_second": 200,
		"concurrency":             4,
		"config_hash":             "servetest-config-hash",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func (d *Daemon) writeConfig() {
	raw, err := json.MarshalIndent(d.RenderedConfig(d.opts.Config), "", "  ")
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

// Start starts the daemon process under an EMPTY environment and working
// directory "/"; unless NoWait it returns once the daemon answers requests (its
// gate is open).
func (d *Daemon) Start(t *testing.T) {
	t.Helper()
	d.start(t, !d.opts.NoWait)
}

// StartNoWait is Start that does not wait for the daemon to answer: for a start
// expected to refuse, or to observe the migrating window.
func (d *Daemon) StartNoWait(t *testing.T) {
	t.Helper()
	d.start(t, false)
}

func (d *Daemon) start(t *testing.T, wait bool) {
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
	cmd.Env = []string{}
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
	if wait {
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
	d.Term(t)
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
// matters on a loaded host: a pjira child forked just before the kill holds a
// copy of the lock's descriptor until its exec closes it.
func (d *Daemon) Kill(t *testing.T) {
	t.Helper()
	d.kill()
	if !awaitLockFree(filepath.Join(d.StateDir, config.LockFileName), 30*time.Second) {
		t.Fatalf("servetest: the killed daemon's lock is still held after 30s")
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

// WaitReady waits until the daemon answers a status request (its gate is open;
// status makes no origin call), failing if the process ends first.
func (d *Daemon) WaitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ended, code := d.Exited(); ended {
			t.Fatalf("servetest: the daemon exited (%d) before it was ready; output: %s", code, d.Stderr())
		}
		resp, err := d.Call(scriptout.Request{Op: "status"}, 5*time.Second)
		if err == nil && resp.Error == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("servetest: the daemon never became ready; output: %s", d.Stderr())
}

// Call sends one request with the client's deadline and returns the daemon's
// answer.
func (d *Daemon) Call(req scriptout.Request, deadline time.Duration) (scriptout.Response, error) {
	return d.CallProtocol(req, deadline, socket.ProtocolVersion)
}

// CallProtocol is Call with the socket protocol the frame claims.
func (d *Daemon) CallProtocol(req scriptout.Request, deadline time.Duration, protocol int) (scriptout.Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return scriptout.Response{}, err
	}
	reply, err := d.Raw(socket.RequestFrame{
		SocketProtocol: protocol,
		ClientBuild:    "servetest",
		Instance:       RouterInstance,
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
func (d *Daemon) MustCall(t *testing.T, req scriptout.Request, deadline time.Duration) scriptout.Response {
	t.Helper()
	resp, err := d.Call(req, deadline)
	if err != nil {
		t.Fatalf("servetest: %s: %v", req.Op, err)
	}
	return resp
}

// Op builds a request of op with the arguments given as a JSON object text (or
// nothing).
func Op(op, args string) scriptout.Request {
	req := scriptout.Request{Op: op}
	if args != "" {
		req.Args = json.RawMessage(args)
	}
	return req
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

// ClientResult is one run of the client binary.
type ClientResult struct {
	// Stdout is the raw answer, Stderr what the process wrote to stderr and Exit
	// its exit code. Resp is Stdout decoded (zero when it is not a response).
	Stdout string
	Stderr string
	Exit   int
	Resp   scriptout.Response
}

// ClientEnv is the environment the client binary is run under: the caller's own
// (a PATH and the fixtures a WRITE needs to reach the shim) and nothing else.
func (d *Daemon) ClientEnv() []string {
	return []string{
		"PATH=" + DaemonPath(),
		"FAKE_PJIRA_DIR=" + d.Shim.Dir,
		"PG_CONNECTOR_ISSUE_JIRA_BINARY=" + d.Shim.Path,
		"PG_CONNECTOR_ISSUE_JIRA_PROJECT=" + Project,
	}
}

// ExecArgs are the words after the binary of the registered {name, command}
// form of the instance: --config <rendered file> --instance <name>.
func (d *Daemon) ExecArgs() []string {
	return []string{"--config", d.ConfigPath, "--instance", Instance}
}

// ClientWith runs the client binary built with clientBudget (empty is the
// daemon's own binary) with one request on stdin.
func (d *Daemon) ClientWith(t testing.TB, bin, request string) ClientResult {
	t.Helper()
	return RunBinary(t, bin, request, d.ClientEnv(), d.ExecArgs()...)
}

// Client runs the client binary of this daemon's build with one request on stdin
// and returns the decoded answer and the exit code.
func (d *Daemon) Client(t testing.TB, request string) ClientResult {
	t.Helper()
	return d.ClientWith(t, d.bin, request)
}

// RunBinary runs bin with args under exactly env, working directory "/" and
// stdin as given, and decodes its stdout as a response when it is one.
func RunBinary(t testing.TB, bin, stdin string, env []string, args ...string) ClientResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = "/"
	cmd.Env = append([]string{}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := ClientResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		t.Fatalf("servetest: run %s: %v", bin, err)
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &res.Resp)
	return res
}

// Bin is the daemon binary this daemon was built with (the client binary too).
func (d *Daemon) Bin() string { return d.bin }

// OpenStore opens the daemon's store file read-only. The daemon may be running
// (the store is a WAL database); the handle is closed at the end of the test.
func (d *Daemon) OpenStore(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.StorePath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// WritableStore opens the store file read-write with plain SQL, for a test that
// edits it while the daemon is stopped.
func (d *Daemon) WritableStore(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.StorePath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// StorePath is the store file's path.
func (d *Daemon) StorePath() string { return filepath.Join(d.StateDir, config.StoreFileName) }

// EventLog returns the rows of events.jsonl.
func (d *Daemon) EventLog(t testing.TB) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(d.StateDir, config.EventLogFileName))
}

// DaemonLog returns the lines of daemon.log.jsonl.
func (d *Daemon) DaemonLog(t testing.TB) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(d.StateDir, config.DaemonLogFileName))
}

// ClientLog returns the lines of client.log.jsonl.
func (d *Daemon) ClientLog(t testing.TB) []map[string]any {
	t.Helper()
	return readJSONL(t, filepath.Join(d.StateDir, config.ClientLogFileName))
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

func readJSONL(t testing.TB, path string) []map[string]any {
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
		root, err := os.MkdirTemp("", "pcjsrv")
		if err != nil {
			t.Fatal(err)
		}
		buildRoot = root
	}
	bin := filepath.Join(buildRoot, strconv.Itoa(len(builds)), "pg-connector-issue-jira")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "./cmd/pg-connector-issue-jira")
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

// shortDir returns a fresh directory with a short absolute path (a unix socket
// path is capped near 104 bytes, and t.TempDir can exceed that).
func shortDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcj")
	if err != nil {
		d, err = os.MkdirTemp("", "pcj")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// ShortDir is shortDir for a test that needs a scratch directory of its own.
func ShortDir(t testing.TB) string { return shortDir(t) }

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

// DaemonPath is the PATH the shim's own script (and a client's pjira) gets: the
// minimal system dirs followed by the test process's own PATH. The tail matters
// in the nix build sandbox, where /usr/bin and /bin hold nothing but env and sh,
// while the coreutils the script needs (tr, cat, sleep, ...) live only on the
// build's PATH.
func DaemonPath() string {
	if p := os.Getenv("PATH"); p != "" {
		return "/usr/bin:/bin:" + p
	}
	return "/usr/bin:/bin"
}
