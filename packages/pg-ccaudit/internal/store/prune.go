package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TSLayout is the layout ingest writes events.ts in (UTC, millisecond precision),
// so a cutoff formatted with it compares correctly as a plain string.
const TSLayout = "2006-01-02T15:04:05.000Z"

// PruneResult counts what a prune removed (or, for a dry run, would remove).
type PruneResult struct {
	Cutoff string `json:"cutoff"`
	DryRun bool   `json:"dry_run"`
	// Files is the number of transcript/feed units (files rows) removed.
	Files int64 `json:"files"`
	// Rows is the per-table count of rows removed, keyed by table name.
	Rows map[string]int64 `json:"rows"`
	// Vacuumed is true when VACUUM ran (only after at least one deletion).
	Vacuumed bool `json:"vacuumed"`
}

// pathTables are the tables keyed by a `path` column that has no foreign key to
// files (events cascades on its own).
var pathTables = []string{"tool_calls", "tool_results", "assistant_text", "user_text"}

// expiredFilesSQL selects the files whose newest event is older than the cutoff,
// plus files with no event at all that were last touched before it. A file is
// removed whole or not at all: a session that straddles the cutoff keeps every
// row, so no query ever sees half a session.
const expiredFilesSQL = `
SELECT f.path FROM files f
 WHERE COALESCE((SELECT MAX(e.ts) FROM events e WHERE e.path = f.path), '') < ?1
   AND (EXISTS (SELECT 1 FROM events e WHERE e.path = f.path)
        OR f.ingested_at < ?2)`

// Prune deletes every indexed file whose newest event is older than cutoff,
// along with all its rows, then runs VACUUM if anything was deleted and dryRun
// is false. VACUUM needs free disk space about the size of the database.
// feed_cursors is never touched, so ingest resumes where it was and does not
// re-read what was pruned.
func Prune(ctx context.Context, db *sql.DB, cutoff time.Time, dryRun bool) (PruneResult, error) {
	cutoff = cutoff.UTC()
	res := PruneResult{Cutoff: cutoff.Format(TSLayout), DryRun: dryRun, Rows: map[string]int64{}}

	hasThinking, err := HasThinkingTable(db)
	if err != nil {
		return res, err
	}
	tables := append([]string{}, pathTables...)
	if hasThinking {
		tables = append(tables, "thinking")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE prune_paths AS `+expiredFilesSQL, res.Cutoff, cutoff.Unix()); err != nil {
		return res, fmt.Errorf("select expired files: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM prune_paths`).Scan(&res.Files); err != nil {
		return res, err
	}
	count := func(table string) (int64, error) {
		var n int64
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE path IN (SELECT path FROM prune_paths)`, table)).Scan(&n)
		return n, err
	}
	for _, t := range append([]string{"events"}, tables...) {
		n, err := count(t)
		if err != nil {
			return res, fmt.Errorf("count %s: %w", t, err)
		}
		res.Rows[t] = n
	}

	if dryRun {
		return res, nil
	}
	for _, t := range append([]string{"events"}, tables...) {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE path IN (SELECT path FROM prune_paths)`, t)); err != nil {
			return res, fmt.Errorf("delete from %s: %w", t, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path IN (SELECT path FROM prune_paths)`); err != nil {
		return res, fmt.Errorf("delete from files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE prune_paths`); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("commit prune: %w", err)
	}
	if res.Files > 0 {
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return res, fmt.Errorf("vacuum: %w", err)
		}
		res.Vacuumed = true
	}
	return res, nil
}
