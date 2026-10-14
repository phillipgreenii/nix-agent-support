package corpus

import (
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// walkFile is one classified corpus file discovered by walkCorpus.
type walkFile struct {
	path  string
	class FileClass
	mtime time.Time
	size  int64
}

// dirRacyMargin is how much older than its own read a directory's mtime must be
// before the cached listing is trusted. A directory changed again inside the same
// timestamp tick as the read that cached it would keep an identical mtime, so a
// listing recorded closer than this to the directory's mtime is always re-read
// (the "racy timestamp" rule git uses for its index).
const dirRacyMargin = 2 * time.Second

// walkEntry is one corpus-relevant child of a directory listing: a subdirectory
// (class unused) or a transcript / status-sibling file.
type walkEntry struct {
	name  string
	isDir bool
	class FileClass
}

// walkDirCache is a remembered directory listing, valid while the directory's
// own mtime is unchanged (creating, removing or renaming an entry bumps it).
type walkDirCache struct {
	mtime    time.Time
	cachedAt time.Time
	entries  []walkEntry
}

// corpusWalker is the stateful walk behind walkCorpus. The projects tree holds
// thousands of directories and tens of thousands of transcripts, and the producer
// re-walks it on every assemble, so re-listing every directory (open, getdents,
// close) dominated the daemon's CPU (pg2-uxtdf profiles). The walker re-lists a
// directory only when its mtime changed and otherwise reuses the remembered names,
// but still lstats every remembered FILE each walk, because appending to a file
// does not bump its directory's mtime and a file may enter the pricing window at
// any time. Not safe for concurrent use (one producer goroutine owns it).
type corpusWalker struct {
	dirs  map[string]*walkDirCache
	clock func() time.Time

	// readDirs counts directory listings performed (test hook: the unchanged-tree
	// walk must do none).
	readDirs int
}

func newCorpusWalker() *corpusWalker {
	return &corpusWalker{dirs: map[string]*walkDirCache{}, clock: time.Now}
}

// walkCorpus walks the <claudeHome>/projects tree once with a throwaway walker
// (see corpusWalker.walk). The Monitor keeps a long-lived walker instead.
func walkCorpus(claudeHome string, window time.Duration, now time.Time) ([]walkFile, error) {
	return newCorpusWalker().walk(claudeHome, window, now)
}

// walk walks the <claudeHome>/projects tree RECURSIVELY (matching the old
// NativePricer's filepath.WalkDir, which prices BOTH main transcripts and
// subagent transcripts <slug>/<id>/subagents/agent-*.jsonl — subagent token usage
// is real billable spend) and returns:
//   - transcript files (any *.jsonl that is not a status sibling) whose mtime is
//     within `window` of now — the pricing candidacy gate, so files older than the
//     current week + block margin are NEVER opened (design §2/§8);
//   - status siblings (*.status.jsonl) UNGATED — the greatest resets_at window may
//     live in an older file, matching the old SiblingLimitsSource's unbounded read
//     (status files are tiny; in practice they exist only at depth 1).
//
// Entries come out in the same lexical pre-order filepath.WalkDir produced. A
// missing projects tree yields no files and no error; any per-entry error is
// skipped (never fatal — matches the old pricer's best-effort walk), so a hung or
// unreadable file cannot fail the whole scan.
func (w *corpusWalker) walk(claudeHome string, window time.Duration, now time.Time) ([]walkFile, error) {
	root := filepath.Join(claudeHome, "projects")
	cutoff := now.Add(-window)
	visited := map[string]bool{}
	var out []walkFile
	if fi, err := os.Lstat(root); err == nil && fi.IsDir() {
		w.visitDir(root, fi, cutoff, visited, &out)
	}
	for p := range w.dirs {
		if !visited[p] {
			delete(w.dirs, p) // bound the cache to the directories that still exist
		}
	}
	return out, nil
}

func (w *corpusWalker) visitDir(dir string, fi os.FileInfo, cutoff time.Time, visited map[string]bool, out *[]walkFile) {
	visited[dir] = true
	mtime := fi.ModTime() // read BEFORE listing: a change in between only forces a re-list
	c := w.dirs[dir]
	if c == nil || !c.mtime.Equal(mtime) || c.cachedAt.Sub(c.mtime) < dirRacyMargin {
		var err error
		c, err = w.list(dir, mtime)
		if err != nil {
			// A failed listing is not remembered, but the entries read before the
			// error are still walked (WalkDir behaviour).
			delete(w.dirs, dir)
		} else {
			w.dirs[dir] = c
		}
	}
	for _, e := range c.entries {
		p := filepath.Join(dir, e.name)
		cfi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if e.isDir {
			if cfi.IsDir() {
				w.visitDir(p, cfi, cutoff, visited, out)
			}
			continue
		}
		if cfi.IsDir() {
			continue
		}
		if e.class == Transcript && cfi.ModTime().Before(cutoff) {
			continue // out of the pricing window — never opened
		}
		*out = append(*out, walkFile{path: p, class: e.class, mtime: cfi.ModTime(), size: cfi.Size()})
	}
}

// list reads dir and keeps only the entries the walk cares about.
func (w *corpusWalker) list(dir string, mtime time.Time) (*walkDirCache, error) {
	w.readDirs++
	des, err := os.ReadDir(dir)
	c := &walkDirCache{mtime: mtime, cachedAt: w.clock()}
	for _, de := range des {
		name := de.Name()
		switch {
		case de.IsDir():
			c.entries = append(c.entries, walkEntry{name: name, isDir: true})
		case session.IsTranscriptFile(name):
			c.entries = append(c.entries, walkEntry{name: name, class: Transcript})
		case session.IsStatusSiblingFile(name) && filepath.Ext(name) == ".jsonl":
			c.entries = append(c.entries, walkEntry{name: name, class: StatusSibling})
		}
	}
	return c, err
}
