//go:build integration

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

// runReal runs the real command in a child process the way a supervisor starts
// it: an otherwise empty environment (env -i) and working directory "/".
func runReal(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.Env = []string{reexecEnv + "=1"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func TestRealProcessClientModesAnswerUnderEmptyEnvironment(t *testing.T) {
	abs, err := filepath.Abs(golden)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pr", "ci"} {
		// Nothing on stdin: the client answers an unavailable error body and
		// exits with that code, it does not panic or hang.
		code, out, errs := runReal(t, "--config", abs, mode)
		if code != 4 || !strings.Contains(out, `"code":"unavailable"`) {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", mode, code, out, errs)
		}
		if strings.Contains(errs, "panic") || strings.Contains(errs, "goroutine") {
			t.Errorf("%s: must fail cleanly, not panic: %q", mode, errs)
		}
	}
}

func TestRealProcessVersionAndUsage(t *testing.T) {
	code, out, errs := runReal(t, "--version")
	if code != 0 || !regexp.MustCompile(`^pg-connector-github \S+\n$`).MatchString(out) {
		t.Errorf("--version: exit=%d stdout=%q stderr=%q", code, out, errs)
	}
	if code, _, errs := runReal(t); code != exitUsage || !strings.Contains(errs, "usage:") {
		t.Errorf("no mode: exit=%d stderr=%q", code, errs)
	}
	// With nothing in the environment and no flag there is no config to load.
	if code, _, errs := runReal(t, "serve"); code != exitConfig || !strings.Contains(errs, config.EnvConfig) {
		t.Errorf("no config: exit=%d stderr=%q", code, errs)
	}
}
