package watcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// A sweep in flight stops at the next file when the watcher is closed: Close
// returns once the sink call in progress does, and the files after it are not
// reported. Reverting the context check lets the sweep report every file.
func TestSweepAbortsOnClose(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		write(t, filepath.Join(root, "example-project", fmt.Sprintf("s%d.jsonl", i)), "{}\n")
	}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls int
	w, err := New(Config{
		Roots: []string{root}, backend: &fakeBackend{}, DisableEvents: true,
		Ticker: make(chan time.Time),
		Sink: func(Event) {
			calls++ // only the one sweep goroutine calls it
			entered <- struct{}{}
			<-release
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep never reached the sink")
	}
	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()
	time.Sleep(20 * time.Millisecond) // Close has cancelled and now waits on the sweep
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if calls != 1 {
		t.Fatalf("sink called %d times, want 1: the sweep must stop at the next file", calls)
	}
}

func TestRescanContextAbortsAndReturnsErrSweepAborted(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "example-project", "a.jsonl"), "{}\n")
	w, sink := newTestWatcher(t, Config{Roots: []string{root}, backend: &fakeBackend{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.RescanContext(ctx); !errors.Is(err, ErrSweepAborted) {
		t.Fatalf("err = %v, want ErrSweepAborted", err)
	}
	if len(sink.paths()) != 0 || !w.LastSweep().IsZero() {
		t.Fatal("an aborted walk reported a file or recorded a sweep")
	}
	if err := w.Rescan(); err != nil || len(sink.paths()) != 1 {
		t.Fatalf("next sweep: err %v, files %v", err, sink.paths())
	}
}
