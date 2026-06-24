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
	var id int64
	err := db.InTx(ctx, func(tx *Tx) error {
		var e error
		id, e = tx.UpsertPR(pr)
		return e
	})
	return id, err
}

// UpsertPR inserts or updates a PR by (repo, number) inside the transaction,
// returning its id.
func (t *Tx) UpsertPR(pr PullRequest) (int64, error) {
	if pr.Repo == "" || pr.Number == 0 {
		return 0, errors.New("store: UpsertPR requires repo and number")
	}
	now := nowRFC3339()
	_, err := t.Exec(`
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
	var id int64
	err = t.QueryRow("SELECT id FROM pull_request WHERE repo=? AND number=?", pr.Repo, pr.Number).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: read back pr id %s#%d: %w", pr.Repo, pr.Number, err)
	}
	return id, nil
}

// ListOpenPRs returns the open/draft PRs for a repo — used by sync to detect
// PRs that have disappeared upstream so it can emit pr.closed/pr.merged.
func (db *DB) ListOpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at
FROM pull_request WHERE repo=? AND state IN ('open','draft')`, repo)
	if err != nil {
		return nil, fmt.Errorf("store: list open prs %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PullRequest
	for rows.Next() {
		var pr PullRequest
		if err := rows.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
			&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("store: scan open pr: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
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

// GetPRByID returns the PR by its row id, or nil if not found.
func (db *DB) GetPRByID(ctx context.Context, id int64) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx, `
SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at
FROM pull_request WHERE id=?`, id)
	var pr PullRequest
	err := row.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
		&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr id=%d: %w", id, err)
	}
	return &pr, nil
}
