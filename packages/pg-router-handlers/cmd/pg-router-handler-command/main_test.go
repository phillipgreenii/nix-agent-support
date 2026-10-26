package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestMain lets the test binary stand in for the adapter: with
// PG_ROUTER_HANDLER_COMMAND_TEST_RUN set it runs the real run(os.Args[1:]) (the
// real subcommand switch, flag parsing and signal handling) instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("PG_ROUTER_HANDLER_COMMAND_TEST_RUN") == "1" {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestRun_SubcommandSet(t *testing.T) {
	if got := run(nil); got != conformance.ExitUsage {
		t.Errorf("no subcommand: exit %d, want %d", got, conformance.ExitUsage)
	}
	for _, ok := range []string{"help", "version"} {
		if got := run([]string{ok}); got != conformance.ExitOK {
			t.Errorf("%s: exit %d, want 0", ok, got)
		}
	}
	// The ccpool handler's own subcommands do not belong to this binary.
	for _, gone := range []string{"origin", "pool-capacity", "split", "query", "register", "self-status", "bogus"} {
		if got := run([]string{gone}); got != conformance.ExitUsage {
			t.Errorf("%s: exit %d, want %d (not a subcommand)", gone, got, conformance.ExitUsage)
		}
	}
	// Requests are JSON on stdin, never arguments.
	if got := run([]string{"validate", "stray"}); got != conformance.ExitUsage {
		t.Errorf("validate with an argument: exit %d, want %d", got, conformance.ExitUsage)
	}
}

// adapter starts the test binary as the adapter with a request on stdin.
func adapter(t *testing.T, args []string, stdin string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "PG_ROUTER_HANDLER_COMMAND_TEST_RUN=1")
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	return cmd, &out
}

// TestProcess_DispatchAndValidateOverTheRealWire runs the adapter as a process:
// a real dispatch whose argv exits 0, the same with exit 9, and a validate.
func TestProcess_DispatchAndValidateOverTheRealWire(t *testing.T) {
	ev := eventWith(map[string]any{"id": "b-1"})
	cases := []struct {
		name, sub string
		req       string
		wantExit  int
	}{
		{"dispatch ok", "dispatch", request(t, ev, "r", cmdConfig(false, "/bin/sh", "-c", ":"), true), conformance.ExitOK},
		{"dispatch busy", "dispatch", request(t, ev, "r", cmdConfig(false, "/bin/sh", "-c", "exit 9"), true), conformance.ExitBusy},
		{"validate", "validate", request(t, nil, "r", cmdConfig(false, "/bin/sh", "-c", ":"), true), conformance.ExitOK},
		{"postStartup", "postStartup", request(t, nil, "r", nil, true), conformance.ExitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, out := adapter(t, []string{c.sub}, c.req)
			err := cmd.Run()
			got := 0
			if ee, ok := err.(*exec.ExitError); ok {
				got = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if got != c.wantExit {
				t.Fatalf("exit = %d, want %d; stdout %s", got, c.wantExit, out)
			}
			if out.Len() == 0 {
				t.Fatal("no reply on stdout")
			}
		})
	}
}

// TestProcess_TermKillsThePayloadGroup: a TERM sent to the adapter cancels the
// dispatch context, so the payload's whole process group goes with it and no
// orphan is left behind.
func TestProcess_TermKillsThePayloadGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	cfg := cmdConfig(false, "/bin/sh", "-c", `sleep 600 & echo $! > "`+pidFile+`"; wait`)
	req := request(t, eventWith(map[string]any{"id": "b-1"}), "r", cfg, true)
	cmd, out := adapter(t, []string{"dispatch"}, req)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := waitPid(t, pidFile)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	select {
	case err := <-waited:
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != conformance.ExitError {
			t.Fatalf("adapter ended with %v, want exit %d; stdout %s", err, conformance.ExitError, out)
		}
	case <-ctx.Done():
		t.Fatal("the adapter did not exit after TERM")
	}
	waitGone(t, pid, "sleep child")
}
