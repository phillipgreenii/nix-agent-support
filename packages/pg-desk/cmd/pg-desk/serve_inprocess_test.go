package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestRunServeReturnsCleanlyOnACancelledContext runs runServe in this process
// against the hermetic schema-2 fixture (config with serve.addr on an
// ephemeral port, store, log file) with a context that is already cancelled:
// it must open everything, notice the cancellation, shut the server down and
// return nil, and it must have written its start-up line to the serve log.
// The daemon as its own SIGTERM-stopped process is the integration-tier
// TestDaemonVerbsRunAndStopCleanlyOnASchema2Store.
func TestRunServeReturnsCleanlyOnACancelledContext(t *testing.T) {
	f := newVerbFixture(t)
	defer f.applyEnv()()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &cobra.Command{Use: "serve"}
	cmd.SetContext(ctx)

	done := make(chan error, 1)
	go func() { done <- runServe(cmd, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe on a cancelled context = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runServe did not return on a cancelled context")
	}
	b, err := os.ReadFile(f.serveLog)
	if err != nil {
		t.Fatalf("read serve log: %v", err)
	}
	if !strings.Contains(string(b), "pg-desk serve listening") {
		t.Errorf("serve log lacks the start-up line:\n%s", b)
	}
	for _, phase := range []string{"http drain started", "http drained", "read pool closed", "store closed", "finished"} {
		if !strings.Contains(string(b), "phase=\""+phase+"\"") && !strings.Contains(string(b), "phase="+phase) {
			t.Errorf("serve log lacks shutdown phase %q:\n%s", phase, b)
		}
	}
	// The run group reports the shutdown to the same log: it started on the
	// cancellation, finished inside the deadline and never hit the watchdog.
	for _, want := range []string{"shutdown_signal", "shutdown_complete"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("serve log lacks the %s lifecycle event:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "shutdown_overrun") {
		t.Errorf("serve log reports a shutdown overrun on a clean stop:\n%s", b)
	}
}

// TestServeShutdownDeadlineFlag pins the flag the nix module passes and derives
// launchd's ExitTimeOut from: its name, and a default equal to the lifecycle
// default (the module's own default must match it).
func TestServeShutdownDeadlineFlag(t *testing.T) {
	f := serveCmd.Flags().Lookup("shutdown-deadline")
	if f == nil {
		t.Fatal("serve has no --shutdown-deadline flag")
	}
	if f.DefValue != "8s" {
		t.Errorf("--shutdown-deadline default = %q, want 8s (lifecycle.DefaultDeadline, mirrored by the nix module)", f.DefValue)
	}
}

// TestRunServeRefusesAddrAndPortTogether covers the flag conflict before
// anything is opened.
func TestRunServeRefusesAddrAndPortTogether(t *testing.T) {
	oldA, oldP := serveAddr, servePort
	serveAddr, servePort = "127.0.0.1:1", "2"
	defer func() { serveAddr, servePort = oldA, oldP }()
	cmd := &cobra.Command{Use: "serve"}
	cmd.SetContext(context.Background())
	if err := runServe(cmd, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want the mutually exclusive refusal", err)
	}
}
