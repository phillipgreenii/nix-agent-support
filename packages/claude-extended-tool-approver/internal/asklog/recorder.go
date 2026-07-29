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
	outcome := OutcomePending
	var resolvedAt *string
	if result.Decision == hookio.Reject {
		// The hook refused the call itself, so it is already resolved — but it
		// is NOT a denial: no user was ever asked. OutcomeRejected keeps this
		// distinct from a decline judgement (OutcomeDenied) and from a call that
		// was never resolved at all (OutcomeUnresolved).
		outcome = OutcomeRejected
		now := nowISO()
		resolvedAt = &now
	}

	if result.Trace != nil {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}

		res, err := tx.Exec(
			`
			INSERT INTO tool_decisions
				(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
				 tool_input_hash, tool_input_json, tool_summary,
				 hook_decision, hook_reason, outcome, created_at, resolved_at,
				 sandbox_enabled, permission_mode, prompt_id, transcript_path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			input.SessionID, input.CWD,
			nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
			input.ToolName, nilIfEmpty(input.ToolUseID),
			inputHash(input.ToolInput), string(input.ToolInput),
			ToolSummary(input.ToolName, input.ToolInput),
			hookDec, result.Reason,
			outcome, nowISO(), resolvedAt,
			s.sandboxEnabledArg(),
			nilIfEmpty(input.PermissionMode), nilIfEmpty(input.PromptID),
			nilIfEmpty(input.TranscriptPath),
		)
		if err != nil {
			_ = tx.Rollback()
			return err
		}

		decID, err := res.LastInsertId()
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("last insert id: %w", err)
		}

		for i, entry := range result.Trace {
			_, err := tx.Exec(
				`
				INSERT INTO decision_trace_entries
					(tool_decision_id, rule_order, rule_name, decision, reason)
				VALUES (?, ?, ?, ?, ?)`,
				decID, i+1, entry.RuleName,
				hookDecisionString(entry.Decision), entry.Reason,
			)
			if err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("insert trace entry %d: %w", i+1, err)
			}
		}

		return tx.Commit()
	}

	// Non-trace path: single INSERT
	_, err := s.db.Exec(
		`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			 tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, hook_reason, outcome, created_at, resolved_at,
			 sandbox_enabled, permission_mode, prompt_id, transcript_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName, nilIfEmpty(input.ToolUseID),
		inputHash(input.ToolInput), string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		hookDec, result.Reason,
		outcome, nowISO(), resolvedAt,
		s.sandboxEnabledArg(),
		nilIfEmpty(input.PermissionMode), nilIfEmpty(input.PromptID),
		nilIfEmpty(input.TranscriptPath),
	)
	return err
}

func RecordPermissionRequest(s *Store, input *hookio.HookInput, permissionSuggestions string) error {
	hash := inputHash(input.ToolInput)

	res, err := s.db.Exec(
		`
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

	_, err = s.db.Exec(
		`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name,
			 tool_input_hash, tool_input_json, tool_summary,
			 permission_suggestions, outcome, created_at, sandbox_enabled,
			 permission_mode, prompt_id, transcript_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName,
		hash, string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		nilIfEmpty(permissionSuggestions),
		nowISO(),
		s.sandboxEnabledArg(),
		nilIfEmpty(input.PermissionMode), nilIfEmpty(input.PromptID),
		nilIfEmpty(input.TranscriptPath),
	)
	return err
}

// ResolveApproved marks the pending row for this tool call as approved and
// records the PostToolUse tool_response. It is a pure UPDATE: it only sets
// outcome/resolved_at/outcome_notes/tool_response and never touches columns set
// at PreToolUse (permission_mode, prompt_id, transcript_path, agent_type). If no
// pending row matches (by tool_use_id, then by hash), there is NO INSERT
// fallback — the tool_response is dropped, since without a PreToolUse row there
// is no context to attach it to.
func ResolveApproved(s *Store, input *hookio.HookInput, outcomeNotes string) error {
	now := nowISO()
	toolResponse := nilIfEmpty(string(input.ToolResponse))

	if input.ToolUseID != "" {
		res, err := s.db.Exec(
			`
			UPDATE tool_decisions
			SET outcome = 'approved', resolved_at = ?, outcome_notes = ?, tool_response = ?
			WHERE tool_use_id = ? AND outcome = 'pending'`,
			now, nilIfEmpty(outcomeNotes), toolResponse, input.ToolUseID,
		)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows > 0 {
			return nil
		}
	}

	hash := inputHash(input.ToolInput)
	_, err := s.db.Exec(
		`
		UPDATE tool_decisions
		SET outcome = 'approved', resolved_at = ?, outcome_notes = ?, tool_response = ?
		WHERE session_id = ? AND tool_name = ? AND tool_input_hash = ? AND outcome = 'pending'`,
		now, nilIfEmpty(outcomeNotes), toolResponse,
		input.SessionID, input.ToolName, hash,
	)
	return err
}

// ResolveUnresolvedAll closes out a session at SessionEnd: every row still
// 'pending' is flipped to OutcomeUnresolved, NOT to OutcomeDenied.
//
// A row is still pending at SessionEnd precisely because nothing ever resolved
// it — the call was interrupted, abandoned, the session died, or the agent moved
// on. Recording that as 'denied' (the pre-pg2-ac3b9 behavior) claimed a user
// decision that never happened, and because a whole session's leftovers are
// swept in one statement it stamped hundreds of rows with one identical
// resolved_at. Downstream that read as "the user denied this but the hook allows
// it" — a phantom false-allow indistinguishable from a real one without
// per-row provenance archaeology.
//
// This is a bulk UPDATE with no per-row correlation: it MUST therefore only ever
// write the non-committal outcome. Real declines arrive through
// RecordPermissionDenied, which correlates by tool_use_id/hash.
func ResolveUnresolvedAll(s *Store, sessionID string) error {
	_, err := s.db.Exec(
		`
		UPDATE tool_decisions
		SET outcome = 'unresolved', resolved_at = ?
		WHERE session_id = ? AND outcome = 'pending'`,
		nowISO(), sessionID,
	)
	return err
}

// RecordPermissionDenied records the PermissionDenied hook event: a decline
// JUDGEMENT was rendered against this specific call, by the user or by the
// auto-mode classifier.
//
// This is the ONLY writer of OutcomeDenied, and it always sets outcome_notes.
// Those two facts together are what make 'denied' mean exactly one thing: a
// hook Reject is OutcomeRejected and a SessionEnd sweep is OutcomeUnresolved.
func RecordPermissionDenied(s *Store, input *hookio.HookInput) error {
	now := nowISO()
	notes := "auto_mode_classifier: " + input.Reason

	if input.ToolUseID != "" {
		res, err := s.db.Exec(
			`
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
	res, err := s.db.Exec(
		`
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

	// Fallback INSERT for an auto-mode-classifier denial with no prior PreToolUse
	// row. This is the primary auto-denial calibration signal, so it MUST capture
	// permission_mode (and agent_type) — otherwise the denial would derive to an
	// approval_source of "unknown", defeating the calibration.
	_, err = s.db.Exec(
		`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			 tool_input_hash, tool_input_json, tool_summary,
			 outcome, outcome_notes, created_at, resolved_at, sandbox_enabled,
			 permission_mode, prompt_id, transcript_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'denied', ?, ?, ?, ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName, nilIfEmpty(input.ToolUseID),
		hash, string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		notes, now, now,
		s.sandboxEnabledArg(),
		nilIfEmpty(input.PermissionMode), nilIfEmpty(input.PromptID),
		nilIfEmpty(input.TranscriptPath),
	)
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
