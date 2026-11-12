package main

import (
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// TestShutdownReoffer_RealRouterProcess re-executes this test binary as a
	// router process (TestHelperRouterProcess) with a complete, explicit
	// environment that includes the PG_ROUTER_* variables the router needs.
	// That child must keep them, so the scrub is skipped for it only; it is
	// recognised by the same exact sentinel the helper itself requires, which no
	// caller exports. Every other process, including the test process itself,
	// is scrubbed.
	if os.Getenv(routerSentinelEnv) != routerSentinelValue {
		unsetRouterEnv()
	}
	os.Exit(m.Run())
}

// unsetRouterEnv removes every PG_ROUTER_* variable from this test process
// before any test runs, so no test sees the pg-router deployment of whoever
// runs `go test` (bead pg2-50k41, same class as pg2-pjqk3 in internal/config).
// Drain and pool sessions export several of them: PG_ROUTER_REPO_ROOT names a
// real repository and PG_ROUTER_CONFIG the live daemon's config, both of which
// config.Load() would otherwise read. A test that needs a variable
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

// TestRouterEnvIsUnsetByDefault pins unsetRouterEnv directly, in-process: no
// PG_ROUTER_* variable the caller exported (a drain session exports several)
// may survive TestMain, absent or empty alike. Config loading itself is
// covered with an injected environment in internal/config.
func TestRouterEnvIsUnsetByDefault(t *testing.T) {
	for _, kv := range os.Environ() {
		name, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PG_ROUTER_") {
			t.Errorf("%s is set to %q at test start; TestMain must unset it", name, v)
		}
	}
}
