package store

import (
	"context"
	"database/sql"
	"errors"
)

// Querier is what a read needs: a pool (one implicit transaction per
// statement) or one open read transaction (one snapshot for every statement).
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Reader carries the store's read methods over a Querier. The Store embeds a
// Reader over the read pool; ReadTx hands fn a Reader over one transaction, so
// a multi-statement read takes the WAL read lock once and sees one snapshot.
type Reader struct{ q Querier }

// Q returns the querier for a raw read statement.
func (s *Reader) Q() Querier { return s.q }

// ReadTx runs fn with a Reader over one read transaction on the read pool. The
// transaction is always rolled back (it only reads) and ends at the read
// deadline like every read-pool transaction, so fn MUST be a bounded read: it
// MUST NOT wait on a writer, and a caller that needs the result of its own
// write MUST NOT read it through this Reader (the snapshot predates it).
func (s *Store) ReadTx(fn func(*Reader) error) error {
	s.mu.Lock()
	db := s.rdb
	s.mu.Unlock()
	if db == nil {
		return errors.New("store closed")
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(&Reader{q: tx})
}

// PoolStats is the database/sql pool statistics of the store's connections.
type PoolStats struct {
	Read, Write sql.DBStats
}

// PoolStats returns the pool statistics of the read and the write handles
// (counters kept in memory; it issues no query).
func (s *Store) PoolStats() PoolStats {
	s.mu.Lock()
	r, w := s.rdb, s.wdb
	s.mu.Unlock()
	var ps PoolStats
	if r != nil {
		ps.Read = r.Stats()
	}
	if w != nil {
		ps.Write = w.Stats()
	}
	return ps
}
