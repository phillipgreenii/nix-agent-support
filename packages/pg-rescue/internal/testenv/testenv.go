// Package testenv isolates a test binary from the developer's real state.
// Every test package's TestMain calls Run, so no test can touch the real
// ~/.local/state/pg-rescue or ~/.config/pg-rescue, inherit a PG_RESCUE_*
// variable, or pick up the GIT_* variables that `run-unit-tests` inherits
// when it runs inside a git hook.
package testenv

import (
	"fmt"
	"os"
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
	defer os.RemoveAll(root)

	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "PG_RESCUE_") || strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
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
		os.Setenv(k, dir)
	}
	return m.Run()
}
