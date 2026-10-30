//go:build integration

package watcher

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestWatcherRealFSEventsBurst drives the real FSEvents stream: a burst of 1,000
// appends must reach the sink. Events coalesce, so the proof is that the sink
// eventually sees the file at its final size, polled to a deadline.
func TestWatcherRealFSEventsBurst(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real FSEvents needs darwin; Linux inotify is covered by the unit seams")
	}
	root, err := filepath.EvalSymlinks(t.TempDir()) // FSEvents reports real paths
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "example-project", "example-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	sink := &sinkLog{}
	w, err := New(Config{
		Roots:         []string{root},
		Filter:        func(p string) bool { return strings.HasSuffix(p, ".jsonl") },
		Sink:          sink.add,
		SweepInterval: time.Hour, // events, not the safety-net sweep, must deliver
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.b == nil {
		t.Skip("no FSEvents backend in this build (cgo disabled)")
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// The start-up sweep is not under test; wait for it, then burst.
	deadline := time.Now().Add(30 * time.Second)
	for w.LastSweep().IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("start-up sweep never completed")
		}
		runtime.Gosched()
	}
	sink.reset()

	const n = 1000
	line := "{\"example\":\"line\"}\n"
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	wantSize := int64(n * len(line))
	deadline = time.Now().Add(30 * time.Second)
	for sink.lastSize(target) != wantSize {
		if time.Now().After(deadline) {
			t.Fatalf("sink never saw %s at size %d (last %d); events=%d",
				target, wantSize, sink.lastSize(target), sink.count())
		}
		runtime.Gosched()
	}
}

func (s *sinkLog) reset() { s.mu.Lock(); s.ev = nil; s.mu.Unlock() }
func (s *sinkLog) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ev)
}

func (s *sinkLog) lastSize(path string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.ev) - 1; i >= 0; i-- {
		if s.ev[i].Path == path && !s.ev[i].Gone {
			return s.ev[i].Info.Size
		}
	}
	return -1
}
