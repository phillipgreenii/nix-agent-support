package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SourceStateFileName is the file, under the router's state dir, that holds
// each pull source's last-success time across restarts (bead pg2-yagdb2).
const SourceStateFileName = "source-last-success.json"

// WithSourceStateFile makes the per-source last-success time survive a restart:
// New restores each registered source's time from the JSON file at path (source
// name to RFC 3339 time), and every success or deliberate pause rewrites it. A
// missing, unreadable or malformed file, an unknown source, or a time in the
// future of the Emitter's clock falls back to process start, the old behaviour,
// so the file can never make a source look younger than it is. A write failure
// is ignored: the gauge is still right for this process. Without this option
// nothing is read or written. It has no effect without WithPullSources.
//
// A restored time has no monotonic reading, so the age gauge measures it on the
// wall clock (time spent with the host asleep while the router was down counts);
// the first success after the restart restores the monotonic reading.
func WithSourceStateFile(path string) Option {
	return func(o *options) { o.sourceStatePath = path }
}

// loadSourceState returns the persisted last-success times, or nil.
func loadSourceState(path string) map[string]time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]time.Time
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// saveSourceStateLocked writes srcPersisted atomically. The caller holds srcMu.
func (e *Emitter) saveSourceStateLocked() {
	if e.srcStatePath == "" {
		return
	}
	b, err := json.Marshal(e.srcPersisted)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(e.srcStatePath), 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.srcStatePath), ".source-last-success-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), e.srcStatePath) != nil {
		_ = os.Remove(tmp.Name())
	}
}
