package main

import "testing"

func TestPprofEnvNameIsThisDaemons(t *testing.T) {
	if pprofEnv != "PG_CONNECTOR_ISSUE_JIRA_PPROF_ADDR" {
		t.Errorf("pprofEnv = %q", pprofEnv)
	}
}
