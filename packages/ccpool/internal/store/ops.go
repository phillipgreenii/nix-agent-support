package store

import (
	"context"
	"database/sql"
	"fmt"
)

const cols = `name, uuid, cwd, transcript_path, state, generation, created_at, last_activity_at, tmux_session, model, flags`

func scanRow(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var uuid sql.NullString
	err := sc.Scan(&s.Name, &uuid, &s.CWD, &s.TranscriptPath, &s.State, &s.Generation,
		&s.CreatedAt, &s.LastActivityAt, &s.TmuxSession, &s.Model, &s.Flags)
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
		`INSERT INTO sessions (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		in.Name, uuid, in.CWD, in.TranscriptPath, in.State, in.Generation,
		in.CreatedAt, in.LastActivityAt, in.TmuxSession, in.Model, in.Flags)
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
	// All bind params positional; uuid/transcriptPath are passed twice so the
	// CASE-WHEN guard and the assignment share one value.
	_, err = tx.ExecContext(ctx, `
		UPDATE sessions SET
			state = ?,
			generation = generation + 1,
			last_activity_at = ?,
			uuid = CASE WHEN ? <> '' THEN ? ELSE uuid END,
			transcript_path = CASE WHEN ? <> '' THEN ? ELSE transcript_path END
		WHERE name = ?`,
		to, now, uuid, uuid, transcriptPath, transcriptPath, name)
	if err != nil {
		return "", fmt.Errorf("transition %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return prior, nil
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
