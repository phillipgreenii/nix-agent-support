package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// This file holds the typed accessors for the job table (schema version 2): the
// CI jobs of one run attempt, cached by (run id, attempt). A completed
// attempt's jobs never change, so a row is written once and read on every later
// refresh of the run's group. When to fetch a row, whether a row may be reused
// and how long to keep it belong to the CI runs group that uses these accessors,
// not to this file.

// JobResult is one job of a run attempt: the fields of schema.CIJob. The struct
// tags fix the stored encoding of JobRow.Jobs, so renaming a Go field cannot
// change what an existing store holds.
type JobResult struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
}

// JobRow is the cached job list of one run attempt. RunID is the CI run's
// decimal-string id (schema.CIRun.ID), kept as text.
type JobRow struct {
	RunID   string
	Attempt int
	// Jobs keeps the order they were stored in. A nil list is stored as an
	// empty one and read back as an empty, non-nil list.
	Jobs      []JobResult
	FetchedAt time.Time
}

// PutJobs inserts or replaces the row for (j.RunID, j.Attempt); a second call
// for the same pair replaces every column of the first.
func (s *Store) PutJobs(ctx context.Context, j JobRow) error {
	jobs := j.Jobs
	if jobs == nil {
		jobs = []JobResult{}
	}
	raw, err := json.Marshal(jobs)
	if err != nil {
		return fmt.Errorf("store: encode jobs of run %s attempt %d: %w", j.RunID, j.Attempt, err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO job (run_id, attempt, jobs, fetched_at) VALUES (?, ?, ?, ?)
ON CONFLICT (run_id, attempt) DO UPDATE SET jobs = excluded.jobs, fetched_at = excluded.fetched_at`,
		j.RunID, j.Attempt, string(raw), j.FetchedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put jobs of run %s attempt %d: %w", j.RunID, j.Attempt, err)
	}
	return nil
}

// GetJobs returns the row for (runID, attempt); ok is false when there is none.
func (s *Store) GetJobs(ctx context.Context, runID string, attempt int) (j JobRow, ok bool, err error) {
	var (
		raw     string
		fetched int64
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT run_id, attempt, jobs, fetched_at FROM job WHERE run_id = ? AND attempt = ?`, runID, attempt).
		Scan(&j.RunID, &j.Attempt, &raw, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return JobRow{}, false, nil
	}
	if err != nil {
		return JobRow{}, false, fmt.Errorf("store: get jobs of run %s attempt %d: %w", runID, attempt, err)
	}
	if err := json.Unmarshal([]byte(raw), &j.Jobs); err != nil {
		return JobRow{}, false, fmt.Errorf("store: decode jobs of run %s attempt %d: %w", runID, attempt, err)
	}
	j.FetchedAt = time.UnixMilli(fetched).UTC()
	return j, true, nil
}

// PruneJobsBefore deletes the rows whose fetched_at is older than t (a row
// fetched at exactly t is kept) and returns how many.
func (s *Store) PruneJobsBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM job WHERE fetched_at < ?`, t.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: prune jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune jobs: %w", err)
	}
	return n, nil
}
