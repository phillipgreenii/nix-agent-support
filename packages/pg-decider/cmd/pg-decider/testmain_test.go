// testmain_test.go: reentrant test-helper-process double for pg-desk (see
// internal/view/testmain_test.go for the protocol).
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/view"
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

func withHelper(t *testing.T, env ...string) {
	t.Helper()
	orig := view.ExecCommand
	view.ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(append(os.Environ(), "GO_WANT_HELPER_PROCESS=1"), env...)
		return cmd
	}
	t.Cleanup(func() { view.ExecCommand = orig })
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut bytes.Buffer
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
