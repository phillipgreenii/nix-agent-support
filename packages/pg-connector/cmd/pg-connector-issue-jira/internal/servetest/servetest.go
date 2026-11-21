// Package servetest is the test support for the assembled pg-connector-issue-jira
// daemon and its client: it builds the REAL binary, starts the daemon as a child
// process on a short temp socket path with an EMPTY environment, points it at a
// counting fake pjira (shim.go) and a temp state directory, and gives a test the
// handles it needs: the state directory, the socket, the rendered config, the
// shim's call log, a socket caller, a runner of the client binary and readers
// for the store and the logs.
//
// The process lifecycle, the socket client, the binary build and the readers are
// the connector daemon kernel's shared harness (pkg/connectord/daemon/daemontest);
// this package adds what is this daemon's own: the fixtures, the rendered config
// and the fake pjira.
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
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon/daemontest"
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
// a stop, so Start after StopDaemon is a restart. The process handle (Stderr,
// Exited, Wait, Term, Stop) is the kernel harness's daemontest.Proc.
type Daemon struct {
	daemontest.Proc

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
	return builder.Build(t, strings.Join(flags, " "))
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
	cmd := exec.CommandContext(context.Background(), d.bin, "--config", d.ConfigPath, "serve")
	cmd.Dir = "/"
	cmd.Env = []string{}
	for k, v := range d.opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	d.Proc.Start(t, cmd)
	if wait {
		d.WaitReady(t)
	}
}

// StopDaemon sends SIGTERM, waits for the drain and returns the exit code.
func StopDaemon(t *testing.T, d *Daemon) int {
	t.Helper()
	return d.Stop(t)
}

// Kill ends the daemon with SIGKILL (a crash) and returns once it is gone: the
// process is reaped and its single-instance lock can be taken again (see
// daemontest.Proc.KillAndAwaitLock for why the second matters).
func (d *Daemon) Kill(t *testing.T) {
	t.Helper()
	d.KillAndAwaitLock(t, filepath.Join(d.StateDir, config.LockFileName))
}

// WaitReady waits until the daemon answers a status request (its gate is open;
// status makes no origin call), failing if the process ends first.
func (d *Daemon) WaitReady(t *testing.T) {
	t.Helper()
	d.WaitUntilReady(t, func() bool {
		resp, err := d.Call(scriptout.Request{Op: "status"}, 5*time.Second)
		return err == nil && resp.Error == nil
	})
}

// Call sends one request with the client's deadline and returns the daemon's
// answer.
func (d *Daemon) Call(req scriptout.Request, deadline time.Duration) (scriptout.Response, error) {
	return d.CallProtocol(req, deadline, socket.ProtocolVersion)
}

// CallProtocol is Call with the socket protocol the frame claims.
func (d *Daemon) CallProtocol(req scriptout.Request, deadline time.Duration, protocol int) (scriptout.Response, error) {
	return daemontest.Call(d.Socket, RouterInstance, protocol, req, deadline)
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
	return daemontest.Raw(d.Socket, frame)
}

// ClientResult is one run of the client binary.
type ClientResult = daemontest.ClientResult

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
	return daemontest.RunBinary(t, bin, stdin, env, args...)
}

// Bin is the daemon binary this daemon was built with (the client binary too).
func (d *Daemon) Bin() string { return d.bin }

// OpenStore opens the daemon's store file read-only. The daemon may be running
// (the store is a WAL database); the handle is closed at the end of the test.
func (d *Daemon) OpenStore(t testing.TB) *sql.DB {
	t.Helper()
	return daemontest.OpenStoreRO(t, d.StorePath())
}

// WritableStore opens the store file read-write with plain SQL, for a test that
// edits it while the daemon is stopped.
func (d *Daemon) WritableStore(t testing.TB) *sql.DB {
	t.Helper()
	return daemontest.OpenStoreRW(t, d.StorePath())
}

// StorePath is the store file's path.
func (d *Daemon) StorePath() string { return filepath.Join(d.StateDir, config.StoreFileName) }

// EventLog returns the rows of events.jsonl.
func (d *Daemon) EventLog(t testing.TB) []map[string]any {
	t.Helper()
	return daemontest.ReadJSONL(t, filepath.Join(d.StateDir, config.EventLogFileName))
}

// DaemonLog returns the lines of daemon.log.jsonl.
func (d *Daemon) DaemonLog(t testing.TB) []map[string]any {
	t.Helper()
	return daemontest.ReadJSONL(t, filepath.Join(d.StateDir, config.DaemonLogFileName))
}

// ClientLog returns the lines of client.log.jsonl.
func (d *Daemon) ClientLog(t testing.TB) []map[string]any {
	t.Helper()
	return daemontest.ReadJSONL(t, filepath.Join(d.StateDir, config.ClientLogFileName))
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

// ---- building the binary ------------------------------------------------------

// binaryImportPath is the import path of the daemon's main package.
const binaryImportPath = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira"

var builder = &daemontest.Builder{ImportPath: binaryImportPath, BinName: "pg-connector-issue-jira", DirPrefix: "pcjsrv"}

// RemoveBuilds deletes the built binaries; a package's TestMain calls it after
// the run.
func RemoveBuilds() { builder.Remove() }

// shortDir returns a fresh directory with a short absolute path (a unix socket
// path is capped near 104 bytes, and t.TempDir can exceed that).
func shortDir(t testing.TB) string {
	t.Helper()
	return daemontest.ShortDir(t, "pcj")
}

// ShortDir is shortDir for a test that needs a scratch directory of its own.
func ShortDir(t testing.TB) string { return shortDir(t) }

// DaemonPath is the PATH the shim's own script (and a client's pjira) gets: see
// daemontest.DaemonPath.
func DaemonPath() string { return daemontest.DaemonPath() }
