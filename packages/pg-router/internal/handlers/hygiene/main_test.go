package hygiene

import (
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/corecall"
	"github.com/phillipgreenii/pg-router/internal/handlers/dtest"
	"github.com/phillipgreenii/pg-router/internal/handlers/rolefile"
)

// ambientEnvInputs are the process-environment variables the passes read as
// fallback defaults for their own flags and for the ccpool pool they target.
// A test that does not pass its own flag would silently pick up a deployed
// host's live configuration, so TestMain unsets them.
var ambientEnvInputs = []string{
	rolefile.EnvConfig,
	corecall.EnvSocket,
	corecall.EnvToken,
	"CCPOOL_POOL",
}

// TestMain points XDG_STATE_HOME at a throwaway directory so that any test
// that builds production deps never writes into the developer's real handler
// state dir, drops the ambient configuration inputs, and stubs the live
// process probe so no test depends on lsof.
func TestMain(m *testing.M) {
	for _, k := range ambientEnvInputs {
		_ = os.Unsetenv(k)
	}
	dir, err := os.MkdirTemp("", "ccpool-hygiene-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	removeSharedSweepFixture()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestClosedReconcileStampPrefixMatchesDtest pins the shared test helper's copy
// of the stamp prefix to the production constant.
func TestClosedReconcileStampPrefixMatchesDtest(t *testing.T) {
	if dtest.ClosedReconcileStampPrefix != closedReconcileStampPrefix {
		t.Fatalf("dtest.ClosedReconcileStampPrefix = %q, want %q", dtest.ClosedReconcileStampPrefix, closedReconcileStampPrefix)
	}
}

func containsSubstring(msgs []string, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}
