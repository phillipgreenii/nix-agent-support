package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// The first signal ends the serve context; serve then releases the signal
// registration so a second SIGTERM kills by default. The fake notify stands in
// for signal.NotifyContext, and cancelling its context for the first signal.
func TestSignalStopCalledAfterFirstSignal(t *testing.T) {
	var cancelFirst context.CancelFunc
	notify := func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		if len(sigs) != 2 || sigs[1] != syscall.SIGTERM {
			t.Errorf("signals %v", sigs)
		}
		ctx, cancel := context.WithCancel(parent)
		cancelFirst = cancel
		return ctx, cancel
	}
	ctx, _ := armSignals(notify, os.Interrupt, syscall.SIGTERM)
	stopped := make(chan struct{})
	a := &app{ctx: ctx, stop: func() { close(stopped) }}
	a.releaseSignalsOnFirst()

	select {
	case <-stopped:
		t.Fatal("stop called before any signal")
	case <-time.After(50 * time.Millisecond):
	}
	cancelFirst() // the first signal
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop not called after the first signal")
	}
}

// A nil stop (every cmd test builds the app without one) is a no-op.
func TestReleaseSignalsWithoutStopIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{ctx: ctx}
	a.releaseSignalsOnFirst()
	cancel()
}
