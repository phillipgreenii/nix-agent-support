// Package sessionmeta is ccpool's PUBLIC, importable surface for attaching and
// querying arbitrary key/value metadata on a ccpool session (pg2-01ys, Option 2).
// It is the ONLY exported ccpool Go API; the rest of ccpool stays internal. It is
// a thin facade over internal/store's metadata methods.
//
// Concurrency: a Store wraps the same SQLite DB the ccpool binary uses, opened
// WAL + busy_timeout (see internal/store.Open). Two processes (e.g. ccpool and
// pr-pool) may hold Stores on the same DB; every write is a single autocommit
// statement, so cross-process contention is covered by busy_timeout. Concurrent
// writes to the same (externalID,key) are last-writer-wins.
package sessionmeta

import (
	"context"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// Store is a handle to a ccpool pool's session metadata.
type Store struct{ st *store.Store }

// Open opens the metadata store for the ccpool pool whose SQLite DB is at dbPath
// (use DBPathForPool to resolve it from a pool root). Migrations run on open
// (idempotent). Close when done.
func Open(dbPath string) (*Store, error) {
	st, err := store.Open(dbPath, clock.Real{})
	if err != nil {
		return nil, err
	}
	return &Store{st: st}, nil
}

// OpenPool opens the metadata store for the pool rooted at poolRoot (empty =
// default XDG pool), resolving the DB path the way the ccpool CLI does.
func OpenPool(poolRoot string) (*Store, error) {
	dbPath, err := DBPathForPool(poolRoot)
	if err != nil {
		return nil, err
	}
	return Open(dbPath)
}

// DBPathForPool resolves the SQLite DB path for the pool rooted at poolRoot
// (empty = default XDG pool), matching ccpool's own resolution.
func DBPathForPool(poolRoot string) (string, error) {
	cfg, err := config.LoadForPool(poolRoot)
	if err != nil {
		return "", err
	}
	return cfg.DBPath, nil
}

func (s *Store) Close() error { return s.st.Close() }

// Set upserts value for (externalID, key). Empty key errors; empty value is a
// valid bare tag. Replaces any existing value.
func (s *Store) Set(ctx context.Context, externalID, key, value string) error {
	return s.st.SetMeta(ctx, externalID, key, value)
}

// Get returns the value for (externalID, key). ok=false (no error) when unset.
func (s *Store) Get(ctx context.Context, externalID, key string) (string, bool, error) {
	return s.st.GetMeta(ctx, externalID, key)
}

// Meta returns all metadata for externalID (non-nil empty map when none).
func (s *Store) Meta(ctx context.Context, externalID string) (map[string]string, error) {
	return s.st.Meta(ctx, externalID)
}

// Delete removes (externalID, key). Removing an absent key is not an error.
func (s *Store) Delete(ctx context.Context, externalID, key string) error {
	return s.st.DeleteMeta(ctx, externalID, key)
}

// ListByMeta returns external_ids whose metadata matches ALL filters (AND across
// keys; exact value per key), sorted ascending. Empty filters returns every
// external_id that has any metadata row.
func (s *Store) ListByMeta(ctx context.Context, filters map[string]string) ([]string, error) {
	return s.st.ListExternalIDsByMeta(ctx, filters)
}
