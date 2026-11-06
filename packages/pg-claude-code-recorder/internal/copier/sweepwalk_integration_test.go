//go:build integration

package copier

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// The copier lock is released between batches: an Ingest completes while the
// pass is blocked after batch 1, and a file ingested then is not tombstoned
// from the stale snapshot of the file list.
func TestSweepMissingWalkReleasesLockBetweenBatches(t *testing.T) {
	e := newEnv(t)
	const n = 1000
	var paths []string
	for i := 0; i < n; i++ {
		paths = append(paths, e.write(fmt.Sprintf("%sf%04d.jsonl", sweepDir, i), "x\n"))
	}
	for _, r := range e.cp.IngestBatch(paths) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	// A late file: its row exists but only its first path is added after the
	// first batch, by moving a known file onto a new name.
	moved := paths[n-1]
	movedID := e.ingest(moved).File
	newPath := e.abs(sweepDir + "moved.jsonl")

	found, walked := walkOf(t, paths[:n-1]...)
	// The walk saw neither the old nor the new name of the last file.
	blocked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	e.cp.afterSweepBatch = func(done int) {
		if done == 1 {
			close(blocked)
			<-release
		}
	}
	go func() { _, err := e.cp.SweepMissingWalk(found, walked); done <- err }()
	<-blocked
	// Between batches: rename the last file and ingest it at its new path.
	if err := os.Rename(moved, newPath); err != nil {
		t.Fatal(err)
	}
	ingested := make(chan struct{})
	go func() { _, _ = e.cp.Ingest(newPath); close(ingested) }()
	select {
	case <-ingested:
	case <-time.After(10 * time.Second):
		t.Fatal("Ingest did not complete while the sweep was between batches")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(movedID); len(ts) != 0 {
		t.Fatal("a file ingested between batches was tombstoned from a stale snapshot")
	}
	if cur, _ := e.st.CurrentPaths(movedID); len(cur) != 1 || cur[0] != newPath {
		t.Fatalf("current paths %v", cur)
	}
}
