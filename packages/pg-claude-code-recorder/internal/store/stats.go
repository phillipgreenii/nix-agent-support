package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Planner statistics (sqlite_stat1).
//
// The store was run without statistics until the operator ruled otherwise
// (Phillip, 2026-11-10: "2) yes, enable stats"). The cost that made the earlier
// decision say no was the first PRAGMA optimize: ANALYZE holds the database
// write lock for its whole run, and on the live-sized store that is minutes for
// the records table. CollectStats therefore runs the statements PRAGMA optimize
// would run, one index at a time, each under its own short write transaction
// and a lowered analysis_limit, so ingest waits for one index at most and not
// for the whole pass.
const (
	// DefaultAnalysisLimit is the analysis_limit CollectStats runs with: each
	// index is sampled for about that many rows instead of being scanned whole.
	DefaultAnalysisLimit = 1000
	// StatsEvery is how often the daemon's maintenance tick re-collects.
	StatsEvery = 24 * time.Hour

	metaStatsAtNS = "stats_collected_at_ns"
)

// CollectStatsDue reports whether the last collection is older than every (or
// none was recorded).
func (s *Store) CollectStatsDue(now time.Time, every time.Duration) bool {
	last := s.metaInt(metaStatsAtNS)
	return last == 0 || now.Sub(time.Unix(0, last)) >= every
}

// CollectStats gathers planner statistics with analysis_limit=limit (0 means
// DefaultAnalysisLimit), one ANALYZE per index of every table PRAGMA optimize
// would analyse, on the checkpoint connection so the writer's pool is never held.
// It stops at ctx and between statements checks it, so a close or shutdown cuts
// the pass short; the next call resumes (statements for tables that already have
// statistics are not listed again). It records the completion time in meta only
// when every statement ran. A busy database ends the pass with the error; the
// caller retries on its next tick.
func (s *Store) CollectStats(ctx context.Context, limit int, now time.Time) (statements int, err error) {
	if limit <= 0 {
		limit = DefaultAnalysisLimit
	}
	s.mu.Lock()
	c := s.cdb
	s.mu.Unlock()
	if c == nil {
		return 0, errors.New("store closed")
	}
	return s.collectStats(ctx, c, limit, now)
}

// collectStats is CollectStats on a given connection pool; Close calls it
// while it already holds s.mu.
func (s *Store) collectStats(ctx context.Context, c *sql.DB, limit int, now time.Time) (statements int, err error) {
	if limit <= 0 {
		limit = DefaultAnalysisLimit
	}
	conn, err := c.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA analysis_limit=`+strconv.Itoa(limit)); err != nil {
		return 0, err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `PRAGMA analysis_limit=0`) }()
	targets, err := statsTargets(ctx, conn)
	if err != nil {
		return 0, err
	}
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return statements, err
		}
		if _, err := conn.ExecContext(ctx, `ANALYZE `+t); err != nil {
			return statements, err
		}
		statements++
	}
	// Mark done on the writer: the stamp is a normal committed write.
	if _, err := s.wdb.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		metaStatsAtNS, strconv.FormatInt(now.UnixNano(), 10)); err != nil {
		return statements, err
	}
	return statements, nil
}

// statsTargets lists what PRAGMA optimize would analyse (its dry run), each
// table expanded into its indexes so each ANALYZE is a separate short
// transaction. A table optimize lists that has no index is analysed whole.
func statsTargets(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rs, err := conn.QueryContext(ctx, `PRAGMA optimize(-1)`)
	if err != nil {
		return nil, err
	}
	var stmts []string
	for rs.Next() {
		var st string
		if err := rs.Scan(&st); err != nil {
			_ = rs.Close()
			return nil, err
		}
		stmts = append(stmts, st)
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}
	var out []string
	for _, st := range stmts {
		target := strings.TrimSpace(strings.TrimPrefix(st, "ANALYZE"))
		tbl := strings.Trim(target[strings.LastIndex(target, ".")+1:], `"`)
		idx, err := tableIndexes(ctx, conn, tbl)
		if err != nil {
			return nil, err
		}
		if len(idx) == 0 {
			out = append(out, target)
			continue
		}
		for _, i := range idx {
			out = append(out, `"main".`+quoteIdent(i))
		}
	}
	return out, nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func tableIndexes(ctx context.Context, conn *sql.Conn, tbl string) ([]string, error) {
	rs, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL`, tbl)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var names []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, rs.Err()
}
