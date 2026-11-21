package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A Process list that waits behind another reader of the registry state gives up
// when its request context ends, instead of staying queued after its client has
// gone. process.list calls cancelled by their client waited up to 90 s on the
// live host without running a statement.
func TestProcessListQueueWaitEndsWithTheRequestContext(t *testing.T) {
	v := newLiveEnv(t)
	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "busy"))
	v.k.Parse()
	if err := v.e.incMu.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer v.e.incMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := v.e.Processes(ctx, ProcessFilter{Limit: 10})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Processes error %v, want the context deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Processes still waiting for the registry state after its context ended")
	}
}

// Lists that arrive together after a registry write share one read of it: each
// caller that gets the registry state after the first finds that one's answer
// instead of reading the registry again.
func TestConcurrentProcessListsShareOneRegistryRead(t *testing.T) {
	v := newLiveEnv(t)
	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "busy"))
	v.k.Parse()

	v.cnt.Reset()
	if _, err := v.e.Processes(context.Background(), ProcessFilter{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	one := v.cnt.Queries()
	if one == 0 {
		t.Fatal("a list after a registry write ran no query")
	}

	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	v.cnt.Reset()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ps, err := v.e.Processes(context.Background(), ProcessFilter{Limit: 10})
			if err != nil || len(ps) != 1 {
				t.Errorf("Processes = %+v, %v", ps, err)
			}
		}()
	}
	wg.Wait()
	if got := v.cnt.Queries(); got != one {
		t.Fatalf("8 concurrent lists ran %d queries, one list after a write runs %d", got, one)
	}
}
