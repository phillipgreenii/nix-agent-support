// testmain_test.go: the reentrant test-helper-process double for pg-desk.
// The test binary re-execs itself with GO_WANT_HELPER_PROCESS=1;
// TestHelperProcess then impersonates pg-desk: it records its argv to
// GO_HELPER_ARGS_FILE, prints GO_HELPER_STDOUT_FILE (or GO_HELPER_STDOUT)
// to stdout, GO_HELPER_STDERR to stderr and exits GO_HELPER_EXIT. Pattern:
// packages/pg-router-source-pg-desk's testmain_test.go.
package view

import (
	"context"
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

// withHelper swaps ExecCommand for a factory spawning the helper process.
func withHelper(t *testing.T, env ...string) {
	t.Helper()
	orig := ExecCommand
	ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(append(os.Environ(), "GO_WANT_HELPER_PROCESS=1"), env...)
		return cmd
	}
	t.Cleanup(func() { ExecCommand = orig })
}
