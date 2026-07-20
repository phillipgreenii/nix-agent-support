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

// walkCorpus enumerates the <claudeHome>/projects tree once (one os.ReadDir per
// project dir) and returns:
//   - transcript files (<slug>/<id>.jsonl) whose mtime is within `window` of now —
//     the pricing candidacy gate, so files older than the current week + block
//     margin are NEVER opened (design §2/§8);
//   - every status sibling (<slug>/<id>.status.jsonl) UNGATED — the greatest
//     resets_at window may live in an older file, matching the old
//     SiblingLimitsSource's unbounded read (status files are tiny).
//
// A missing projects dir yields no files and no error; an unreadable project dir
// or file is skipped, never fatal.
func walkCorpus(claudeHome string, window time.Duration, now time.Time) ([]walkFile, error) {
	projects := filepath.Join(claudeHome, "projects")
	projEntries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := now.Add(-window)
	var out []walkFile
	for _, pe := range projEntries {
		if !pe.IsDir() {
			continue
		}
		dir := filepath.Join(projects, pe.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fe := range files {
			if fe.IsDir() {
				continue
			}
			name := fe.Name()
			switch {
			case session.IsTranscriptFile(name):
				info, err := fe.Info()
				if err != nil {
					continue
				}
				if info.ModTime().Before(cutoff) {
					continue // out of the pricing window — never opened
				}
				out = append(out, walkFile{
					path:  filepath.Join(dir, name),
					class: Transcript,
					mtime: info.ModTime(),
					size:  info.Size(),
				})
			case session.IsStatusSiblingFile(name) && filepath.Ext(name) == ".jsonl":
				info, err := fe.Info()
				if err != nil {
					continue
				}
				out = append(out, walkFile{
					path:  filepath.Join(dir, name),
					class: StatusSibling,
					mtime: info.ModTime(),
					size:  info.Size(),
				})
			}
		}
	}
	return out, nil
}
