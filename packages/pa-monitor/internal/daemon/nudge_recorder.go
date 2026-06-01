package daemon

import (
	"context"
	"database/sql"

	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// nudgeRecorder implements nudger.NudgeRecorder by translating the string
// session_id to a surrogate row id (via a DB query) and delegating to
// WriteService.RecordNudge. If the session is not yet in the DB the event
// is silently dropped.
type nudgeRecorder struct {
	ws *service.WriteService
	db *sql.DB
}

var _ nudger.NudgeRecorder = (*nudgeRecorder)(nil)

func (r *nudgeRecorder) Record(ctx context.Context, ev nudger.RecordEvent) error {
	var sessionRowID int64
	if err := r.db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = ?", ev.SessionID).Scan(&sessionRowID); err != nil {
		// Session not in DB yet (or DB unavailable); skip recording.
		return nil //nolint:nilerr
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
