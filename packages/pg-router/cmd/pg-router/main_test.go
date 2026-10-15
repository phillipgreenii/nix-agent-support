package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	unsetRouterEnv()
	os.Exit(m.Run())
}

// unsetRouterEnv removes every PG_ROUTER_* variable from this test process
// before any test runs, so no test sees the pg-router deployment of whoever
// runs `go test` (bead pg2-50k41, same class as pg2-pjqk3 in internal/config).
// Drain and pool sessions export several of them: PG_ROUTER_REPO_ROOT names a
// real repository and PG_ROUTER_CONFIG the live daemon's config, both of which
// config.Load() would otherwise read, and PG_ROUTER_HANDLER_COMMAND_DIR changes
// which code path the runRunRole tests exercise. A test that needs a variable
// sets it with t.Setenv, whose cleanup restores this unset state.
//
// The variables are unset, not set to "": config.Load() reads them with
// os.LookupEnv, so an empty value still overrides the default.
func unsetRouterEnv() {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG_ROUTER_") {
			_ = os.Unsetenv(name)
		}
	}
}

// routerEnvProbeSentinel is deliberately not PG_ROUTER_-prefixed, so
// unsetRouterEnv leaves it alone.
const routerEnvProbeSentinel = "PGR_TEST_ROUTER_ENV_PROBE"

// drainSessionRouterEnv lists the PG_ROUTER_* variables a drain session exports.
var drainSessionRouterEnv = []string{
	"PG_ROUTER_CONFIG",
	"PG_ROUTER_HANDLER_COMMAND",
	"PG_ROUTER_HANDLER_COMMAND_DIR",
	"PG_ROUTER_CCPOOL_HANDLER_CONFIG",
	"PG_ROUTER_REPO_ROOT",
	"PG_ROUTER_BEADS_PREFIX",
	"PG_ROUTER_METRICS_ADDR",
}

// TestRouterEnvIsUnsetByDefault pins unsetRouterEnv directly: a variable the
// caller exported must be absent (not merely empty) once TestMain has run. It
// only does its work in the child process started by
// TestPackageTestsPassWithRouterEnvExported, which supplies the exported
// values; they must exist before TestMain runs.
func TestRouterEnvIsUnsetByDefault(t *testing.T) {
	if os.Getenv(routerEnvProbeSentinel) != "1" {
		t.Skip("runs only inside TestPackageTestsPassWithRouterEnvExported's child process")
	}
	for _, name := range drainSessionRouterEnv {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is set to %q at test start; TestMain must unset it", name, v)
		}
	}
}

// TestPackageTestsPassWithRouterEnvExported is the regression guard for
// unsetRouterEnv. It re-runs this package's test binary with the seven
// PG_ROUTER_* variables a drain session exports set to arbitrary non-empty
// values, and requires every other test to pass and
// TestRouterEnvIsUnsetByDefault to observe them unset. A child process is
// needed because the variables must already be exported when TestMain runs; a
// t.Setenv here would come too late.
func TestPackageTestsPassWithRouterEnvExported(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], // #nosec G204 -- re-executes this test binary
		"-test.skip=^"+t.Name()+"$", fmt.Sprintf("-test.short=%t", testing.Short()), "-test.v")
	cmd.Env = append(os.Environ(), routerEnvProbeSentinel+"=1")
	for _, name := range drainSessionRouterEnv {
		cmd.Env = append(cmd.Env, name+"=/nonexistent/pg2-50k41/"+strings.ToLower(name))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("this package's tests fail when the caller exports PG_ROUTER_* variables: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestRouterEnvIsUnsetByDefault") {
		t.Fatalf("TestRouterEnvIsUnsetByDefault did not run and pass in the child process:\n%s", out)
	}
}
