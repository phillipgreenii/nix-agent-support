package shutdown

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func testLog() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestWatchdogFiresPastDeadlineAndNamesPhase(t *testing.T) {
	log, buf := testLog()
	tr := NewTracker(log)
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	Watch(ctx, Options{
		Deadline: 20 * time.Millisecond, Log: log, Tracker: tr,
		Exit: func(c int) { exited <- c },
	})
	cancel()
	waitBegun(t, tr)
	tr.Phase("emitter_flush") // stuck here
	select {
	case code := <-exited:
		if code != ExitCode {
			t.Fatalf("exit code = %d, want %d", code, ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog never fired")
	}
	out := buf.String()
	for _, want := range []string{EvStarted, EvOverrun, "phase=emitter_flush"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

func TestDisarmBeforeDeadlineNeverExits(t *testing.T) {
	log, buf := testLog()
	tr := NewTracker(log)
	exited := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	w := Watch(ctx, Options{
		Deadline: 50 * time.Millisecond, Log: log, Tracker: tr,
		Exit: func(c int) { exited <- c },
	})
	cancel()
	waitBegun(t, tr)
	tr.Phase("emitter_flush")
	tr.Done()
	if !w.Disarm() {
		t.Fatal("Disarm lost the race")
	}
	select {
	case <-exited:
		t.Fatal("watchdog exited after Disarm")
	case <-time.After(150 * time.Millisecond):
	}
	out := buf.String()
	for _, want := range []string{EvStarted, EvPhase, EvDone} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, EvOverrun) {
		t.Errorf("unexpected overrun:\n%s", out)
	}
}

func TestDisarmWithoutContextEndStopsCleanly(t *testing.T) {
	w := Watch(context.Background(), Options{Exit: func(int) { t.Error("exit") }})
	if !w.Disarm() || w.Fired() {
		t.Fatal("expected clean disarm")
	}
}

func TestNilTrackerIsNoop(t *testing.T) {
	var tr *Tracker
	tr.Begin()
	tr.Phase("x")
	tr.Done()
	if n, _ := tr.Current(); n != "unknown" {
		t.Fatalf("got %q", n)
	}
}

// waitBegun waits for the AfterFunc that ends the root context to have called
// Begin, so the test's Phase call lands after it, as in the daemon.
func waitBegun(t *testing.T, tr *Tracker) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := tr.Current(); n != "not_started" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Begin never ran")
		}
		time.Sleep(time.Millisecond)
	}
}
