package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// realHarness is the harness with the production ChainExecutor, a real PATH
// lookup and a real environment, running real (shell) commands.
func realHarness(t *testing.T, cfg string) (*harness, *ChainExecutor) {
	t.Helper()
	h := newHarness(t, "")
	h.writeConfig(h.cfgPath, cfg)
	ex := &ChainExecutor{Foreground: func() bool { return false }}
	h.rt.Executor = ex
	h.rt.LookPath = exec.LookPath
	h.rt.Environ = os.Environ
	return h, ex
}

// markerCommand is a command that leaves marker behind if it ever runs.
func markerCommand(marker string) []string {
	return []string{"sh", "-c", `: > "$0"`, marker}
}

// swapCommand replaces everything after the first "--" with cmd.
func swapCommand(args, cmd []string) []string {
	for i, a := range args {
		if a == "--" {
			return append(append([]string(nil), args[:i+1]...), cmd...)
		}
	}
	return args
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestEveryWrapperErrorNeverSpawnsTheCommand proves, end to end with the real
// executor, that every wrapper-error case exits 70 and the command never
// started: the fake command leaves a marker file if it ever runs.
func TestEveryWrapperErrorNeverSpawnsTheCommand(t *testing.T) {
	for _, c := range wrapperErrorCases(t) {
		t.Run(c.name, func(t *testing.T) {
			h, ex := realHarness(t, "")
			cfg := c.config
			if cfg == "" {
				cfg = goodConfig
			}
			if cfg != "-" {
				h.writeConfig(h.cfgPath, cfg)
			}
			if c.prep != nil {
				c.prep(h)
			}
			marker := filepath.Join(h.root, "command-ran")
			code, stdout, stderr := h.run(swapCommand(c.args(h), markerCommand(marker))...)
			if code != 70 {
				t.Errorf("exit = %d; want 70\nstderr: %s", code, stderr)
			}
			if fileExists(marker) {
				t.Error("the command was spawned; a wrapper error must come first")
			}
			if ex.Last != nil {
				t.Error("the runner was reached")
			}
			if stdout != "" || !strings.HasPrefix(stderr, "pg-rescue: ") {
				t.Errorf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

// TestPositiveControlTheMarkerCommandDoesRun keeps the test above honest: with
// a valid invocation the very same fake command does leave its marker.
func TestPositiveControlTheMarkerCommandDoesRun(t *testing.T) {
	h, _ := realHarness(t, goodConfig)
	marker := filepath.Join(h.root, "command-ran")
	code, _, stderr := h.run("--config", h.cfgPath, "--handlers", "notify", "--", "sh", "-c", `: > "$0"; exit 0`, marker)
	if code != 0 || !fileExists(marker) {
		t.Errorf("exit=%d marker=%v stderr=%s", code, fileExists(marker), stderr)
	}
}

func TestMissingWorkingDirectoryIsAWrapperError(t *testing.T) {
	h, _ := realHarness(t, goodConfig)
	marker := filepath.Join(h.root, "command-ran")
	args := func(dir string) []string {
		return append([]string{"--config", h.cfgPath, "--handlers", "notify", "-C", dir, "--"}, markerCommand(marker)...)
	}
	code, _, stderr := h.run(args(filepath.Join(h.root, "no-such-dir"))...)
	if code != 70 || fileExists(marker) || !strings.Contains(stderr, "cannot run in") {
		t.Errorf("exit=%d marker=%v stderr=%s", code, fileExists(marker), stderr)
	}
	// A file is not a directory either.
	file := filepath.Join(h.root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := h.run(args(file)...); code != 70 || fileExists(marker) {
		t.Errorf("exit=%d stderr=%s", code, stderr)
	}
}

func TestUnspawnableCommandIsAWrapperError(t *testing.T) {
	h, ex := realHarness(t, goodConfig)
	for name, cmd := range map[string][]string{
		"not on PATH":       {"pg-rescue-no-such-command-xyz"},
		"missing path":      {"/no/such/dir/cmd"},
		"not an executable": {h.cfgPath},
		"relative missing":  {"./no-such-script"},
	} {
		code, stdout, stderr := h.run(append([]string{"--config", h.cfgPath, "--handlers", "notify", "--"}, cmd...)...)
		if code != 70 || stdout != "" || !strings.HasPrefix(stderr, "pg-rescue: cannot run") {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", name, code, stdout, stderr)
		}
		if ex.Last == nil || ex.Last.Kind != runner.KindError {
			t.Errorf("%s: result = %+v", name, ex.Last)
		}
		if dirs := h.runDirs(); len(dirs) != 0 {
			t.Errorf("%s: run directory left behind: %v", name, dirs)
		}
	}
}
