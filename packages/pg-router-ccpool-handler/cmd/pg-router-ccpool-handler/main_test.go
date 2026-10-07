package main

import (
	"os"
	"testing"
)

// ambientEnvInputs are the process-environment variables this module reads
// as fallback defaults for its own flags (--config, --role-config,
// --query-config, --socket, --token) and for the ccpool pool it targets. On a
// deployed machine (or inside a pn-managed shell) they point at the LIVE
// launch config, whose originProbe watches a real repo, so a test that does
// not pass its own flag silently picks the live config up: dispatch then
// declines with "origin-unavailable" instead of the branch the test targets
// (bead pg2-sljo4). The nix sandbox has none of them set, which is why the
// failure only appeared in a plain `go test` run.
var ambientEnvInputs = []string{
	envConfig,
	envRoleConfig,
	envQueryConfig,
	envSocket,
	envToken,
	"CCPOOL_POOL",
}

// isolateAmbientEnv unsets every ambientEnvInputs variable for the whole test
// process so no test can inherit a developer's or a deployed host's live
// configuration. A test that needs one sets it explicitly (t.Setenv).
func isolateAmbientEnv() {
	for _, k := range ambientEnvInputs {
		_ = os.Unsetenv(k)
	}
}

// TestMain points XDG_STATE_HOME at a throwaway directory so that any test
// that builds production deps (buildDeps opens <stateDir>/events.jsonl,
// bead pg2-ui2gk) never writes into the developer's real handler state dir,
// and drops the ambient configuration inputs (see isolateAmbientEnv).
func TestMain(m *testing.M) {
	if mode := os.Getenv(brokenPipeHelperEnv); mode != "" {
		os.Exit(runBrokenPipeHelper(mode))
	}
	isolateAmbientEnv()
	dir, err := os.MkdirTemp("", "ccpool-handler-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestIsolateAmbientEnv is the regression guard for pg2-sljo4: with hostile
// ambient values set (as on a deployed host), isolateAmbientEnv must clear
// every one, and the process TestMain already isolated must hold none.
func TestIsolateAmbientEnv(t *testing.T) {
	for _, k := range ambientEnvInputs {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s = %q leaked into the test process; TestMain must call isolateAmbientEnv", k, v)
		}
	}
	for _, k := range ambientEnvInputs {
		t.Setenv(k, "/hostile/ambient/value")
	}
	isolateAmbientEnv()
	for _, k := range ambientEnvInputs {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("isolateAmbientEnv left %s set to %q", k, v)
		}
	}
}
