package runner_test

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

func TestMain(m *testing.M) {
	// The fake command, handler and verify are this very binary, re-executed
	// with GO_WANT_HELPER_PROCESS=1 (see helper_test.go). It must run BEFORE
	// testenv.Run, which unsets the PG_RESCUE_* variables a fake has to see.
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		runHelper()
	}
	os.Exit(testenv.Run(m))
}
