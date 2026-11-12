package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
)

// QueryCounter counts what a store handle sends to SQLite: statements run
// and bytes of column data read back. It wraps the REAL driver connection, so
// the store under it is the real store on real SQLite, not a mock. Tests use
// it to prove a code path issues a constant number of queries and reads no
// facts bytes. Production never constructs one.
type QueryCounter struct {
	queries atomic.Int64
	bytes   atomic.Int64
	mu      sync.Mutex
	sqls    []string
}

// Queries is the number of statements (queries, execs, prepares) run so far.
func (c *QueryCounter) Queries() int64 { return c.queries.Load() }

// Bytes is the number of string and blob column bytes read back so far.
func (c *QueryCounter) Bytes() int64 { return c.bytes.Load() }

// SQL returns the statement texts run so far, in order.
func (c *QueryCounter) SQL() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sqls...)
}

func (c *QueryCounter) note(q string) {
	c.queries.Add(1)
	c.mu.Lock()
	c.sqls = append(c.sqls, q)
	c.mu.Unlock()
}

type countingConnector struct {
	dsn string
	drv driver.Driver
	c   *QueryCounter
}

func (cc *countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := cc.drv.Open(cc.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, c: cc.c}, nil
}

func (cc *countingConnector) Driver() driver.Driver { return cc.drv }

type countingConn struct {
	driver.Conn
	c *QueryCounter
}

func (cn *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := cn.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.c.note(query)
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &countingRows{Rows: rows, c: cn.c}, nil
}

func (cn *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := cn.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.c.note(query)
	return e.ExecContext(ctx, query, args)
}

func (cn *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	cn.c.note(query)
	if p, ok := cn.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return cn.Prepare(query)
}

func (cn *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := cn.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return cn.Conn.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx
}

type countingRows struct {
	driver.Rows
	c *QueryCounter
}

func (r *countingRows) Next(dest []driver.Value) error {
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	var n int64
	for _, v := range dest {
		switch x := v.(type) {
		case string:
			n += int64(len(x))
		case []byte:
			n += int64(len(x))
		}
	}
	r.c.bytes.Add(n)
	return nil
}

// OpenCountingForTest opens a store on a fresh temp file exactly as
// OpenForTest does, but through a driver connection that counts statements and
// bytes read. The counter starts at zero after the open (the schema check and
// migrations are not counted).
func OpenCountingForTest(t testT) (*Store, *QueryCounter) {
	SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("OpenCountingForTest: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("OpenCountingForTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.WithCounterForTest(t)
}

// WithCounterForTest returns a second handle on the same file as s whose
// connection counts, together with its counter. The handle shares the file,
// not the in-process state, of s.
func (s *Store) WithCounterForTest(t testT) (*Store, *QueryCounter) {
	probe, err := sql.Open("sqlite", "file:"+s.path)
	if err != nil {
		t.Fatalf("WithCounterForTest: %v", err)
	}
	drv := probe.Driver()
	_ = probe.Close()
	c := &QueryCounter{}
	dsn := sqliteopen.DSN(sqliteopen.Standard(s.path))
	db := sql.OpenDB(&countingConnector{dsn: dsn, drv: drv, c: c})
	db.SetMaxOpenConns(1)
	cs := &Store{sql: db, path: s.path}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, c
}

// ExecForTest runs a statement on the store without any validation, for
// fixtures that must build many rows faster than the writers can.
func (s *Store) ExecForTest(t seedT, query string, args ...any) {
	t.Helper()
	if _, err := s.sql.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("ExecForTest: %v", err)
	}
}
