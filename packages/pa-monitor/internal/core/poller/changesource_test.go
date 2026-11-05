package poller

import (
	"context"
	"testing"
	"time"
)

// TestFeedChangeSource_RaisesAFastIntervalToTheMinimum: the design polls the
// change feed every 5 to 10 seconds, so a faster ask is raised.
func TestFeedChangeSource_RaisesAFastIntervalToTheMinimum(t *testing.T) {
	for _, in := range []time.Duration{-1, 0, time.Millisecond, time.Second} {
		if got := newFeedChangeSource(in).interval; got != MinFeedInterval {
			t.Errorf("interval(%v) = %v, want %v", in, got, MinFeedInterval)
		}
	}
	if got := newFeedChangeSource(9 * time.Second).interval; got != 9*time.Second {
		t.Errorf("a 9s ask changed to %v", got)
	}
}

// TestFeedChangeSource_FiresAfterTheInterval and stops on a cancelled context.
func TestFeedChangeSource_FiresAfterTheInterval(t *testing.T) {
	cs := &feedChangeSource{interval: 20 * time.Millisecond}
	start := time.Now()
	if !cs.WaitForChange(context.Background()) {
		t.Fatal("never fired")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("fired after %v, want about the interval", elapsed)
	}
}

func TestFeedChangeSource_CtxDoneReturnsFalse(t *testing.T) {
	cs := &feedChangeSource{interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cs.WaitForChange(ctx) {
		t.Fatal("WaitForChange returned true on a cancelled context")
	}
}

// TestProducerDefaultsToTheFeedCadence pins the producer's default change source.
func TestProducerDefaultsToTheFeedCadence(t *testing.T) {
	pr := &Producer{}
	cs, ok := pr.ensureChangeSource().(*feedChangeSource)
	if !ok || cs.interval != MinFeedInterval {
		t.Fatalf("default change source = %#v, want a %v feed source", pr.ensureChangeSource(), MinFeedInterval)
	}
	pr = &Producer{Interval: time.Millisecond}
	if got := pr.ensureChangeSource().(*feedChangeSource).interval; got != MinFeedInterval {
		t.Errorf("a 1ms Interval gave %v, want the %v minimum", got, MinFeedInterval)
	}
}
