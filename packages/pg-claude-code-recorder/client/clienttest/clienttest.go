// Package clienttest is the reusable in-process test server of the recorder's
// Go client: it starts a real daemon (a real store, copier, parser, feed and
// socket listener) over throwaway directories, driven by hand instead of by
// file-system events, so a package that moves onto the client can test against
// a real client without a started daemon process.
package clienttest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
)

// Env is a started in-process recorder over a throwaway Claude directory.
type Env struct {
	t *testing.T
	// ClaudeDir is the fake Claude directory (projects/ and sessions/ exist).
	ClaudeDir string
	// StateDir is the recorder's state directory.
	StateDir string
	// Socket is the daemon's socket (also in the environment of Client).
	Socket string
	// Project is the project directory name Write uses.
	Project string
	// Clock is the daemon's clock (the real one unless set before Start).
	Daemon *serve.Daemon
	// Procs is the process table of the daemon (the ProcessTable seam): add live
	// processes and fire exits by hand.
	Procs *proc.Fake
	// ControlDir is the control mod's state directory ("" unless Options.Control).
	ControlDir string
	cfg        config.Config
	clk        clock.Clock
	events     bool
	real       bool
}

// Options tune Start.
type Options struct {
	// Clock replaces the daemon's clock (a *clock.Fake is not importable
	// outside the module; leave nil for the real clock).
	Clock clock.Clock
	// Events turns the real file-system watcher on (an integration test of the
	// write-to-feed latency); the default is a hand-driven sweep (Settle).
	Events bool
	// Mutate edits the config before the daemon opens.
	Mutate func(*config.Config)
	// Control configures a control state directory (inbox/ and ends/ exist).
	Control bool
	// RealProcs gives the daemon the operating system's process table (a
	// kqueue exit watch on darwin) instead of the fake: for an integration test
	// against a real child process.
	RealProcs bool
}

// Start starts the daemon and registers its cleanup with t.
func Start(t *testing.T, opts ...Options) *Env {
	t.Helper()
	// Unix socket paths are short on darwin: keep the directory under /tmp.
	tmp, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	root, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{
		t: t, ClaudeDir: filepath.Join(root, "claude"), StateDir: filepath.Join(root, "state"),
		Socket: filepath.Join(root, "r.sock"), Project: "example-proj",
	}
	for _, d := range []string{filepath.Join(e.ClaudeDir, "projects", e.Project), filepath.Join(e.ClaudeDir, "sessions")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.Procs = proc.NewFake()
	e.cfg = config.Defaults(func(string) string { return "" })
	e.cfg.ClaudeDir, e.cfg.StateDir, e.cfg.Socket, e.cfg.CopyThrottle = e.ClaudeDir, e.StateDir, e.Socket, 0
	e.cfg.MaxFilesPerSecond = 0
	if len(opts) > 0 {
		e.clk, e.events, e.real = opts[0].Clock, opts[0].Events, opts[0].RealProcs
		if opts[0].Control {
			e.ControlDir = filepath.Join(root, "control")
			e.cfg.ControlStateDir = e.ControlDir
			for _, d := range []string{"ends", "inbox"} {
				if err := os.MkdirAll(filepath.Join(e.ControlDir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		if opts[0].Mutate != nil {
			opts[0].Mutate(&e.cfg)
		}
	}
	e.open()
	t.Cleanup(func() { _ = e.Daemon.Close() })
	return e
}

func (e *Env) open() {
	e.t.Helper()
	so := serve.Options{Config: e.cfg, NoEvents: !e.events, Version: "test", Clock: e.clk}
	if !e.real {
		so.Procs = e.Procs
	}
	d, err := serve.Open(so)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := d.Start(); err != nil {
		_ = d.Close()
		e.t.Fatal(err)
	}
	e.Daemon = d
}

// Restart stops the daemon and starts it again on the same state (a daemon
// restart: open connections close).
func (e *Env) Restart() {
	e.t.Helper()
	if err := e.Daemon.Close(); err != nil {
		e.t.Fatal(err)
	}
	e.open()
}

// Stop stops the daemon and leaves it stopped (the socket stops answering).
func (e *Env) Stop() {
	e.t.Helper()
	if err := e.Daemon.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// Path joins elements under the Claude directory.
func (e *Env) Path(elem ...string) string {
	return filepath.Join(append([]string{e.ClaudeDir}, elem...)...)
}

// Write writes (replaces) the file at rel under the Claude directory.
func (e *Env) Write(rel, content string) string {
	e.t.Helper()
	p := e.Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// Append appends to the file at rel.
func (e *Env) Append(rel, content string) {
	e.t.Helper()
	f, err := os.OpenFile(e.Path(rel), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
	_ = f.Close()
}

// Settle sweeps the Claude directory and parses everything, so what was written
// is in the feed when it returns.
func (e *Env) Settle() {
	e.t.Helper()
	if err := e.Daemon.Watcher().Rescan(); err != nil {
		e.t.Fatal(err)
	}
	if err := e.Daemon.Parser().Drain(); err != nil {
		e.t.Fatal(err)
	}
}

// Dial returns a client of the daemon.
func (e *Env) Dial(ctx context.Context) *client.Client {
	e.t.Helper()
	c, err := client.Dial(ctx, client.Options{Socket: e.Socket, Name: "cli", ConnectTimeout: time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}
