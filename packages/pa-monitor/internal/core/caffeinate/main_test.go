package caffeinate

import (
	"os"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate/keepertest"
)

// TestMain builds the real pg-keep-awake once for the package's tests (see
// keepertest); tests skip when its source is not present.
func TestMain(m *testing.M) {
	// The env -i helper subprocess (TestHelperKeeperProcess) has no `go` on its
	// PATH and needs no build: the parent hands it the built binary's path.
	if os.Getenv(helperModeEnv) != "" {
		os.Exit(m.Run())
	}
	os.Exit(keepertest.Main(m))
}
