package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// binaryEnv makes the test binary behave as the pg-keep-awake binary, so a
// test can start the real command as a child process (signals, exit status).
const binaryEnv = "PG_KEEP_AWAKE_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(binaryEnv) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// useStateDir points XDG_STATE_HOME at a fresh temp directory and returns the
// pg-keep-awake state directory inside it (not yet created).
func useStateDir(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	return filepath.Join(xdg, "pg-keep-awake")
}

// requireProcInfo skips a test on a platform where process inspection is not
// implemented (pg-keep-awake is a macOS tool).
func requireProcInfo(t *testing.T) {
	t.Helper()
	if _, err := lease.ProcStateOf(os.Getpid()); err != nil {
		t.Skipf("process inspection unavailable: %v", err)
	}
}

func leasePath(stateDir, id string) string {
	return filepath.Join(stateDir, "leases", id+".json")
}

func readLeaseFile(t *testing.T, stateDir, id string) lease.Lease {
	t.Helper()
	data, err := os.ReadFile(leasePath(stateDir, id))
	if err != nil {
		t.Fatalf("lease %s not readable: %v", id, err)
	}
	var l lease.Lease
	if err := json.Unmarshal(data, &l); err != nil {
		t.Fatalf("lease %s is not JSON: %v", id, err)
	}
	return l
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.Pid()
}

// startBinary starts the real command (this test binary in binary mode).
func startBinary(t *testing.T, stdout, stderr *bytes.Buffer, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), binaryEnv+"=1")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func itoa(n int) string { return strconv.Itoa(n) }
