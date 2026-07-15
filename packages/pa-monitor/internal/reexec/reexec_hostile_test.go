//go:build hostile

// Sandbox-hostile reexec E2E (bead pg2-ymi3l). This test `go build`s a stub
// binary and re-invokes THIS test binary as a helper subprocess to exercise the
// real syscall.Exec path. Building + spawning subprocesses is unavailable/flaky
// inside the no-network `pa-monitor-go-tests` nix build sandbox, so it is split
// out of reexec_test.go behind the `hostile` build tag; the default gate (plain
// `go test ./...`, no tag) runs only the sandbox-safe fake-seam tests. Run the
// full set locally with `go test -tags hostile ./...`. TestReexecHelperProcess
// is the child half — it only exists (and can be re-invoked) when the test
// binary is itself built with the `hostile` tag, so both live here together.
package reexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- E2E: exercise the REAL syscall.Exec once via a helper subprocess. ---
//
// The parent builds a tiny stub binary named "pa-monitor", then re-invokes THIS
// test binary as a helper (TestReexecHelperProcess). The helper calls the real
// Reexec (with the real sysExec) which execve-replaces the helper process image
// with the stub. The stub prints its argv + selected env and exits 0. The parent
// asserts args/env survived the execve. We deliberately never syscall.Exec the
// test binary itself (that never returns and would re-run the whole suite).
func TestReexecExecvePreservesArgsEnv(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "reexec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Build the stub, named "pa-monitor" so filepath.Base(argv0) == "pa-monitor".
	stubSrc := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(stubSrc, []byte(stubProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	stubBin := filepath.Join(dir, "pa-monitor")
	if out, err := exec.Command("go", "build", "-o", stubBin, stubSrc).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestReexecHelperProcess")
	cmd.Env = append(
		os.Environ(),
		"GO_WANT_REEXEC_HELPER=1",
		"REEXEC_STUB_DIR="+dir,
		"REEXEC_SENTINEL=hello-world",
		genEnv+"=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper subprocess failed: %v\n%s", err, out)
	}
	got := string(out)

	// argv[0] pinned to "pa-monitor" (the original args[0]), then the subcommand.
	if !strings.Contains(got, "ARGV=pa-monitor|cmux-bridge|--flag") {
		t.Errorf("argv not preserved across execve; output:\n%s", got)
	}
	// GEN incremented to attempt+1 = 1.
	if !strings.Contains(got, "GEN=1") {
		t.Errorf("GEN not set to attempt+1; output:\n%s", got)
	}
	// Unrelated env preserved.
	if !strings.Contains(got, "SENTINEL=hello-world") {
		t.Errorf("env not preserved across execve; output:\n%s", got)
	}
}

// TestReexecHelperProcess is not a real test: it is the child half of
// TestReexecExecvePreservesArgsEnv. It runs the real Reexec (real sysExec) which
// should execve into the stub and never return here.
func TestReexecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_REEXEC_HELPER") != "1" {
		t.Skip("helper process; only runs when re-invoked by the E2E parent")
	}
	dir := os.Getenv("REEXEC_STUB_DIR")
	argv0 := filepath.Join(dir, "pa-monitor")
	args := []string{"pa-monitor", "cmux-bridge", "--flag"}
	lookPath := func(name string) (string, error) {
		return filepath.Join(dir, name), nil // absolute stub path
	}
	// no-op sleep to keep the E2E fast
	err := Reexec(argv0, args, os.Environ(), 0, lookPath, sysExec, func(time.Duration) {})
	// Reaching here means execve failed. Report and exit non-zero so the parent
	// sees the failure instead of a silent replacement.
	fmt.Fprintf(os.Stdout, "REEXEC_RETURNED err=%v\n", err)
	os.Exit(7)
}

// stubProgram is a stdlib-only main that echoes its argv and two env vars so the
// parent can verify they survived execve, then exits 0.
const stubProgram = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println("ARGV=" + strings.Join(os.Args, "|"))
	fmt.Println("GEN=" + os.Getenv("` + "PA_MONITOR_REEXEC_GEN" + `"))
	fmt.Println("SENTINEL=" + os.Getenv("REEXEC_SENTINEL"))
	os.Exit(0)
}
`
