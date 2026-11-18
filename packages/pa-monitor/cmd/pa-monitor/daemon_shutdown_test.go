package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func shutdownTestLog() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

// A shutdown stuck in a named phase past the deadline logs shutdown_overrun
// with that phase and the deadline, then exits with the hard-exit status.
func TestShutdownGroupOverrunNamesPhaseAndExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, buf := shutdownTestLog()
		exited := make(chan int, 1)
		ctx, cancel := context.WithCancel(context.Background())
		g := newShutdownGroup(ctx, 8*time.Second, log, func(c int) { exited <- c })
		defer g.Disarm()

		cancel()
		synctest.Wait()
		g.SetPhase("emitter_flush") // stuck here
		time.Sleep(8 * time.Second)
		synctest.Wait()

		select {
		case code := <-exited:
			if code != 10 {
				t.Fatalf("exit code = %d, want 10", code)
			}
		default:
			t.Fatal("watchdog never fired")
		}
		if !g.Fired() {
			t.Error("Fired() = false after overrun")
		}
		out := buf.String()
		for _, want := range []string{"shutdown_signal", "shutdown_overrun", "phase=emitter_flush", "deadline_ms=8000"} {
			if !strings.Contains(out, want) {
				t.Errorf("log missing %q:\n%s", want, out)
			}
		}
	})
}

// A shutdown that finishes in time never exits and logs per-phase durations.
func TestShutdownGroupCleanFinishDisarms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, buf := shutdownTestLog()
		exited := make(chan int, 1)
		ctx, cancel := context.WithCancel(context.Background())
		g := newShutdownGroup(ctx, 8*time.Second, log, func(c int) { exited <- c })

		cancel()
		synctest.Wait()
		g.SetPhase("emitter_flush")
		time.Sleep(2 * time.Second)
		g.SetPhase("release_lock")
		if !g.Disarm() {
			t.Fatal("Disarm lost the race")
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()

		select {
		case c := <-exited:
			t.Fatalf("exited with %d after Disarm", c)
		default:
		}
		out := buf.String()
		for _, want := range []string{"shutdown_complete", "emitter_flush_duration_ms=2000", "total_ms=2000"} {
			if !strings.Contains(out, want) {
				t.Errorf("log missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "shutdown_overrun") {
			t.Errorf("unexpected overrun:\n%s", out)
		}
	})
}

// The flag default matches the value the launchd ExitTimeOut derives from.
func TestDefaultShutdownDeadline(t *testing.T) {
	if defaultShutdownDeadline != 8*time.Second {
		t.Fatalf("defaultShutdownDeadline = %v, want 8s", defaultShutdownDeadline)
	}
}
