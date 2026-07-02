package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// SessionStore is the SQLite implementation of store.SessionStore.
type SessionStore struct {
	db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

var _ store.SessionStore = (*SessionStore)(nil)

func (s *SessionStore) Upsert(ctx context.Context, sess store.Session) error {
	labelsJSON, err := json.Marshal(sess.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}
	now := time.Now().UTC()
	if sess.LastProcessedAt.IsZero() {
		sess.LastProcessedAt = now
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = now
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}

	_, err = s.db.ExecContext(
		ctx, `
		INSERT INTO sessions (
			session_id, pid, command_hash, cwd, name, kind, entrypoint,
			model, terminal_host, branch, status, first_prompt, labels,
			transcript_mtime, started_at,
			context_tokens, session_tokens, subagent_count, subshell_count,
			burn_rate_short, burn_rate_long, cost_usd, awaiting_input,
			last_error_kind, last_error_text, last_error_at,
			last_error_terminal, last_error_retryable, last_error_from_subagent,
			last_processed_at, updated_at, created_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
		ON CONFLICT(session_id) DO UPDATE SET
			pid = excluded.pid,
			command_hash = excluded.command_hash,
			cwd = excluded.cwd,
			name = excluded.name,
			kind = excluded.kind,
			entrypoint = excluded.entrypoint,
			model = excluded.model,
			terminal_host = excluded.terminal_host,
			branch = excluded.branch,
			status = excluded.status,
			first_prompt = excluded.first_prompt,
			labels = excluded.labels,
			transcript_mtime = excluded.transcript_mtime,
			started_at = excluded.started_at,
			context_tokens = excluded.context_tokens,
			session_tokens = excluded.session_tokens,
			subagent_count = excluded.subagent_count,
			subshell_count = excluded.subshell_count,
			burn_rate_short = excluded.burn_rate_short,
			burn_rate_long = excluded.burn_rate_long,
			cost_usd = excluded.cost_usd,
			awaiting_input = excluded.awaiting_input,
			last_error_kind = excluded.last_error_kind,
			last_error_text = excluded.last_error_text,
			last_error_at = excluded.last_error_at,
			last_error_terminal = excluded.last_error_terminal,
			last_error_retryable = excluded.last_error_retryable,
			last_error_from_subagent = excluded.last_error_from_subagent,
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`,
		sess.SessionID, pidPtr(sess.PID), sess.CommandHash, sess.Cwd, sess.Name, sess.Kind, sess.Entrypoint,
		sess.Model, sess.TerminalHost, sess.Branch, sess.Status, sess.FirstPrompt, string(labelsJSON),
		formatTime(sess.TranscriptMTime), formatTime(sess.StartedAt),
		sess.ContextTokens, sess.SessionTokens, sess.SubagentCount, sess.SubshellCount,
		sess.BurnRateShort, sess.BurnRateLong, sess.CostUSD, boolInt(sess.AwaitingInput),
		sess.LastErrorKind, sess.LastErrorText, formatTime(sess.LastErrorAt),
		boolInt(sess.LastErrorTerminal), boolInt(sess.LastErrorRetryable), boolInt(sess.LastErrorFromSubagent),
		formatTime(sess.LastProcessedAt), formatTime(sess.UpdatedAt), formatTime(sess.CreatedAt),
	)
	return err
}

func (s *SessionStore) GetByID(ctx context.Context, sessionID string, fresh store.FreshnessWindow) (*store.Session, error) {
	cutoff := time.Now().UTC().Add(-fresh.Sessions)
	row := s.db.QueryRowContext(ctx, sessionSelectColumns+`
		FROM sessions s
		WHERE s.session_id = ?
		  AND s.deleted_at IS NULL
		  AND s.last_processed_at > ?
	`, sessionID, formatTime(cutoff))
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SessionStore) List(ctx context.Context, filter store.Filter, activeBlockID int64, fresh store.FreshnessWindow) ([]store.SessionWithContribution, error) {
	cutoff := time.Now().UTC().Add(-fresh.Sessions)

	var query string
	switch filter {
	case store.FilterActive:
		query = sessionSelectColumns + `,
			COALESCE(c.cost_usd, 0), COALESCE(c.tokens, 0)
			FROM sessions s
			INNER JOIN session_block_contributions c ON c.session_id = s.id
			WHERE s.deleted_at IS NULL
			  AND s.last_processed_at > ?
			  AND s.pid IS NOT NULL
			  AND c.block_id = ?
		`
	case store.FilterAll:
		query = sessionSelectColumns + `,
			COALESCE(c.cost_usd, 0), COALESCE(c.tokens, 0)
			FROM sessions s
			LEFT JOIN session_block_contributions c ON c.session_id = s.id AND c.block_id = ?
			WHERE s.deleted_at IS NULL
			  AND s.last_processed_at > ?
			  AND (s.pid IS NOT NULL OR c.id IS NOT NULL)
		`
	default:
		return nil, fmt.Errorf("unknown filter: %d", filter)
	}

	var rows *sql.Rows
	var err error
	if filter == store.FilterActive {
		rows, err = s.db.QueryContext(ctx, query, formatTime(cutoff), activeBlockID)
	} else {
		rows, err = s.db.QueryContext(ctx, query, activeBlockID, formatTime(cutoff))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.SessionWithContribution
	for rows.Next() {
		var sc store.SessionWithContribution
		if err := scanSessionInto(rows, &sc.Session, &sc.BlockCostUSD, &sc.BlockTokens); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *SessionStore) MarkDeleted(ctx context.Context, keepIDs []string, now time.Time) error {
	if len(keepIDs) == 0 {
		_, err := s.db.ExecContext(ctx,
			"UPDATE sessions SET deleted_at = ? WHERE deleted_at IS NULL",
			formatTime(now))
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keepIDs)), ",")
	args := []any{formatTime(now)}
	for _, id := range keepIDs {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET deleted_at = ? WHERE deleted_at IS NULL AND session_id NOT IN ("+placeholders+")",
		args...)
	return err
}

func (s *SessionStore) MarkRevived(ctx context.Context, reviveIDs []string) error {
	if len(reviveIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(reviveIDs)), ",")
	args := []any{}
	for _, id := range reviveIDs {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET deleted_at = NULL WHERE deleted_at IS NOT NULL AND session_id IN ("+placeholders+")",
		args...)
	return err
}

func (s *SessionStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SessionStore) MarkEscalated(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_error_retryable = 0 WHERE session_id = ? AND deleted_at IS NULL",
		sessionID)
	return err
}

func (s *SessionStore) AllSessionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT session_id FROM sessions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- helpers ---

const sessionSelectColumns = `SELECT
	s.session_id, s.pid, s.command_hash, s.cwd, s.name, s.kind, s.entrypoint,
	s.model, s.terminal_host, s.branch, s.status, s.first_prompt, s.labels,
	s.transcript_mtime, s.started_at,
	s.context_tokens, s.session_tokens, s.subagent_count, s.subshell_count,
	s.burn_rate_short, s.burn_rate_long, s.cost_usd, s.awaiting_input,
	s.last_error_kind, s.last_error_text, s.last_error_at,
	s.last_error_terminal, s.last_error_retryable, s.last_error_from_subagent,
	s.last_processed_at, s.updated_at, s.created_at, s.deleted_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(r rowScanner) (store.Session, error) {
	var sess store.Session
	return sess, scanSessionInto(r, &sess, nil, nil)
}

func scanSessionInto(r rowScanner, sess *store.Session, extraCost *float64, extraTokens *uint64) error {
	var (
		pid                   sql.NullInt64
		labelsRaw             string
		transcriptMTime       string
		startedAt             string
		lastErrorAt           string
		lastProcessedAt       string
		updatedAt             string
		createdAt             string
		deletedAt             sql.NullString
		awaitingInput         int
		lastErrorTerminal     int
		lastErrorRetryable    int
		lastErrorFromSubagent int
	)
	dest := []any{
		&sess.SessionID, &pid, &sess.CommandHash, &sess.Cwd, &sess.Name, &sess.Kind, &sess.Entrypoint,
		&sess.Model, &sess.TerminalHost, &sess.Branch, &sess.Status, &sess.FirstPrompt, &labelsRaw,
		&transcriptMTime, &startedAt,
		&sess.ContextTokens, &sess.SessionTokens, &sess.SubagentCount, &sess.SubshellCount,
		&sess.BurnRateShort, &sess.BurnRateLong, &sess.CostUSD, &awaitingInput,
		&sess.LastErrorKind, &sess.LastErrorText, &lastErrorAt,
		&lastErrorTerminal, &lastErrorRetryable, &lastErrorFromSubagent,
		&lastProcessedAt, &updatedAt, &createdAt, &deletedAt,
	}
	if extraCost != nil {
		dest = append(dest, extraCost, extraTokens)
	}
	if err := r.Scan(dest...); err != nil {
		return err
	}
	if pid.Valid {
		p := int(pid.Int64)
		sess.PID = &p
	}
	sess.AwaitingInput = awaitingInput != 0
	sess.LastErrorTerminal = lastErrorTerminal != 0
	sess.LastErrorRetryable = lastErrorRetryable != 0
	sess.LastErrorFromSubagent = lastErrorFromSubagent != 0
	if labelsRaw != "" {
		_ = json.Unmarshal([]byte(labelsRaw), &sess.Labels)
	}
	sess.TranscriptMTime = parseTime(transcriptMTime)
	sess.StartedAt = parseTime(startedAt)
	sess.LastErrorAt = parseTime(lastErrorAt)
	sess.LastProcessedAt = parseTime(lastProcessedAt)
	sess.UpdatedAt = parseTime(updatedAt)
	sess.CreatedAt = parseTime(createdAt)
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		sess.DeletedAt = &t
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func pidPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
