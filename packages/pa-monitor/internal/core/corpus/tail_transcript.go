package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// scanModeCacheHit is the RecordScan mode emitted when a transcript is unchanged
// since the last Scan (matching poller.go's literal at poller.go:198).
const scanModeCacheHit = "cache_hit"

type tcacheEntry struct {
	path  string
	mtime time.Time
	snap  transcript.Snapshot
}

// transcriptTail owns per-session incremental scan state for resolved
// transcripts. It reuses transcript.ScanIncremental/Accumulator so the
// incremental and cold paths cannot diverge, caches the parsed Snapshot while
// (path,mtime) is unchanged (cache_hit), and emits RecordScan with the same
// full/incremental/cache_hit modes poller.Snapshot does today.
type transcriptTail struct {
	accs  map[string]*transcript.Accumulator
	cache map[string]tcacheEntry
	scans int // cumulative ScanIncremental calls (Monitor resets per Scan for the perf guard)
}

func newTranscriptTail() *transcriptTail {
	return &transcriptTail{
		accs:  map[string]*transcript.Accumulator{},
		cache: map[string]tcacheEntry{},
	}
}

// fold reads sessionID's resolved transcript at path (mtime from resolution) and
// returns its Snapshot, recording the scan metric via rec (nil-safe). A cache
// hit (unchanged path+mtime) reuses the parsed Snapshot with no re-parse. An
// empty path yields a zero Snapshot and records a "full" scan (parity with
// poller.go:208-210, which calls ScanIncremental("") in the miss branch).
func (tt *transcriptTail) fold(sessionID, path string, mtime time.Time, rec Recorder) transcript.Snapshot {
	if path == "" {
		recordScan(rec, string(transcript.ScanModeFull), 0, 0)
		return transcript.Snapshot{}
	}
	if c, ok := tt.cache[sessionID]; ok && c.path == path && c.mtime.Equal(mtime) {
		recordScan(rec, scanModeCacheHit, 0, 0)
		return c.snap
	}
	// prevAcc only when the resolved path is unchanged (matches poller.go:200-206:
	// a rotated/renamed transcript re-parses from scratch).
	var prevAcc *transcript.Accumulator
	if c, ok := tt.cache[sessionID]; ok && c.path == path {
		prevAcc = tt.accs[sessionID]
	}
	start := time.Now()
	snap, acc, stats, _ := transcript.ScanIncremental(path, prevAcc)
	tt.scans++
	recordScan(rec, string(stats.Mode), time.Since(start), stats.BytesFolded)
	tt.accs[sessionID] = acc
	tt.cache[sessionID] = tcacheEntry{path: path, mtime: mtime, snap: snap}
	return snap
}

// prune drops per-session scan state for sessions absent from activeIDs.
func (tt *transcriptTail) prune(activeIDs map[string]bool) {
	for id := range tt.cache {
		if !activeIDs[id] {
			delete(tt.cache, id)
			delete(tt.accs, id)
		}
	}
}
