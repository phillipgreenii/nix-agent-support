package lock

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// T-12: the writer is single-instance, and the SECOND writer must detect the
// lock rather than block on it or fail. A ~15 minute tick over a growing corpus
// will overlap with a slow predecessor, and two writers racing on the same
// file's resume offset is the one way this design could corrupt its own coverage
// accounting.
func TestSecondAcquireIsDetectedNotBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest.lock")
	first, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := TryAcquire(path)
	if err == nil {
		_ = second.Release()
		t.Fatal("second TryAcquire succeeded; the writer is not single-instance")
	}
	var held *ErrHeld
	if !errors.As(err, &held) {
		t.Fatalf("second TryAcquire returned %T (%v), want *ErrHeld so the caller can NO-OP instead of failing", err, err)
	}
	if held.Path != path {
		t.Errorf("ErrHeld.Path = %q, want %q", held.Path, path)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest.lock")
	first, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// Exactly one of N concurrent contenders may hold the lock. Run this with
// -race: the lock is the sweep's only concurrency primitive, so if it has a data
// race the single-instance guarantee is decorative.
func TestOnlyOneConcurrentHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest.lock")
	const contenders = 8

	var (
		mu       sync.Mutex
		acquired int
		held     int
		start    sync.WaitGroup
		done     sync.WaitGroup
	)
	start.Add(1)
	done.Add(contenders)
	release := make(chan struct{})

	for range contenders {
		go func() {
			defer done.Done()
			start.Wait()
			h, err := TryAcquire(path)
			mu.Lock()
			switch {
			case err == nil:
				acquired++
			case errors.As(err, new(*ErrHeld)):
				held++
			}
			mu.Unlock()
			if err == nil {
				<-release
				_ = h.Release()
			}
		}()
	}
	start.Done()
	// Let every contender reach its TryAcquire, then let the winner go.
	for {
		mu.Lock()
		settled := acquired+held == contenders
		mu.Unlock()
		if settled {
			break
		}
	}
	close(release)
	done.Wait()

	mu.Lock()
	defer mu.Unlock()
	if acquired != 1 {
		t.Errorf("%d contender(s) acquired the lock, want exactly 1", acquired)
	}
	if held != contenders-1 {
		t.Errorf("%d contender(s) saw ErrHeld, want %d", held, contenders-1)
	}
}

func TestDefaultPathSitsBesideTheDatabase(t *testing.T) {
	got := DefaultPath("/var/lib/pg-ccaudit/transcripts.db")
	want := "/var/lib/pg-ccaudit/ingest.lock"
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestReleaseOnNilHandleIsSafe(t *testing.T) {
	var h *Handle
	if err := h.Release(); err != nil {
		t.Errorf("Release on a nil handle: %v", err)
	}
}
