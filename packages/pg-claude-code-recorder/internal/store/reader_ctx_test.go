package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// ctxPing is a SQLite scalar function that tells the test a statement is now
// running (the first call), so a cancel can follow the statement's start with
// no timing. It returns 1 so a CPU-bound query keeps going.
var (
	ctxPingMu sync.Mutex
	ctxPingCh chan struct{}
)

func init() {
	sqlite.MustRegisterScalarFunction("ctxtest_ping", 1, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		ctxPingMu.Lock()
		if ctxPingCh != nil {
			close(ctxPingCh)
			ctxPingCh = nil
		}
		ctxPingMu.Unlock()
		return int64(1), nil
	})
}

func armPing() chan struct{} {
	ch := make(chan struct{})
	ctxPingMu.Lock()
	ctxPingCh = ch
	ctxPingMu.Unlock()
	return ch
}

// A CPU-bound statement that calls ctxtest_ping for every row.
const pingingSlowQuery = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 4000000000) SELECT count(*) FROM c WHERE ctxtest_ping(x) >= 0`

func openCtxStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Cancelling the context while a statement runs inside the read transaction
// ends that statement with the context error, and the pool then serves the next
// read (the connection came back clean). Real SQLite: the interrupt semantics
// are the point.
func TestReadTxCtxCancelInterruptsTheRunningStatement(t *testing.T) {
	s := openCtxStore(t)
	started := armPing()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() {
		res <- s.ReadTxCtx(ctx, func(rd *Reader) error {
			var n int64
			return rd.Q().QueryRow(pingingSlowQuery).Scan(&n)
		})
	}()
	<-started // the statement is running
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("read ended with %v, want context.Canceled", err)
	}
	if err := s.ReadTx(func(rd *Reader) error {
		var n int
		return rd.Q().QueryRow(`SELECT 1`).Scan(&n)
	}); err != nil {
		t.Fatalf("a read after the cancel failed: %v", err)
	}
	if st := s.PoolStats().Read; st.InUse != 0 {
		t.Fatalf("%d read connections still in use after the cancel", st.InUse)
	}
}

// A Reader method (not only raw Q) honours the context: WithContext is the
// context variant of every method.
func TestReaderWithContextMethodsStopOnACancelledContext(t *testing.T) {
	s := openCtxStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Reader.WithContext(ctx).AllFiles(); !errors.Is(err, context.Canceled) {
		t.Fatalf("AllFiles under a cancelled context returned %v", err)
	}
	if _, err := s.AllFiles(); err != nil {
		t.Fatalf("the pool reader without a context failed: %v", err)
	}
}

// A request deadline given through the BeginTx context ends a statement inside
// the transaction (the connection fixes its own end at BeginTx), even though
// the store's read timeout is far away.
func TestReadTxCtxDeadlineEndsTheStatementInTheTransaction(t *testing.T) {
	s := openCtxStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.ReadTxCtx(ctx, func(rd *Reader) error {
		var n int64
		return rd.Q().QueryRow(slowQuery).Scan(&n)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read ended with %v, want the request deadline", err)
	}
	if err := s.ReadTx(func(rd *Reader) error {
		var n int
		return rd.Q().QueryRow(`SELECT 1`).Scan(&n)
	}); err != nil {
		t.Fatalf("a read after the deadline failed: %v", err)
	}
}

// dconn.bound takes the earlier of the request deadline and the transaction end.
func TestBoundTakesTheEarlierOfRequestAndTransactionDeadline(t *testing.T) {
	cn := &dconn{timeout: time.Hour}
	now := time.Now()

	cn.setTxEnd(now.Add(time.Minute))
	req, cancelReq := context.WithDeadline(context.Background(), now.Add(time.Hour))
	defer cancelReq()
	bctx, cancel := cn.bound(req)
	if dl, _ := bctx.Deadline(); !dl.Equal(now.Add(time.Minute)) {
		t.Fatalf("transaction end earlier: deadline %v, want the transaction end", dl)
	}
	cancel()

	cn.setTxEnd(now.Add(time.Hour))
	req2, cancelReq2 := context.WithDeadline(context.Background(), now.Add(time.Minute))
	defer cancelReq2()
	bctx, cancel = cn.bound(req2)
	defer cancel()
	if dl, _ := bctx.Deadline(); !dl.Equal(now.Add(time.Minute)) {
		t.Fatalf("request deadline earlier: deadline %v, want the request deadline", dl)
	}
}
