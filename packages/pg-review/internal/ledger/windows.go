package ledger

import (
	"context"
	"time"
)

// ScheduledWindow is the latest scheduled run of one (lens, scope, schedule
// id) and what the ledger knows about its window: whether any run sharing its
// run key Filed and how many runs share the key.
type ScheduledWindow struct {
	Lens, Scope, Schedule string
	// Latest is the schedule's newest terminal run (Filed, Skipped or Failed)
	// that carries a schedule id.
	Latest Run
	// Filed reports whether a Filed run shares Latest's run key (or Latest
	// itself is Filed).
	Filed bool
	// Attempts counts the runs of any state that share Latest's run key (1 for
	// a run without a key).
	Attempts int
}

// Bounds parses the window bounds; ok is false when either is unreadable or
// the window is empty.
func (w ScheduledWindow) Bounds() (from, to time.Time, ok bool) {
	f, ferr := time.Parse(time.RFC3339Nano, w.Latest.WindowFrom)
	t, terr := time.Parse(time.RFC3339Nano, w.Latest.WindowTo)
	if ferr != nil || terr != nil || !t.After(f) {
		return time.Time{}, time.Time{}, false
	}
	return f, t, true
}

// ScheduledWindows lists the latest scheduled run of every (lens, scope,
// schedule id), ordered by lens, scope and schedule id. Manual runs (no
// schedule id) never appear.
func (l *Ledger) ScheduledWindows(ctx context.Context) ([]ScheduledWindow, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT `+runCols+` FROM run r
		WHERE schedule_id<>'' AND state IN ('Filed','Skipped','Failed')
		AND seq=(SELECT MAX(seq) FROM run x WHERE x.lens=r.lens AND x.scope=r.scope AND x.schedule_id=r.schedule_id
			AND x.state IN ('Filed','Skipped','Failed'))
		ORDER BY lens, scope, schedule_id`)
	if err != nil {
		return nil, err
	}
	var out []ScheduledWindow
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, ScheduledWindow{Lens: r.Lens, Scope: r.Scope, Schedule: r.ScheduleID, Latest: r, Filed: r.State == RunFiled, Attempts: 1})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for i := range out {
		w := &out[i]
		if w.Latest.RunKey == "" {
			continue
		}
		var n, filed int
		if err := l.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(state='Filed'),0) FROM run WHERE run_key=?`, w.Latest.RunKey).Scan(&n, &filed); err != nil {
			return nil, err
		}
		w.Attempts = n
		w.Filed = w.Filed || filed > 0
	}
	return out, nil
}
