package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
)

type statusEntry struct {
	size  int64
	mtime time.Time
	recs  []limits.Record
}

// statusTail owns per-file caching of parsed status-sibling records, keyed by
// path. Status files are tiny (a handful of rate_limits lines), so on a change it
// re-reads the whole file (limits.ReadStatusRecords) rather than offset-tailing;
// size participates in the cache key so a same-mtime append is still detected. The
// enclosing Monitor does the single os.ReadDir per project dir (the walk); this
// tail only avoids re-reading unchanged files.
type statusTail struct {
	cache map[string]statusEntry // keyed by path
	reads int                    // cumulative ReadStatusRecords calls (Monitor resets per Scan for the perf guard)
}

func newStatusTail() *statusTail {
	return &statusTail{cache: map[string]statusEntry{}}
}

// foldFile returns path's status records, re-reading only when (size,mtime)
// changed since the last fold.
func (st *statusTail) foldFile(path string, size int64, mtime time.Time) []limits.Record {
	if e, ok := st.cache[path]; ok && e.size == size && e.mtime.Equal(mtime) {
		return e.recs
	}
	recs := limits.ReadStatusRecords(path)
	st.reads++
	st.cache[path] = statusEntry{size: size, mtime: mtime, recs: recs}
	return recs
}

// prune drops per-path cache for paths absent from activePaths.
func (st *statusTail) prune(activePaths map[string]bool) {
	for p := range st.cache {
		if !activePaths[p] {
			delete(st.cache, p)
		}
	}
}
