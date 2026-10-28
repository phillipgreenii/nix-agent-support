// Package testenv isolates a test binary from the developer's real state.
// Every test package's TestMain calls Run, so no test can touch the real
// ~/.local/state/pg-rescue or ~/.config/pg-rescue, inherit a PG_RESCUE_*
// variable, or pick up the GIT_* variables that `run-unit-tests` inherits
// when it runs inside a git hook.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run points XDG_STATE_HOME, XDG_CONFIG_HOME and HOME at a fresh temp
// directory, unsets PG_RESCUE_*, clears GIT_*, runs the tests and returns
// their exit code. Use it as: func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }.
func Run(m *testing.M) int {
	root, err := os.MkdirTemp("", "pg-rescue-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()

	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "PG_RESCUE_") || strings.HasPrefix(k, "GIT_") {
			_ = os.Unsetenv(k)
		}
	}
	for k, sub := range map[string]string{
		"XDG_STATE_HOME":  "state",
		"XDG_CONFIG_HOME": "config",
		"HOME":            "home",
	} {
		dir := root + "/" + sub
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
		_ = os.Setenv(k, dir)
	}
	return m.Run()
}

// BuildHelper builds the main package at importPath into a temp dir WITHOUT
// the race detector and returns the binary's path and a cleanup func. A test
// that supervises a child process uses it instead of re-executing its own
// test binary: the race runtime sleeps 1.0 s in every process that exits
// (atexit_sleep_ms), which a race-built re-executed child would pay each time.
// Call it from TestMain BEFORE Run, while HOME and the Go build cache are still
// the real ones. The cwd must be inside the module (a test's is).
func BuildHelper(importPath string) (bin string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pg-rescue-helper-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	bin = filepath.Join(dir, filepath.Base(importPath))
	cmd := exec.Command("go", "build", "-race=false", "-o", bin, importPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build %s: %v\n%s", importPath, err, out)
	}
	return bin, cleanup, nil
}
