package recorderfacts

import (
	"context"
	"sync"
)

// sessionConcurrency bounds the recorder queries in flight for one listing. A
// session needs about five sequential reads and a week holds around a thousand
// active sessions, so reading them one after another exceeded the 25 s
// deadline while the recorder sat mostly idle; a small fixed fan-out cuts the
// wall time without flooding the recorder.
const sessionConcurrency = 8

// forEachBounded calls fn(ctx, i) for every i in [0, n) with at most limit calls
// in flight. The first error cancels the context handed to the remaining calls
// and is the one returned; calls not yet started are skipped.
func forEachBounded(ctx context.Context, n, limit int, fn func(ctx context.Context, i int) error) error {
	if limit < 1 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
		sem   = make(chan struct{}, limit)
	)
	for i := 0; i < n; i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}(i)
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
