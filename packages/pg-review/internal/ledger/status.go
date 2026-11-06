package ledger

import (
	"context"
	"time"
)

// ScopeRun is the newest run of one (lens, scope) pair.
type ScopeRun struct {
	Lens  string
	Scope string
	State string
	At    time.Time
	RunID int64
}

// LastRunsByScope returns the newest run of every (lens, scope) pair, in lens
// then scope order.
func (l *Ledger) LastRunsByScope(ctx context.Context) ([]ScopeRun, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id,lens,scope,state,created_at FROM run r
		WHERE seq=(SELECT MAX(seq) FROM run WHERE lens=r.lens AND scope=r.scope) ORDER BY lens,scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopeRun
	for rows.Next() {
		var r ScopeRun
		var at string
		if err := rows.Scan(&r.RunID, &r.Lens, &r.Scope, &r.State, &at); err != nil {
			return nil, err
		}
		r.At = parseTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SkippedSince counts the Skipped runs created at or after since.
func (l *Ledger) SkippedSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run WHERE state='Skipped' AND created_at>=?`, fmtTime(since)).Scan(&n)
	return n, err
}
