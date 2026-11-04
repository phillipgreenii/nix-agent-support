package ingest

import (
	"context"
	"database/sql"
	"fmt"
)

// writeParsed upserts every row of one parsed delta inside tx. Every insert is
// an upsert on the row's natural key, so replaying a range never double-counts.
func writeParsed(ctx context.Context, tx *sql.Tx, path string, parsed parsedFile, withThinking bool) error {
	// byte range can never double-count.
	evStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (path, seq, uuid, parent_uuid, session_id, ts, type, is_sidechain,
			cwd, git_branch, permission_mode, duration_ms, hook_count, hook_errors,
			prompt_source, user_type, source_tool_assistant_uuid, prompt_id, entrypoint)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET
			uuid=excluded.uuid, parent_uuid=excluded.parent_uuid, session_id=excluded.session_id,
			ts=excluded.ts, type=excluded.type, is_sidechain=excluded.is_sidechain, cwd=excluded.cwd,
			git_branch=excluded.git_branch, permission_mode=excluded.permission_mode,
			duration_ms=excluded.duration_ms, hook_count=excluded.hook_count,
			hook_errors=excluded.hook_errors, prompt_source=excluded.prompt_source,
			user_type=excluded.user_type, source_tool_assistant_uuid=excluded.source_tool_assistant_uuid,
			prompt_id=excluded.prompt_id, entrypoint=excluded.entrypoint`)
	if err != nil {
		return fmt.Errorf("prepare events insert: %w", err)
	}
	defer func() { _ = evStmt.Close() }()
	for _, e := range parsed.events {
		if _, err := evStmt.ExecContext(ctx, path, e.seq, e.uuid, e.parentUUID, e.sessionID, e.ts,
			e.typ, e.isSidechain, e.cwd, e.gitBranch, e.permissionMode, e.durationMs, e.hookCount,
			e.hookErrors, e.promptSource, e.userType, e.sourceToolAssistantUUID, e.promptID,
			e.entrypoint); err != nil {
			return fmt.Errorf("insert event %s#%d: %w", path, e.seq, err)
		}
	}

	callStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(tool_use_id) DO UPDATE SET
			path=excluded.path, seq=excluded.seq, tool_name=excluded.tool_name,
			input_json=excluded.input_json, lead_cmd=excluded.lead_cmd`)
	if err != nil {
		return fmt.Errorf("prepare tool_calls insert: %w", err)
	}
	defer func() { _ = callStmt.Close() }()
	for _, c := range parsed.calls {
		if _, err := callStmt.ExecContext(ctx, c.toolUseID, path, c.seq, c.toolName, c.inputJSON, c.leadCmd); err != nil {
			return fmt.Errorf("insert tool_call %s: %w", c.toolUseID, err)
		}
	}

	resStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len, content, signature)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(tool_use_id) DO UPDATE SET
			path=excluded.path, seq=excluded.seq, is_error=excluded.is_error,
			content_len=excluded.content_len, content=excluded.content, signature=excluded.signature`)
	if err != nil {
		return fmt.Errorf("prepare tool_results insert: %w", err)
	}
	defer func() { _ = resStmt.Close() }()
	for _, r := range parsed.results {
		isErr := 0
		if r.isError {
			isErr = 1
		}
		if _, err := resStmt.ExecContext(ctx, r.toolUseID, path, r.seq, isErr, r.contentLen, r.content, r.signature); err != nil {
			return fmt.Errorf("insert tool_result %s: %w", r.toolUseID, err)
		}
	}

	textStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO assistant_text (path, seq, text) VALUES (?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET text=excluded.text`)
	if err != nil {
		return fmt.Errorf("prepare assistant_text insert: %w", err)
	}
	defer func() { _ = textStmt.Close() }()
	for _, t := range parsed.narration {
		if _, err := textStmt.ExecContext(ctx, path, t.seq, t.text); err != nil {
			return fmt.Errorf("insert assistant_text %s#%d: %w", path, t.seq, err)
		}
	}

	userStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO user_text (path, seq, text_len, text, interrupted) VALUES (?,?,?,?,?)
		ON CONFLICT(path, seq) DO UPDATE SET
			text_len=excluded.text_len, text=excluded.text, interrupted=excluded.interrupted`)
	if err != nil {
		return fmt.Errorf("prepare user_text insert: %w", err)
	}
	defer func() { _ = userStmt.Close() }()
	for _, u := range parsed.userText {
		interrupted := 0
		if u.interrupted {
			interrupted = 1
		}
		if _, err := userStmt.ExecContext(ctx, path, u.seq, u.textLen, u.text, interrupted); err != nil {
			return fmt.Errorf("insert user_text %s#%d: %w", path, u.seq, err)
		}
	}

	if withThinking && len(parsed.thinking) > 0 {
		thStmt, err := tx.PrepareContext(ctx, `
			INSERT INTO thinking (path, seq, text) VALUES (?,?,?)
			ON CONFLICT(path, seq) DO UPDATE SET text=excluded.text`)
		if err != nil {
			return fmt.Errorf("prepare thinking insert: %w", err)
		}
		defer func() { _ = thStmt.Close() }()
		for _, t := range parsed.thinking {
			if _, err := thStmt.ExecContext(ctx, path, t.seq, t.text); err != nil {
				return fmt.Errorf("insert thinking %s#%d: %w", path, t.seq, err)
			}
		}
	}

	return nil
}
