package orphan

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

func TestMain(m *testing.M) {
	// The re-executed helper roles of TestOrphanHelperProcess end in os.Exit
	// (or are killed), which skips testenv.Run's deferred RemoveAll and would
	// leave an empty pg-rescue-test-* dir in $TMPDIR per role. They touch no
	// HOME/XDG state, so they run un-isolated; the parent test still isolates.
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(m.Run())
	}
	os.Exit(testenv.Run(m))
}
