package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PullRequest is the authoritative PR row.
type PullRequest struct {
	ID             int64
	Repo           string
	Number         int
	Ownership      string // "mine" | "team"
	Author         string
	State          string
	Branch         string
	Base           string
	URL            string
	HeadSHA        string
	LastSyncedAt   string
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
}

// nowRFC3339 is the clock; overridable in tests.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// prColumns is the canonical SELECT column order; scanPR must match it.
const prColumns = `id, repo, number, ownership, author, state, branch, base, url,
	head_sha, last_synced_at, kind, languages, size, urgency, urgency_score, urgency_reasons`

type rowScanner interface{ Scan(dest ...any) error }

// scanPR scans one pull_request row (in prColumns order), decoding the JSON
// languages/urgency_reasons columns.
func scanPR(s rowScanner) (PullRequest, error) {
	var pr PullRequest
	var langs, reasons string
	if err := s.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
		&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt,
		&pr.Kind, &langs, &pr.Size, &pr.Urgency, &pr.UrgencyScore, &reasons); err != nil {
		return pr, err
	}
	pr.Languages = decodeJSONSlice(langs)
	pr.UrgencyReasons = decodeJSONSlice(reasons)
	return pr, nil
}

func decodeJSONSlice(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// Enrichment is the computed enrichment payload persisted by SetEnrichment.
// Kept store-local (no dependency on internal/enrich) so the store package
// stays free of go-enry.
type Enrichment struct {
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
}

// SetEnrichment writes ONLY the enrichment columns for an existing PR row via a
// targeted UPDATE. These columns are deliberately absent from UpsertPR, so a
// lifecycle upsert (or ingest's full-row upsert) cannot clobber them. A missing
// row is a no-op (0 rows affected); the lifecycle emit always creates the row
// first.
func (db *DB) SetEnrichment(ctx context.Context, repo string, number int, e Enrichment) error {
	langs, err := json.Marshal(nonNilSlice(e.Languages))
	if err != nil {
		return fmt.Errorf("store: marshal languages: %w", err)
	}
	reasons, err := json.Marshal(nonNilSlice(e.UrgencyReasons))
	if err != nil {
		return fmt.Errorf("store: marshal urgency_reasons: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
UPDATE pull_request SET kind=?, languages=?, size=?, urgency=?, urgency_score=?, urgency_reasons=?, updated_at=?
WHERE repo=? AND number=?`,
		e.Kind, string(langs), e.Size, e.Urgency, e.UrgencyScore, string(reasons), nowRFC3339(), repo, number)
	if err != nil {
		return fmt.Errorf("store: set enrichment %s#%d: %w", repo, number, err)
	}
	return nil
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

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
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+prColumns+" FROM pull_request WHERE repo=? AND state IN ('open','draft')", repo)
	if err != nil {
		return nil, fmt.Errorf("store: list open prs %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PullRequest
	for rows.Next() {
		pr, err := scanPR(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan open pr %s: %w", repo, err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// GetPR returns the PR by (repo, number), or nil if not found.
func (db *DB) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx,
		"SELECT "+prColumns+" FROM pull_request WHERE repo=? AND number=?", repo, number)
	pr, err := scanPR(row)
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
	row := db.sql.QueryRowContext(ctx, "SELECT "+prColumns+" FROM pull_request WHERE id=?", id)
	pr, err := scanPR(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr id=%d: %w", id, err)
	}
	return &pr, nil
}
