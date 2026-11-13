package pathspec

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
	suiteMemo.hold()
	code := m.Run()
	suiteMemo.release()
	os.Exit(code)
}

// suiteMemo is the test binary's hold on the memo.
var suiteMemo suiteMemoHold

type suiteMemoHold struct{ restore func() }

func (h *suiteMemoHold) hold() { h.restore = patheval.MemoizeRealPaths() }

func (h *suiteMemoHold) release() { h.restore() }

// suspend drops the suite hold for the rest of the test and re-takes it on
// cleanup. A test that changes the filesystem under a path it has already
// resolved (removing a marker file, say) MUST call it, because the memo would
// serve the earlier answer. Tests are not parallel in this package.
func (h *suiteMemoHold) suspend(t *testing.T) {
	t.Helper()
	h.release()
	t.Cleanup(h.hold)
}
