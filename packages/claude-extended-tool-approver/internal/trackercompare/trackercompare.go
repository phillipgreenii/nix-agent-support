// Package trackercompare joins CETA's ask log and the claude-tool-tracker's
// published views on session_id + tool_use_id over a time window and reports
// where they differ, so the operator can decide whether the tracker can take
// over (compare-tracker).
//
// The package is pure: it takes the rows both readers already return
// (asklog.CompareRow, trackeradapter.Row) and the adapter's per-session source
// list and skipped-line counts, and returns a Report. It opens no database.
//
// # Semantics (each is asserted in this package's tests)
//
// Which side recorded a call. CETA recorded a call when the ask log has a row
// with that session_id + tool_use_id. The tracker recorded it when the call has
// a tracker event (Row.TrackerSeen: tracker.call_start); a tracker call that
// only carries CETA's ceta.* events is NOT a tracker recording, so a CETA row
// matched only by those is "only CETA recorded".
//
// Which timestamp selects a call into the window [Since, Until). A CETA row
// by its created_at, a tracker call by its start time (call_start_ts). A key is
// in the window when EITHER side's time is in it, and both sides are then read
// whole, so a call whose two clocks straddle the window edge is compared, never
// reported as only-one-side. The latest CETA row of a key carries the key's
// CETA time.
//
// Duplicate keys. A key with several rows on one side is listed under
// Duplicates (side and count) and the comparison uses the latest row of that
// side (highest id). A CETA row with no tool_use_id cannot be joined; it is
// counted in UnkeyedCeta.
//
// Outcome mapping. Both sides speak CETA's outcome vocabulary: the adapter
// derives the tracker's from its stored settlement (result error -> failed;
// refused settled by ceta -> rejected, refused otherwise -> denied; approved ->
// approved; pending -> pending; anything else -> unresolved) and the outcomes
// are compared for equality.
//
// Verdict. CETA's hook_decision (allow, deny, ask, abstain) against the
// tracker's engine verdict (ceta.verdict "verdict": approve, reject, ask,
// abstain) after mapping approve to allow and reject to deny. A call with no
// ceta.verdict in the tracker has no verdict and differs from any CETA verdict.
// The delivered verdict is not compared: the ask log records the engine's.
//
// Carried fields. permission_mode and agent_type compare as present-or-absent
// plus value; sandbox_enabled as NULL, 0 or 1; is_error only when both sides
// know it (CETA: tool_response_exit_code, else true for a failed outcome, else
// unknown); denial reason by presence (CETA: outcome_notes begins
// "auto_mode_classifier:", the only writer of a denial reason; tracker:
// denial_reason non-empty).
//
// Clean. A report is clean when it lists no only-one-side call, difference,
// duplicate, unkeyed CETA row or session without a tracker file. Skipped-line
// counts are informational and never make a report unclean.
package trackercompare

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
)

// Difference categories (Diff.Category).
const (
	CatVerdict        = "verdict"
	CatOutcome        = "outcome"
	CatPermissionMode = "permission_mode"
	CatAgentType      = "agent_type"
	CatSandboxEnabled = "sandbox_enabled"
	CatIsError        = "is_error"
	CatDenialReason   = "denial_reason"
)

// authoritative note for the denial writer, see the package doc.
const denialNotesPrefix = "auto_mode_classifier:"

// Window selects calls by time. Zero SinceMs and UntilMs of 0 mean unbounded;
// the window is [SinceMs, UntilMs) in milliseconds since the epoch.
type Window struct {
	SinceMs int64
	UntilMs int64
}

func (w Window) contains(ms int64) bool {
	return ms >= w.SinceMs && (w.UntilMs == 0 || ms < w.UntilMs)
}

// Input is everything Compare reads.
type Input struct {
	Window  Window
	Ceta    []asklog.CompareRow
	Tracker []trackeradapter.Row
	// Sources is the adapter's per-session source list.
	Sources []trackeradapter.SessionSource
	// Skips is the adapter's skipped-line count for the window.
	Skips trackeradapter.SkipCounts
}

// CallRef names a call one side recorded and the other did not.
type CallRef struct {
	SessionID string `json:"session_id"`
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name,omitempty"`
	CetaID    int    `json:"ceta_id,omitempty"`
	CallID    int    `json:"call_id,omitempty"`
}

// Diff is one differing field of one call.
type Diff struct {
	SessionID string `json:"session_id"`
	ToolUseID string `json:"tool_use_id"`
	CetaID    int    `json:"ceta_id"`
	CallID    int    `json:"call_id"`
	Category  string `json:"category"`
	Ceta      string `json:"ceta"`
	Tracker   string `json:"tracker"`
}

// Duplicate is a key with several rows on one side.
type Duplicate struct {
	Side      string `json:"side"`
	SessionID string `json:"session_id"`
	ToolUseID string `json:"tool_use_id"`
	Count     int    `json:"count"`
}

// SessionNoTracker is a session with CETA rows in the window and no tracker
// event file.
type SessionNoTracker struct {
	SessionID string `json:"session_id"`
	CetaRows  int    `json:"ceta_rows"`
}

// Report is the result of a comparison.
type Report struct {
	CetaCalls              int                `json:"ceta_calls"`
	TrackerCalls           int                `json:"tracker_calls"`
	Matched                int                `json:"matched"`
	OnlyCeta               []CallRef          `json:"only_ceta"`
	OnlyTracker            []CallRef          `json:"only_tracker"`
	Differences            []Diff             `json:"differences"`
	DifferenceCounts       map[string]int     `json:"difference_counts"`
	Duplicates             []Duplicate        `json:"duplicates"`
	UnkeyedCeta            int                `json:"unkeyed_ceta_rows"`
	SessionsWithoutTracker []SessionNoTracker `json:"sessions_without_tracker"`
	Skips                  SkipCounts         `json:"ingest_skips"`
	Clean                  bool               `json:"clean"`
}

// SkipCounts mirrors trackeradapter.SkipCounts with JSON names.
type SkipCounts struct {
	IngestFiles         int `json:"ingest_files"`
	LinesRead           int `json:"lines_read"`
	EventsNew           int `json:"events_new"`
	EventsDuplicate     int `json:"events_duplicate"`
	SkippedInvalidJSON  int `json:"skipped_invalid_json"`
	SkippedMissingField int `json:"skipped_missing_field"`
	SkippedTotal        int `json:"skipped_total"`
	EventsWithoutCall   int `json:"events_without_call"`
}

type key = asklog.CallKey

// Compare joins the two histories and reports the differences.
func Compare(in Input) (Report, error) {
	rep := Report{
		OnlyCeta:               []CallRef{},
		OnlyTracker:            []CallRef{},
		Differences:            []Diff{},
		DifferenceCounts:       map[string]int{},
		Duplicates:             []Duplicate{},
		SessionsWithoutTracker: []SessionNoTracker{},
		Skips: SkipCounts{
			IngestFiles: in.Skips.Files, LinesRead: in.Skips.LinesRead, EventsNew: in.Skips.EventsNew,
			EventsDuplicate: in.Skips.EventsDuplicate, SkippedInvalidJSON: in.Skips.SkippedInvalidJSON,
			SkippedMissingField: in.Skips.SkippedMissingField, SkippedTotal: in.Skips.SkippedTotal,
			EventsWithoutCall: in.Skips.EventsWithoutCall,
		},
	}

	ceta := map[key]asklog.CompareRow{}
	cetaCount := map[key]int{}
	cetaMs := map[key]int64{}
	unkeyed := map[string]int{} // by session: only for the count; window below
	var unkeyedRows []asklog.CompareRow
	for _, r := range in.Ceta {
		if r.ToolUseID == "" {
			unkeyedRows = append(unkeyedRows, r)
			continue
		}
		k := key{SessionID: r.SessionID, ToolUseID: r.ToolUseID}
		ms, err := parseCreatedAt(r.CreatedAt)
		if err != nil {
			return rep, fmt.Errorf("ceta row %d: %w", r.ID, err)
		}
		ceta[k] = r // rows arrive oldest id first: the latest wins
		cetaCount[k]++
		cetaMs[k] = ms
	}
	for _, r := range unkeyedRows {
		ms, err := parseCreatedAt(r.CreatedAt)
		if err != nil {
			return rep, fmt.Errorf("ceta row %d: %w", r.ID, err)
		}
		if in.Window.contains(ms) {
			rep.UnkeyedCeta++
			unkeyed[r.SessionID]++
		}
	}

	trk := map[key]trackeradapter.Row{}
	trkCount := map[key]int{}
	for _, r := range in.Tracker {
		if !r.TrackerSeen {
			continue // only ceta.* events: the tracker did not record the call
		}
		k := r.Key()
		trk[k] = r // call_id order: the latest wins
		trkCount[k]++
	}

	inWindow := map[key]bool{}
	for k, ms := range cetaMs {
		if in.Window.contains(ms) {
			inWindow[k] = true
		}
	}
	for k, r := range trk {
		if in.Window.contains(r.StartMs) {
			inWindow[k] = true
		}
	}

	keys := make([]key, 0, len(inWindow))
	for k := range inWindow {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].SessionID != keys[j].SessionID {
			return keys[i].SessionID < keys[j].SessionID
		}
		return keys[i].ToolUseID < keys[j].ToolUseID
	})

	cetaSessions := map[string]int{}
	for _, k := range keys {
		c, hasC := ceta[k]
		t, hasT := trk[k]
		if hasC {
			rep.CetaCalls++
			cetaSessions[k.SessionID]++
			if n := cetaCount[k]; n > 1 {
				rep.Duplicates = append(rep.Duplicates, Duplicate{"ceta", k.SessionID, k.ToolUseID, n})
			}
		}
		if hasT {
			rep.TrackerCalls++
			if n := trkCount[k]; n > 1 {
				rep.Duplicates = append(rep.Duplicates, Duplicate{"tracker", k.SessionID, k.ToolUseID, n})
			}
		}
		switch {
		case hasC && !hasT:
			rep.OnlyCeta = append(rep.OnlyCeta, CallRef{SessionID: k.SessionID, ToolUseID: k.ToolUseID, ToolName: c.ToolName, CetaID: c.ID})
		case hasT && !hasC:
			rep.OnlyTracker = append(rep.OnlyTracker, CallRef{SessionID: k.SessionID, ToolUseID: k.ToolUseID, ToolName: t.ToolName, CallID: t.CallID})
		default:
			rep.Matched++
			for _, d := range diffPair(c, t) {
				d.SessionID, d.ToolUseID, d.CetaID, d.CallID = k.SessionID, k.ToolUseID, c.ID, t.CallID
				rep.Differences = append(rep.Differences, d)
				rep.DifferenceCounts[d.Category]++
			}
		}
	}

	hasTrackerFile := map[string]bool{}
	for _, s := range in.Sources {
		hasTrackerFile[s.SessionID] = s.HasTracker
	}
	sessions := make([]string, 0, len(cetaSessions))
	for s := range cetaSessions {
		sessions = append(sessions, s)
	}
	for s := range unkeyed {
		if cetaSessions[s] == 0 {
			sessions = append(sessions, s)
		}
	}
	sort.Strings(sessions)
	for _, s := range sessions {
		if !hasTrackerFile[s] {
			rep.SessionsWithoutTracker = append(rep.SessionsWithoutTracker, SessionNoTracker{SessionID: s, CetaRows: cetaSessions[s] + unkeyed[s]})
		}
	}

	rep.Clean = len(rep.OnlyCeta) == 0 && len(rep.OnlyTracker) == 0 && len(rep.Differences) == 0 &&
		len(rep.Duplicates) == 0 && rep.UnkeyedCeta == 0 && len(rep.SessionsWithoutTracker) == 0
	return rep, nil
}

// diffPair compares one matched call; the caller fills the identity fields.
func diffPair(c asklog.CompareRow, t trackeradapter.Row) []Diff {
	var out []Diff
	add := func(cat, cv, tv string) {
		if cv != tv {
			out = append(out, Diff{Category: cat, Ceta: show(cv), Tracker: show(tv)})
		}
	}
	add(CatVerdict, deref(c.HookDecision), deref(t.HookDecision))
	add(CatOutcome, c.Outcome, t.Outcome)
	add(CatPermissionMode, presence(c.PermissionMode), presence(t.PermissionMode))
	add(CatAgentType, presence(c.AgentType), presence(t.AgentType))
	add(CatSandboxEnabled, nullInt(c.SandboxEnabled), nullInt(t.SandboxEnabled))
	if ce, known := cetaIsError(c); known && t.IsError != nil {
		add(CatIsError, fmt.Sprint(ce), fmt.Sprint(*t.IsError))
	}
	add(CatDenialReason, fmt.Sprint(c.OutcomeNotes != nil && strings.HasPrefix(*c.OutcomeNotes, denialNotesPrefix)),
		fmt.Sprint(t.DenialReason != ""))
	return out
}

func cetaIsError(c asklog.CompareRow) (bool, bool) {
	if c.ExitCode.Valid {
		return c.ExitCode.Int64 != 0, true
	}
	if c.Outcome == asklog.OutcomeFailed {
		return true, true
	}
	return false, false
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// presence distinguishes NULL from the empty string.
func presence(p *string) string {
	if p == nil {
		return "\x00null"
	}
	return *p
}

func nullInt(n sql.NullInt64) string {
	if !n.Valid {
		return "\x00null"
	}
	return fmt.Sprint(n.Int64)
}

// show renders a compared value for a report: absent values read "(none)".
func show(v string) string {
	if v == "" || v == "\x00null" {
		return "(none)"
	}
	return v
}
