package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"

	"github.com/phillipgreenii/pg-router/internal/handlers/beads"
	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/dispatchlib"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/originguard"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// buildDepsUnderTest is the deps builder dispatchWith hands serveDispatch: the
// production dispatchlib.BuildDeps, unless a test installed a process fake with
// useRunner.
var buildDepsUnderTest = dispatchlib.BuildDeps

// useRunner makes every ccpool and bd call the dispatch wiring makes go through
// run instead of a process (the production CLIRunners, with their Exec seam
// replaced), for the rest of the test.
func useRunner(t *testing.T, run cmdrun.Runner) {
	t.Helper()
	prev := buildDepsUnderTest
	buildDepsUnderTest = func(cfg config.Config, role roles.Role) executor.Deps {
		d := dispatchlib.BuildDeps(cfg, role)
		d.CC.(*ccpool.CLIRunner).Exec = run
		d.BD.(*beads.CLIRunner).Exec = run
		return d
	}
	t.Cleanup(func() { buildDepsUnderTest = prev })
}

// bdAbsent answers every bd call as a bd that is not installed, the state the
// stub-on-PATH tests ran in when they put no bd on PATH.
func bdAbsent(f *cmdruntest.Fake) {
	f.On(cmdruntest.MatchName("bd")).Return(cmdrun.Result{Code: -1, StartErr: exec.ErrNotFound})
}

// ccpoolNewFailsFake answers like a ccpool whose `new` fails before ready: an
// empty pool with room, `close` succeeds, any other subcommand is unavailable.
// The fake records every call.
func ccpoolNewFailsFake(t *testing.T) *cmdruntest.Fake {
	t.Helper()
	f := cmdruntest.New(t)
	sub := func(s string) func(cmdrun.Call) bool {
		return func(c cmdrun.Call) bool { return c.Name == "ccpool" && len(c.Args) > 0 && c.Args[0] == s }
	}
	f.On(sub("list")).Return(cmdruntest.Out("[]\n"))
	f.On(sub("capacity")).Return(cmdruntest.Out(`{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}` + "\n"))
	f.On(sub("new")).Return(cmdruntest.Fail(1, "did not reach ready (state=starting) Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF\n"))
	f.On(sub("close")).Return(cmdruntest.Out(""))
	f.On(cmdruntest.MatchName("ccpool")).Return(cmdruntest.Fail(1, "stub ccpool unavailable\n"))
	bdAbsent(f)
	useRunner(t, f.Runner())
	return f
}

// callArgsOf returns the argument list of the first recorded call to tool whose
// first argument is sub.
func callArgsOf(t *testing.T, f *cmdruntest.Fake, tool, sub string) []string {
	t.Helper()
	for _, c := range f.Calls() {
		if c.Name == tool && len(c.Args) > 0 && c.Args[0] == sub {
			return c.Args
		}
	}
	t.Fatalf("no `%s %s` call recorded: %v", tool, sub, f.Calls())
	return nil
}

// dispatchWith runs serveDispatch (real dispatchlib.BuildDeps: a real ccpool
// CLIRunner and bd CLIRunner shelling out to whatever is on PATH) for the role
// and returns the exit code and the reply.
func dispatchWith(t *testing.T, name string, cfg map[string]any, cfgPath, event string) (int, string) {
	t.Helper()
	return dispatchCtx(context.Background(), t, name, cfg, cfgPath, event)
}

// testWiring is the production wiring with no origin guard: no test depends on
// a pg-origin-guard installed on the machine running it. No test depends on
// lsof either: the closed-bead teardown sees no live process.
func testWiring() wiring {
	w := newWiring()
	w.sweeper.DisableTeardownProbe()
	w.newGuardCheck = func(config.Config) originguard.Check { return nil }
	return w
}

func dispatchCtx(ctx context.Context, t *testing.T, name string, cfg map[string]any, cfgPath, event string) (int, string) {
	t.Helper()
	return dispatchCtxWith(testWiring(), ctx, t, name, cfg, cfgPath, event)
}

func dispatchCtxWith(w wiring, ctx context.Context, t *testing.T, name string, cfg map[string]any, cfgPath, event string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := w.serveDispatch(ctx, strings.NewReader(dtest.Request(event, name, cfg)), &out, cfgPath, buildDepsUnderTest)
	return code, out.String()
}

// stubBin writes executable shell scripts (name -> body after the shebang) into
// a new directory and returns it.
func stubBin(t *testing.T, scripts map[string]string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func decodeReply(t *testing.T, reply string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(reply), &m); err != nil {
		t.Fatalf("reply %q is not a JSON object: %v", reply, err)
	}
	return m
}

// mustCfg decodes a JSON role config.
func mustCfg(body string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		panic(err)
	}
	return m
}

// redirectStdin temporarily replaces os.Stdin with a pipe pre-loaded with body
// and already closed for writing (so a reader never blocks), returning a restore
// func.
func redirectStdin(t *testing.T, body string) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(body); err != nil {
		t.Fatalf("write stdin pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin pipe writer: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = old
		_ = r.Close()
	}
}

// captureStdout temporarily replaces os.Stdout with a pipe, runs fn, restores
// os.Stdout, and returns everything fn wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	return <-done
}
