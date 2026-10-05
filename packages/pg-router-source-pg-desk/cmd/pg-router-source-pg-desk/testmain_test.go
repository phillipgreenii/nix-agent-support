// testmain_test.go: the reentrant test-helper-process double for pg-desk.
// The test binary re-execs itself with GO_WANT_HELPER_PROCESS=1;
// TestHelperProcess then runs helperMain, which impersonates pg-desk: it
// records the argv it was given (so tests can assert on the exact pg-desk
// command line, e.g. that --cached is never passed), prints the fixture
// named by GO_HELPER_STDOUT_FILE (or the literal GO_HELPER_STDOUT) to
// stdout, GO_HELPER_STDERR to stderr, and exits GO_HELPER_EXIT. Same
// pattern as packages/pg-router-source-pg-connector's testmain_test.go.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// childArgs recovers the argv this adapter passed to execCmdFactory (the
// helper factory inserts "--" then the impersonated binary name then the args).
func childArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			if i+2 <= len(os.Args) {
				return os.Args[i+2:]
			}
			return nil
		}
	}
	return nil
}

func helperMain() {
	if f := os.Getenv("GO_HELPER_ARGS_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(childArgs(), "\n")), 0o600)
	}
	if f := os.Getenv("GO_HELPER_STDOUT_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			_, _ = os.Stderr.WriteString("helper: " + err.Error())
			os.Exit(99)
		}
		_, _ = os.Stdout.Write(b)
	}
	if s := os.Getenv("GO_HELPER_STDOUT"); s != "" {
		_, _ = os.Stdout.WriteString(s)
	}
	if s := os.Getenv("GO_HELPER_STDERR"); s != "" {
		_, _ = os.Stderr.WriteString(s)
	}
	code, _ := strconv.Atoi(os.Getenv("GO_HELPER_EXIT"))
	os.Exit(code)
}

// withHelper swaps execCmdFactory for a factory spawning the helper process
// with the given extra environment entries.
func withHelper(t *testing.T, env ...string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(append(os.Environ(), "GO_WANT_HELPER_PROCESS=1"), env...)
		return cmd
	}
	t.Cleanup(func() { execCmdFactory = orig })
}

type buf struct{ b []byte }

func (w *buf) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *buf) String() string              { return string(w.b) }

// runCLI drives the adapter end-to-end and returns what main() would emit.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut buf
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

func mustItems(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON array of objects: %v (stdout=%q)", err, stdout)
	}
	return items
}
