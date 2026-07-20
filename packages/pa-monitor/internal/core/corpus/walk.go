package corpus

import (
	"io/fs"
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

// walkCorpus walks the <claudeHome>/projects tree RECURSIVELY (matching the old
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
// A missing projects tree yields no files and no error; any per-entry error is
// skipped (never fatal — matches the old pricer's best-effort walk), so a hung or
// unreadable file cannot fail the whole scan.
func walkCorpus(claudeHome string, window time.Duration, now time.Time) ([]walkFile, error) {
	projects := filepath.Join(claudeHome, "projects")
	cutoff := now.Add(-window)
	var out []walkFile
	err := filepath.WalkDir(projects, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries (incl. a missing projects root)
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case session.IsTranscriptFile(name):
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				return nil // out of the pricing window — never opened
			}
			out = append(out, walkFile{path: path, class: Transcript, mtime: info.ModTime(), size: info.Size()})
		case session.IsStatusSiblingFile(name) && filepath.Ext(name) == ".jsonl":
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			out = append(out, walkFile{path: path, class: StatusSibling, mtime: info.ModTime(), size: info.Size()})
		}
		return nil
	})
	return out, err
}
