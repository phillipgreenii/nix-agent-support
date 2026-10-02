package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// TestProcessSetsUmask077: the real binary, started under a permissive umask,
// keeps everything under the state root private. The handler creates a file
// with the default mode; it can only come out 0600 if main set umask 077.
func TestProcessSetsUmask077(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	root := t.TempDir()
	cfg := filepath.Join(root, "config.toml")
	handler := `umask > "$PG_RESCUE_RUN_DIR/umask.txt"; : > "$PG_RESCUE_RUN_DIR/created"; exit 0`
	text := "[handler.h]\ncommand = [\"sh\", \"-c\", " + strconv.Quote(handler) + "]\n"
	if err := os.WriteFile(cfg, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	code, stdout, stderr := runMain(t, []string{"XDG_STATE_HOME=" + state}, "--config", cfg, "--handlers", "h", "--verify", "true",
		"--", "sh", "-c", "echo out; echo err >&2; exit 3")
	if code != 0 || stdout != "out\n" || !strings.HasPrefix(stderr, "err\n\n== pg-rescue: `sh -c 'echo out; echo err >&2; exit 3'` exited 3 · handlers h ==\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	runs, err := os.ReadDir(filepath.Join(state, "pg-rescue", "runs"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("run directories: %v %v", runs, err)
	}
	dir := filepath.Join(state, "pg-rescue", "runs", runs[0].Name())
	if b, _ := os.ReadFile(filepath.Join(dir, "umask.txt")); strings.TrimSpace(string(b)) != "0077" {
		t.Errorf("the handler's umask = %q; want 0077", b)
	}
	for _, p := range []string{filepath.Join(state, "pg-rescue", "runs.jsonl"), filepath.Join(dir, "created"), filepath.Join(dir, "display.log"), filepath.Join(dir, "report.json")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, err %v; want 0600", p, fi, err)
		}
	}
	for _, p := range []string{filepath.Join(state, "pg-rescue"), filepath.Join(state, "pg-rescue", "runs"), dir} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, err %v; want 0700", p, fi, err)
		}
	}
}
