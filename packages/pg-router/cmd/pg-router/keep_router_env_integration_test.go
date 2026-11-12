//go:build integration && unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// keepRouterEnv reports whether this process is the re-executed router child
// (TestHelperRouterProcess), recognised by the same exact sentinel the helper
// itself requires, which no caller exports.
func keepRouterEnv() bool { return os.Getenv(routerSentinelEnv) == routerSentinelValue }

// routerEnvProbeSentinel is deliberately not PG_ROUTER_-prefixed, so
// unsetRouterEnv leaves it alone.
const routerEnvProbeSentinel = "PGR_TEST_ROUTER_ENV_PROBE"

// drainSessionRouterEnv lists the PG_ROUTER_* variables a drain session exports.
var drainSessionRouterEnv = []string{
	"PG_ROUTER_CONFIG",
	"PG_ROUTER_CCPOOL_HANDLER_CONFIG",
	"PG_ROUTER_REPO_ROOT",
	"PG_ROUTER_BEADS_PREFIX",
	"PG_ROUTER_METRICS_ADDR",
}

// TestHelperRouterEnvIsUnsetByDefault pins unsetRouterEnv directly: a variable the
// caller exported must be absent (not merely empty) once TestMain has run. It
// only does its work in the child process started by
// TestPackageTestsPassWithRouterEnvExported, which supplies the exported
// values; they must exist before TestMain runs.
func TestHelperRouterEnvIsUnsetByDefault(t *testing.T) {
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
// unsetRouterEnv. It re-runs this package's test binary, restricted to
// TestHelperRouterEnvIsUnsetByDefault, with the PG_ROUTER_* variables a drain
// session exports set to arbitrary non-empty values, and requires that test to
// observe them unset (re-running every test here would also re-run the router
// process tests). A child process is
// needed because the variables must already be exported when TestMain runs; a
// t.Setenv here would come too late.
func TestPackageTestsPassWithRouterEnvExported(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], // #nosec G204 -- re-executes this test binary
		"-test.run=^TestHelperRouterEnvIsUnsetByDefault$", "-test.v")
	cmd.Env = append(os.Environ(), routerEnvProbeSentinel+"=1")
	for _, name := range drainSessionRouterEnv {
		cmd.Env = append(cmd.Env, name+"=/nonexistent/pg2-50k41/"+strings.ToLower(name))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("this package's tests fail when the caller exports PG_ROUTER_* variables: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestHelperRouterEnvIsUnsetByDefault") {
		t.Fatalf("TestHelperRouterEnvIsUnsetByDefault did not run and pass in the child process:\n%s", out)
	}
}
