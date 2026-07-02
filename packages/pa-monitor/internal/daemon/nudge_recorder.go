package daemon

import (
	"context"
	"database/sql"
	"errors"

	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// nudgeRecorder implements nudger.NudgeRecorder by translating the string
// session_id to a surrogate row id (via a DB query) and delegating to
// WriteService.RecordNudge. nudge_history.session_id carries a NOT NULL FK to
// sessions(id), so a row cannot be inserted before the target session is
// persisted. Auto-resume nudges frequently target a freshly-observed session
// whose poller-driven Upsert has not yet landed — precisely when a Send failure
// is most likely — so when the session is missing the recorder inserts a
// minimal placeholder session row (satisfying only the FK + NOT NULL columns)
// and records against it, rather than silently dropping the failure (pg2-evwy).
// The poller's later Upsert (ON CONFLICT DO UPDATE) replaces every placeholder
// field with the real session data.
type nudgeRecorder struct {
	ws *service.WriteService
	db *sql.DB
}

var _ nudger.NudgeRecorder = (*nudgeRecorder)(nil)

func (r *nudgeRecorder) Record(ctx context.Context, ev nudger.RecordEvent) error {
	sessionRowID, err := r.resolveSessionRowID(ctx, ev.SessionID)
	if err != nil {
		return err
	}
	return r.ws.RecordNudge(ctx, store.NudgeEvent{
		SessionID:       sessionRowID,
		Text:            ev.Text,
		Result:          ev.Result,
		ErrorText:       ev.ErrorText,
		CausedByErrorAt: ev.CausedByErrorAt,
		Escalated:       ev.Escalated,
		FiredAt:         ev.FiredAt,
		Sources:         ev.Sources,
	})
}

// resolveSessionRowID returns the sessions.id surrogate for the given string
// session id. When the session is not yet persisted it upserts a minimal
// placeholder row via the WriteService (going through the single writer
// goroutine, same as every other mutation) and re-resolves the id, so a Send
// failure targeting a not-yet-registered session is still durably captured
// instead of being dropped by the nudge_history FK.
func (r *nudgeRecorder) resolveSessionRowID(ctx context.Context, sessionID string) (int64, error) {
	rowID, err := r.lookupSessionRowID(ctx, sessionID)
	if err == nil {
		return rowID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Session not persisted yet: create a minimal placeholder so the failure is
	// captured. All non-timestamp columns fall back to their schema defaults;
	// the poller's later Upsert overwrites them with the real session data.
	if err := r.ws.UpsertSession(ctx, store.Session{SessionID: sessionID}); err != nil {
		return 0, err
	}
	return r.lookupSessionRowID(ctx, sessionID)
}

func (r *nudgeRecorder) lookupSessionRowID(ctx context.Context, sessionID string) (int64, error) {
	var rowID int64
	err := r.db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = ?", sessionID).Scan(&rowID)
	return rowID, err
}
