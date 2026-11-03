// Package countdriver is TEST SUPPORT: a database/sql/driver wrapper around
// the SQLite driver that counts the statements sent to the database and the
// rows read back, so a test can assert how much work a code path does (read
// counts, never wall-clock time). It lives outside _test.go files so tests in
// other packages can import it. Nothing in the daemon or the CLI imports it.
package countdriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite" // the wrapped driver
)

// Counter reports what the wrapped driver has seen since the last Reset.
type Counter struct {
	queries atomic.Int64
	rows    atomic.Int64
}

// Queries is the number of statements passed to QueryContext, ExecContext and
// Prepare.
func (c *Counter) Queries() int64 { return c.queries.Load() }

// RowsRead is the number of rows returned through Rows.Next.
func (c *Counter) RowsRead() int64 { return c.rows.Load() }

// Reset zeroes both counters.
func (c *Counter) Reset() {
	c.queries.Store(0)
	c.rows.Store(0)
}

var (
	mu       sync.Mutex
	registry = map[string]*Counter{}
)

// Register registers a counting driver under name and returns its counter.
// Registering the same name again returns the same counter (it is safe under
// go test -count=N and for several stores opened with one name).
func Register(name string) *Counter {
	mu.Lock()
	defer mu.Unlock()
	if c, ok := registry[name]; ok {
		return c
	}
	probe, err := sql.Open("sqlite", "")
	if err != nil {
		panic("countdriver: " + err.Error())
	}
	under := probe.Driver()
	_ = probe.Close()
	c := &Counter{}
	sql.Register(name, &drv{under: under, c: c})
	registry[name] = c
	return c
}

type drv struct {
	under driver.Driver
	c     *Counter
}

func (d *drv) Open(name string) (driver.Conn, error) {
	cn, err := d.under.Open(name)
	if err != nil {
		return nil, err
	}
	return &conn{Conn: cn, c: d.c}, nil
}

type conn struct {
	driver.Conn
	c *Counter
}

func (cn *conn) Prepare(query string) (driver.Stmt, error) {
	cn.c.queries.Add(1)
	st, err := cn.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &stmt{Stmt: st, c: cn.c}, nil
}

func (cn *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	cn.c.queries.Add(1)
	var st driver.Stmt
	var err error
	if p, ok := cn.Conn.(driver.ConnPrepareContext); ok {
		st, err = p.PrepareContext(ctx, query)
	} else {
		st, err = cn.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &stmt{Stmt: st, c: cn.c}, nil
}

func (cn *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := cn.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return cn.Conn.Begin() //nolint:staticcheck // fallback for drivers without BeginTx
}

func (cn *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := cn.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.c.queries.Add(1)
	rs, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &rows{Rows: rs, c: cn.c}, nil
}

func (cn *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := cn.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	cn.c.queries.Add(1)
	return e.ExecContext(ctx, query, args)
}

func (cn *conn) Ping(ctx context.Context) error {
	if p, ok := cn.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (cn *conn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := cn.Conn.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (cn *conn) ResetSession(ctx context.Context) error {
	if r, ok := cn.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

type stmt struct {
	driver.Stmt
	c *Counter
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return s.Stmt.Exec(values(args)) //nolint:staticcheck // fallback
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	var rs driver.Rows
	var err error
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rs, err = q.QueryContext(ctx, args)
	} else {
		rs, err = s.Stmt.Query(values(args)) //nolint:staticcheck // fallback
	}
	if err != nil {
		return nil, err
	}
	return &rows{Rows: rs, c: s.c}, nil
}

func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := s.Stmt.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}

type rows struct {
	driver.Rows
	c *Counter
}

func (r *rows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == nil {
		r.c.rows.Add(1)
	}
	return err
}
