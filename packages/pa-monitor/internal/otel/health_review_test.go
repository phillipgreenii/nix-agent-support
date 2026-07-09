package otel

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// TestExportHealth_ConcurrentRecordIsRaceFree drives the tracker's record entry
// point (what both exporter decorators call) from many goroutines with -race, so
// removing the mutex that guards the tracker's state would be caught. The other
// health tests are sequential, so the tracker is invoked from two SDK background
// goroutines in production but its locking is otherwise unexercised.
// (pg2-gweng, from the pg2-waji review.) Run with `go test -race`.
func TestExportHealth_ConcurrentRecordIsRaceFree(t *testing.T) {
	// Tiny throttle so both the first-of-streak and throttle-elapsed branches
	// are exercised under contention.
	h := newExportHealth(time.Now, time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				h.record(errors.New("collector down"), io.Discard)
			} else {
				h.record(nil, io.Discard)
			}
		}(i)
	}
	wg.Wait()

	// Reads must also be safe concurrently; healthy() takes the same lock.
	_ = h.healthy()
}
