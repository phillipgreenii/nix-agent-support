package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// SiblingLimitsSource is the default LimitsSource adapter (ADR 0021 §1): it reads
// the status-line rate_limits records the wrapper appends to
// <ClaudeHome>/projects/**/<session_id>.status.jsonl and returns the SINGLE
// most-recent record across ALL such files, ordered by the embedded `ts`.
//
// It MUST NOT correlate by session_id (Claude rewrites the id on resume / compact /
// fork, so a per-session view would fragment the account-global value), and it MUST
// NOT substitute 0 for an absent value: every field is independently optional and a
// missing value stays "unknown" (nil pointer / zero time), never 0 / 1970.
type SiblingLimitsSource struct {
	// ClaudeHome is ~/.claude. The reader scans ClaudeHome/projects/*/*.status.jsonl.
	ClaudeHome string
}

var _ LimitsSource = (*SiblingLimitsSource)(nil)

// statusRecord is the on-disk shape written by capture-status.bash. Every field is
// a pointer / optional so "absent" is distinguishable from a real 0 — a nil
// *float64 stays nil; a missing epoch stays a zero time.Time.
type statusRecord struct {
	TS               *int64   `json:"ts"`
	FiveHourPct      *float64 `json:"five_hour_pct"`
	FiveHourResetsAt *int64   `json:"five_hour_resets_at"`
	SevenDayPct      *float64 `json:"seven_day_pct"`
	SevenDayResetsAt *int64   `json:"seven_day_resets_at"`
}

// Current returns the newest record across all status.jsonl files, or (nil, nil)
// when none exists yet. A projects dir that is missing / unreadable is treated as
// "no reading yet" (nil, nil), not an error — the daemon must keep running.
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

	var (
		best     statusRecord
		bestTS   int64
		haveBest bool
	)
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
			rec, ts, ok := newestRecordInFile(filepath.Join(dir, fe.Name()))
			if !ok {
				continue
			}
			// Newest ts wins across files. On an equal ts, keep the first seen
			// (deterministic given ReadDir's sorted order) — the value is
			// account-global, so near-duplicate concurrent records are interchangeable.
			if !haveBest || ts > bestTS {
				best, bestTS, haveBest = rec, ts, true
			}
		}
	}
	if !haveBest {
		return nil, nil
	}
	return best.toLimits(bestTS), nil
}

// newestRecordInFile parses every line of one status.jsonl and returns the record
// with the largest ts (and that ts). ok is false when the file has no parseable,
// ts-bearing record. Malformed / ts-less lines are skipped, not fatal.
func newestRecordInFile(path string) (statusRecord, int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return statusRecord{}, 0, false
	}
	var (
		best     statusRecord
		bestTS   int64
		haveBest bool
	)
	// Scan lines without bufio.Scanner's default limit: files are tiny.
	start := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		line := data[start:i]
		start = i + 1
		if len(line) == 0 {
			continue
		}
		var rec statusRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.TS == nil {
			continue // a record without ts cannot be ordered — skip
		}
		if !haveBest || *rec.TS > bestTS {
			best, bestTS, haveBest = rec, *rec.TS, true
		}
	}
	return best, bestTS, haveBest
}

// toLimits maps the on-disk record to the daemon's Limits, converting present
// epoch fields to time.Time and leaving absent fields as unknown (nil / zero).
func (r statusRecord) toLimits(ts int64) *Limits {
	l := &Limits{
		FiveHourPct: r.FiveHourPct,
		SevenDayPct: r.SevenDayPct,
		CapturedAt:  time.Unix(ts, 0),
	}
	if r.FiveHourResetsAt != nil {
		l.FiveHourResetsAt = time.Unix(*r.FiveHourResetsAt, 0)
	}
	if r.SevenDayResetsAt != nil {
		l.SevenDayResetsAt = time.Unix(*r.SevenDayResetsAt, 0)
	}
	return l
}
