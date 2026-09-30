package main

import (
	"os"
	"testing"
)

// TestMain points XDG_STATE_HOME at a throwaway directory so that any test
// that builds production deps (buildDeps opens <stateDir>/events.jsonl,
// bead pg2-ui2gk) never writes into the developer's real handler state dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ccpool-handler-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
