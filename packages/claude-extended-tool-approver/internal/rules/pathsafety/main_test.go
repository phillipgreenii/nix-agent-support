package pathsafety

import (
	"os"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestMain memoizes real-path resolution for the whole test binary. Each
// patheval.New resolves a few dozen reference paths with lstat chains; the
// cost under host load is blocking filesystem latency, not CPU. Fixtures live
// under per-test t.TempDir() paths, so a memo shared across tests is safe.
func TestMain(m *testing.M) {
	restore := patheval.MemoizeRealPaths()
	code := m.Run()
	restore()
	os.Exit(code)
}
