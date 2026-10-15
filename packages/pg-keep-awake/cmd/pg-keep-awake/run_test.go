package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func TestRunPassesExitStatusThrough(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"run", "--owner", "t", "--", "true"}, 0},
		{"exit 7", []string{"run", "--owner", "t", "--", "sh", "-c", "exit 7"}, 7},
		{"not found", []string{"run", "--owner", "t", "--", "no-such-cmd-pg-keep-awake"}, 127},
		{"killed by SIGKILL", []string{"run", "--owner", "t", "--", "sh", "-c", "kill -9 $$"}, 128 + 9},
		{"killed by SIGTERM", []string{"run", "--owner", "t", "--", "sh", "-c", "kill -TERM $$"}, 128 + 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, stderr := runCLI(t, tc.args...); code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.want, stderr)
			}
			if entries, _ := filepath.Glob(filepath.Join(sd, "leases", "*.json")); len(entries) != 0 {
				t.Errorf("lease files left behind: %v", entries)
			}
		})
	}
}

func TestRunNonExecutableIs126(t *testing.T) {
	requireProcInfo(t)
	useStateDir(t)
	p := filepath.Join(t.TempDir(), "not-exec")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "run", "--owner", "t", "--", p); code != 126 {
		t.Fatalf("exit = %d, want 126 (stderr %q)", code, stderr)
	}
}

func TestRunHoldsLeaseForTheCommandsLife(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	path := leasePath(sd, "t.mykey")
	code, _, stderr := runCLI(t, "run", "--owner", "t", "--key", "mykey", "-s", "--", "sh", "-c", fmt.Sprintf("test -f %q", path))
	if code != 0 {
		t.Fatalf("the lease did not exist while the command ran: exit %d, stderr %q", code, stderr)
	}
	if fileExists(path) {
		t.Error("lease left after the command exited")
	}
}

func TestRunDefaultKeyIsPidAndStartOfRunProcess(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	self, err := lease.Ref(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("t.%d-%d", self.PID, self.StartUS)
	snap := filepath.Join(t.TempDir(), "snapshot.json")
	script := fmt.Sprintf("cp %q %q", leasePath(sd, id), snap)
	if code, _, stderr := runCLI(t, "run", "--owner", "t", "-d", "--", "sh", "-c", script); code != 0 {
		t.Fatalf("exit %d (the default-key lease %s did not exist): %s", code, id, stderr)
	}
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind": "run"`, `"owner": "t"`, fmt.Sprintf(`"pid": %d`, self.PID), `"d"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lease snapshot lacks %s:\n%s", want, data)
		}
	}
}

func TestRunForwardsSIGTERMAndRemovesLease(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	var out, errb bytes.Buffer
	cmd := startBinary(t, &out, &errb, "run", "--owner", "t", "--key", "sig", "--",
		"sh", "-c", `trap 'exit 9' TERM; touch "$0"; while :; do sleep 0.05; done`, filepath.Join(sd, "ready"))
	waitFor(t, func() bool { return fileExists(filepath.Join(sd, "ready")) }, "the command to start")
	if !fileExists(leasePath(sd, "t.sig")) {
		t.Fatal("no lease while the command runs")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 9 {
		t.Fatalf("exit = %v, want 9 (the command's own status after the forwarded SIGTERM); stderr %q", err, errb.String())
	}
	if fileExists(leasePath(sd, "t.sig")) {
		t.Error("lease left after a forwarded signal")
	}
}

func TestRunUsageErrors(t *testing.T) {
	useStateDir(t)
	cases := map[string][]string{
		"no owner":        {"run", "--", "true"},
		"no command":      {"run", "--owner", "t"},
		"no command (--)": {"run", "--owner", "t", "--"},
		"bad key":         {"run", "--owner", "t", "--key", "a.b", "--", "true"},
		"bad owner":       {"run", "--owner", "a/b", "--", "true"},
		"unknown flag":    {"run", "--owner", "t", "--bogus", "--", "true"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, _, stderr := runCLI(t, args...); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr)
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
