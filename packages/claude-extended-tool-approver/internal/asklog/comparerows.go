package asklog

import "database/sql"

// CompareRow is one ask-log row as compare-tracker reads it: the lasting
// join key (session_id + tool_use_id) and the fields the comparison checks.
type CompareRow struct {
	ID             int
	SessionID      string
	ToolUseID      string // "" when the row carries no tool_use_id
	ToolName       string
	HookDecision   *string
	Outcome        string
	OutcomeNotes   *string
	PermissionMode *string
	AgentType      *string
	SandboxEnabled sql.NullInt64
	// ExitCode is tool_response_exit_code (the is_error proxy); NULL when no
	// PostToolUse payload was summarized.
	ExitCode  sql.NullInt64
	CreatedAt string
}

// QueryCompareRows returns every row (excluded ones too: the comparison is
// about what was recorded, not what is graded), oldest id first. It shares
// QueryRows' torn-read retry.
func (s *Store) QueryCompareRows() ([]CompareRow, error) {
	var result []CompareRow
	err := s.withTornReadRetry(func() error {
		r, err := s.queryCompareRowsOnce()
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) queryCompareRowsOnce() ([]CompareRow, error) {
	rows, err := s.db.Query(`SELECT id, session_id, COALESCE(tool_use_id, ''), tool_name,
		hook_decision, outcome, outcome_notes, permission_mode, agent_type, sandbox_enabled,
		tool_response_exit_code, created_at
		FROM tool_decisions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CompareRow
	for rows.Next() {
		var r CompareRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.ToolUseID, &r.ToolName, &r.HookDecision,
			&r.Outcome, &r.OutcomeNotes, &r.PermissionMode, &r.AgentType, &r.SandboxEnabled,
			&r.ExitCode, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
