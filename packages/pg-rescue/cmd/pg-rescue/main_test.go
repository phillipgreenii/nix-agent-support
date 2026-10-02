package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// TestMain re-executes the test binary as the real pg-rescue main (the
// GO_WANT_HELPER_PROCESS pattern used across this repo), so the tests below
// see genuine process exit codes without a separate `go build`.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		args := os.Args
		for i, a := range args {
			if a == "--" {
				os.Args = append([]string{"pg-rescue"}, args[i+1:]...)
				break
			}
		}
		main()
		return
	}
	os.Exit(testenv.Run(m))
}

func runMain(t *testing.T, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), append([]string{"GO_WANT_HELPER_PROCESS=1"}, env...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func TestResultProcessExitCodes(t *testing.T) {
	for outcome, want := range map[string]int{"resolved": 0, "declined": 2, "deferred": 3} {
		code, stdout, _ := runMain(t, nil, "result", outcome, "s")
		if code != want || !strings.Contains(stdout, `"outcome":"`+outcome+`"`) {
			t.Errorf("%s: code=%d stdout=%q", outcome, code, stdout)
		}
	}
}

func TestWrapperErrorProcessExit70(t *testing.T) {
	code, stdout, stderr := runMain(t, nil, "--", "true")
	if code != 70 || stdout != "" || !strings.Contains(stderr, "pg-rescue: ") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCheckProcessUsesXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	cfg := filepath.Join(xdg, "pg-rescue", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[handler.h]\ncommand = [\"sh\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runMain(t, []string{"XDG_CONFIG_HOME=" + xdg}, "check")
	if code != 0 || !strings.Contains(stdout, "config: "+cfg) || !strings.Contains(stdout, "handler h: ") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	if code, stdout, _ := runMain(t, nil, "--version"); code != 0 || strings.TrimSpace(stdout) != "pg-rescue dev" {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
}
