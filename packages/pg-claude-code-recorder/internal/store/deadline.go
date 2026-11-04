package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// DefaultReadTimeout is the deadline every read-pool statement and read
// transaction runs under unless Options.ReadTimeout says otherwise.
const DefaultReadTimeout = 30 * time.Second

// Read-pool connection recycling. A pool lifetime alone cannot unpin a WAL
// (database/sql never closes a connection in use); the deadline does that.
// These only retire idle connections so none lives forever. A connection that
// is closed throws its page cache away, and against a file of tens of
// gigabytes every fresh connection starts cold, so the bounds are long: a
// connection is replaced rarely (and only when idle), and the pool keeps a few
// idle ones so a burst of concurrent readers does not close and re-open them.
const (
	readConnMaxLifetime = 6 * time.Hour
	readConnMaxIdleTime = 30 * time.Minute
	readMaxIdleConns    = 4
)

// Read connection memory. Both are ceilings per connection: SQLite fills the
// page cache lazily, and mapped pages are shared file cache the kernel evicts.
const (
	// DefaultReadCacheKiB is the default cache_size of a read connection: 64 MiB
	// (the driver default is about 2 MiB).
	DefaultReadCacheKiB = 64 * 1024
	// DefaultReadMmapBytes is the default mmap_size ceiling of a read connection.
	DefaultReadMmapBytes = int64(512) << 20
)

// readPragmas is the DSN tail of a read connection: read only, a larger page
// cache and a bounded memory map.
func (s *Store) readPragmas() string {
	kib := s.readCacheKiB
	if kib <= 0 {
		kib = DefaultReadCacheKiB
	}
	mm := s.readMmap
	if mm == 0 {
		mm = DefaultReadMmapBytes
	}
	if mm < 0 {
		mm = 0
	}
	return "&_pragma=query_only(1)&_pragma=cache_size(-" + strconv.Itoa(kib) + ")&_pragma=mmap_size(" + strconv.FormatInt(mm, 10) + ")"
}

// deadlineConnector opens read-pool connections whose every statement, and
// every transaction as a whole, ends at a deadline: a reader cannot pin the
// WAL snapshot for longer than the timeout, so a checkpoint can reset the log.
type deadlineConnector struct {
	under   driver.Driver
	dsn     string
	timeout time.Duration
	// stmtCtx, when set, replaces the wall-clock deadline of a statement that
	// runs outside a transaction (a test injects its own trigger).
	stmtCtx stmtCtxFunc
}

// stmtCtxFunc derives the context a standalone statement runs under.
type stmtCtxFunc func(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc)

func (c *deadlineConnector) Connect(context.Context) (driver.Conn, error) {
	cn, err := c.under.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &dconn{Conn: cn, timeout: c.timeout, stmtCtx: c.stmtCtx}, nil
}

func (c *deadlineConnector) Driver() driver.Driver { return c.under }

// openDeadlineDB opens dsn on the named driver with the deadline wrapper.
func openDeadlineDB(driverName, dsn string, timeout time.Duration, stmtCtx stmtCtxFunc) (*sql.DB, error) {
	probe, err := sql.Open(driverName, "")
	if err != nil {
		return nil, err
	}
	under := probe.Driver()
	_ = probe.Close()
	return sql.OpenDB(&deadlineConnector{under: under, dsn: dsn, timeout: timeout, stmtCtx: stmtCtx}), nil
}

type dconn struct {
	driver.Conn
	timeout time.Duration
	stmtCtx stmtCtxFunc

	mu    sync.Mutex
	txEnd time.Time // end of the open transaction's deadline; zero outside one

	// txTrace and txStart account the open transaction's run time to its request.
	txTrace *obs.ReqTrace
	txStart time.Time
}

// stmtTrace returns the request trace to charge a statement to, with its
// start time; nil when the statement runs inside a transaction (the
// transaction is charged as a whole) or the request carries no trace.
func (cn *dconn) stmtTrace(ctx context.Context) (*obs.ReqTrace, time.Time) {
	cn.mu.Lock()
	inTx := !cn.txEnd.IsZero()
	cn.mu.Unlock()
	if inTx {
		return nil, time.Time{}
	}
	if tr := obs.TraceFrom(ctx); tr != nil {
		return tr, time.Now()
	}
	return nil, time.Time{}
}

// endTx charges the open transaction's run time to its request.
func (cn *dconn) endTx() {
	cn.mu.Lock()
	tr, t0 := cn.txTrace, cn.txStart
	cn.txTrace, cn.txStart = nil, time.Time{}
	cn.mu.Unlock()
	if tr != nil {
		tr.AddExec(time.Since(t0))
	}
}

// bound derives the context a statement runs under: the open transaction's
// end, else now plus the timeout. The returned cancel MUST run once the
// statement (or its rows) is done.
func (cn *dconn) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	cn.mu.Lock()
	end := cn.txEnd
	cn.mu.Unlock()
	if end.IsZero() && cn.stmtCtx != nil {
		return cn.stmtCtx(ctx, cn.timeout)
	}
	if end.IsZero() {
		end = time.Now().Add(cn.timeout)
	}
	return context.WithDeadline(ctx, end)
}

func (cn *dconn) setTxEnd(t time.Time) {
	cn.mu.Lock()
	cn.txEnd = t
	cn.mu.Unlock()
}

// fixErr reports the deadline as the context error whatever the driver says.
func fixErr(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (cn *dconn) Prepare(query string) (driver.Stmt, error) {
	st, err := cn.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &dstmt{Stmt: st, cn: cn}, nil
}

func (cn *dconn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
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
	return &dstmt{Stmt: st, cn: cn}, nil
}

func (cn *dconn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if b, ok := cn.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = cn.Conn.Begin() //nolint:staticcheck // fallback for drivers without BeginTx
	}
	if err != nil {
		return nil, err
	}
	cn.setTxEnd(time.Now().Add(cn.timeout))
	if tr := obs.TraceFrom(ctx); tr != nil {
		cn.mu.Lock()
		cn.txTrace, cn.txStart = tr, time.Now()
		cn.mu.Unlock()
	}
	return &dtx{Tx: tx, cn: cn}, nil
}

func (cn *dconn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := cn.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	tr, t0 := cn.stmtTrace(ctx)
	bctx, cancel := cn.bound(ctx)
	rs, err := q.QueryContext(bctx, query, args)
	if err != nil {
		err = fixErr(bctx, err)
		cancel()
		tr.AddExec(time.Since(t0))
		return nil, err
	}
	return &drows{Rows: rs, ctx: bctx, cancel: cancel, tr: tr, t0: t0}, nil
}

func (cn *dconn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := cn.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	tr, t0 := cn.stmtTrace(ctx)
	bctx, cancel := cn.bound(ctx)
	defer cancel()
	defer func() { tr.AddExec(time.Since(t0)) }()
	res, err := e.ExecContext(bctx, query, args)
	return res, fixErr(bctx, err)
}

func (cn *dconn) Ping(ctx context.Context) error {
	if p, ok := cn.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (cn *dconn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := cn.Conn.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (cn *dconn) ResetSession(ctx context.Context) error {
	cn.endTx()
	cn.setTxEnd(time.Time{})
	if r, ok := cn.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

type dtx struct {
	driver.Tx
	cn *dconn
}

func (t *dtx) Commit() error {
	t.cn.endTx()
	t.cn.setTxEnd(time.Time{})
	return t.Tx.Commit()
}

func (t *dtx) Rollback() error {
	t.cn.endTx()
	t.cn.setTxEnd(time.Time{})
	return t.Tx.Rollback()
}

type dstmt struct {
	driver.Stmt
	cn *dconn
}

func (s *dstmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	tr, t0 := s.cn.stmtTrace(ctx)
	bctx, cancel := s.cn.bound(ctx)
	defer cancel()
	defer func() { tr.AddExec(time.Since(t0)) }()
	var res driver.Result
	var err error
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		res, err = e.ExecContext(bctx, args)
	} else {
		res, err = s.Stmt.Exec(values(args)) //nolint:staticcheck // fallback
	}
	return res, fixErr(bctx, err)
}

func (s *dstmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	tr, t0 := s.cn.stmtTrace(ctx)
	bctx, cancel := s.cn.bound(ctx)
	var rs driver.Rows
	var err error
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rs, err = q.QueryContext(bctx, args)
	} else {
		rs, err = s.Stmt.Query(values(args)) //nolint:staticcheck // fallback
	}
	if err != nil {
		err = fixErr(bctx, err)
		cancel()
		tr.AddExec(time.Since(t0))
		return nil, err
	}
	return &drows{Rows: rs, ctx: bctx, cancel: cancel, tr: tr, t0: t0}, nil
}

func (s *dstmt) CheckNamedValue(nv *driver.NamedValue) error {
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

// drows releases the statement's deadline timer when the rows close.
type drows struct {
	driver.Rows
	ctx    context.Context
	cancel context.CancelFunc
	tr     *obs.ReqTrace // charged the statement's run time at Close; nil when untraced
	t0     time.Time
}

func (r *drows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err != nil && err != io.EOF {
		return fixErr(r.ctx, err)
	}
	return err
}

func (r *drows) Close() error {
	err := r.Rows.Close()
	r.cancel()
	if r.tr != nil {
		r.tr.AddExec(time.Since(r.t0))
		r.tr = nil
	}
	return err
}
