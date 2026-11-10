//go:build darwin

package proc

import (
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// Close stops the kqueue loop: no goroutine of the table is left, Close returns
// within the loop's one-second wake, and a later Watch is refused.
func TestRealCloseStopsKqueueLoop(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	r := NewReal()
	// A watch on our own pid starts the loop (it never exits during the test).
	stop, err := r.Watch(os.Getpid(), func(time.Time) {})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Real.Close did not return: the kqueue loop was not stopped")
	}
	if err := goleak.Find(opt); err != nil {
		t.Fatalf("kqueue goroutine outlived Close: %v", err)
	}
	if _, err := r.Watch(os.Getpid(), func(time.Time) {}); err == nil {
		t.Fatal("Watch after Close succeeded")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
