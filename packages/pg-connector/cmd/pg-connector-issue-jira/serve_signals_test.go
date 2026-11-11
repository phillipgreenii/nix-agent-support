package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestArmServeSignalsReleasesRegistrationWhenContextEnds proves the second
// signal takes its default action: stop runs after the signal context ends and
// not before.
func TestArmServeSignalsReleasesRegistrationWhenContextEnds(t *testing.T) {
	var gotSigs []os.Signal
	var stopped atomic.Bool
	released := make(chan struct{})
	var cancel context.CancelFunc
	notify := func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		gotSigs = sigs
		var ctx context.Context
		ctx, cancel = context.WithCancel(parent)
		return ctx, func() {
			if stopped.CompareAndSwap(false, true) {
				close(released)
			}
			cancel()
		}
	}
	ctx, _ := armServeSignals(notify)

	if len(gotSigs) != 2 || gotSigs[0] != syscall.SIGTERM || gotSigs[1] != syscall.SIGINT {
		t.Fatalf("registered signals = %v, want SIGTERM and SIGINT", gotSigs)
	}
	select {
	case <-released:
		t.Fatal("stop ran before the signal context ended")
	case <-time.After(50 * time.Millisecond):
	}
	if ctx.Err() != nil {
		t.Fatal("context ended without a signal")
	}

	// Simulate the first signal: the notify context ends without stop being called.
	cancel()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not run after the signal context ended")
	}
}
