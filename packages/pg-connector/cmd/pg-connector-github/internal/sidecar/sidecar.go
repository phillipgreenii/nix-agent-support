// Package sidecar is the daemon's posted-review sidecar: the per-PR record of
// which review comments review_submit posted, kept in the daemon's own sqlite
// store (table posted_review, schema version 9).
//
// One row per (owner, repo, number); state is the posted.State JSON verbatim and
// updated_at is Unix milliseconds. The Store has Load and Save only. There is
// no cross-process lock: Engine.SerializeWrite serializes writes to one PR
// inside the daemon, the daemon.lock file keeps the daemon single-instance, and
// no sqlite transaction is held across the GitHub write (the store has one
// pooled connection), so a crash holds nothing.
//
// A corrupt row stays a refusal: Load wraps posted.ErrCorrupt and leaves the row
// where it is. The operator deletes a record by hand with a raw SQL DELETE on
// store.sqlite:
//
//	DELETE FROM posted_review WHERE owner = ? AND repo = ? AND number = ?
//
// The records of the old pg-connector-pr-github backend are brought in once at
// daemon start by package legacyimport, the only code that names the old state
// directory.
package sidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Store reads and writes the posted_review table of the daemon's store.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open returns the sidecar over st. A nil st is an error.
func Open(st *store.Store) (*Store, error) {
	if st == nil {
		return nil, errors.New("sidecar: no store")
	}
	return &Store{db: st.DB(), now: time.Now}, nil
}

func checkKey(owner, repo string, pr int) error {
	if owner == "" || repo == "" {
		return fmt.Errorf("sidecar: %q/%q is not an owner/name", owner, repo)
	}
	if pr <= 0 {
		return fmt.Errorf("sidecar: PR number %d is not positive", pr)
	}
	return nil
}

// Load reads a PR's record. A missing row is an empty State, not an error. A row
// whose state does not decode returns an error wrapping posted.ErrCorrupt and
// the row stays.
func (s *Store) Load(ctx context.Context, owner, repo string, pr int) (posted.State, error) {
	if err := checkKey(owner, repo, pr); err != nil {
		return posted.State{}, err
	}
	var text string
	err := s.db.QueryRowContext(ctx,
		`SELECT state FROM posted_review WHERE owner = ? AND repo = ? AND number = ?`, owner, repo, pr).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return posted.State{Version: posted.StateVersion}, nil
	}
	if err != nil {
		return posted.State{}, fmt.Errorf("sidecar: read record: %w", err)
	}
	var st posted.State
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		return posted.State{}, fmt.Errorf("%w: %s/%s#%d: %v", posted.ErrCorrupt, owner, repo, pr, err)
	}
	return st, nil
}

// Save replaces a PR's record. Callers write an entry only for fingerprints
// confirmed by the post-write re-read.
func (s *Store) Save(ctx context.Context, owner, repo string, pr int, st posted.State) error {
	if err := checkKey(owner, repo, pr); err != nil {
		return err
	}
	st.Version = posted.StateVersion
	if st.Fingerprints == nil {
		st.Fingerprints = []string{}
	}
	if st.BodyHeads == nil {
		st.BodyHeads = []string{}
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("sidecar: marshal record: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (owner, repo, number) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		owner, repo, pr, string(data), s.now().UnixMilli()); err != nil {
		return fmt.Errorf("sidecar: write record: %w", err)
	}
	return nil
}
