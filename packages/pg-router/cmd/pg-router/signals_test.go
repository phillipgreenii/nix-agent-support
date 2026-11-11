package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The first signal ends the context; the registration is then released so a
// second signal kills by default. The fake notify stands in for
// signal.NotifyContext, and cancelling its context stands in for the first
// signal.
func TestArmSignalsReleasesRegistrationAfterFirstSignal(t *testing.T) {
	var cancelFirst context.CancelFunc
	var stopped atomic.Bool
	notify := func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		if len(sigs) != 2 || sigs[0] != os.Interrupt || sigs[1] != syscall.SIGTERM {
			t.Errorf("signals %v", sigs)
		}
		ctx, cancel := context.WithCancel(parent)
		cancelFirst = cancel
		return ctx, func() { stopped.Store(true); cancel() }
	}
	armSignals(notify, os.Interrupt, syscall.SIGTERM)

	time.Sleep(50 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("stop called before any signal")
	}
	cancelFirst() // the first signal
	deadline := time.Now().Add(5 * time.Second)
	for !stopped.Load() {
		if time.Now().After(deadline) {
			t.Fatal("stop not called after the first signal")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
