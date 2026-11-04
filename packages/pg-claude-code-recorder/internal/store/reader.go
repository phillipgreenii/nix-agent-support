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

// ctxQuerier is a Querier whose every statement runs under ctx (QueryContext),
// so cancelling ctx interrupts the statement in flight and not only the next
// one. database/sql's own Query on a transaction runs under context.Background
// (the transaction's context only rolls it back, and that waits for the
// statement), which is why a read is bound explicitly.
type ctxQuerier struct {
	ctx context.Context
	q   Querier
}

type ctxQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (c ctxQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	if cq, ok := c.q.(ctxQueryer); ok {
		return cq.QueryContext(c.ctx, query, args...)
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.q.Query(query, args...)
}

func (c ctxQuerier) QueryRow(query string, args ...any) *sql.Row {
	if cq, ok := c.q.(ctxQueryer); ok {
		return cq.QueryRowContext(c.ctx, query, args...)
	}
	return c.q.QueryRow(query, args...)
}

// BindCtx returns q with every statement bound to ctx. Binding a nil ctx, or
// binding twice, is harmless: the inner binding is replaced.
func BindCtx(ctx context.Context, q Querier) Querier {
	if ctx == nil {
		return q
	}
	if c, ok := q.(ctxQuerier); ok {
		q = c.q
	}
	return ctxQuerier{ctx: ctx, q: q}
}

// Reader carries the store's read methods over a Querier. The Store embeds a
// Reader over the read pool; ReadTx hands fn a Reader over one transaction, so
// a multi-statement read takes the WAL read lock once and sees one snapshot.
type Reader struct{ q Querier }

// Q returns the querier for a raw read statement.
func (s *Reader) Q() Querier { return s.q }

// WithContext returns a Reader over the same connection (the pool or the open
// transaction) whose every read method runs its statements under ctx: the
// context variant of each Reader method. A cancelled ctx ends the statement in
// flight and returns the context error; the connection goes back to the pool
// clean.
func (s *Reader) WithContext(ctx context.Context) *Reader {
	return &Reader{q: BindCtx(ctx, s.q)}
}

// ReadTx runs fn with a Reader over one read transaction on the read pool. The
// transaction is always rolled back (it only reads) and ends at the read
// deadline like every read-pool transaction, so fn MUST be a bounded read: it
// MUST NOT wait on a writer, and a caller that needs the result of its own
// write MUST NOT read it through this Reader (the snapshot predates it).
func (s *Store) ReadTx(fn func(*Reader) error) error {
	return s.ReadTxCtx(context.Background(), fn)
}

// ReadTxCtx is ReadTx under ctx: the transaction begins with ctx and fn's
// Reader runs every statement under ctx, so cancelling ctx stops the read in
// flight and not only the BeginTx. The transaction still ends at the read
// deadline (the connection fixes it at BeginTx), so a request deadline works
// through ctx and the earlier of the two ends the statement.
func (s *Store) ReadTxCtx(ctx context.Context, fn func(*Reader) error) error {
	s.mu.Lock()
	db := s.rdb
	s.mu.Unlock()
	if db == nil {
		return errors.New("store closed")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(&Reader{q: BindCtx(ctx, tx)})
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
