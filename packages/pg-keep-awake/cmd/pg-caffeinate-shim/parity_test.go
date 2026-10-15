package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The parity test runs the shim and the real /usr/bin/caffeinate over every
// row of the shim table and compares exit status, death signal and stderr.
// The real tool's results are recorded in testdata/parity-golden.json; the
// shim is compared against both the recording and a live run of the real tool.
// Regenerate the recording (on a Mac, after reading the diff) with
//
//	UPDATE_PARITY_GOLDEN=1 go test ./cmd/pg-caffeinate-shim -run TestParityWithRealCaffeinate
//
// The one row not covered is -u: the shim deliberately rejects it while the
// real tool accepts it (see TestDashURejectedWithMessageAndExit1).

const (
	realCaffeinate = "/usr/bin/caffeinate"
	goldenPath     = "testdata/parity-golden.json"
	goldenEnv      = "UPDATE_PARITY_GOLDEN"
)

// outcome is what a caffeinate run looked like to its caller.
type outcome struct {
	Exit   int    `json:"exit"`   // exit status; -1 when killed by a signal
	Signal int    `json:"signal"` // terminating signal, 0 when it exited
	Stderr string `json:"stderr"`
}

func (o outcome) String() string {
	return fmt.Sprintf("exit %d signal %d stderr %q", o.Exit, o.Signal, o.Stderr)
}

// row is one line of the shim table.
type row struct {
	name string
	args []string // "{PID}" is replaced by a helper process's pid
	// helper is how many seconds the pid helper sleeps ("" for none).
	helper string
	// forever rows hold until signalled: the test checks they still run after
	// about a second, signals only the children it started, and records how
	// they ended.
	forever bool
	sig     syscall.Signal
}

var parityRows = []row{
	// Usage errors and no-lease outcomes: exit at once.
	{name: "unknown flag", args: []string{"-z"}},
	{name: "unknown flag after valid flag", args: []string{"-d", "-z"}},
	{name: "-t abc", args: []string{"-t", "abc"}},
	{name: "-w abc", args: []string{"-w", "abc"}},
	{name: "-t without value", args: []string{"-t"}},
	{name: "-w without value", args: []string{"-w"}},
	{name: "-dt without value", args: []string{"-dt"}},
	{name: "-t negative", args: []string{"-t", "-5"}},
	{name: "-t overflow", args: []string{"-t", "99999999999999999999"}},
	{name: "-t LONG_MAX", args: []string{"-t", "9223372036854775807"}},
	{name: "-w -1", args: []string{"-w", "-1"}},
	{name: "-w nonexistent pid", args: []string{"-w", "99999999"}},
	{name: "-t with -w nonexistent", args: []string{"-t", "30", "-w", "99999999"}},

	// Timed rows with -t 1-class timings.
	{name: "-t 1", args: []string{"-t", "1"}},
	{name: "-i -t 1", args: []string{"-i", "-t", "1"}},
	{name: "-dims -t 1 combined flags", args: []string{"-dimst", "1"}},
	{name: "attached -t1", args: []string{"-t1"}},
	{name: "attached -it1", args: []string{"-it1"}},
	{name: "numeric prefix -t 1x", args: []string{"-t", "1x"}},
	{name: "numeric prefix -t 1.5", args: []string{"-t", "1.5"}},
	{name: "numeric prefix -t 0x1", args: []string{"-t", "0x1"}},
	{name: "repeated -t last wins", args: []string{"-t", "30", "-t", "1"}},

	// Watch rows: the helper is a short-lived process this test spawned.
	{name: "-w PID", args: []string{"-w", "{PID}"}, helper: "2"},
	{name: "attached -wPID", args: []string{"-w{PID}"}, helper: "2"},
	{name: "-t and -w, watch first", args: []string{"-t", "30", "-w", "{PID}"}, helper: "2"},
	{name: "-t and -w, timer first", args: []string{"-t", "1", "-w", "{PID}"}, helper: "60"},
	{name: "repeated -w last wins", args: []string{"-w", "99999999", "-w", "{PID}"}, helper: "2"},

	// Utility form.
	{name: "utility exit 0", args: []string{"true"}},
	{name: "utility exit 7", args: []string{"sh", "-c", "exit 7"}},
	{name: "utility with flags", args: []string{"-dims", "sh", "-c", "exit 3"}},
	{name: "utility after --", args: []string{"--", "sh", "-c", "exit 4"}},
	{name: "utility options are not parsed", args: []string{"-i", "sh", "-c", "exit 2"}},
	{name: "utility ignores -t", args: []string{"-t", "30", "sh", "-c", "exit 6"}},
	{name: "utility ignores dead -w", args: []string{"-w", "99999999", "sh", "-c", "exit 8"}},
	{name: "utility killed by TERM", args: []string{"sh", "-c", "kill -TERM $$; sleep 5"}},
	{name: "missing utility", args: []string{"pg-no-such-command-zz"}},

	// Holds forever: bounded by the test signalling its own children.
	{name: "-t 0 / TERM", args: []string{"-t", "0"}, forever: true, sig: syscall.SIGTERM},
	{name: "-w 0 / TERM", args: []string{"-w", "0"}, forever: true, sig: syscall.SIGTERM},
	{name: "no arguments / TERM", args: nil, forever: true, sig: syscall.SIGTERM},
	{name: "no arguments / HUP", args: nil, forever: true, sig: syscall.SIGHUP},
	{name: "no arguments / INT", args: nil, forever: true, sig: syscall.SIGINT},
	{name: "-- / TERM", args: []string{"--"}, forever: true, sig: syscall.SIGTERM},
	{name: "-dims / TERM", args: []string{"-dims"}, forever: true, sig: syscall.SIGTERM},
}

// requireRealCaffeinate skips the parity test unless the real tool is present
// and this machine allows power assertions.
func requireRealCaffeinate(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("parity needs macOS")
	}
	if _, err := os.Stat(realCaffeinate); err != nil {
		t.Skipf("%s not present", realCaffeinate)
	}
	if err := exec.Command("pmset", "-g", "assertions").Run(); err != nil {
		t.Skipf("pmset -g assertions failed (%v): this sandbox does not allow assertions", err)
	}
}

func TestParityWithRealCaffeinate(t *testing.T) {
	requireRealCaffeinate(t)

	update := os.Getenv(goldenEnv) == "1"
	golden := map[string]outcome{}
	if !update {
		data, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("golden file %s is missing while %s is present (%v); record it with %s=1", goldenPath, realCaffeinate, err, goldenEnv)
		}
		if err := json.Unmarshal(data, &golden); err != nil {
			t.Fatalf("parsing %s: %v", goldenPath, err)
		}
	}

	var mu sync.Mutex
	recorded := map[string]outcome{}
	t.Run("rows", func(t *testing.T) {
		for _, r := range parityRows {
			t.Run(r.name, func(t *testing.T) {
				t.Parallel()
				real := runRow(t, r, func(args []string, env []string) *exec.Cmd {
					cmd := exec.Command(realCaffeinate, args...)
					cmd.Args[0] = "caffeinate"
					return cmd
				}, nil)
				mu.Lock()
				recorded[r.name] = real
				mu.Unlock()
				if update {
					return
				}
				want, ok := golden[r.name]
				if !ok {
					t.Fatalf("row %q is not in %s; re-record with %s=1", r.name, goldenPath, goldenEnv)
				}
				if real != want {
					t.Errorf("the real tool changed since the golden file was recorded:\n live   %v\n golden %v", real, want)
				}
				xdg := t.TempDir()
				got := runRow(t, r, func(args []string, env []string) *exec.Cmd {
					cmd := exec.Command(shimBin, args...)
					cmd.Env = env
					return cmd
				}, shimEnv(xdg))
				if got != want {
					t.Errorf("shim differs from the real tool for %q:\n shim %v\n real %v", strings.Join(r.args, " "), got, want)
				}
				noLeases(t, xdg)
			})
		}
	})

	if update {
		names := make([]string, 0, len(recorded))
		for n := range recorded {
			names = append(names, n)
		}
		sort.Strings(names)
		data, err := json.MarshalIndent(recorded, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d rows to %s", len(names), goldenPath)
	}
}

// runRow runs one table row against the command built by mk and describes how
// it ended. mk receives the args (helper pid substituted) and the environment.
func runRow(t *testing.T, r row, mk func(args, env []string) *exec.Cmd, env []string) outcome {
	t.Helper()
	args := make([]string, len(r.args))
	copy(args, r.args)
	if r.helper != "" {
		h := start(t, exec.Command("sleep", r.helper))
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "{PID}", fmt.Sprint(h.cmd.Process.Pid))
		}
	}
	cmd := mk(args, env)
	if r.forever {
		return runForever(t, r, cmd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd = withContext(ctx, cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%v did not finish within 20s", cmd.Args)
	}
	return describeEnd(t, cmd, err, stderr.String())
}

// withContext rebuilds cmd so that it dies with ctx.
func withContext(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Args[0] = cmd.Args[0]
	c.Env = cmd.Env
	return c
}

// runForever starts a row that holds forever, confirms it is still running
// after about a second, signals only that child and records how it ended.
func runForever(t *testing.T, r row, cmd *exec.Cmd) outcome {
	t.Helper()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
		}
	})
	select {
	case err := <-done:
		t.Fatalf("%v exited on its own (%v, stderr %q); it should hold forever", cmd.Args, err, stderr.String())
	case <-time.After(1200 * time.Millisecond):
	}
	if err := cmd.Process.Signal(r.sig); err != nil {
		t.Fatalf("signal %v: %v", r.sig, err)
	}
	select {
	case err := <-done:
		return describeEnd(t, cmd, err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("%v did not end after %v", cmd.Args, r.sig)
		return outcome{}
	}
}

// describeEnd turns a finished command into an outcome.
func describeEnd(t *testing.T, cmd *exec.Cmd, err error, stderr string) outcome {
	t.Helper()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("%v: %v", cmd.Args, err)
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("no wait status for %v", cmd.Args)
	}
	if ws.Signaled() {
		return outcome{Exit: -1, Signal: int(ws.Signal()), Stderr: stderr}
	}
	return outcome{Exit: ws.ExitStatus(), Stderr: stderr}
}
