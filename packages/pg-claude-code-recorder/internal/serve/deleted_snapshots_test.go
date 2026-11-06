package serve

import (
	"path/filepath"
	"testing"
)

// notifyDeletedSnapshots reads only the deleted snapshot rows: it never issues
// the whole-table AllFiles statement.
func TestNotifyDeletedSnapshotsDoesNotReadAllFiles(t *testing.T) {
	d, e, c := openCounting(t, "deleted-snapshots-counting")
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 20)
	c.Reset()
	d.notifyDeletedSnapshots()
	if n := c.Matching("FROM files ORDER BY id"); n != 0 {
		t.Fatalf("notifyDeletedSnapshots issued %d AllFiles statements", n)
	}
	if n := c.Matching("mode='snapshot' AND state='deleted'"); n != 1 {
		t.Fatalf("expected one deleted-snapshot query, got %d", n)
	}
}
