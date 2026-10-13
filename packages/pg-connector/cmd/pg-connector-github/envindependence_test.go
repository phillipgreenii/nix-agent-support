package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestEnvIndependenceServeUnderEnvDashI is the "Environment independence" row of
// the design's Testing section: `pg-connector-github --config <path> serve`
// started under `env -i` with working directory "/" starts, answers a client,
// and calls the configured gh with the configured host. It is the platform gate
// that proves the daemon behaves exactly as under launchd, where nothing of the
// operator's shell environment is inherited: everything it needs (the GitHub
// host, the gh path, the state directory, the socket path) comes from the
// rendered config file.
//
// The process is started through the real `env -i` (not a Go cmd.Env that merely
// looks empty), so the test also fails if the daemon needs a variable `env -i`
// removes. The fake gh's own FAKE_GH_DIR is the one thing a test fixture needs
// that the daemon itself does not: the configured gh is a one-line wrapper that
// names it, logs that it was exec'd, and hands over to the shim.
func TestEnvIndependenceServeUnderEnvDashI(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("no env(1) on PATH: %v", err)
	}
	bin := servetest.Binary(t, "", "")

	// NewDaemon prepares the state directory, the shim and the rendered config
	// without starting the process; this test starts it itself, under env -i.
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		d.Shim.SetPRs(servetest.MineExpr, envPRA, envPRB)
		d.Shim.SetPRs(servetest.TeamExpr, envPRA)
	}})
	execLog := filepath.Join(d.Shim.Dir, "wrapper-exec.log")
	wrapper := filepath.Join(d.Shim.Dir, "gh-wrapper")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$0\" >>'" + execLog + "'\n" +
		"FAKE_GH_DIR='" + d.Shim.Dir + "' exec '" + d.Shim.Path + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// The configured gh is the wrapper: the daemon must call exactly this path.
	d.Rewrite(map[string]any{"gh_path": wrapper})

	cmd := exec.Command(envBin, "-i", bin, "--config", d.ConfigPath, "serve")
	cmd.Dir = "/"
	cmd.Env = []string{} // env itself starts empty too; nothing leaks in either way
	var out lockedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon under env -i: %v", err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	// It starts and answers a client: poll until the gate is open.
	listArgs := json.RawMessage(`{"query":"` + servetest.QueryMine + `"}`)
	var resp scriptout.Response
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-exited:
			t.Fatalf("the daemon exited before it answered (%v); output: %s", waitErr, out.String())
		default:
		}
		resp, err = d.Call("pr", scriptout.Request{Op: scriptout.OpList, Args: listArgs}, 20*time.Second)
		if err == nil && resp.Error == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never answered a list request (last: %v, %+v); output: %s", err, resp.Error, out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	var res struct {
		PresentIDs []string `json:"present_ids"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode the list result %s: %v", resp.Result, err)
	}
	want := []string{envPRA.Ref(), envPRB.Ref()}
	if !slices.Equal(res.PresentIDs, want) {
		t.Errorf("present_ids = %v, want %v (the configured query was answered through the configured gh)", res.PresentIDs, want)
	}

	// The configured gh path was exec'd, and every call it received carried the
	// configured host.
	logged, err := os.ReadFile(execLog)
	if err != nil {
		t.Fatalf("the configured gh was never exec'd (no %s): %v; output: %s", execLog, err, out.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if line != wrapper {
			t.Errorf("gh exec'd as %q, want the configured path %q", line, wrapper)
		}
	}
	calls := d.Shim.OriginCalls()
	if len(calls) == 0 {
		t.Fatalf("the shim recorded no call that reached GitHub; output: %s", out.String())
	}
	for _, c := range d.Shim.Calls() {
		if c.Host != servetest.Host {
			t.Errorf("a gh call carried GH_HOST=%q, want the configured %q: %v", c.Host, servetest.Host, c.Args)
		}
	}

	// And it stops the way a supervisor stops it.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-exited:
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			t.Errorf("SIGTERM exit = %d, want 0; output: %s", ee.ExitCode(), out.String())
		} else if waitErr != nil {
			t.Errorf("wait: %v", waitErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the daemon did not exit within 30s of SIGTERM; output: %s", out.String())
	}
}

// lockedBuf is a strings.Builder safe to read while the child process writes.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var (
	envPRA = servetest.PR{NodeID: "PR_env_a", Repo: "example-org/widget", Number: 3}
	envPRB = servetest.PR{NodeID: "PR_env_b", Repo: "example-org/gadget", Number: 7}
)
