package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// holdWrite runs a WithWrite whose function blocks until the returned release
// is called, and returns once the write lock is held.
func holdWrite(t *testing.T, s *Store) (release func()) {
	t.Helper()
	inside := make(chan struct{})
	gate := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.WithWrite("test.hold", func(*sql.Tx) error {
			close(inside)
			<-gate
			return nil
		})
	}()
	<-inside
	var once sync.Once
	release = func() { once.Do(func() { close(gate); <-done }) }
	t.Cleanup(release)
	return release
}

func TestPoolStatsDoesNotWaitOnAWriteInFlight(t *testing.T) {
	s, _ := openCounting(t, Options{})
	holdWrite(t, s)
	got := make(chan PoolStats, 1)
	go func() { got <- s.PoolStats() }()
	select {
	case ps := <-got:
		if ps.Write.OpenConnections < 1 {
			t.Fatalf("pool stats %+v", ps)
		}
	case <-time.After(time.Second):
		t.Fatal("PoolStats waited on the write lock")
	}
}

func TestReadTxCtxDoesNotWaitOnAWriteInFlight(t *testing.T) {
	s, _ := openCounting(t, Options{})
	holdWrite(t, s)
	res := make(chan error, 1)
	ran := false
	go func() {
		res <- s.ReadTxCtx(context.Background(), func(rd *Reader) error {
			ran = true
			_, err := rd.AllFiles()
			return err
		})
	}()
	select {
	case err := <-res:
		if err != nil || !ran {
			t.Fatalf("read: ran=%v err=%v", ran, err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadTxCtx waited on the write lock")
	}
}

func TestCheckpointDoesNotWaitOnAWriteInFlight(t *testing.T) {
	s, _ := openCounting(t, Options{})
	holdWrite(t, s)
	res := make(chan error, 1)
	go func() { _, _, err := s.Checkpoint(); res <- err }()
	select {
	case err := <-res:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Checkpoint waited on the write lock")
	}
}

// Close racing PoolStats and ReadTxCtx callers: each gets a clean error or
// zero stats, never a panic (run under -race).
func TestCloseRacingPoolStatsAndReadTxCtx(t *testing.T) {
	s, _ := openCounting(t, Options{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.PoolStats()
				}
			}
		}()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.ReadTxCtx(context.Background(), func(rd *Reader) error {
						_, err := rd.AllFiles()
						return err
					})
					_, _, _ = s.Checkpoint()
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
	if ps := s.PoolStats(); ps != (PoolStats{}) {
		t.Fatalf("pool stats after close: %+v", ps)
	}
	if err := s.ReadTxCtx(context.Background(), func(*Reader) error { return nil }); err == nil {
		t.Fatal("ReadTxCtx after close returned no error")
	}
}
