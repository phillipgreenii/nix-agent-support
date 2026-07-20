package corpus

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// titleScanLineCap is a defensive bound on the custom-title probe. It replaces
// the dead titleScanLines=200 cap in session.transcriptHasTitle: custom-title now
// lands at lines ~490-1100, so 200 never matched. The probe stops at the first
// custom-title record anyway; the cap only guards a pathological title-less file.
const titleScanLineCap = 20000

type titleEntry struct {
	title       string
	found       bool
	scannedSize int64
}

// titleCache resolves transcripts and memoizes each file's custom-title
// WRITE-ONCE by path. A title never changes once written, so once found it is
// cached permanently and survives mtime bumps — otherwise the active transcript
// (whose mtime advances every tick) would be re-scanned to line ~500 every tick,
// a replay of the very hotspot this removes.
type titleCache struct {
	entries map[string]titleEntry
	opens   int // test-only: count of file-content opens for title probing
}

func newTitleCache() *titleCache { return &titleCache{entries: map[string]titleEntry{}} }

// customTitle returns path's custom-title ("" if none). Cached titles are
// returned without reopening; an "absent" result is re-probed only when the file
// has grown (size differs) — a title could appear as an initially-short file
// grows. size comes from the caller's ReadDir entry, so this adds no stat.
func (h *titleCache) customTitle(path string, size int64) string {
	if e, ok := h.entries[path]; ok {
		if e.found {
			return e.title
		}
		if e.scannedSize == size {
			return ""
		}
	}
	title := h.scanTitle(path)
	h.entries[path] = titleEntry{title: title, found: title != "", scannedSize: size}
	return title
}

func (h *titleCache) scanTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h.opens++
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<16), 16*1024*1024)
	for lines := 0; scanner.Scan() && lines < titleScanLineCap; lines++ {
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		if rec.Type == "custom-title" {
			return rec.CustomTitle
		}
	}
	return ""
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
