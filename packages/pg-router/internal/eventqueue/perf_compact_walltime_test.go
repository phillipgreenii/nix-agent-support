//go:build perf

package eventqueue

import (
	"path/filepath"
	"testing"
)

// Wall-clock perf gate (V-4: never in the unit gate); see perf_walltime_test.go
// for how to run it.

// Replay time after compacting a live-shaped (~180k-record) log is bounded, and
// the compacted log rebuilds the identical state.
func TestCompactLiveShapedReplayPerf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	records := writeLiveShapedLog(t, path, 62000, 120)
	sizeBefore := fileSize(t, path)
	replayBefore, stateBefore := measureReplay(t, path)

	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs.Compact()
	_ = fs.Close()
	if err != nil {
		t.Fatal(err)
	}
	replayAfter, stateAfter := measureReplay(t, path)

	t.Logf("live-shaped log: %d records, %d bytes -> %d records, %d bytes (compaction took %s)",
		records, sizeBefore, st.RecordsAfter, st.BytesAfter, st.Duration)
	t.Logf("replay (New) before=%s after=%s", replayBefore, replayAfter)

	if stateAfter != stateBefore {
		t.Fatal("compacted live-shaped log rebuilt a different state")
	}
	if st.RecordsAfter*50 > records {
		t.Fatalf("compaction kept %d of %d records; expected live state only", st.RecordsAfter, records)
	}
	if st.BytesAfter*50 > sizeBefore {
		t.Fatalf("compaction kept %d of %d bytes; expected live state only", st.BytesAfter, sizeBefore)
	}
	if replayAfter*5 > replayBefore {
		t.Fatalf("replay after compaction (%s) is not clearly faster than before (%s)", replayAfter, replayBefore)
	}
}
