// Package limits owns the account-global rate_limits (5h / 7d) window-peak fold
// (ADR 0021 §1, ADR 0029). It is a pure leaf: the status-line wrapper
// (capture-status.bash) appends per-session records to
// <ClaudeHome>/projects/**/<session_id>.status.jsonl, and this package parses one
// such file's lines (ReadStatusRecords) and collapses a set of records into the
// current-window reading (Current). Discovery of which files to read lives in the
// caller (the daemon's SiblingLimitsSource, or the corpus Monitor's status tail),
// so this package imports only the standard library and can be consumed by both
// internal/daemon and internal/core/corpus without an import cycle.
package limits

import (
	"encoding/json"
	"os"
	"time"
)

// Limits is the current account-global rate_limits reading (ADR 0021 §1). Every
// field is independently optional: a nil *float64 or a zero time.Time means
// "unknown/stale", explicitly distinct from a real 0% reading or a 1970
// timestamp.
type Limits struct {
	FiveHourPct      *float64
	FiveHourResetsAt time.Time
	SevenDayPct      *float64
	SevenDayResetsAt time.Time
	CapturedAt       time.Time
}

// Record is the on-disk shape written by capture-status.bash (one line of a
// <session_id>.status.jsonl sibling). Every field is a pointer / optional so
// "absent" is distinguishable from a real 0 — a nil *float64 stays nil; a missing
// epoch stays a zero time.Time.
type Record struct {
	TS               *int64   `json:"ts"`
	FiveHourPct      *float64 `json:"five_hour_pct"`
	FiveHourResetsAt *int64   `json:"five_hour_resets_at"`
	SevenDayPct      *float64 `json:"seven_day_pct"`
	SevenDayResetsAt *int64   `json:"seven_day_resets_at"`
}

// ReadStatusRecords parses every line of one status.jsonl and returns each
// parseable, ts-bearing record. Malformed / ts-less lines are skipped, not fatal;
// an unreadable file yields nil. Reads the whole file (files are tiny) and
// hand-splits on '\n' to avoid bufio.Scanner's default token-size limit.
func ReadStatusRecords(path string) []Record {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Record
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
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.TS == nil {
			continue // a record without ts cannot be ordered — skip
		}
		out = append(out, rec)
	}
	return out
}

// Current collapses all parsed status records into the account-global
// current-window Limits (ADR 0029), or nil when recs is empty. CapturedAt is the
// newest ts across every record (reading-stream liveness — NOT the instant the
// reported peak was captured). Each window (5h keyed by FiveHourResetsAt, 7d by
// SevenDayResetsAt) reports its PEAK used_percentage independently; see
// windowPeak. It MUST NOT correlate by session_id and MUST NOT substitute 0 for
// an absent value.
func Current(recs []Record) *Limits {
	if len(recs) == 0 {
		return nil
	}
	var capturedTS int64
	for i := range recs {
		if recs[i].TS != nil && *recs[i].TS > capturedTS {
			capturedTS = *recs[i].TS
		}
	}
	l := &Limits{CapturedAt: time.Unix(capturedTS, 0)}

	fivePct, fiveReset := windowPeak(recs, capturedTS,
		func(r Record) *int64 { return r.FiveHourResetsAt },
		func(r Record) *float64 { return r.FiveHourPct })
	l.FiveHourPct = fivePct
	if fiveReset != nil {
		l.FiveHourResetsAt = time.Unix(*fiveReset, 0)
	}

	sevenPct, sevenReset := windowPeak(recs, capturedTS,
		func(r Record) *int64 { return r.SevenDayResetsAt },
		func(r Record) *float64 { return r.SevenDayPct })
	l.SevenDayPct = sevenPct
	if sevenReset != nil {
		l.SevenDayResetsAt = time.Unix(*sevenReset, 0)
	}
	return l
}

// windowPeak computes one window's current used_percentage and reset epoch (ADR
// 0029). reset/pct select this window's fields off a record.
//
//   - The CURRENT window is the GREATEST reset epoch observed across all records.
//     Windows only advance (a later window has a later reset), so max-reset always
//     names the newest window. It is deliberately NOT "the reset of the newest-ts
//     record": a lagging session reports an OLD reset with its stale percentage and
//     can render — advancing its ts — after a new window has begun. Keying on ts
//     would let such a straggler mask the new window; keying on max-reset cannot.
//   - The percentage is the MAX non-nil pct among records sharing that reset; nil
//     when the window carries a reset but no pct at all (unknown, never 0). The max
//     is the freshest true reading: each session shows only its last-seen server
//     value, so lower same-window readings are staler snapshots, not real drops.
//   - When NO record carries a reset for this window, the pct falls back to the
//     globally-newest record's value (nil if absent) and the reset is nil
//     (preserving pre-ADR-0029 behavior for that degenerate case).
func windowPeak(recs []Record, capturedTS int64, reset func(Record) *int64, pct func(Record) *float64) (*float64, *int64) {
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

// newestPct returns a copy of the pct on the record with the globally-newest ts,
// or nil. Used only as the no-window fallback in windowPeak (no record carries a
// reset for the window), so the reader still surfaces the newest raw value there.
func newestPct(recs []Record, ts int64, pct func(Record) *float64) *float64 {
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
