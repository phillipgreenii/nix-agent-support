package daemon

import (
	"context"
	"testing"
	"time"
)

type hangingEmitter struct{}

// Shutdown blocks until its context ends, like an OTLP flush to an
// unreachable collector.
func (hangingEmitter) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestFlushEmitterIsBounded(t *testing.T) {
	done := make(chan struct{})
	go func() {
		flushEmitter(hangingEmitter{}, 30*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flushEmitter did not return: the flush is unbounded")
	}
}
