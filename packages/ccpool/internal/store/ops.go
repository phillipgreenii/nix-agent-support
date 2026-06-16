package store

import (
	"context"
	"database/sql"
	"fmt"
)

const cols = `name, uuid, cwd, transcript_path, state, generation, created_at, last_activity_at, tmux_session, model, flags, pending_question`

func scanRow(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var uuid sql.NullString
	err := sc.Scan(&s.Name, &uuid, &s.CWD, &s.TranscriptPath, &s.State, &s.Generation,
		&s.CreatedAt, &s.LastActivityAt, &s.TmuxSession, &s.Model, &s.Flags, &s.PendingQuestion)
	s.UUID = uuid.String
	return s, err
}

// Insert creates a new row. generation starts at 1; created_at/last_activity_at
// default to the clock's now when zero.
func (s *Store) Insert(ctx context.Context, in Session) error {
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
	var uuid any
	if in.UUID != "" {
		uuid = in.UUID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.Name, uuid, in.CWD, in.TranscriptPath, in.State, in.Generation,
		in.CreatedAt, in.LastActivityAt, in.TmuxSession, in.Model, in.Flags, in.PendingQuestion)
	if err != nil {
		return fmt.Errorf("insert %q: %w", in.Name, err)
	}
	return nil
}

func (s *Store) GetByName(ctx context.Context, name string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM sessions WHERE name = ?`, name)
	sess, err := scanRow(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("get by name %q: %w", name, err)
	}
	return sess, true, nil
}

func (s *Store) GetByUUID(ctx context.Context, uuid string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM sessions WHERE uuid = ?`, uuid)
	sess, err := scanRow(row)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("get by uuid %q: %w", uuid, err)
	}
	return sess, true, nil
}

func (s *Store) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM sessions ORDER BY last_activity_at DESC, name ASC`)
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

// Transition sets state on the row named `name`, bumps generation, sets
// last_activity_at to now, and (when non-empty) updates uuid and transcript_path.
// Returns the prior state (for the hook's notifier edge-trigger, §10).
func (s *Store) Transition(ctx context.Context, name string, to State, uuid, transcriptPath string) (State, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var prior State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM sessions WHERE name = ?`, name).Scan(&prior); err != nil {
		return "", fmt.Errorf("transition: load %q: %w", name, err)
	}
	now := s.clock.Now().Unix()
	// CLEAR a stale pending_question whenever the turn moves OFF needs_input, so a
	// question never lingers past the turn (pg2-7a5b). When moving INTO NeedsInput
	// we leave it untouched so the `ask` hook's subsequent SetPendingQuestion (which
	// runs after this transition) survives.
	clearQuestion := to != NeedsInput
	// All bind params positional; uuid/transcriptPath are passed twice so the
	// CASE-WHEN guard and the assignment share one value.
	_, err = tx.ExecContext(ctx, `
		UPDATE sessions SET
			state = ?,
			generation = generation + 1,
			last_activity_at = ?,
			uuid = CASE WHEN ? <> '' THEN ? ELSE uuid END,
			transcript_path = CASE WHEN ? <> '' THEN ? ELSE transcript_path END,
			pending_question = CASE WHEN ? THEN '' ELSE pending_question END
		WHERE name = ?`,
		to, now, uuid, uuid, transcriptPath, transcriptPath, clearQuestion, name)
	if err != nil {
		return "", fmt.Errorf("transition %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	// Record the transition to the append-only event log (nil-safe no-op when no
	// log is wired). Uses the store's injected clock so a fake clock yields a
	// deterministic ts; transcriptPath is the optional claude-session line ref.
	_ = s.events.Transition(s.clock.Now(), name, string(prior), string(to), uuid, transcriptPath)
	return prior, nil
}

// SetPendingQuestion records the AskUserQuestion text on the row named `name`.
// Bumps last_activity_at from the injected clock (so a fresh question counts as
// activity). Called by the `ask` hook right after it transitions the row to
// NeedsInput (pg2-7a5b).
func (s *Store) SetPendingQuestion(ctx context.Context, name, q string) error {
	now := s.clock.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET pending_question = ?, last_activity_at = ? WHERE name = ?`,
		q, now, name)
	if err != nil {
		return fmt.Errorf("set pending_question %q: %w", name, err)
	}
	return nil
}

// Poll returns the row's current generation and state (implements wait.Poller).
func (s *Store) Poll(ctx context.Context, name string) (int64, State, bool, error) {
	sess, ok, err := s.GetByName(ctx, name)
	if err != nil || !ok {
		return 0, "", ok, err
	}
	return sess.Generation, sess.State, true, nil
}

// Delete removes the row for name. Deleting a missing row is not an error.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete %q: %w", name, err)
	}
	return nil
}

// Upsert ensures a row exists for name; if absent it is inserted as Starting.
// If present it is left untouched (does not clobber uuid/state). Used by `hook start`.
func (s *Store) Upsert(ctx context.Context, name, uuid string) error {
	_, ok, err := s.GetByName(ctx, name)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return s.Insert(ctx, Session{Name: name, UUID: uuid, State: Starting})
}

// turnCols is the column order shared by InsertTurn/GetTurn scans.
const turnCols = `turn_id, name, prompt, status, transcript_path, created_at, resolved_at`

func scanTurn(sc interface{ Scan(...any) error }) (Turn, error) {
	var t Turn
	err := sc.Scan(&t.TurnID, &t.Name, &t.Prompt, &t.Status, &t.TranscriptPath, &t.CreatedAt, &t.ResolvedAt)
	return t, err
}

// InsertTurn records a fire-and-forget turn as pending (pg2-12ko). The caller
// supplies turn_id + name + prompt; status defaults pending and created_at is
// stamped from the injected clock (deterministic in tests).
func (s *Store) InsertTurn(ctx context.Context, in Turn) error {
	if in.Status == "" {
		in.Status = TurnPending
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = s.clock.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (`+turnCols+`) VALUES (?,?,?,?,?,?,?)`,
		in.TurnID, in.Name, in.Prompt, in.Status, in.TranscriptPath, in.CreatedAt, in.ResolvedAt)
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
// `name` (FIFO by created_at), flipping it to resolved and recording resolved_at
// from the injected clock. Returns ok=false (no error) when there is no pending
// turn for the name.
//
// KNOWN LIMITATION (v1, pg2-12ko): FIFO-pop is the correlation assumption between
// a completing turn (Stop hook → Done) and its emitted turn-id. It is correct only
// when fire-and-forget turns for a name complete in emit order. It breaks if an
// interactive (blocking) reply's Stop interleaves with a pending fire-and-forget
// turn (the blocking reply's Stop would pop the wrong turn-id), or if a turn ends
// `needs_input` rather than Done (the Stop hook never fires, so the turn stays
// pending). This is a documented limitation, NOT a v1 requirement.
func (s *Store) ResolveOldestPendingTurn(ctx context.Context, name, transcriptPath string) (turnID string, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT turn_id FROM turns WHERE name = ? AND status = ? ORDER BY created_at ASC, turn_id ASC LIMIT 1`,
		name, TurnPending).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find pending turn for %q: %w", name, err)
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
