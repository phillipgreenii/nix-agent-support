// Package servetest is the test support for the assembled pg-connector-github
// daemon: it builds the REAL binary, starts it as a child process on a short
// temp socket path with an empty-but-explicit environment, points it at a
// counting fake gh (internal/serve/testdata/fake-gh.sh) and a temp state
// directory, and gives a test the handles it needs: the state directory, the
// socket, the rendered config, the shim's call log, a socket client and
// readers for the store and the logs.
//
// The process lifecycle, the socket client, the binary build and the readers are
// the connector daemon kernel's shared harness (pkg/connectord/daemon/daemontest);
// this package adds what is this daemon's own: the fixtures, the rendered config
// and the fake gh.
//
// It is reused by the assembly's own tests (internal/serve), the wire
// conformance tests and the platform gates, so the daemon is always started the
// same way: StartDaemon, work, StopDaemon (SIGTERM, then the exit code).
//
// Everything in it is generic: fixture owners, repositories and logins are
// invented placeholders, and the token a test supplies is a made-up string.
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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon/daemontest"
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

// ParentWatchHook is the serve hook every harness-built binary carries: the
// daemon exits when the process that started it is gone.
const ParentWatchHook = "parent_watch=250ms"

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
// outlive a stop, so Start after StopDaemon is a restart. The process handle
// (Stderr, Exited, Wait, Term, Stop) is the kernel harness's daemontest.Proc.
type Daemon struct {
	daemontest.Proc

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
}

// Binary returns the path of the daemon binary built for hooks (and
// clientBudget), building it on first use.
func Binary(t testing.TB, hooks, clientBudget string) string {
	t.Helper()
	var flags []string
	// Every daemon the harness starts drains and exits when its parent (the test
	// process) ends, however it ends: t.Cleanup does not run when the test
	// binary is killed (a timeout, SIGKILL), and an orphaned daemon would then
	// live on for days (pg2-qupkv0).
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
		// Both queries are watched, as a deployment declares the queries a consumer
		// follows: the registration emits change rows for watched PRs only, so a test
		// of the rows needs its PRs watched. A test of an unwatched PR overrides "pr"
		// through Options.Config with the plain string form.
		"pr": map[string]any{"queries": map[string]any{
			QueryMine: map[string]any{"expr": MineExpr, "watched": true},
			QueryTeam: map[string]any{"expr": TeamExpr, "watched": true},
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
	cmd := exec.CommandContext(context.Background(), d.bin, "--config", d.ConfigPath, "serve")
	cmd.Dir = "/"
	cmd.Env = []string{
		"PATH=" + DaemonPath(),
		"FAKE_GH_DIR=" + d.Shim.Dir,
	}
	for k, v := range d.opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	d.Proc.Start(t, cmd)
	if !d.opts.NoWait {
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

// WaitReady waits until the daemon answers an auth_status request (its gate is
// open), failing if the process ends first.
func (d *Daemon) WaitReady(t *testing.T) {
	t.Helper()
	d.WaitUntilReady(t, func() bool {
		resp, err := d.Call("pr", scriptout.Request{Op: scriptout.OpAuthStatus}, 5*time.Second)
		return err == nil && resp.Error == nil
	})
}

// Call sends one request for an instance with the client's deadline and returns
// the daemon's answer.
func (d *Daemon) Call(instance string, req scriptout.Request, deadline time.Duration) (scriptout.Response, error) {
	return daemontest.Call(d.Socket, instance, socket.ProtocolVersion, req, deadline)
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
	return daemontest.Raw(d.Socket, frame)
}

// OpenStore opens the daemon's store file read-only. The daemon may be running
// (the store is a WAL database); the handle is closed at the end of the test.
func (d *Daemon) OpenStore(t *testing.T) *sql.DB {
	t.Helper()
	return daemontest.OpenStoreRO(t, d.StorePath())
}

// StorePath is the store file's path.
func (d *Daemon) StorePath() string { return filepath.Join(d.StateDir, "store.sqlite") }

// EventLog returns the rows of events.jsonl.
func (d *Daemon) EventLog(t *testing.T) []map[string]any {
	t.Helper()
	return daemontest.ReadJSONL(t, filepath.Join(d.StateDir, "events.jsonl"))
}

// DaemonLog returns the lines of daemon.log.jsonl.
func (d *Daemon) DaemonLog(t *testing.T) []map[string]any {
	t.Helper()
	return daemontest.ReadJSONL(t, filepath.Join(d.StateDir, "daemon.log.jsonl"))
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
const binaryImportPath = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github"

var builder = &daemontest.Builder{ImportPath: binaryImportPath, BinName: "pg-connector-github", DirPrefix: "pcgsrv"}

// RemoveBuilds deletes the built binaries; a package's TestMain calls it after
// the run.
func RemoveBuilds() { builder.Remove() }

// shimSource is the fake gh script's path.
func shimSource(t testing.TB) string {
	t.Helper()
	return filepath.Join(daemontest.ModuleRoot(t), "cmd", "pg-connector-github", "internal", "serve", "testdata", "fake-gh.sh")
}

// shortDir returns a fresh directory with a short absolute path (a unix socket
// path is capped near 104 bytes, and t.TempDir can exceed that).
func shortDir(t testing.TB) string {
	t.Helper()
	return daemontest.ShortDir(t, "pcg")
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
	return daemontest.OpenStoreRW(t, d.StorePath())
}

// DaemonPath is the PATH of a daemon (and of the fake gh it runs): see
// daemontest.DaemonPath.
func DaemonPath() string { return daemontest.DaemonPath() }
