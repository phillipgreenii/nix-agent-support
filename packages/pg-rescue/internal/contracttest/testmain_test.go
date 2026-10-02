package contracttest

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// TestMain isolates the test process like every other package. The fake
// handler is this same binary re-executed (GO_WANT_HELPER_PROCESS=1); it must
// see the PG_RESCUE_* variables Run gives it, which testenv.Run would clear, so
// it skips the isolation.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(m.Run())
	}
	os.Exit(testenv.Run(m))
}
