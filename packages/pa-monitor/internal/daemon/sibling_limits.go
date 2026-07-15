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
// <ClaudeHome>/projects/**/<session_id>.status.jsonl. It returns the account-global
// CURRENT-WINDOW reading: the capture time is the newest record's ts (reading-stream
// liveness), and each window's used_percentage is that window's PEAK (ADR 0029), NOT
// the literal newest record.
//
// Why the peak rather than the newest record (ADR 0029, refining ADR 0021 §1's
// "single most-recent record"): each record's used_percentage is that session's
// LAST-SEEN server value (from its API rate-limit headers), but its ts is the
// status-line RENDER time. A session re-renders (advancing ts) without a fresh API
// call, so it emits a fresh-ts record carrying a STALE percentage — e.g. a session
// that got rate-limited at 50% and stopped making calls keeps writing 50% while other
// sessions have driven the account to 100%. The account-global value only ACCUMULATES
// within a fixed window (constant resets_at), so the MAX across a window's records is
// the freshest true reading; a later-but-lower same-window value is a staler snapshot,
// not a real drop. The current window is the GREATEST resets_at observed (windows only
// advance; see windowPeak) and CapturedAt is the newest ts (reading-stream liveness).
// The block "releases" only when a record carrying a NEWER resets_at appears — a
// genuinely new window. This is in the spirit of ADR 0021 §1's mandate to "ignore the
// near-duplicate records that concurrent sessions emit for the same global value"; it
// corrects the mechanism (newest-by-ts is not robust to per-session staleness).
//
// It MUST NOT correlate by session_id (Claude rewrites the id on resume / compact /
// fork, so a per-session view would fragment the account-global value; the peak is
// taken across ALL sessions), and it MUST NOT substitute 0 for an absent value: every
// field is independently optional and a missing value stays "unknown" (nil pointer /
// zero time), never 0 / 1970.
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

// Current returns the account-global current-window reading across all status.jsonl
// files (see the type doc for the peak-hold semantics), or (nil, nil) when none
// exists yet. A projects dir that is missing / unreadable is treated as "no reading
// yet" (nil, nil), not an error — the daemon must keep running.
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

	var recs []statusRecord // every ts-bearing record across all status files
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
			recs = appendRecordsInFile(recs, filepath.Join(dir, fe.Name()))
		}
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return currentWindowLimits(recs), nil
}

// appendRecordsInFile parses every line of one status.jsonl and appends each
// parseable, ts-bearing record to dst. Malformed / ts-less lines are skipped, not
// fatal; an unreadable file contributes nothing.
func appendRecordsInFile(dst []statusRecord, path string) []statusRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return dst
	}
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
		dst = append(dst, rec)
	}
	return dst
}

// currentWindowLimits collapses all parsed status records into the account-global
// current-window Limits (ADR 0029). CapturedAt is the newest ts across every record
// (reading-stream liveness — NOT the instant the reported peak was captured). Each
// window (5h keyed by five_hour_resets_at, 7d by seven_day_resets_at) reports its
// PEAK used_percentage independently; see windowPeak.
func currentWindowLimits(recs []statusRecord) *Limits {
	var capturedTS int64
	for i := range recs {
		if recs[i].TS != nil && *recs[i].TS > capturedTS {
			capturedTS = *recs[i].TS
		}
	}
	l := &Limits{CapturedAt: time.Unix(capturedTS, 0)}

	fivePct, fiveReset := windowPeak(recs, capturedTS,
		func(r statusRecord) *int64 { return r.FiveHourResetsAt },
		func(r statusRecord) *float64 { return r.FiveHourPct })
	l.FiveHourPct = fivePct
	if fiveReset != nil {
		l.FiveHourResetsAt = time.Unix(*fiveReset, 0)
	}

	sevenPct, sevenReset := windowPeak(recs, capturedTS,
		func(r statusRecord) *int64 { return r.SevenDayResetsAt },
		func(r statusRecord) *float64 { return r.SevenDayPct })
	l.SevenDayPct = sevenPct
	if sevenReset != nil {
		l.SevenDayResetsAt = time.Unix(*sevenReset, 0)
	}
	return l
}

// windowPeak computes one window's current used_percentage and reset epoch (ADR 0029).
// reset/pct select this window's fields off a record.
//
//   - The CURRENT window is the GREATEST reset epoch observed across all records.
//     Windows only advance (a later window has a later reset), so max-reset always names
//     the newest window. It is deliberately NOT "the reset of the newest-ts record": a
//     lagging session reports an OLD reset together with its stale percentage (both come
//     from the same API response), and can render — advancing its ts — after a new
//     window has already begun. Keying on ts would let such a straggler mask the new
//     window; keying on max-reset cannot, because its reset is the older (smaller) one.
//   - The percentage is the MAX non-nil pct among records sharing that reset; nil when
//     the window carries a reset but no pct at all (unknown, never a fabricated 0). The
//     max is the freshest true reading: each session shows only its last-seen server
//     value, so lower same-window readings are staler snapshots, not real decreases.
//   - When NO record carries a reset for this window, there is no window to key on:
//     the pct falls back to the globally-newest record's value (nil if that is absent)
//     and the reset is nil. This preserves the pre-ADR-0029 behavior for that
//     degenerate case (e.g. the Phase-0 seven_day-absent account).
func windowPeak(recs []statusRecord, capturedTS int64, reset func(statusRecord) *int64, pct func(statusRecord) *float64) (*float64, *int64) {
	var (
		winReset   int64
		haveWindow bool
	)
	for i := range recs {
		rst := reset(recs[i])
		if rst == nil {
			continue
		}
		if !haveWindow || *rst > winReset {
			winReset, haveWindow = *rst, true
		}
	}
	if !haveWindow {
		return newestPct(recs, capturedTS, pct), nil
	}

	var (
		peak    float64
		havePct bool
	)
	for i := range recs {
		r := recs[i]
		rst := reset(r)
		if rst == nil || *rst != winReset {
			continue
		}
		if p := pct(r); p != nil && (!havePct || *p > peak) {
			peak, havePct = *p, true
		}
	}
	rst := winReset
	if !havePct {
		return nil, &rst
	}
	return &peak, &rst
}

// newestPct returns a copy of the pct on the record with the globally-newest ts, or
// nil. Used only as the no-window fallback in windowPeak (no record carries a reset
// for the window), so the reader still surfaces the newest raw value there.
func newestPct(recs []statusRecord, ts int64, pct func(statusRecord) *float64) *float64 {
	for i := range recs {
		r := recs[i]
		if r.TS == nil || *r.TS != ts {
			continue
		}
		if p := pct(r); p != nil {
			v := *p
			return &v
		}
		return nil
	}
	return nil
}
