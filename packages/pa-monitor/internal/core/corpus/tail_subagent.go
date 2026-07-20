package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

type subEntry struct {
	size  int64
	mtime time.Time
	rec   transcript.ErrorRecord
	ok    bool // transcript.LastAPIError returned no error
}

// subagentTail reproduces transcript.LastSubagentError's aggregation (scan
// <sid>/subagents/agent-*.jsonl, keep the latest *terminal* api-error, tag
// FromSubagent) but CACHES each agent file's transcript.LastAPIError result
// keyed by (size,mtime). Unchanged agent files — the vast majority, since a
// finished subagent's file never changes — are never re-read; that is the CPU
// win over today's always-re-read LastSubagentError. size participates in the
// key so a same-mtime append is still detected. Using LastAPIError verbatim (not
// scanState) preserves IsContextLimit and every other field exactly. The cache
// is keyed sessionID -> agentPath so it prunes by active session id, matching
// the transcript tail.
type subagentTail struct {
	cache    map[string]map[string]subEntry
	reads    int // cumulative transcript.LastAPIError calls (Monitor resets per Scan)
	readDirs int // cumulative subagents-dir ReadDir calls (Monitor resets per Scan)
}

func newSubagentTail() *subagentTail {
	return &subagentTail{cache: map[string]map[string]subEntry{}}
}

// fold returns sessionID's latest terminal subagent error (nil when none),
// tagged FromSubagent, plus the max mtime over its agent-*.jsonl files (zero
// when the subagents dir is absent). Behaviorally identical to
// transcript.LastSubagentError(resolvedPath), with per-file caching and a single
// ReadDir that also yields maxActivity (eliminating today's double ReadDir).
func (st *subagentTail) fold(sessionID, resolvedPath string) (*transcript.ErrorRecord, time.Time) {
	if resolvedPath == "" {
		return nil, time.Time{}
	}
	subDir := strings.TrimSuffix(resolvedPath, ".jsonl") + "/subagents"
	entries, err := os.ReadDir(subDir)
	st.readDirs++
	if err != nil {
		return nil, time.Time{}
	}
	sess := st.cache[sessionID]
	if sess == nil {
		sess = map[string]subEntry{}
		st.cache[sessionID] = sess
	}
	var best transcript.ErrorRecord
	found := false
	var maxMtime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "agent-") || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(maxMtime) {
			maxMtime = info.ModTime()
		}
		path := filepath.Join(subDir, e.Name())
		size, mtime := info.Size(), info.ModTime()
		entry, cached := sess[path]
		if !cached || entry.size != size || !entry.mtime.Equal(mtime) {
			rec, ferr := transcript.LastAPIError(path)
			st.reads++
			entry = subEntry{size: size, mtime: mtime, rec: rec, ok: ferr == nil}
			sess[path] = entry
		}
		if !entry.ok || entry.rec.Kind == "" || !entry.rec.IsTerminal {
			continue
		}
		if !found || entry.rec.At.After(best.At) {
			best = entry.rec
			found = true
		}
	}
	if !found {
		return nil, maxMtime
	}
	best.FromSubagent = true
	return &best, maxMtime
}

// prune drops per-session cache for sessions absent from activeIDs.
func (st *subagentTail) prune(activeIDs map[string]bool) {
	for id := range st.cache {
		if !activeIDs[id] {
			delete(st.cache, id)
		}
	}
}
