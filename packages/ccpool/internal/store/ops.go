package store

import (
	"context"
	"database/sql"
	"fmt"
)

// cols is the non-id column order shared by Insert and the scanRow projection.
// id is auto-assigned by SQLite, so it is read back separately (SELECT prepends
// it) and never written by Insert.
const cols = `external_id, claude_session_id, name, cwd, transcript_path, state, generation, created_at, last_activity_at, tmux_session, model, flags, pending_question, retry_count, retry_window_started_at`

// selectCols prepends the surrogate id so scanRow can populate Session.ID.
const selectCols = `id, ` + cols

func scanRow(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var csid sql.NullString
	var name sql.NullString
	err := sc.Scan(&s.ID, &s.ExternalID, &csid, &name, &s.CWD, &s.TranscriptPath, &s.State, &s.Generation,
		&s.CreatedAt, &s.LastActivityAt, &s.TmuxSession, &s.Model, &s.Flags, &s.PendingQuestion,
		&s.RetryCount, &s.RetryWindowStartedAt)
	s.ClaudeSessionID = csid.String
	s.Name = name.String
	return s, err
}

// Insert creates a new row keyed by ExternalID (required). SQLite assigns id;
// it is read back into in.ID via last_insert_rowid(). generation starts at 1;
// created_at/last_activity_at default to the clock's now when zero.
func (s *Store) Insert(ctx context.Context, in Session) error {
	if in.ExternalID == "" {
		return fmt.Errorf("insert: ExternalID is required")
	}
	now := s.clock.Now().Unix()
	if in.CreatedAt == 0 {
		in.CreatedAt = now
	}
	if in.LastActivityAt == 0 {
		in.LastActivityAt = now
	}
	if in.Generation == 0 {
		in.Generation = 1
	}
	// claude_session_id and name are nullable; bind NULL when empty so the UNIQUE
	// constraint on claude_session_id does not collide across rows that have none.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ExternalID, nullString(in.ClaudeSessionID), nullString(in.Name), in.CWD, in.TranscriptPath, in.State, in.Generation,
		in.CreatedAt, in.LastActivityAt, in.TmuxSession, in.Model, in.Flags, in.PendingQuestion,
		in.RetryCount, in.RetryWindowStartedAt)
	if err != nil {
		return fmt.Errorf("insert %q: %w", in.ExternalID, err)
	}
	if id, lerr := res.LastInsertId(); lerr == nil {
		in.ID = id
	}
	return nil
}

// nullString binds an empty string as SQL NULL (so UNIQUE columns tolerate many
// rows with no value) and any non-empty value as itself.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Store) GetByExternalID(ctx context.Context, externalID string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM sessions WHERE external_id = ?`, externalID)
	sess, err := scanRow(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("get by external_id %q: %w", externalID, err)
	}
	return sess, true, nil
}

func (s *Store) GetByClaudeSessionID(ctx context.Context, csid string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM sessions WHERE claude_session_id = ?`, csid)
	sess, err := scanRow(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("get by claude_session_id %q: %w", csid, err)
	}
	return sess, true, nil
}

func (s *Store) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectCols+` FROM sessions ORDER BY last_activity_at DESC, external_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Transition sets state on the row with external_id, bumps generation, sets
// last_activity_at to now, and (when non-empty) updates claude_session_id and
// transcript_path. Returns the prior state (for the hook's notifier edge-trigger).
func (s *Store) Transition(ctx context.Context, externalID string, to State, claudeSessionID, transcriptPath string) (State, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var prior State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM sessions WHERE external_id = ?`, externalID).Scan(&prior); err != nil {
		return "", fmt.Errorf("transition: load %q: %w", externalID, err)
	}
	now := s.clock.Now().Unix()
	// CLEAR a stale pending_question whenever the turn moves OFF needs_input, so a
	// question never lingers past the turn (pg2-7a5b). When moving INTO NeedsInput
	// we leave it untouched so the `ask` hook's subsequent SetPendingQuestion (which
	// runs after this transition) survives.
	clearQuestion := to != NeedsInput
	// All bind params positional; claudeSessionID/transcriptPath are passed twice so
	// the CASE-WHEN guard and the assignment share one value.
	_, err = tx.ExecContext(ctx, `
		UPDATE sessions SET
			state = ?,
			generation = generation + 1,
			last_activity_at = ?,
			claude_session_id = CASE WHEN ? <> '' THEN ? ELSE claude_session_id END,
			transcript_path = CASE WHEN ? <> '' THEN ? ELSE transcript_path END,
			pending_question = CASE WHEN ? THEN '' ELSE pending_question END
		WHERE external_id = ?`,
		to, now, claudeSessionID, claudeSessionID, transcriptPath, transcriptPath, clearQuestion, externalID)
	if err != nil {
		return "", fmt.Errorf("transition %q: %w", externalID, err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	// Record the transition to the append-only event log (nil-safe no-op when no
	// log is wired). Uses the store's injected clock so a fake clock yields a
	// deterministic ts; transcriptPath is the optional claude-session line ref.
	_ = s.events.Transition(s.clock.Now(), externalID, string(prior), string(to), claudeSessionID, transcriptPath)
	return prior, nil
}

// BumpRetry records one in-place transient-error retry attempt for external_id:
// it increments retry_count and, on the FIRST retry of a window (when
// retry_window_started_at is still 0), anchors the window to the injected
// clock's now. The window start is left untouched on subsequent retries so the
// overall retry-timeout measures from the first attempt. last_activity_at is
// NOT bumped (a retry is ccpool actuation, not observed session activity).
func (s *Store) BumpRetry(ctx context.Context, externalID string) error {
	now := s.clock.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET
			retry_count = retry_count + 1,
			retry_window_started_at = CASE WHEN retry_window_started_at = 0 THEN ? ELSE retry_window_started_at END
		WHERE external_id = ?`,
		now, externalID)
	if err != nil {
		return fmt.Errorf("bump retry %q: %w", externalID, err)
	}
	return nil
}

// ResetRetry clears the retry budget for external_id (retry_count and
// retry_window_started_at to 0) so a later, unrelated transient error gets a
// fresh budget rather than inheriting an old count. Called on a successful turn.
func (s *Store) ResetRetry(ctx context.Context, externalID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET retry_count = 0, retry_window_started_at = 0 WHERE external_id = ?`,
		externalID)
	if err != nil {
		return fmt.Errorf("reset retry %q: %w", externalID, err)
	}
	return nil
}

// SetPendingQuestion records the AskUserQuestion text on the row with external_id.
// Bumps last_activity_at from the injected clock (so a fresh question counts as
// activity). Called by the `ask` hook right after it transitions the row to
// NeedsInput (pg2-7a5b).
func (s *Store) SetPendingQuestion(ctx context.Context, externalID, q string) error {
	now := s.clock.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET pending_question = ?, last_activity_at = ? WHERE external_id = ?`,
		q, now, externalID)
	if err != nil {
		return fmt.Errorf("set pending_question %q: %w", externalID, err)
	}
	return nil
}

// Poll returns the row's current generation and state (implements wait.Poller).
func (s *Store) Poll(ctx context.Context, externalID string) (int64, State, bool, error) {
	sess, ok, err := s.GetByExternalID(ctx, externalID)
	if err != nil || !ok {
		return 0, "", ok, err
	}
	return sess.Generation, sess.State, true, nil
}

// Delete removes the row for external_id AND its session metadata. Deleting a
// missing row is not an error.
func (s *Store) Delete(ctx context.Context, externalID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE external_id = ?`, externalID); err != nil {
		return fmt.Errorf("delete %q: %w", externalID, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_metadata WHERE external_id = ?`, externalID); err != nil {
		return fmt.Errorf("delete meta for %q: %w", externalID, err)
	}
	return nil
}

// Upsert ensures a row exists for external_id; if absent it is inserted as
// Starting (with the supplied claude_session_id + name). If present it is left
// untouched (does not clobber claude_session_id/state). Used by `hook start`.
func (s *Store) Upsert(ctx context.Context, externalID, claudeSessionID, name string) error {
	_, ok, err := s.GetByExternalID(ctx, externalID)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return s.Insert(ctx, Session{ExternalID: externalID, ClaudeSessionID: claudeSessionID, Name: name, State: Starting})
}

// turnCols is the column order shared by InsertTurn/GetTurn scans.
const turnCols = `turn_id, external_id, prompt, status, transcript_path, created_at, resolved_at`

func scanTurn(sc interface{ Scan(...any) error }) (Turn, error) {
	var t Turn
	var resolvedAt sql.NullInt64
	err := sc.Scan(&t.TurnID, &t.ExternalID, &t.Prompt, &t.Status, &t.TranscriptPath, &t.CreatedAt, &resolvedAt)
	t.ResolvedAt = resolvedAt.Int64
	return t, err
}

// InsertTurn records a fire-and-forget turn as pending (pg2-12ko). The caller
// supplies turn_id + external_id + prompt; status defaults pending and created_at
// is stamped from the injected clock (deterministic in tests).
func (s *Store) InsertTurn(ctx context.Context, in Turn) error {
	if in.Status == "" {
		in.Status = TurnPending
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = s.clock.Now().Unix()
	}
	var resolvedAt any
	if in.ResolvedAt != 0 {
		resolvedAt = in.ResolvedAt
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (`+turnCols+`) VALUES (?,?,?,?,?,?,?)`,
		in.TurnID, in.ExternalID, in.Prompt, in.Status, in.TranscriptPath, in.CreatedAt, resolvedAt)
	if err != nil {
		return fmt.Errorf("insert turn %q: %w", in.TurnID, err)
	}
	return nil
}

// GetTurn loads one turn by id. ok=false (no error) when no such turn.
func (s *Store) GetTurn(ctx context.Context, turnID string) (Turn, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+turnCols+` FROM turns WHERE turn_id = ?`, turnID)
	t, err := scanTurn(row)
	if err == sql.ErrNoRows {
		return Turn{}, false, nil
	}
	if err != nil {
		return Turn{}, false, fmt.Errorf("get turn %q: %w", turnID, err)
	}
	return t, true, nil
}

// ResolveOldestPendingTurn stamps transcriptPath onto the OLDEST pending turn for
// externalID (FIFO by created_at), flipping it to resolved and recording
// resolved_at from the injected clock. Returns ok=false (no error) when there is
// no pending turn for the external_id.
//
// KNOWN LIMITATION (v1, pg2-12ko): FIFO-pop is the correlation assumption between
// a completing turn (Stop hook → Idle) and its emitted turn-id. It is correct only
// when fire-and-forget turns for a session complete in emit order.
func (s *Store) ResolveOldestPendingTurn(ctx context.Context, externalID, transcriptPath string) (turnID string, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT turn_id FROM turns WHERE external_id = ? AND status = ? ORDER BY created_at ASC, turn_id ASC LIMIT 1`,
		externalID, TurnPending).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find pending turn for %q: %w", externalID, err)
	}
	now := s.clock.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE turns SET status = ?, transcript_path = ?, resolved_at = ? WHERE turn_id = ?`,
		TurnResolved, transcriptPath, now, id); err != nil {
		return "", false, fmt.Errorf("resolve turn %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, true, nil
}
