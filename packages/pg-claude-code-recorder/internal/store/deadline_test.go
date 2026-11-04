package store

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// slowQuery runs for far longer than any test waits: a recursive count that also reads a table, so it holds a read snapshot.
const slowQuery = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 4000000000) SELECT count(*) + (SELECT count(*) FROM meta) FROM c`

func checkpoint(t *testing.T, s *Store, mode string) (busy, log, done int) {
	t.Helper()
	if err := s.wdb.QueryRow(`PRAGMA wal_checkpoint(`+mode+`)`).Scan(&busy, &log, &done); err != nil {
		t.Fatal(err)
	}
	return busy, log, done
}

// A held-open slow read is cut at the deadline, and the WAL then resets.
// firingCtx is a context whose deadline fires when the test says so, not when
// a clock does: it reports context.DeadlineExceeded after fire.
type firingCtx struct {
	context.Context
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *firingCtx) Err() error {
	if c.fired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

func (c *firingCtx) fire() {
	c.fired.Store(true)
	c.cancel()
}

// The test fires the deadline itself, after the reader is shown to pin the
// WAL, so no wall-clock margin decides the outcome however loaded the host is.
func TestSlowReadIsCutAtTheDeadlineAndTheWALResets(t *testing.T) {
	fires := make(chan *firingCtx, 4)
	s, err := Open(t.TempDir(), Options{stmtCtx: func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		inner, cancel := context.WithCancel(ctx)
		fc := &firingCtx{Context: inner, cancel: cancel}
		fires <- fc
		return fc, cancel
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// The guard is never cancelled by a timer: a missing deadline would hang
	// the run until the package timeout, never pass it.
	guard, stop := context.WithCancel(context.Background())
	defer stop()
	res := make(chan error, 1)
	go func() {
		var n int64
		res <- s.Read().QueryRowContext(guard, slowQuery).Scan(&n)
	}()

	// Write after the reader began until a checkpoint shows the reader pins
	// the log: frames written after its snapshot cannot be backfilled.
	pinned := false
	for i := 0; i < 100000 && !pinned; i++ {
		if _, err := s.wdb.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('probe', ?)`, i); err != nil {
			t.Fatal(err)
		}
		_, log, done := checkpoint(t, s, "PASSIVE")
		pinned = done < log
		if !pinned {
			time.Sleep(time.Millisecond)
		}
	}
	if !pinned {
		t.Fatal("the slow reader never pinned the WAL; the fixture proves nothing")
	}

	(<-fires).fire()
	if err := <-res; !errors.Is(err, context.DeadlineExceeded) || guard.Err() != nil {
		t.Fatalf("the read ended with %v (guard %v), want the store deadline", err, guard.Err())
	}

	busy, log, done := checkpoint(t, s, "TRUNCATE")
	if busy != 0 || log != done {
		t.Fatalf("checkpoint after the deadline: busy=%d log=%d done=%d, want busy 0 and done == log", busy, log, done)
	}
}

// A read transaction as a whole ends at the deadline, not only each statement.
func TestReadTransactionEndsAtTheDeadline(t *testing.T) {
	s, err := Open(t.TempDir(), Options{ReadTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	tx, err := s.Read().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := tx.QueryRow(`SELECT 1`).Scan(&n); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a statement after the transaction deadline returned %v, want the deadline error", err)
	}
}

// Every connection the read pool opens carries query_only and busy_timeout.
func TestReopenedReadConnectionsKeepTheirPragmas(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.Read().SetMaxIdleConns(0) // every use opens a fresh connection
	for i := 0; i < 3; i++ {
		var qo, bt int
		if err := s.Read().QueryRow(`PRAGMA query_only`).Scan(&qo); err != nil {
			t.Fatal(err)
		}
		if err := s.Read().QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil {
			t.Fatal(err)
		}
		if qo != 1 || bt != 5000 {
			t.Fatalf("connection %d: query_only=%d busy_timeout=%d, want 1 and 5000", i, qo, bt)
		}
	}
	if _, err := s.Read().Exec(`INSERT INTO meta(key,value) VALUES('x','y')`); err == nil {
		t.Fatal("the read pool accepted a write")
	}
}

// Statements and transactions on the read pool charge their run time to the
// request trace in their context, so a timed-out request can split its time
// into running and waiting; a context with no trace costs nothing.
func TestReadPoolChargesRunTimeToTheRequestTrace(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	tr := &obs.ReqTrace{ID: "r1"}
	ctx := obs.WithTrace(context.Background(), tr)
	var n int
	if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	afterStmt := tr.Exec()
	if afterStmt <= 0 {
		t.Fatalf("a statement outside a transaction was not charged: %v", afterStmt)
	}
	tx, err := s.Read().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if got := tr.Exec(); got != afterStmt {
		t.Fatalf("charged while the transaction was open: %v then %v", afterStmt, got)
	}
	_ = tx.Rollback()
	if tr.Exec() <= afterStmt {
		t.Fatalf("the transaction was not charged at its end: %v", tr.Exec())
	}
	other := &obs.ReqTrace{}
	if err := s.Read().QueryRowContext(context.Background(), `SELECT count(*) FROM meta`).Scan(&n); err != nil || other.Exec() != 0 {
		t.Fatalf("untraced read: %v %v", err, other.Exec())
	}
}

// Open reports each store start phase to Options.Phase, in order, with the
// phase ended before the next begins.
func TestOpenReportsStartPhases(t *testing.T) {
	var log []string
	s, err := Open(t.TempDir(), Options{Phase: func(name string) func() {
		log = append(log, "begin "+name)
		return func() { log = append(log, "end "+name) }
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	want := []string{"begin store_journal_read", "end store_journal_read", "begin store_open_db", "end store_open_db", "begin store_replay", "end store_replay"}
	if strings.Join(log, ",") != strings.Join(want, ",") {
		t.Fatalf("phases %v, want %v", log, want)
	}
}
