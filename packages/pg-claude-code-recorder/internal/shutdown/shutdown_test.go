package shutdown

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testLogger(w *lockedBuf) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }

// An expired deadline logs the in-flight loops (oldest first), the phase and
// the extra counters, THEN exits 10: the log line is checked inside Exit.
func TestWatchdogOverrunLogsInFlightAndExits10(t *testing.T) {
	var buf lockedBuf
	exited := make(chan int, 1)
	var logAtExit string
	ctx, cancel := context.WithCancel(context.Background())
	Watch(ctx, Options{
		Deadline: 5 * time.Millisecond,
		Log:      testLogger(&buf),
		InFlight: func() []Flight {
			return []Flight{{Name: "young", Age: time.Second}, {Name: "old", Age: 3 * time.Second}}
		},
		Phase: func() PhaseInfo {
			return PhaseInfo{Name: "store", Elapsed: 2 * time.Second, Attrs: []any{"files_total", 7}}
		},
		Exit: func(code int) { logAtExit = buf.String(); exited <- code },
	})
	cancel()
	select {
	case code := <-exited:
		if code != 10 || ExitCode != 10 {
			t.Fatalf("exit code %d (ExitCode %d), want 10", code, ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog never fired")
	}
	for _, want := range []string{`"msg":"shutdown_overrun"`, `"deadline_ms":5`, `"phase":"store"`, `"phase_elapsed_ms":2000`, `"in_flight":"old:3000,young:1000"`, `"files_total":7`} {
		if !strings.Contains(logAtExit, want) {
			t.Fatalf("log at exit %q lacks %s", logAtExit, want)
		}
	}
}

// Close finishing first (Disarm) stops the watchdog for good, and a deadline
// that already fired makes Disarm lose; either order, exactly one side wins.
func TestCloseFinishingFirstDisarmsWatchdog(t *testing.T) {
	t.Run("disarm first", func(t *testing.T) {
		exited := make(chan int, 1)
		ctx, cancel := context.WithCancel(context.Background())
		w := Watch(ctx, Options{Deadline: 5 * time.Millisecond, Exit: func(c int) { exited <- c }})
		cancel()
		if !w.Disarm() {
			t.Fatal("Disarm lost with no deadline fired")
		}
		select {
		case <-exited:
			t.Fatal("watchdog exited after Disarm")
		case <-time.After(60 * time.Millisecond):
		}
		if w.Fired() {
			t.Fatal("Fired after Disarm")
		}
	})
	t.Run("deadline first", func(t *testing.T) {
		exited := make(chan int, 1)
		ctx, cancel := context.WithCancel(context.Background())
		w := Watch(ctx, Options{Deadline: time.Millisecond, Exit: func(c int) { exited <- c }})
		cancel()
		<-exited
		if w.Disarm() {
			t.Fatal("Disarm won after the deadline fired")
		}
		if !w.Fired() {
			t.Fatal("not Fired")
		}
	})
	t.Run("disarm before ctx ends", func(t *testing.T) {
		exited := make(chan int, 1)
		ctx, cancel := context.WithCancel(context.Background())
		w := Watch(ctx, Options{Deadline: time.Millisecond, Exit: func(c int) { exited <- c }})
		if !w.Disarm() {
			t.Fatal("Disarm lost")
		}
		cancel()
		select {
		case <-exited:
			t.Fatal("exited though disarmed before cancel")
		case <-time.After(60 * time.Millisecond):
		}
	})
}
