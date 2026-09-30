package asklog

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"

// RecordPostToolUseFailure records the PostToolUseFailure hook event: the
// tool call was approved and ran, but Claude Code reports that the call
// itself failed. This is NOT a decline judgement (see RecordPermissionDenied,
// OutcomeDenied) and NOT a hook refusal (see RecordPreToolDecision,
// OutcomeRejected) — nobody declined this call, and the hook never blocked
// it. It gets its own OutcomeFailed value (see outcomes.go) rather than
// overloading OutcomeApproved, which specifically means "ran WITHOUT error".
//
// hookio.HookInput has no separate failure-specific struct — a
// PostToolUseFailure event's failure detail arrives in the SAME
// ToolResponse/Reason fields any other event uses (types.go). This mirrors
// two existing patterns rather than inventing a third:
//
//   - RecordPermissionDenied's tool_use_id → hash → INSERT-fallback
//     correlation chain, so a failure is never silently dropped even when no
//     prior PreToolUse row is found (e.g. it was already swept by
//     ResolveUnresolvedAll).
//   - ResolveApproved's tool_response summarization (summarizeToolResponse:
//     hash/size/exit-code-proxy/scrubbed-excerpt, never the raw payload),
//     since the failure payload arrives in the same ToolResponse field
//     ResolveApproved already summarizes.
func RecordPostToolUseFailure(s *Store, input *hookio.HookInput) error {
	now := nowISO()
	notes := "tool_failure"
	if input.Reason != "" {
		notes = "tool_failure: " + input.Reason
	}
	respHash, respSize, respExitCode, respExcerpt := summarizeToolResponse(input.ToolResponse)

	if input.ToolUseID != "" {
		res, err := s.db.Exec(
			`
			UPDATE tool_decisions
			SET outcome = 'failed', resolved_at = ?, outcome_notes = ?,
				tool_response_hash = ?, tool_response_size = ?,
				tool_response_exit_code = ?, tool_response_excerpt = ?
			WHERE tool_use_id = ? AND outcome = 'pending'`,
			now, notes, respHash, respSize, respExitCode, respExcerpt,
			input.ToolUseID,
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
		SET outcome = 'failed', resolved_at = ?, outcome_notes = ?,
			tool_response_hash = ?, tool_response_size = ?,
			tool_response_exit_code = ?, tool_response_excerpt = ?
		WHERE id = (
			SELECT id FROM tool_decisions
			WHERE session_id = ? AND tool_name = ? AND tool_input_hash = ? AND outcome = 'pending'
			ORDER BY id DESC LIMIT 1
		)`,
		now, notes, respHash, respSize, respExitCode, respExcerpt,
		input.SessionID, input.ToolName, hash,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows > 0 {
		return nil
	}

	// Fallback INSERT for a PostToolUseFailure with no matching pending row
	// (e.g. it was already resolved/swept before the failure event arrived).
	// Mirrors RecordPermissionDenied's INSERT fallback so the failure is
	// never silently dropped.
	_, err = s.db.Exec(
		`
		INSERT INTO tool_decisions
			(session_id, cwd, agent_id, agent_type, tool_name, tool_use_id,
			 tool_input_hash, tool_input_json, tool_summary,
			 outcome, outcome_notes, created_at, resolved_at, sandbox_enabled,
			 permission_mode, prompt_id, transcript_path,
			 tool_response_hash, tool_response_size, tool_response_exit_code,
			 tool_response_excerpt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'failed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.SessionID, input.CWD,
		nilIfEmpty(input.AgentID), nilIfEmpty(input.AgentType),
		input.ToolName, nilIfEmpty(input.ToolUseID),
		hash, string(input.ToolInput),
		ToolSummary(input.ToolName, input.ToolInput),
		notes, now, now,
		s.sandboxEnabledArg(),
		nilIfEmpty(input.PermissionMode), nilIfEmpty(input.PromptID),
		nilIfEmpty(input.TranscriptPath),
		respHash, respSize, respExitCode, respExcerpt,
	)
	return err
}
