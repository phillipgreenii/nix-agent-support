package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ResolveTranscript finds the most relevant transcript file for s under
// claudeHome/projects/<slug>/*.jsonl and returns its path and mtime.
//
// Why this is not a simple TranscriptPath lookup any more:
//
// The session record under ~/.claude/sessions/<pid>.json stores the sessionId
// that was in effect when the Claude Code process started. But Claude Code
// rewrites the on-disk transcript to a NEW sessionId (and thus a new .jsonl
// filename) whenever the user resumes, compacts, or forks a conversation.
// Matching strictly on the original sessionId yields a stale file whose mtime
// hasn't moved in hours, making every live session classify as Dormant.
//
// ResolveTranscript instead scans the session's project directory and picks:
//  1. the most-recently-modified transcript whose `customTitle` event matches
//     the session record's Name, if Name is set; otherwise
//  2. the most-recently-modified transcript outright.
//
// ok is false when the project directory does not exist or contains no
// readable transcripts.
func ResolveTranscript(claudeHome string, s *Session) (path string, mtime time.Time, ok bool) {
	if s == nil {
		return "", time.Time{}, false
	}
	dir := filepath.Join(claudeHome, "projects", slugify(s.Cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, false
	}

	type cand struct {
		path  string
		mtime time.Time
	}
	var cands []cand
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, cand{
			path:  filepath.Join(dir, e.Name()),
			mtime: info.ModTime(),
		})
	}
	if len(cands) == 0 {
		return "", time.Time{}, false
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })

	if s.Name != "" {
		for _, c := range cands {
			if transcriptHasTitle(c.path, s.Name) {
				return c.path, c.mtime, true
			}
		}
	}

	// Try exact SessionID match before generic newest-fallback.
	// This lets multiple unnamed sessions in the same directory each
	// resolve to their own transcript when the original file is still present.
	for _, c := range cands {
		if filepath.Base(c.path) == s.SessionID+".jsonl" {
			return c.path, c.mtime, true
		}
	}

	// Fallback: newest transcript in the directory. Good enough when only
	// one session runs in this cwd, and no worse than the old behavior when
	// multiple sessions share one cwd.
	return cands[0].path, cands[0].mtime, true
}

// transcriptHasTitle returns true if any of the first handful of events in
// the transcript carries a `custom-title` record with the given title.
//
// Claude Code writes the `custom-title` event close to the start of a
// transcript (within the first ~30 events in practice), so we cap the scan
// at titleScanLines lines to keep this cheap on a per-poll hot path.
func transcriptHasTitle(path, wantTitle string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	lines := 0
	for scanner.Scan() && lines < titleScanLines {
		lines++
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Type == "custom-title" && rec.CustomTitle == wantTitle {
			return true
		}
	}
	return false
}

const titleScanLines = 200
