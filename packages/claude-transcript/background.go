package claudetranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PendingBackgroundAgents reports whether the session behind the transcript at
// path ended its last turn with a background agent still running, and that
// agent is not silent: the last system/turn_duration record carries
// pendingBackgroundAgentCount > 0, AND the newest write among the session's
// subagent transcripts (<path minus .jsonl>/subagents/*.jsonl; the main
// transcript itself when there is none) is no older than recent before now.
//
// A drain orchestrator ends its turn right after dispatching a background
// subagent and then sits idle for the whole subagent run, so the session reads
// as idle although work is in progress. The recency bound keeps a subagent that
// died without reporting from counting as active forever. A missing or
// unreadable transcript, or a turn_duration without the field, reports false.
func PendingBackgroundAgents(path string, now time.Time, recent time.Duration) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	type rec struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Pending int    `json:"pendingBackgroundAgentCount"`
	}
	pending := 0
	sc := newTranscriptScanner(f)
	for sc.Scan() {
		b := sc.Bytes()
		// Cheap prefilter: only system records can be turn_duration.
		if !strings.Contains(string(b), `"turn_duration"`) {
			continue
		}
		var r rec
		if json.Unmarshal(b, &r) != nil || r.Type != "system" || r.Subtype != "turn_duration" {
			continue
		}
		pending = r.Pending
	}
	if sc.Err() != nil || pending <= 0 {
		return false
	}

	newest := time.Time{}
	dir := strings.TrimSuffix(path, ".jsonl") + string(filepath.Separator) + "subagents"
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
	}
	if newest.IsZero() {
		fi, err := os.Stat(path)
		if err != nil {
			return false
		}
		newest = fi.ModTime()
	}
	return now.Sub(newest) <= recent
}
