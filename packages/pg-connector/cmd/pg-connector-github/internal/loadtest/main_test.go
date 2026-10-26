package loadtest

import (
	"fmt"
	"os"
	"testing"
)

// loadTestEnv is the opt-in switch of this package: its scenarios simulate
// nineteen minutes of fake time over 150 pull requests and take tens of
// minutes under -race, so they are an on-demand load test, not a unit test
// (operator ruling, 2026-10-25). Only the value "1" turns them on.
const loadTestEnv = "PG_CONNECTOR_LOAD_TEST"

// TestMain gates the WHOLE package, not individual tests, so that `go test
// ./...`, `go test -short` and the module's go-tests check skip every test in
// it, including any test file added later, without that file having to
// remember a skip. With PG_CONNECTOR_LOAD_TEST=1 it runs them all.
//
// Disabled, it exits 0 before running anything (so `-test.list` and every
// `-run` pattern also list and run nothing); the checks.<system>.
// pg-connector-load-test-gating check proves both directions.
func TestMain(m *testing.M) {
	if os.Getenv(loadTestEnv) != "1" {
		fmt.Printf("loadtest: skipped, set %s=1 to run the load test\n", loadTestEnv)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
