// Package candidate is Tier 1 of the mistake census (bead pg2-oisvb): it turns
// the canned structural queries into one typed candidate set.
//
// # Why Tier 1 exists at all
//
// The failure census that preceded this can only find what announces itself —
// `is_error == true`. That is the cheap half of the waste: a failed command is
// usually self-correcting inside one round trip and the agent notices unaided.
// The expensive half is invisible to it. Work that SUCCEEDED technically and was
// wrong emits no error, and a correction a PERSON had to type means nothing in the
// harness caught it: no exit code, no schema, no hook.
//
// Detecting that semantically over the whole corpus is not affordable — 405,986
// events. Detecting it structurally is: the nine signals below reduce the corpus
// to ~2,100 candidates, measured, which is a set a semantic pass can afford to be
// careful about. That ratio IS Tier 1's job. It is deliberately tuned for RECALL,
// so a large share of what it returns is not a mistake at all; deciding which is
// Tier 2's job and must not be attempted here.
//
// # Why keyword matching is not one of the signals
//
// It was measured and it does not work. Re-measured over EVERY stored human turn
// rather than a sample — 1,209 of them, `user_text` where text is present — the four
// candidate patterns match a combined 3 turns:
//
//	i said              0
//	why did you         0
//	you should have     1
//	not what i          2
//	any of the four     3   (0.25% of human turns)
//
// So a keyword detector's ceiling on this corpus is 3 detections, against 15
// corrections in a 63-entry gold set. People correct in unboundedly varied phrasing
// and the vocabulary cannot be enumerated. Every signal here is therefore
// STRUCTURAL — a promptSource, a sentinel the harness writes, a JSON field, a
// repeated key, a reversed string — with exactly one exception, `Ack`, which is
// lexical, supplementary, and marked as such in the type system so it cannot quietly
// become primary.
package candidate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/query"
)

// Signal is a Tier 1 detector.
type Signal string

const (
	// TypedTurn is a turn a person actually typed, paired with what the agent had
	// just done. The highest-recall signal and the noisiest.
	TypedTurn Signal = "typed-turn"
	// Interruption is a person cutting the agent off mid-action.
	Interruption Signal = "interruption"
	// Denial is a tool call a person or the permission layer refused.
	Denial Signal = "denied-tool-call"
	// HookRejection is a rejection recorded in the structured hookErrors payload.
	HookRejection Signal = "hook-rejection"
	// HookRefusalBody is a hook- or guard-authored refusal read out of the
	// tool_result BODY (bead pg2-v150u). It is a SEPARATE detector from
	// HookRejection rather than a replacement: that one reads the structured field
	// and is correct, this one reads the prose the refusals actually arrive as, and
	// on today's corpus only this one finds anything.
	HookRefusalBody Signal = "hook-refusal-body"
	// Undo is work that had to be taken back although every call succeeded.
	Undo Signal = "undo"
	// Churn is one file rewritten N+ times in a session.
	Churn Signal = "churn"
	// EscapingRetry is the same command re-issued with only quoting changed.
	EscapingRetry Signal = "escaping-retry"
	// Ack is the agent's own acknowledgment. SUPPLEMENTARY — see AckIsSupplementary.
	Ack Signal = "ack"
)

// AckIsSupplementary states, in code, the constraint the bead makes binding:
// acknowledgment text MUST NOT be a primary detector.
//
// Three independent reasons, all of them measured rather than assumed:
//
//  1. It only fires when the agent NOTICED and said so, so what it measures is an
//     ACKNOWLEDGED mistake rate. Reported as a mistake rate it would make agents
//     getting quieter look like agents getting better.
//  2. Its vocabulary is shaped by the harness system prompt, which suppresses
//     apology — `sorry` and `i was wrong` both measured 0 occurrences — so a prompt
//     change moves the number for reasons unrelated to mistakes.
//  3. The `Correction:` stem (rules M-1..M-3) is FORWARD-ONLY from 2026-07-30, so
//     its series is structurally zero before then, and M-2 forbids the rule from
//     changing acknowledgment FREQUENCY at all. A rise across that boundary is a
//     MARKING artifact and MUST NOT be read as a rise in mistakes.
//
// Set on every Ack candidate, and honoured by ranking, which excludes
// supplementary candidates from a finding's weight.
const AckIsSupplementary = true

// Candidate is one structurally detected thing that MIGHT be a mistake.
type Candidate struct {
	Signal Signal
	// Kind is the signal's sub-discriminator: the undo shape, who denied a call,
	// which acknowledgment form. Empty where a signal has only one shape.
	Kind string
	// Supplementary marks a candidate whose signal is not evidence on its own.
	Supplementary bool
	// Query and QueryVersion record which canned query produced this row, so a
	// candidate set is as self-describing as a query result (T-10).
	Query        string
	QueryVersion int

	SessionID   string
	Path        string
	Seq         int64
	IsSidechain bool
	TS          string
	// StartTS is when the work now suspected of being wrong began; TS is when the
	// evidence landed. SpanMS is their difference and is MEASURED — both timestamps
	// are recorded in the transcript — never estimated.
	//
	// StartTS IS DELIBERATELY EMPTY FOR EVERY SIGNAL WHOSE INTERVAL ENDS AT A HUMAN
	// ACTION, which is a correctness requirement rather than an omission. For a typed
	// turn, an interruption or an acknowledgment, the gap between the agent's last
	// action and the evidence is however long the PERSON took to read, think and
	// type — measured, that sums to 8,390,705,460 ms (2,330 hours) across 719 typed
	// turns, none of which is agent waste. Feeding it into the ranking as "cost"
	// promoted the noisiest signal in the set to the top of the report by three
	// orders of magnitude. A span is only a cost when BOTH endpoints are agent
	// actions: an undo, a churn window, an escaping retry.
	StartTS string
	SpanMS  int64

	// Key is the candidate's UNIQUE identity, used by the gold set and by the
	// classifier to attach a verdict to the right row.
	//
	// (signal, path, seq) is NOT sufficient and this was found by measurement, not by
	// inspection: one Edit can reverse TWO earlier Edits, so `undo-signatures` emits
	// two rows at the same (path, seq), and 63 gold entries matched 64 candidates.
	// Left alone that silently attaches one row's verdict to the other. Extract
	// therefore guarantees uniqueness by appending an ordinal to any repeated base
	// key, which is deterministic because every Tier 1 query has a total ORDER BY.
	Key string

	// Signature is the grouping key: a normalized, stable description of the
	// candidate's SHAPE, so N occurrences of one problem are one finding.
	Signature string
	// Excerpt is the human-readable evidence.
	Excerpt string
	// Detail carries signal-specific fields for a reader or a classifier.
	Detail map[string]string
}

// Set is one Tier 1 extraction.
type Set struct {
	Candidates []Candidate
	Since      string
	Until      string
	// Sources records every query that ran, with its version and row count.
	Sources []Source
}

// Source is one canned query's contribution.
type Source struct {
	Signal  Signal
	Query   string
	Version int
	Rows    int
}

// EmptySignals lists the detectors that produced nothing.
//
// This is reported rather than left implicit because a silently empty detector is
// indistinguishable from a healthy one, and this index has a live example: the
// `hook-rejections` query is CORRECT and returns zero rows corpus-wide, because
// Claude Code writes `hookErrors: []` and puts the rejection in the tool_result
// body instead. Read as "no hook rejected anything" that is simply false.
//
// pg2-v150u added HookRefusalBody, which collects that same signal from the body —
// and it did NOT retire this reporting or special-case the detector it was filed
// against. HookRejection still runs, still returns zero, and is still NAMED as
// empty on every report, because the day the structured field starts arriving is
// the day that zero becomes information; suppressing it would throw away the only
// evidence of the transition. The rule is general: every detector that finds
// nothing is named, whether or not a sibling detector covers the same ground.
func (s Set) EmptySignals() []Signal {
	var out []Signal
	for _, src := range s.Sources {
		if src.Rows == 0 {
			out = append(out, src.Signal)
		}
	}
	return out
}

// Counts is the per-signal tally, in the order the signals were extracted.
func (s Set) Counts() []Source { return s.Sources }

// Options configures an extraction.
type Options struct {
	Since string
	Until string
	// ChurnMin overrides file-churn's N. Zero uses the query's own default.
	ChurnMin int
	// RetryGap overrides escaping-retries' N. Zero uses the query's own default.
	RetryGap int
}

// Extract runs every Tier 1 query and returns one typed candidate set.
//
// The db handle MUST be read-only (store.OpenReadOnly): a census never writes.
func Extract(ctx context.Context, db *sql.DB, opt Options) (Set, error) {
	set := Set{Since: opt.Since, Until: opt.Until}
	churn := ""
	if opt.ChurnMin > 0 {
		churn = fmt.Sprint(opt.ChurnMin)
	}
	retry := ""
	if opt.RetryGap > 0 {
		retry = fmt.Sprint(opt.RetryGap)
	}

	sources := []struct {
		signal Signal
		name   string
		args   []string
		mapper func(row) Candidate
	}{
		{TypedTurn, "typed-turn-candidates", nil, typedTurn},
		{Interruption, "interruptions", nil, interruption},
		{Denial, "denied-tool-calls", nil, denial},
		{HookRejection, "hook-rejections", nil, hookRejection},
		{HookRefusalBody, "hook-refusals-in-body", nil, hookRefusalBody},
		{Undo, "undo-signatures", nil, undo},
		{Churn, "file-churn", []string{churn}, churnCand},
		{EscapingRetry, "escaping-retries", []string{retry}, escapingRetry},
		{Ack, "ack-markers", nil, ack},
	}

	used := map[string]int{}
	for _, src := range sources {
		q, err := query.Lookup(src.name)
		if err != nil {
			return set, err
		}
		req := query.Request{Query: q, Args: src.args, Since: opt.Since, Until: opt.Until, Format: query.FormatJSON}
		res, err := query.Run(ctx, db, req)
		if err != nil {
			return set, fmt.Errorf("tier 1 signal %s: %w", src.signal, err)
		}
		n := 0
		for _, raw := range res.Rows {
			c := src.mapper(newRow(res.Columns, raw))
			c.Signal = src.signal
			c.Query = q.Name
			c.QueryVersion = q.Version
			c.Supplementary = src.signal == Ack
			if c.StartTS != "" && c.TS != "" {
				c.SpanMS = spanMS(c.StartTS, c.TS)
			}
			c.Key = uniqueKey(used, fmt.Sprintf("%s:%s#%d", c.Signal, c.Path, c.Seq))
			set.Candidates = append(set.Candidates, c)
			n++
		}
		set.Sources = append(set.Sources, Source{Signal: src.signal, Query: q.Name, Version: q.Version, Rows: n})
	}
	return set, nil
}

// uniqueKey returns base the first time it is seen and base~N afterwards.
//
// The suffix is only reached when a signal genuinely emits two rows for one
// transcript line, which is a property of the signal rather than a bug — an Edit
// that reverses two earlier Edits IS two findings. What must not happen is the two
// sharing an identity, because then a gold label or a classifier verdict lands on
// whichever of them was written last.
func uniqueKey(used map[string]int, base string) string {
	used[base]++
	if n := used[base]; n > 1 {
		return fmt.Sprintf("%s~%d", base, n)
	}
	return base
}

// row is one query result row addressed by column name.
type row map[string]any

func newRow(cols []string, cells []any) row {
	m := make(row, len(cols))
	for i, c := range cols {
		if i < len(cells) {
			m[c] = cells[i]
		}
	}
	return m
}

func (r row) str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return fmt.Sprint(v)
	case float64:
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func (r row) num(k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func (r row) has(k string) bool { return r[k] != nil }

func (r row) flag(k string) bool { return r.num(k) == 1 }

// spanMS is the measured wall time between two transcript timestamps. An
// unparseable or missing timestamp yields 0 rather than a guess — a fabricated
// duration would propagate into the ranking as if it had been measured.
func spanMS(from, to string) int64 {
	a, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return 0
	}
	b, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return 0
	}
	d := b.Sub(a).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// tools normalizes a group_concat of tool names into a stable grouping token.
func tools(s string) string {
	if s == "" {
		return "none"
	}
	parts := strings.Split(s, ",")
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

func typedTurn(r row) Candidate {
	sig := "typed-turn after " + tools(r.str("prev_tool_names"))
	if !r.has("prev_tool_seq") {
		sig = "typed-turn opening a session"
	} else if r.num("prev_errors") > 0 {
		sig = "typed-turn after failed " + tools(r.str("prev_tool_names"))
		if s := r.str("prev_signature"); s != "" {
			sig += ": " + s
		}
	}
	return Candidate{
		Kind:        r.str("prompt_source"),
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("turn_seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		Signature:   sig,
		Excerpt:     excerpt(r.str("turn_text"), 400),
		Detail: map[string]string{
			"prev_ts":         r.str("prev_ts"),
			"prev_tool_seq":   r.str("prev_tool_seq"),
			"prev_tool_names": r.str("prev_tool_names"),
			"prev_lead_cmds":  r.str("prev_lead_cmds"),
			"prev_errors":     r.str("prev_errors"),
			"prev_signature":  r.str("prev_signature"),
			"seq_gap":         r.str("seq_gap"),
		},
	}
}

func interruption(r row) Candidate {
	return Candidate{
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		Signature:   "interruption during " + tools(r.str("prev_tool_names")),
		Excerpt:     excerpt(r.str("marker"), 120),
		Detail: map[string]string{
			"prev_ts":         r.str("prev_ts"),
			"prev_tool_seq":   r.str("prev_tool_seq"),
			"prev_tool_names": r.str("prev_tool_names"),
			"prev_lead_cmds":  r.str("prev_lead_cmds"),
		},
	}
}

func denial(r row) Candidate {
	kind := r.str("kind")
	sig := kind + ": " + tools(r.str("tool_name"))
	if lc := r.str("lead_cmd"); lc != "" {
		sig += "(" + lc + ")"
	}
	return Candidate{
		Kind:        kind,
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		Signature:   sig,
		Excerpt:     excerpt(r.str("signature"), 240),
		Detail: map[string]string{
			"tool_name": r.str("tool_name"),
			"lead_cmd":  r.str("lead_cmd"),
		},
	}
}

// hookRejection groups by the recorded payload. It has no (path, seq) of its own
// beyond the events row, and the query is an aggregate, so Seq stays 0.
func hookRejection(r row) Candidate {
	return Candidate{
		SessionID: "",
		Signature: "hook-rejection: " + excerpt(r.str("hook_errors"), 160),
		Excerpt:   excerpt(r.str("hook_errors"), 240),
		Detail: map[string]string{
			"events":           r.str("events"),
			"sessions":         r.str("sessions"),
			"subagent":         r.str("subagent"),
			"hook_invocations": r.str("hook_invocations"),
		},
	}
}

// hookRefusalBody is one refusal the hook/guard layer wrote into a result body.
//
// StartTS is left EMPTY deliberately, for the same reason it is on Denial: the
// refusal arrives on the tool_result line, so the only span available is
// tool_use -> tool_result, and for a call that was refused rather than run that
// span is decision latency, not agent work. Such a finding ranks on frequency and
// preventability, which is all the evidence there is.
//
// Signature runs the OPENING through the ingest signature normalizer — the same
// collapsing every error signature gets — so `ls in '/a' was blocked` and
// `ls in '/b' was blocked` are one finding rather than two. Kind leads the
// signature so a broad-brush `blocked` never merges with a `deny-listed`.
func hookRefusalBody(r row) Candidate {
	kind := r.str("kind")
	opening := r.str("opening")
	return Candidate{
		Kind:        kind,
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		Signature:   "hook-refusal/" + kind + ": " + ingest.Signature(opening),
		Excerpt:     excerpt(opening, 320),
		Detail: map[string]string{
			"kind":       kind,
			"tool_name":  r.str("tool_name"),
			"lead_cmd":   r.str("lead_cmd"),
			"opening":    excerpt(opening, 320),
			"error_sig":  r.str("signature"),
			"session_id": r.str("session_id"),
		},
	}
}

func undo(r row) Candidate {
	kind := r.str("kind")
	target := r.str("target")
	// The target is normalized through the ingest signature normalizer, which is
	// the same collapsing (paths, hashes, numbers, bead ids) every error signature
	// gets — so `git reset --hard 1a2b3c` and `git reset --hard 9f8e7d` are one
	// finding rather than two, exactly as they are for error bodies.
	sig := kind + ": " + ingest.Signature(target)
	if kind != "git-undo" {
		// For the file-shaped kinds the path collapses to PATH, which is the right
		// grouping: "a Write later deleted" is one class regardless of which file.
		sig = kind
	}
	return Candidate{
		Kind:        kind,
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		StartTS:     r.str("undone_ts"),
		Signature:   sig,
		Excerpt:     excerpt(target, 240),
		Detail: map[string]string{
			"target":     target,
			"undone_seq": r.str("undone_seq"),
			"detail":     excerpt(r.str("detail"), 240),
		},
	}
}

func churnCand(r row) Candidate {
	fp := r.str("file_path")
	ext := strings.ToLower(filepath.Ext(fp))
	if ext == "" {
		ext = "(no extension)"
	}
	return Candidate{
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("last_seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("last_ts"),
		StartTS:     r.str("first_ts"),
		// Grouped by EXTENSION, not by path: the path is unique per candidate so it
		// would make every churn its own finding, while the extension says which
		// KIND of file the agent keeps failing to get right first time.
		Signature: "churn: *" + ext,
		Excerpt:   fmt.Sprintf("%s edited %s time(s) in one session", fp, r.str("edits")),
		Detail: map[string]string{
			"file_path":  fp,
			"edits":      r.str("edits"),
			"writes":     r.str("writes"),
			"first_seq":  r.str("first_seq"),
			"last_seq":   r.str("last_seq"),
			"session_id": r.str("session_id"),
		},
	}
}

func escapingRetry(r row) Candidate {
	first := r.str("first_cmd")
	return Candidate{
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("retry_seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("retry_ts"),
		StartTS:     r.str("ts"),
		Signature:   "escaping-retry: " + ingest.Signature(first),
		Excerpt:     excerpt(first+"  ⇢  "+r.str("retry_cmd"), 320),
		Detail: map[string]string{
			"first_seq":      r.str("first_seq"),
			"retry_seq":      r.str("retry_seq"),
			"seq_gap":        r.str("seq_gap"),
			"first_is_error": r.str("first_is_error"),
			"retry_is_error": r.str("retry_is_error"),
			"first_cmd":      excerpt(first, 240),
			"retry_cmd":      excerpt(r.str("retry_cmd"), 240),
		},
	}
}

func ack(r row) Candidate {
	kind := r.str("kind")
	return Candidate{
		Kind:        kind,
		SessionID:   r.str("session_id"),
		Path:        r.str("path"),
		Seq:         r.num("seq"),
		IsSidechain: r.flag("is_sidechain"),
		TS:          r.str("ts"),
		Signature:   kind + "/" + r.str("provenance"),
		Excerpt:     excerpt(r.str("excerpt"), 320),
		Detail: map[string]string{
			"provenance": r.str("provenance"),
		},
	}
}
