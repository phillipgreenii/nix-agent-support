package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// scanModeCacheHit is the RecordScan mode emitted when a transcript is unchanged
// since the last Scan (matching poller.go's literal at poller.go:198).
const scanModeCacheHit = "cache_hit"

type tcacheEntry struct {
	mtime   time.Time
	snap    transcript.Snapshot
	records []usage.Record
}

// transcriptTail owns per-FILE incremental scan state for resolved transcripts,
// keyed by PATH (not session id) so a file that several sessions resolve to — or
// that is tailed for pricing without any active session — is read exactly once.
// It reuses transcript.ScanIncremental/Accumulator so the incremental and cold
// paths cannot diverge, caches the parsed Snapshot AND the file's timestamped
// pricing records while (path,mtime) is unchanged (cache_hit), and emits
// RecordScan with the same full/incremental/cache_hit modes poller.Snapshot does
// today.
type transcriptTail struct {
	accs  map[string]*transcript.Accumulator // keyed by path
	cache map[string]tcacheEntry             // keyed by path
	scans int                                // cumulative ScanIncremental calls (Monitor resets per Scan for the perf guard)
}

func newTranscriptTail() *transcriptTail {
	return &transcriptTail{
		accs:  map[string]*transcript.Accumulator{},
		cache: map[string]tcacheEntry{},
	}
}

// fold reads the transcript at path (mtime from resolution) and returns its
// Snapshot, its timestamped pricing records (one usage.Record per non-error,
// modeled, non-zero-usage assistant line), and any scan error, recording the scan
// metric via rec (nil-safe). A cache hit (unchanged path+mtime) reuses the parsed
// Snapshot AND records with no re-parse — records MUST be returned on cache-hit
// too, else a caller that replaces its per-path record set each tick would wipe
// it on the (dominant) unchanged-file tick. An empty path yields a zero Snapshot,
// nil records, and records a "full" scan (parity with poller.go:208-210, which
// calls ScanIncremental("") in the miss branch).
func (tt *transcriptTail) fold(path string, mtime time.Time, rec Recorder) (transcript.Snapshot, []usage.Record, error) {
	if path == "" {
		recordScan(rec, string(transcript.ScanModeFull), 0, 0)
		return transcript.Snapshot{}, nil, nil
	}
	if c, ok := tt.cache[path]; ok && c.mtime.Equal(mtime) {
		recordScan(rec, scanModeCacheHit, 0, 0)
		return c.snap, c.records, nil
	}
	// prevAcc when we have prior state for this exact path (incremental tail); a
	// new path starts fresh, matching poller.go:200-206 (a rotated/renamed
	// transcript re-parses from scratch).
	prevAcc := tt.accs[path]
	start := time.Now()
	snap, acc, stats, err := transcript.ScanIncremental(path, prevAcc)
	tt.scans++
	recordScan(rec, string(stats.Mode), time.Since(start), stats.BytesFolded)
	if err != nil {
		// ScanIncremental returns a nil Accumulator on error (open/stat/oversized
		// line). Do NOT dereference it or cache it; surface the error so the caller
		// can thread it to CostProbeErr (parity with NativePricer's firstErr). The
		// prior cache entry is left intact for a possible next-tick recovery.
		return transcript.Snapshot{}, nil, err
	}
	records := acc.Records()
	tt.accs[path] = acc
	tt.cache[path] = tcacheEntry{mtime: mtime, snap: snap, records: records}
	return snap, records, err
}

// prune drops per-path scan state for paths absent from activePaths.
func (tt *transcriptTail) prune(activePaths map[string]bool) {
	for p := range tt.cache {
		if !activePaths[p] {
			delete(tt.cache, p)
			delete(tt.accs, p)
		}
	}
}
