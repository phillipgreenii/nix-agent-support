package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type NudgeStore struct{ db *sql.DB }

func NewNudgeStore(db *sql.DB) *NudgeStore { return &NudgeStore{db: db} }

var _ store.NudgeStore = (*NudgeStore)(nil)

func (s *NudgeStore) Record(ctx context.Context, ev store.NudgeEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.ExecContext(ctx, `
		INSERT INTO nudge_history (
			session_id, text, result, error_text,
			caused_by_error_at, escalated, fired_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ev.SessionID, ev.Text, ev.Result, ev.ErrorText,
		timePtr(ev.CausedByErrorAt), boolInt(ev.Escalated), formatTime(ev.FiredAt))
	if err != nil {
		return err
	}
	historyID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	for _, src := range dedupSorted(ev.Sources) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO nudge_history_sources (nudge_history_id, source) VALUES (?, ?)",
			historyID, src); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *NudgeStore) LatestForSession(ctx context.Context, sessionID int64) (*store.NudgeEvent, error) {
	row := s.db.QueryRowContext(ctx, nudgeSelectColumns+`
		FROM nudge_history WHERE session_id = ?
		ORDER BY fired_at DESC LIMIT 1`, sessionID)
	return scanNudgeWithSources(ctx, s.db, row)
}

func (s *NudgeStore) LatestForSessionWithSource(ctx context.Context, sessionID int64, source string) (*store.NudgeEvent, error) {
	row := s.db.QueryRowContext(ctx, nudgeSelectColumns+`
		FROM nudge_history h
		WHERE h.session_id = ?
		  AND EXISTS (
			SELECT 1 FROM nudge_history_sources s
			WHERE s.nudge_history_id = h.id AND s.source = ?
		  )
		ORDER BY h.fired_at DESC LIMIT 1`, sessionID, source)
	return scanNudgeWithSources(ctx, s.db, row)
}

const nudgeSelectColumns = `SELECT id, session_id, text, result, error_text,
	caused_by_error_at, escalated, fired_at`

func scanNudgeWithSources(ctx context.Context, db *sql.DB, row *sql.Row) (*store.NudgeEvent, error) {
	var (
		id              int64
		ev              store.NudgeEvent
		causedBy        sql.NullString
		escalated       int
		firedAt         string
	)
	err := row.Scan(&id, &ev.SessionID, &ev.Text, &ev.Result, &ev.ErrorText,
		&causedBy, &escalated, &firedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ev.Escalated = escalated != 0
	ev.FiredAt = parseTime(firedAt)
	if causedBy.Valid {
		t := parseTime(causedBy.String)
		ev.CausedByErrorAt = &t
	}
	srows, err := db.QueryContext(ctx,
		"SELECT source FROM nudge_history_sources WHERE nudge_history_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var src string
		if err := srows.Scan(&src); err != nil {
			return nil, err
		}
		ev.Sources = append(ev.Sources, src)
	}
	return &ev, srows.Err()
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	// keep input order; SQL UNIQUE will trigger if dup'd anyway
	_ = strings.Join
	return out
}
