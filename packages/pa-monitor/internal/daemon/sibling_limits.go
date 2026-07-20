package daemon

import (
	"context"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// SiblingLimitsSource is the default LimitsSource adapter (ADR 0021 §1): it reads
// the status-line rate_limits records the wrapper appends to
// <ClaudeHome>/projects/**/<session_id>.status.jsonl. It returns the account-global
// CURRENT-WINDOW reading: the capture time is the newest record's ts (reading-stream
// liveness), and each window's used_percentage is that window's PEAK (ADR 0029), NOT
// the literal newest record.
//
// The record parsing (limits.ReadStatusRecords) and the window-peak fold
// (limits.Current) live in the leaf package internal/core/limits so this daemon
// adapter and the corpus Monitor's Limits observer share one implementation; this
// type owns only the on-disk DISCOVERY (which files to read). See the limits
// package docs for the full peak-hold rationale (why the peak, not the newest
// record). It MUST NOT correlate by session_id and MUST NOT substitute 0 for an
// absent value.
type SiblingLimitsSource struct {
	// ClaudeHome is ~/.claude. The reader scans ClaudeHome/projects/*/*.status.jsonl.
	ClaudeHome string
}

var _ LimitsSource = (*SiblingLimitsSource)(nil)

// Current returns the account-global current-window reading across all status.jsonl
// files, or (nil, nil) when none exists yet. A projects dir that is missing /
// unreadable is treated as "no reading yet" (nil, nil), not an error — the daemon
// must keep running.
func (s *SiblingLimitsSource) Current(_ context.Context) (*Limits, error) {
	if s == nil || s.ClaudeHome == "" {
		return nil, nil
	}
	projects := filepath.Join(s.ClaudeHome, "projects")
	projEntries, err := os.ReadDir(projects)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var recs []limits.Record // every ts-bearing record across all status files
	for _, pe := range projEntries {
		if !pe.IsDir() {
			continue
		}
		dir := filepath.Join(projects, pe.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue // unreadable project dir — skip, don't fail the whole read
		}
		for _, fe := range files {
			if fe.IsDir() || !session.IsStatusSiblingFile(fe.Name()) {
				continue
			}
			// Only the *.status.jsonl record files carry rate_limits; skip the
			// .status.last hash sidecar (IsStatusSiblingFile also matches it).
			if filepath.Ext(fe.Name()) != ".jsonl" {
				continue
			}
			recs = append(recs, limits.ReadStatusRecords(filepath.Join(dir, fe.Name()))...)
		}
	}
	// limits.Current returns nil for an empty record set (matches the old
	// len(recs)==0 -> (nil,nil) guard).
	return limits.Current(recs), nil
}
