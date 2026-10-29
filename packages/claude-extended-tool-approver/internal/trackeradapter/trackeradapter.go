// Package trackeradapter reads the claude-tool-tracker's SQLite database and
// maps its published views onto the row type evaluate, report and show already
// use (asklog.DecisionRow). It is the ONE place CETA knows the tracker's
// schema; nothing else in CETA depends on it, and CETA's hook decisions never
// open the tracker database (only the offline commands use this package).
//
// Contract with the tracker: the adapter reads SQL VIEWS only (tool_calls,
// ingest_skips, session_sources), as documented in
// packages/claude-tool-tracker/docs/views.md, and does not import the tracker's
// Go code. The database is opened read-only (SQLite mode=ro) and never written.
// A missing, unreadable or unexpectedly shaped database is an error from Open,
// never an empty result.
//
// # Exported API
//
//   - DefaultPath() string: the tracker database path ($XDG_STATE_HOME or
//     ~/.local/state, then claude-tool-tracker/tracker.db).
//   - Open(path) (*Adapter, error), (*Adapter).Close().
//   - (*Adapter).Rows(since, labels) ([]Row, error): every call, oldest call_id
//     first, minus the calls the labels exclude; since is "" or an RFC 3339
//     timestamp or a YYYY-MM-DD date, compared to the call's start time.
//   - (*Adapter).RowsByIDs(ids, labels) ([]Row, error): by call_id, excluded
//     calls included (what show needs).
//   - (*Adapter).Key(callID) (asklog.CallKey, bool, error): the lasting
//     session_id + tool_use_id identity under which operator labels are stored.
//   - (*Adapter).SkippedCounts(fromMs, toMs) (SkipCounts, error): the ingest
//     skipped-line counts of every ingest run whose ingest_ms is in the window.
//   - (*Adapter).SessionSources() ([]SessionSource, error) and
//     (*Adapter).SourcesFor(sessionID) (SessionSource, bool, error): per
//     session, which sources (tracker, ceta) have event files.
//
// # Column to field mapping
//
// Row embeds asklog.DecisionRow, so the three commands consume it unchanged.
//
//	tool_calls.call_id            -> DecisionRow.ID, Row.CallID
//	tool_calls.session_id         -> DecisionRow.SessionID
//	tool_calls.tool_use_id        -> Row.ToolUseID
//	tool_calls.cwd                -> DecisionRow.CWD ("" when NULL)
//	tool_calls.tool_name          -> DecisionRow.ToolName ("" when NULL)
//	tool_calls.tool_input         -> DecisionRow.ToolInputJSON ("" when NULL),
//	                                 and, through asklog.ToolSummary, ToolSummary
//	tool_calls.engine_verdict     -> Row.EngineVerdict (approve|ask|reject|abstain)
//	                                 and DecisionRow.HookDecision in CETA's
//	                                 ask-log words (approve=allow, reject=deny,
//	                                 ask, abstain); nil when CETA wrote no verdict
//	tool_calls.delivered_verdict  -> Row.DeliveredVerdict (same words as engine)
//	tool_calls.sandbox_enabled    -> DecisionRow.SandboxEnabled
//	tool_calls.permission_mode    -> DecisionRow.PermissionMode
//	tool_calls.agent_type         -> DecisionRow.AgentType
//	tool_calls.error_text         -> DecisionRow.OutcomeNotes
//	tool_calls.is_error           -> Row.IsError
//	tool_calls.denial_reason      -> Row.DenialReason (and OutcomeNotes when
//	                                 error_text is NULL)
//	tool_calls.reason             -> Row.Reason (show's hook reason)
//	tool_calls.result             -> Row.Result
//	tool_calls.call_start_ts      -> Row.StartMs (first_event_ts when NULL)
//	                                 and Row.TrackerSeen (call_start_ts is not NULL)
//	tool_calls.settled_by         -> Row.SettledBy
//	tool_calls.settlement         -> Row.Settlement
//	tool_calls.converted_ask      -> Row.ConvertedAsk
//	tool_calls.failed_timing      -> Row.FailedTiming
//
// DecisionRow.Outcome is derived from the stored settlement, in the ask log's
// vocabulary: result error -> failed; settlement refused -> rejected when CETA
// settled it, else denied; approved -> approved; pending -> pending; anything
// else (unresolved) -> unresolved. DecisionRow.PromptID is "person" when
// settled_by is person (so asklog.ApprovalSource sees a prompt) and nil
// otherwise. DecisionRow.CorrectDec and Excluded come from the label join
// (CETA's own database, keyed by session_id + tool_use_id), never from the
// tracker. ToolResponse is always nil: the tracker never keeps tool responses.
package trackeradapter

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"modernc.org/sqlite"
)

var _ = sqlite.Driver{} // registers the "sqlite" driver

// views lists, per published view, the columns the adapter reads. A database
// whose views lack any of them is "unexpectedly shaped" and Open fails.
var views = map[string][]string{
	"tool_calls": {
		"call_id", "session_id", "tool_use_id", "agent_id", "tool_name", "tool_input", "cwd",
		"permission_mode", "agent_type", "result", "error_text", "call_start_ts", "first_event_ts",
		"is_error", "denial_reason", "sandbox_enabled", "engine_verdict", "delivered_verdict",
		"reason", "settled_by", "settlement", "converted_ask", "failed_timing",
	},
	"ingest_skips": {
		"ingest_ms", "lines_read", "events_new", "events_duplicate", "skipped_invalid_json",
		"skipped_missing_field", "skipped_total", "events_without_call",
	},
	"session_sources": {
		"session_id", "sources", "has_tracker", "has_ceta", "tracker_events", "ceta_events",
		"first_ts", "last_ts",
	},
}

// DefaultPath is the tracker database's default location.
func DefaultPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "claude-tool-tracker", "tracker.db")
}

// Adapter is an open, read-only view of a tracker database.
type Adapter struct {
	db *sql.DB
}

// Open opens the tracker database read-only and checks that the published
// views carry the columns the adapter reads.
func Open(path string) (*Adapter, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("tracker database: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("tracker database %s is a directory", path)
	}
	esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	db, err := sql.Open("sqlite", "file:"+esc+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("tracker database %s: %w", path, err)
	}
	a := &Adapter{db: db}
	names := make([]string, 0, len(views))
	for name := range views {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		q := fmt.Sprintf("SELECT %s FROM %s LIMIT 0", strings.Join(views[name], ", "), name)
		rows, err := db.Query(q)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tracker database %s is not shaped as expected (view %s): %w; run claude-tool-tracker ingest to (re)create the views", path, name, err)
		}
		_ = rows.Close()
	}
	return a, nil
}

// Close closes the database.
func (a *Adapter) Close() error { return a.db.Close() }

// Row is one tracker call in the evaluation row type, plus the tracker-only
// fields. See the package doc for the column mapping.
type Row struct {
	asklog.DecisionRow
	CallID           int
	ToolUseID        string
	EngineVerdict    string
	DeliveredVerdict string
	Result           string
	Reason           string
	SettledBy        string
	Settlement       string
	ConvertedAsk     bool
	IsError          *bool
	FailedTiming     *bool
	DenialReason     string
	StartMs          int64
	// TrackerSeen is true when a tracker event (tracker.call_start) recorded the
	// call, false for a call only another source (ceta) reported.
	TrackerSeen bool
	// Label is the operator label joined in for this call (zero when none).
	Label asklog.TrackerLabel
}

// Key is the lasting identity of the call.
func (r Row) Key() asklog.CallKey {
	return asklog.CallKey{SessionID: r.SessionID, ToolUseID: r.ToolUseID}
}

const rowSelect = `SELECT call_id, session_id, tool_use_id, tool_name, tool_input, cwd,
	permission_mode, agent_type, result, error_text, call_start_ts, first_event_ts, is_error,
	denial_reason, sandbox_enabled, engine_verdict, delivered_verdict, reason, settled_by,
	settlement, converted_ask, failed_timing FROM tool_calls`

// Rows returns every call recorded since the given time, oldest call first,
// without the calls the labels exclude (as asklog's QueryRows does).
func (a *Adapter) Rows(since string, labels map[asklog.CallKey]asklog.TrackerLabel) ([]Row, error) {
	var sinceMs int64
	if since != "" {
		t, err := parseSince(since)
		if err != nil {
			return nil, err
		}
		sinceMs = t.UnixMilli()
	}
	all, err := a.query(rowSelect+" ORDER BY call_id", nil, labels)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if r.Label.Excluded || (since != "" && r.StartMs < sinceMs) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// RowsByIDs returns the calls with the given call_ids, excluded ones included.
func (a *Adapter) RowsByIDs(ids []int, labels map[asklog.CallKey]asklog.TrackerLabel) ([]Row, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	return a.query(rowSelect+" WHERE call_id IN ("+strings.Join(marks, ",")+") ORDER BY call_id", args, labels)
}

// Key resolves a call_id to the call's lasting identity.
func (a *Adapter) Key(callID int) (asklog.CallKey, bool, error) {
	var k asklog.CallKey
	err := a.db.QueryRow(`SELECT session_id, tool_use_id FROM tool_calls WHERE call_id = ?`, callID).Scan(&k.SessionID, &k.ToolUseID)
	if err == sql.ErrNoRows {
		return k, false, nil
	}
	if err != nil {
		return k, false, fmt.Errorf("tracker database: %w", err)
	}
	return k, true, nil
}

func parseSince(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: want an RFC 3339 timestamp or a YYYY-MM-DD date", s)
}

func (a *Adapter) query(q string, args []any, labels map[asklog.CallKey]asklog.TrackerLabel) ([]Row, error) {
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("tracker database: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Row
	for rows.Next() {
		var r Row
		var (
			toolName, toolInput, cwd, mode, agentType, result, errText, denial sql.NullString
			engine, delivered, reason, settledBy, settlement                   sql.NullString
			startTs, firstTs, isErr, sandbox, converted, failedTiming          sql.NullInt64
		)
		if err := rows.Scan(&r.CallID, &r.SessionID, &r.ToolUseID, &toolName, &toolInput, &cwd,
			&mode, &agentType, &result, &errText, &startTs, &firstTs, &isErr,
			&denial, &sandbox, &engine, &delivered, &reason, &settledBy,
			&settlement, &converted, &failedTiming); err != nil {
			return nil, fmt.Errorf("tracker database: %w", err)
		}
		r.ID = r.CallID
		r.ToolName = toolName.String
		r.ToolInputJSON = toolInput.String
		r.CWD = cwd.String
		r.ToolSummary = asklog.ToolSummary(r.ToolName, json.RawMessage(r.ToolInputJSON))
		r.SandboxEnabled = sandbox
		r.PermissionMode = strPtr(mode)
		r.AgentType = strPtr(agentType)
		r.OutcomeNotes = strPtr(errText)
		if !errText.Valid && denial.Valid {
			r.OutcomeNotes = strPtr(denial)
		}
		r.EngineVerdict = engine.String
		r.DeliveredVerdict = delivered.String
		r.Result = result.String
		r.Reason = reason.String
		r.SettledBy = settledBy.String
		r.Settlement = settlement.String
		r.ConvertedAsk = converted.Int64 != 0
		r.DenialReason = denial.String
		if isErr.Valid {
			b := isErr.Int64 != 0
			r.IsError = &b
		}
		if failedTiming.Valid {
			b := failedTiming.Int64 != 0
			r.FailedTiming = &b
		}
		r.TrackerSeen = startTs.Valid
		r.StartMs = startTs.Int64
		if !startTs.Valid {
			r.StartMs = firstTs.Int64
		}
		if engine.Valid {
			s := hookDecisionWord(engine.String)
			r.HookDecision = &s
		}
		r.Outcome = outcomeOf(r)
		if r.SettledBy == "person" {
			p := "person"
			r.PromptID = &p
		}
		if l, ok := labels[r.Key()]; ok {
			r.Label = l
			r.CorrectDec = l.CorrectDec
			if l.Excluded {
				r.Excluded = 1
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tracker database: %w", err)
	}
	return out, nil
}

func strPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// hookDecisionWord maps the engine's verdict words onto the ask log's.
func hookDecisionWord(v string) string {
	switch v {
	case "approve":
		return "allow"
	case "reject":
		return "deny"
	default:
		return v
	}
}

func outcomeOf(r Row) string {
	switch {
	case r.Result == "error":
		return asklog.OutcomeFailed
	case r.Settlement == "refused":
		if r.SettledBy == "ceta" {
			return asklog.OutcomeRejected
		}
		return asklog.OutcomeDenied
	case r.Settlement == "approved":
		return asklog.OutcomeApproved
	case r.Settlement == "pending":
		return asklog.OutcomePending
	default:
		return asklog.OutcomeUnresolved
	}
}

// SkipCounts totals the ingest runs in a time window.
type SkipCounts struct {
	Files               int
	LinesRead           int
	EventsNew           int
	EventsDuplicate     int
	SkippedInvalidJSON  int
	SkippedMissingField int
	SkippedTotal        int
	EventsWithoutCall   int
}

// SkippedCounts sums the ingest_skips rows with fromMs <= ingest_ms <= toMs.
func (a *Adapter) SkippedCounts(fromMs, toMs int64) (SkipCounts, error) {
	var c SkipCounts
	err := a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(lines_read),0), COALESCE(SUM(events_new),0),
		COALESCE(SUM(events_duplicate),0), COALESCE(SUM(skipped_invalid_json),0),
		COALESCE(SUM(skipped_missing_field),0), COALESCE(SUM(skipped_total),0),
		COALESCE(SUM(events_without_call),0)
		FROM ingest_skips WHERE ingest_ms BETWEEN ? AND ?`, fromMs, toMs).Scan(
		&c.Files, &c.LinesRead, &c.EventsNew, &c.EventsDuplicate, &c.SkippedInvalidJSON,
		&c.SkippedMissingField, &c.SkippedTotal, &c.EventsWithoutCall,
	)
	if err != nil {
		return c, fmt.Errorf("tracker database: %w", err)
	}
	return c, nil
}

// SessionSource says which sources have event files for one session.
type SessionSource struct {
	SessionID     string
	Sources       []string
	HasTracker    bool
	HasCeta       bool
	TrackerEvents int
	CetaEvents    int
	FirstMs       int64
	LastMs        int64
}

const sourcesSelect = `SELECT session_id, sources, has_tracker, has_ceta, tracker_events, ceta_events,
	COALESCE(first_ts,0), COALESCE(last_ts,0) FROM session_sources`

func scanSources(rows *sql.Rows) ([]SessionSource, error) {
	defer func() { _ = rows.Close() }()
	var out []SessionSource
	for rows.Next() {
		var s SessionSource
		var list string
		var ht, hc int
		if err := rows.Scan(&s.SessionID, &list, &ht, &hc, &s.TrackerEvents, &s.CetaEvents, &s.FirstMs, &s.LastMs); err != nil {
			return nil, fmt.Errorf("tracker database: %w", err)
		}
		if list != "" {
			s.Sources = strings.Split(list, ",")
		}
		s.HasTracker, s.HasCeta = ht != 0, hc != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionSources lists every session with at least one event file.
func (a *Adapter) SessionSources() ([]SessionSource, error) {
	rows, err := a.db.Query(sourcesSelect + " ORDER BY session_id")
	if err != nil {
		return nil, fmt.Errorf("tracker database: %w", err)
	}
	return scanSources(rows)
}

// SourcesFor answers, for one session, which sources have event files.
func (a *Adapter) SourcesFor(sessionID string) (SessionSource, bool, error) {
	rows, err := a.db.Query(sourcesSelect+" WHERE session_id = ?", sessionID)
	if err != nil {
		return SessionSource{}, false, fmt.Errorf("tracker database: %w", err)
	}
	list, err := scanSources(rows)
	if err != nil || len(list) == 0 {
		return SessionSource{}, false, err
	}
	return list[0], true, nil
}
