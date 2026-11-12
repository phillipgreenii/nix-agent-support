//go:build integration && unix

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPackageTestsPassWithRouterEnvExported is the regression guard for
// unsetRouterEnv. It re-runs this test binary with the PG_ROUTER_* variables a
// drain session exports, set to arbitrary non-empty values, and requires every
// other test in the package to pass. A child process is needed because the
// variables must already be exported when TestMain runs, as they are for the
// commit-time hook of a drain session; a t.Setenv here would come too late.
func TestPackageTestsPassWithRouterEnvExported(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], // #nosec G204 -- re-executes this test binary
		"-test.skip=^"+t.Name()+"$", fmt.Sprintf("-test.short=%t", testing.Short()))
	// The child also starts with an XDG_CONFIG_HOME holding a malformed
	// pg-router config: a test that reached it would fail (pg2-50k41).
	poisoned := t.TempDir()
	if err := os.MkdirAll(filepath.Join(poisoned, "pg-router"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(poisoned, "pg-router", "config.toml"), []byte("not = valid [[[ toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(
		os.Environ(),
		"XDG_CONFIG_HOME="+poisoned,
		"HOME="+poisoned,
		"PG_ROUTER_CONFIG=/nonexistent/pg2-pjqk3/config.toml",
		"PG_ROUTER_CCPOOL_HANDLER_CONFIG=/nonexistent/pg2-pjqk3/launch.json",
		"PG_ROUTER_REPO_ROOT=/nonexistent/pg2-pjqk3/repo",
		"PG_ROUTER_BEADS_PREFIX=arbitrary",
		"PG_ROUTER_METRICS_ADDR=invalid-host:invalid-port",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("this package's tests fail when the caller exports PG_ROUTER_* variables: %v\n%s", err, out)
	}
}
