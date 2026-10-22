package wireclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/roles"
)

var _ Validator = (*Client)(nil)

func validateClient(run Runner) *Client {
	return &Client{Runner: run, Command: fixedCommand("h")}
}

// TestValidate_exit0ReplyGivesProblemsAndWarnings: a well-formed reply on exit 0
// is Supported with both lists, and the request is the schema-legal lifecycle
// request with "validate" as the subcommand.
func TestValidate_exit0ReplyGivesProblemsAndWarnings(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","problems":["p1","p2"],"warnings":["w1"]}`)}
	res, err := validateClient(run).Validate(context.Background(), roles.Role{Name: "r"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Supported || len(res.Problems) != 2 || res.Problems[0] != "p1" || len(res.Warnings) != 1 || res.Warnings[0] != "w1" {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.Join(run.gotArgv, " "); got != "h validate" {
		t.Errorf("argv = %q, want %q", got, "h validate")
	}
	var req map[string]any
	if err := json.Unmarshal(run.gotStdin, &req); err != nil {
		t.Fatalf("request not JSON: %v", err)
	}
	if req["schemaVersion"] != "1" || req["id"] == "" || len(req) != 2 {
		t.Errorf("request = %v, want exactly schemaVersion and a non-empty id", req)
	}
}

func TestValidate_emptyProblemsIsValid(t *testing.T) {
	run := &fakeRunner{stdout: []byte(`{"schemaVersion":"1","problems":[],"warnings":[]}`)}
	res, err := validateClient(run).Validate(context.Background(), roles.Role{Name: "r"})
	if err != nil || !res.Supported || len(res.Problems) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestValidate_usageExitIsNotSupported: exit 2 is "no validate subcommand", not
// a failure and not an error.
func TestValidate_usageExitIsNotSupported(t *testing.T) {
	res, err := validateClient(&fakeRunner{exitCode: 2}).Validate(context.Background(), roles.Role{Name: "r"})
	if err != nil {
		t.Fatalf("err = %v, want nil for the usage exit", err)
	}
	if res.Supported || len(res.Problems) != 0 {
		t.Fatalf("res = %+v, want unsupported and empty", res)
	}
}

func TestValidate_errorsAreNotVerdicts(t *testing.T) {
	cases := map[string]*fakeRunner{
		"other exit":        {exitCode: 1},
		"busy exit":         {exitCode: 9},
		"unparsable reply":  {stdout: []byte(`not json`)},
		"empty reply":       {},
		"missing warnings":  {stdout: []byte(`{"schemaVersion":"1","problems":[]}`)},
		"extra key":         {stdout: []byte(`{"schemaVersion":"1","problems":[],"warnings":[],"id":"x"}`)},
		"wrong schema vers": {stdout: []byte(`{"schemaVersion":"2","problems":[],"warnings":[]}`)},
		"runner error":      {err: errors.New("boom")},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validateClient(run).Validate(context.Background(), roles.Role{Name: "r"}); err == nil {
				t.Fatal("err = nil, want an error (validation could not run)")
			}
		})
	}
}

func TestValidate_commandResolution(t *testing.T) {
	if _, err := (&Client{Runner: &fakeRunner{}}).Validate(context.Background(), roles.Role{Name: "r"}); err == nil {
		t.Error("no CommandFor: want an error")
	}
	failing := &Client{Runner: &fakeRunner{}, Command: func(roles.Role, string) ([]string, error) { return nil, errors.New("no handler") }}
	if _, err := failing.Validate(context.Background(), roles.Role{Name: "r"}); err == nil {
		t.Error("CommandFor error: want an error")
	}
	empty := &Client{Runner: &fakeRunner{}, Command: func(roles.Role, string) ([]string, error) { return nil, nil }}
	if _, err := empty.Validate(context.Background(), roles.Role{Name: "r"}); err == nil {
		t.Error("empty argv: want an error")
	}
}

// ctxRunner returns as soon as the context is done, as a killed handler would.
type ctxRunner struct{}

func (ctxRunner) Run(ctx context.Context, _ []string, _ []byte) ([]byte, int, error) {
	<-ctx.Done()
	return nil, -1, nil
}

// TestValidate_deadlineIsAnError: a handler killed on the deadline reports exit -1
// with a nil runner error; Validate must still return the deadline error.
func TestValidate_deadlineIsAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := validateClient(ctxRunner{}).Validate(ctx, roles.Role{Name: "r"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// TestOSRunner_deadlineArrivesWhenAChildHoldsStdout is the "handler that hangs"
// case: the handler's own child keeps its stdout open past the handler's death.
// Without cmd.WaitDelay, Run (and so Validate) would block until that child exits.
func TestOSRunner_deadlineArrivesWhenAChildHoldsStdout(t *testing.T) {
	old := runWaitDelay
	runWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { runWaitDelay = old })

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	stub := filepath.Join(dir, "stub")
	// The child (sleep) inherits the stdout pipe and outlives the shell; no exec,
	// so the shell stays the handler process and the sleep is its child.
	body := "#!/bin/sh\ncat >/dev/null\nsleep 600 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	c := &Client{Runner: OSRunner{}, Command: func(roles.Role, string) ([]string, error) { return []string{stub}, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Validate(ctx, roles.Role{Name: "r"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Validate took %v with a child holding stdout; the deadline must arrive within a short bound", elapsed)
	}
}
