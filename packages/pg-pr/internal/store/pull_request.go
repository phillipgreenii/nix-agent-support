package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PullRequest is the authoritative PR row.
type PullRequest struct {
	ID           int64
	Repo         string
	Number       int
	Ownership    string // "mine" | "team"
	Author       string
	State        string
	Branch       string
	Base         string
	URL          string
	HeadSHA      string
	LastSyncedAt string
}

// nowRFC3339 is the clock; overridable in tests.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// UpsertPR inserts or updates a PR by (repo, number) and returns its id.
func (db *DB) UpsertPR(ctx context.Context, pr PullRequest) (int64, error) {
	if pr.Repo == "" || pr.Number == 0 {
		return 0, errors.New("store: UpsertPR requires repo and number")
	}
	now := nowRFC3339()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO pull_request
  (repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, number) DO UPDATE SET
  ownership=excluded.ownership, author=excluded.author, state=excluded.state,
  branch=excluded.branch, base=excluded.base, url=excluded.url,
  head_sha=excluded.head_sha, last_synced_at=excluded.last_synced_at,
  updated_at=excluded.updated_at`,
		pr.Repo, pr.Number, pr.Ownership, pr.Author, pr.State, pr.Branch, pr.Base,
		pr.URL, pr.HeadSHA, pr.LastSyncedAt, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: upsert pr %s#%d: %w", pr.Repo, pr.Number, err)
	}
	got, err := db.GetPR(ctx, pr.Repo, pr.Number)
	if err != nil {
		return 0, err
	}
	return got.ID, nil
}

// GetPR returns the PR by (repo, number), or nil if not found.
func (db *DB) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at
FROM pull_request WHERE repo=? AND number=?`, repo, number)
	var pr PullRequest
	err := row.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
		&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr %s#%d: %w", repo, number, err)
	}
	return &pr, nil
}
