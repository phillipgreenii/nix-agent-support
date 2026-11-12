package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// poolBlock is a SQLite scalar function that reports it started and then waits
// until the test releases it, so a reader holds its pool slot with no sleeping.
var (
	poolBlockMu      sync.Mutex
	poolBlockStarted chan struct{}
	poolBlockRelease chan struct{}
)

func init() {
	sqlite.MustRegisterScalarFunction("poolbound_block", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		poolBlockMu.Lock()
		started, release := poolBlockStarted, poolBlockRelease
		poolBlockMu.Unlock()
		started <- struct{}{}
		<-release
		return int64(1), nil
	})
}

// until yields the processor until cond holds; a test that never gets there
// fails at the deadline instead of hanging (the deadline only bounds a failing
// run, a passing one never waits on it).
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

func openBounded(t *testing.T, max int) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), Options{ReadMaxOpenConns: max})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// With two connections allowed and four concurrent blocked readers, the third
// and fourth queue on the pool (db_wait_total, the pool's WaitCount, rises) and
// run once a running reader finishes.
func TestReadPoolBoundQueuesExcessReaders(t *testing.T) {
	s := openBounded(t, 2)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	poolBlockMu.Lock()
	poolBlockStarted, poolBlockRelease = started, release
	poolBlockMu.Unlock()
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var n int
			errs <- s.Read().QueryRowContext(context.Background(), `SELECT poolbound_block()`).Scan(&n)
		}()
	}
	<-started
	<-started
	until(t, "two readers queued", func() bool { return s.Read().Stats().WaitCount == 2 })
	st := s.Read().Stats()
	if st.OpenConnections > 2 || st.InUse != 2 {
		t.Fatalf("open %d in use %d, want at most 2 open and 2 in use", st.OpenConnections, st.InUse)
	}
	if len(started) != 0 {
		t.Fatal("a third reader started while two held the pool")
	}
	// One finishing hands its slot to a waiter.
	release <- struct{}{}
	<-started
	releaseAll()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A reader that is cancelled gives its slot back: a waiter then proceeds.
func TestReadPoolBoundCancelFreesTheSlot(t *testing.T) {
	s := openBounded(t, 2)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()
	done := make(chan error, 2)
	for _, ctx := range []context.Context{ctx1, ctx2} {
		go func() {
			var n int
			done <- s.Read().QueryRowContext(ctx, slowQuery).Scan(&n)
		}()
	}
	until(t, "both slots held", func() bool { return s.Read().Stats().InUse == 2 })

	third := make(chan error, 1)
	go func() {
		var n int
		third <- s.Read().QueryRowContext(context.Background(), `SELECT 1`).Scan(&n)
	}()
	until(t, "third reader queued", func() bool { return s.Read().Stats().WaitCount == 1 })
	cancel1()
	if err := <-third; err != nil {
		t.Fatalf("waiter after a cancel: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader ended with %v, want context.Canceled", err)
	}
	cancel2()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("second cancelled reader ended with %v", err)
	}
}

// A negative bound leaves the pool unbounded; zero takes the default.
func TestReadPoolBoundDefaults(t *testing.T) {
	if got := openBounded(t, 0).Read().Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("default bound %d, want 8", got)
	}
	if got := openBounded(t, -1).Read().Stats().MaxOpenConnections; got != 0 {
		t.Fatalf("unbounded pool reports max %d, want 0", got)
	}
	if got := openBounded(t, 3).Read().Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("bound %d, want 3", got)
	}
}
