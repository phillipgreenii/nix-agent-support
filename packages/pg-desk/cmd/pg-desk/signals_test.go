package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// The first signal ends the context; the registration must then be released
// (stop runs) so a second SIGTERM kills by default, and not before. The fake
// notify stands in for signal.NotifyContext; cancelling its context models the
// first signal.
func TestArmSignalsReleasesRegistrationAfterFirstSignal(t *testing.T) {
	var firstSignal context.CancelFunc
	stopped := make(chan struct{})
	notify := func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		if len(sigs) != 2 || sigs[0] != syscall.SIGINT || sigs[1] != syscall.SIGTERM {
			t.Errorf("signals %v", sigs)
		}
		ctx, cancel := context.WithCancel(parent)
		firstSignal = cancel
		return ctx, func() { cancel(); close(stopped) }
	}
	armSignals(notify, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-stopped:
		t.Fatal("registration released before any signal")
	case <-time.After(50 * time.Millisecond):
	}
	firstSignal()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("registration not released after the first signal")
	}
}
