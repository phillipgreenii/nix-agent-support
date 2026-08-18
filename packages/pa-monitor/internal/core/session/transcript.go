package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status-line rate_limits sibling suffixes (ADR 0021 §1/§2). The wrapper writes a
// <id>.status.jsonl record file (and MAY keep a <id>.status.last hash sidecar) next
// to the transcript; neither is a transcript and neither is a session record.
const (
	statusRecordSuffix  = ".status.jsonl"
	statusSidecarSuffix = ".status.last"
)

// IsTranscriptFile reports whether name is a Claude Code transcript file, as
// opposed to a status-line rate_limits sibling file (ADR 0021 §2). It is the
// single shared predicate applied everywhere pa-monitor enumerates transcript
// .jsonl files (ResolveTranscript here and the sibling-file LimitsSource reader),
// so a <id>.status.jsonl is NEVER selected as a transcript.
//
// A transcript ends in .jsonl but NOT .status.jsonl. The .status.last hash sidecar
// is likewise excluded — it is never a .jsonl, but rejecting it here documents the
// full exclusion set.
func IsTranscriptFile(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	return !IsStatusSiblingFile(name)
}

// IsStatusSiblingFile reports whether name is a status-line rate_limits sibling
// file (the <id>.status.jsonl record or the <id>.status.last hash sidecar). Used
// by gc.listSessionFiles to skip these so they never derive a phantom session ID
// (ADR 0021 §2), while genuine <id>.json session records still pass.
func IsStatusSiblingFile(name string) bool {
	return strings.HasSuffix(name, statusRecordSuffix) ||
		strings.HasSuffix(name, statusSidecarSuffix)
}

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
		// Skip directories and any non-transcript .jsonl (a <id>.status.jsonl
		// rate_limits sibling would otherwise win the newest-by-mtime fallback
		// and corrupt token counts / model / activity state — ADR 0021 §2).
		if e.IsDir() || !IsTranscriptFile(e.Name()) {
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
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, titleScanInitialBufSize), titleScanMaxLineSize)
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

// titleScanInitialBufSize and titleScanMaxLineSize match the 1 MiB initial /
// 16 MiB ceiling pattern used by every other transcript reader in this repo
// (context.go, subagents.go, first_prompt.go, corpus/resolve.go,
// pr-pool/internal/usage/transcript.go, claude-transcript/scanner.go). A
// single JSONL line here routinely runs to megabytes (a large tool_result or
// pasted file on one line), and bufio.Scanner.Buffer's effective ceiling is
// the LARGER of its two arguments — the previous (1<<16, 1<<20) pair capped
// at 1 MiB, silently dropping the scan (Scan returns false, scanner.Err()
// becomes bufio.ErrTooLong, which this function never checks) whenever an
// early line exceeded that, indistinguishable from "no custom-title record".
const (
	titleScanInitialBufSize = 1 << 20  // 1 MiB
	titleScanMaxLineSize    = 16 << 20 // 16 MiB
)
