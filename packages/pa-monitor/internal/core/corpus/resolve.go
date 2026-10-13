package corpus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// customTitleMarker is the byte string every custom-title record contains. A line
// without it cannot be one, so the probe skips JSON-decoding it.
var customTitleMarker = []byte("custom-title")

type titleEntry struct {
	title       string                 // the file's current (latest) custom-title; "" if none
	cur         *transcript.TailCursor // resume point of the offset read; nil forces a read from the start
	scannedSize int64                  // file size when last probed; an unchanged size needs no open
}

// titleCache resolves transcripts and tracks each file's CURRENT custom-title by
// path. A title can change (Claude Code appends a new custom-title record), so
// every file keeps being tailed: a probe reads only the bytes appended since the
// last one (transcript.TailLines, the same offset read the transcript fold uses),
// JSON-decodes only lines mentioning custom-title, and the last such record wins.
// A file whose size is unchanged is not opened at all, so the active transcript
// (whose mtime advances every tick) costs one read of its newly appended bytes.
type titleCache struct {
	entries map[string]titleEntry
	opens   int   // test-only: count of file-content opens for title probing
	bytes   int64 // test-only: bytes consumed from files for title probing
	decodes int   // test-only: count of json.Unmarshal calls for title probing
}

func newTitleCache() *titleCache { return &titleCache{entries: map[string]titleEntry{}} }

// customTitle returns path's current custom-title ("" if none). It reopens the
// file only when its size differs from the last probe. size comes from the
// caller's ReadDir entry, so this adds no stat.
func (h *titleCache) customTitle(path string, size int64) string {
	e, ok := h.entries[path]
	if ok && e.scannedSize == size {
		return e.title
	}
	e = h.probe(path, e)
	e.scannedSize = size
	h.entries[path] = e
	return e.title
}

// probe reads the bytes of path appended since prev's cursor and folds any
// custom-title records into the returned entry. On a read error the title found
// so far is kept and the cursor is dropped, so the next growth re-reads the file.
func (h *titleCache) probe(path string, prev titleEntry) titleEntry {
	e := prev
	cur, n, _, err := transcript.TailLines(path, prev.cur,
		func() { e.title = "" },
		func(line []byte) {
			if !bytes.Contains(line, customTitleMarker) {
				return
			}
			h.decodes++
			var rec struct {
				Type        string `json:"type"`
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Type == "custom-title" {
				e.title = rec.CustomTitle
			}
		})
	if err == nil {
		h.opens++
	}
	h.bytes += n
	e.cur = cur
	return e
}

type cand struct {
	path  string
	mtime time.Time
	size  int64
}

// resolve replicates session.ResolveTranscript's 3-tier precedence, minus the
// dead 200-line title cap. Candidates are enumerated via os.ReadDir and sorted
// newest-first with the IDENTICAL comparator (never seeded from a map, so
// equal-mtime ties tie-break exactly as the old path does). ok is false when the
// project dir is missing or holds no transcript.
func (h *titleCache) resolve(claudeHome string, s *session.Session) (string, time.Time, bool) {
	if s == nil {
		return "", time.Time{}, false
	}
	dir := filepath.Dir(s.TranscriptPath(claudeHome)) // claudeHome/projects/<slug>
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, false
	}
	var cands []cand
	for _, e := range entries {
		if e.IsDir() || !session.IsTranscriptFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, cand{
			path:  filepath.Join(dir, e.Name()),
			mtime: info.ModTime(),
			size:  info.Size(),
		})
	}
	if len(cands) == 0 {
		return "", time.Time{}, false
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })

	if s.Name != "" {
		for _, c := range cands {
			if h.customTitle(c.path, c.size) == s.Name {
				return c.path, c.mtime, true
			}
		}
	}
	for _, c := range cands {
		if filepath.Base(c.path) == s.SessionID+".jsonl" {
			return c.path, c.mtime, true
		}
	}
	return cands[0].path, cands[0].mtime, true
}

// prune drops title-cache entries whose file is not under an active session's
// project dir, bounding the cache to live project dirs (write-once titles within
// those dirs are retained across ticks).
func (h *titleCache) prune(activeDirs map[string]bool) {
	for p := range h.entries {
		if !activeDirs[filepath.Dir(p)] {
			delete(h.entries, p)
		}
	}
}
