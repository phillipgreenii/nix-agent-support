package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
)

// TestEnvIndependenceServeUnderEnvDashI is the environment independence row of
// the design's testing section for the Jira daemon: `pg-connector-issue-jira
// --config <path> serve` started under `env -i` with working directory "/"
// starts, answers a client, and calls the CONFIGURED ABSOLUTE pjira path with
// the configured pjira_env only. It is the platform gate that proves the daemon
// behaves exactly as under launchd, which gives it no PATH and none of the
// operator's shell environment: everything it needs (the pjira path and its
// environment, the state directory, the socket path) comes from the rendered
// config file.
//
// The process is started through the real `env -i` (not a Go cmd.Env that
// merely looks empty), so the test also fails if the daemon needs a variable
// `env -i` removes. The mutant that resolves pjira through PATH (the daemon
// execs "pjira" by name) fails here: under `env -i` there is no PATH to find it
// in, so no call reaches the shim and the list never answers.
func TestEnvIndependenceServeUnderEnvDashI(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("no env(1) on PATH: %v", err)
	}
	bin := servetest.Binary(t, "", "")

	// NewDaemon prepares the state directory, the shim and the rendered config
	// without starting the process; this test starts it itself, under env -i.
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		d.Shim.SetIssues(servetest.MineJQL, envIssueA, envIssueB)
		d.Shim.SetIssues(servetest.TeamJQL, envIssueA)
		d.Shim.RecordEnv()
	}})

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

	// It starts and answers a client: poll until the gate is open and the query
	// has been answered through the configured pjira.
	listReq := servetest.Op("list", `{"query":"`+servetest.QueryMine+`"}`)
	var res struct {
		PresentIDs []string `json:"present_ids"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-exited:
			t.Fatalf("the daemon exited before it answered (%v); output: %s", waitErr, out.String())
		default:
		}
		resp, err := d.Call(listReq, 20*time.Second)
		if err == nil && resp.Error == nil {
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				t.Fatalf("decode the list result %s: %v", resp.Result, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never answered a list request (last: %v, %+v); output: %s", err, resp.Error, out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	want := []string{"EXA-1", "EXA-2"}
	got := slices.Clone(res.PresentIDs)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("present_ids = %v, want %v (the configured query was answered through the configured pjira)", got, want)
	}

	// The configured pjira path was exec'd on every call.
	argv0 := d.Shim.Argv0()
	if len(argv0) == 0 {
		t.Fatalf("the configured pjira was never exec'd; output: %s", out.String())
	}
	for _, a := range argv0 {
		if a != d.Shim.Path {
			t.Errorf("pjira exec'd as %q, want the configured absolute path %q", a, d.Shim.Path)
		}
	}
	// And every call got the configured pjira_env and nothing else: the shell
	// that runs the shim adds only PWD, SHLVL, OLDPWD and _.
	envs := d.Shim.Env()
	if len(envs) != len(argv0) {
		t.Errorf("recorded %d environments for %d calls", len(envs), len(argv0))
	}
	shellAdded := map[string]bool{"PWD": true, "SHLVL": true, "OLDPWD": true, "_": true}
	for i, env := range envs {
		names := map[string]string{}
		for _, kv := range env {
			name, val, _ := strings.Cut(kv, "=")
			if shellAdded[name] {
				continue
			}
			names[name] = val
		}
		wantEnv := map[string]string{"FAKE_PJIRA_DIR": d.Shim.Dir, "PATH": servetest.DaemonPath()}
		if len(names) != len(wantEnv) || names["FAKE_PJIRA_DIR"] != wantEnv["FAKE_PJIRA_DIR"] || names["PATH"] != wantEnv["PATH"] {
			t.Errorf("call %d ran under %v, want exactly the configured pjira_env %v", i, names, wantEnv)
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
	envIssueA = servetest.Open("EXA-1")
	envIssueB = servetest.Open("EXA-2")
)
