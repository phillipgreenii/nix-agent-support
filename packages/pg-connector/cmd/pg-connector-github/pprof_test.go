package main

import "testing"

func TestPprofEnvNameIsThisDaemons(t *testing.T) {
	if pprofEnv != "PG_CONNECTOR_GITHUB_PPROF_ADDR" {
		t.Errorf("pprofEnv = %q", pprofEnv)
	}
}
