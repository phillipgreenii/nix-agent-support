package asklog

import (
	"database/sql"
	"fmt"
)

// ArchiveRow is the FULL tool_decisions row — every column, including the
// v5+ fields that QueryRows and QueryRowsByIDs each expose only a subset
// of. The archiving ceremony's whole point (bd pg2-riwdh) is capturing a
// representative sample of rows BEFORE they are deleted, so it reads the
// WHOLE row rather than reusing either narrower shape. In particular
// CorrectHookDecision / CorrectHookDecisionExplanation — the ground-truth
// annotation a human may have set via `set-correct-decision` — MUST survive
// into the archived fixture; losing it would silently turn a graded
// regression case back into an ungraded one, which is the exact failure
// mode the bead's acceptance criteria calls out by name.
type ArchiveRow struct {
	ID                             int
	SessionID                      string
	CWD                            string
	AgentID                        *string
	AgentType                      *string
	ToolName                       string
	ToolUseID                      *string
	ToolInputHash                  string
	ToolInputJSON                  string
	ToolSummary                    *string
	HookDecision                   *string
	HookReason                     *string
	PermissionSuggestions          *string
	Outcome                        string
	OutcomeNotes                   *string
	CreatedAt                      string
	ResolvedAt                     *string
	Excluded                       int
	ExcludedReason                 *string
	CorrectHookDecision            *string
	CorrectHookDecisionExplanation *string
	SandboxEnabled                 sql.NullInt64
	PermissionMode                 *string
	PromptID                       *string
	ToolResponse                   *string
	TranscriptPath                 *string
}

// QueryRowsBefore returns every column of every tool_decisions row whose
// created_at is strictly before the given ISO8601 threshold, ordered by id.
// It is the read half of the archiving ceremony: a caller MUST run this
// (and act on the result, e.g. sample it to fixtures) BEFORE calling
// DeleteRowsBefore with the same threshold, or the rows it would have
// captured are gone.
func (s *Store) QueryRowsBefore(before string) ([]ArchiveRow, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason,
			permission_suggestions, outcome, outcome_notes, created_at, resolved_at,
			excluded, excluded_reason, correct_hook_decision, correct_hook_decision_explanation,
			sandbox_enabled, permission_mode, prompt_id, tool_response, transcript_path
		FROM tool_decisions
		WHERE created_at < ?
		ORDER BY id`, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []ArchiveRow
	for rows.Next() {
		var r ArchiveRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.CWD, &r.AgentID, &r.AgentType, &r.ToolName, &r.ToolUseID,
			&r.ToolInputHash, &r.ToolInputJSON, &r.ToolSummary, &r.HookDecision, &r.HookReason,
			&r.PermissionSuggestions, &r.Outcome, &r.OutcomeNotes, &r.CreatedAt, &r.ResolvedAt,
			&r.Excluded, &r.ExcludedReason, &r.CorrectHookDecision, &r.CorrectHookDecisionExplanation,
			&r.SandboxEnabled, &r.PermissionMode, &r.PromptID, &r.ToolResponse, &r.TranscriptPath); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteRowsBefore deletes every tool_decisions row whose created_at is
// strictly before the given ISO8601 threshold and returns how many rows
// were removed.
//
// decision_trace_entries for those rows are removed automatically:
// NewStore's connection runs with `PRAGMA foreign_keys = ON`, and that
// table's tool_decision_id column is declared `REFERENCES
// tool_decisions(id) ON DELETE CASCADE` (migration version 3) — so this one
// DELETE is sufficient, no second statement is needed.
//
// rule_errors rows are deliberately NOT cascaded or otherwise touched: that
// table carries no foreign key at all, by design (see migration version
// 8's comment — it is a failure-observability sink meant to survive even
// when its anchoring decision row never existed), so archiving
// tool_decisions does not obsolete it.
func (s *Store) DeleteRowsBefore(before string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tool_decisions WHERE created_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("delete rows before %s: %w", before, err)
	}
	return res.RowsAffected()
}

// Vacuum runs SQLite's VACUUM, which rebuilds the database file to reclaim
// space DeleteRowsBefore freed. It is deliberately a separate method — never
// called automatically by DeleteRowsBefore — so a caller can run it exactly
// once, as the LAST step of a ceremony that may batch several deletes, and
// can skip it entirely when nothing was actually removed: VACUUM against a
// database with no freelist to reclaim is a wasted, lock-taking no-op (this
// is exactly the diagnosis bd pg2-riwdh recorded against the live corpus
// before deciding VACUUM must only run as part of this ceremony, never
// unconditionally).
func (s *Store) Vacuum() error {
	_, err := s.db.Exec("VACUUM")
	return err
}
