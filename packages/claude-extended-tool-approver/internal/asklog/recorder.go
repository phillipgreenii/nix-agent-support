package asklog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func inputHash(toolInput json.RawMessage) string {
	h := sha256.Sum256(toolInput)
	return fmt.Sprintf("%x", h)
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func hookDecisionString(d hookio.Decision) string {
	switch d {
	case hookio.Reject:
		return "deny"
	case hookio.Approve:
		return "allow"
	case hookio.Ask:
		return "ask"
	case hookio.Abstain:
		return "abstain"
	default:
		return "unknown"
	}
}

func RecordPreToolDecision(s *Store, input *hookio.HookInput, result hookio.RuleResult) error {
	hookDec := hookDecisionString(result.Decision)
	outcome := "pending"
	var resolvedAt *string
	if result.Decision == hookio.Reject {
		outcome = "denied"
		now := nowISO()
		resolvedAt = &now
	}

	if result.Trace != nil {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}

		res, err := tx.Exec(`
			INSERT INTO tool_decisions
				(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
				 tool_input_hash, tool_input_json, tool_summary,
				 hook_decision, hook_reason, outcome, created_at, resolved_at,
				 sandbox_enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			input.SessionID, input.CWD,
			nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
			input.ToolName, nilIfEmpty(input.ToolUseID),
			inputHash(input.ToolInput), string(input.ToolInput),
			ToolSummary(input.ToolName, input.ToolInput),
			hookDec, result.Reason,
			outcome, nowISO(), resolvedAt,
			s.sandboxEnabledArg(),
		)
		if err != nil {
			tx.Rollback()
			return err
		}

		decID, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("last insert id: %w", err)
		}

		for i, entry := range result.Trace {
			_, err := tx.Exec(`
				INSERT INTO decision_trace_entries
					(tool_decision_id, rule_order, rule_name, decision, reason)
				VALUES (?, ?, ?, ?, ?)`,
				decID, i+1, entry.RuleName,
				hookDecisionString(entry.Decision), entry.Reason,
			)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("insert trace entry %d: %w", i+1, err)
			}
		}

		return tx.Commit()
	}

	// Non-trace path: single INSERT
	_, err := s.db.Exec(`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			 tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, hook_reason, outcome, created_at, resolved_at,
			 sandbox_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName, nilIfEmpty(input.ToolUseID),
		inputHash(input.ToolInput), string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		hookDec, result.Reason,
		outcome, nowISO(), resolvedAt,
		s.sandboxEnabledArg(),
	)
	return err
}

func RecordPermissionRequest(s *Store, input *hookio.HookInput, permissionSuggestions string) error {
	hash := inputHash(input.ToolInput)

	res, err := s.db.Exec(`
		UPDATE tool_decisions
		SET permission_suggestions = ?
		WHERE id = (
			SELECT id FROM tool_decisions
			WHERE session_id = ? AND tool_name = ? AND tool_input_hash = ? AND outcome = 'pending'
			ORDER BY id DESC LIMIT 1
		)`,
		nilIfEmpty(permissionSuggestions),
		input.SessionID, input.ToolName, hash,
	)
	if err != nil {
		return err
	}

	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}

	_, err = s.db.Exec(`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name,
			 tool_input_hash, tool_input_json, tool_summary,
			 permission_suggestions, outcome, created_at, sandbox_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName,
		hash, string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		nilIfEmpty(permissionSuggestions),
		nowISO(),
		s.sandboxEnabledArg(),
	)
	return err
}

func ResolveApproved(s *Store, input *hookio.HookInput, outcomeNotes string) error {
	now := nowISO()

	if input.ToolUseID != "" {
		res, err := s.db.Exec(`
			UPDATE tool_decisions
			SET outcome = 'approved', resolved_at = ?, outcome_notes = ?
			WHERE tool_use_id = ? AND outcome = 'pending'`,
			now, nilIfEmpty(outcomeNotes), input.ToolUseID,
		)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows > 0 {
			return nil
		}
	}

	hash := inputHash(input.ToolInput)
	_, err := s.db.Exec(`
		UPDATE tool_decisions
		SET outcome = 'approved', resolved_at = ?, outcome_notes = ?
		WHERE session_id = ? AND tool_name = ? AND tool_input_hash = ? AND outcome = 'pending'`,
		now, nilIfEmpty(outcomeNotes),
		input.SessionID, input.ToolName, hash,
	)
	return err
}

func ResolveDeniedAll(s *Store, sessionID string) error {
	_, err := s.db.Exec(`
		UPDATE tool_decisions
		SET outcome = 'denied', resolved_at = ?
		WHERE session_id = ? AND outcome = 'pending'`,
		nowISO(), sessionID,
	)
	return err
}

func RecordPermissionDenied(s *Store, input *hookio.HookInput) error {
	now := nowISO()
	notes := "auto_mode_classifier: " + input.Reason

	if input.ToolUseID != "" {
		res, err := s.db.Exec(`
			UPDATE tool_decisions
			SET outcome = 'denied', resolved_at = ?, outcome_notes = ?
			WHERE tool_use_id = ? AND outcome = 'pending'`,
			now, notes, input.ToolUseID,
		)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows > 0 {
			return nil
		}
	}

	hash := inputHash(input.ToolInput)
	res, err := s.db.Exec(`
		UPDATE tool_decisions
		SET outcome = 'denied', resolved_at = ?, outcome_notes = ?
		WHERE session_id = ? AND tool_name = ? AND tool_input_hash = ? AND outcome = 'pending'`,
		now, notes,
		input.SessionID, input.ToolName, hash,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows > 0 {
		return nil
	}

	_, err = s.db.Exec(`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			 tool_input_hash, tool_input_json, tool_summary,
			 outcome, outcome_notes, created_at, resolved_at, sandbox_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'denied', ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName, nilIfEmpty(input.ToolUseID),
		hash, string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		notes, now, now,
		s.sandboxEnabledArg(),
	)
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
