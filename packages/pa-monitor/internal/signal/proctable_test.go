package signal_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

type fakePS struct {
	mu    sync.Mutex
	out   string
	err   error
	calls int
}

func (f *fakePS) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if name != "ps" || len(args) != 3 || args[0] != "-ax" {
		return nil, errors.New("unexpected command")
	}
	return []byte(f.out), f.err
}

func (f *fakePS) set(out string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out, f.err = out, err
}

func newTable(f *fakePS, now *time.Time) *signal.PSProcTable {
	return &signal.PSProcTable{Run: f.run, Now: func() time.Time { return *now }}
}

const (
	tOld = "Mon Nov  2 10:00:00 2026"
	tNew = "Mon Nov  2 11:00:00 2026"
)

func TestProcTable_ParentAndOneExecPerTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakePS{out: "  10 5 " + tOld + "\n  5 1 " + tOld + "\n  1 0 " + tOld + "\n"}
	tb := newTable(f, &now)
	for range 5 {
		if p, ok := tb.Parent(10); !ok || p != 5 {
			t.Fatalf("Parent(10) = %d,%v want 5,true", p, ok)
		}
	}
	if f.calls != 1 {
		t.Fatalf("execs within TTL = %d, want 1", f.calls)
	}
	if _, ok := tb.Parent(999); ok {
		t.Fatal("unknown pid must resolve as unknown")
	}
	now = now.Add(5 * time.Second)
	tb.Parent(10)
	if f.calls != 2 {
		t.Fatalf("execs after TTL = %d, want 2", f.calls)
	}
}

// A reused pid (same number, different start time) must not inherit the old
// pid's ancestry.
func TestProcTable_PIDReuseKeyedByStartTime(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakePS{out: "10 5 " + tOld + "\n5 1 " + tOld + "\n"}
	tb := newTable(f, &now)
	if p, ok := tb.ParentOf(10, tOld); !ok || p != 5 {
		t.Fatalf("ParentOf(10, old) = %d,%v want 5,true", p, ok)
	}
	// pid 10 dies; the number is reused by an unrelated process under 7.
	now = now.Add(5 * time.Second)
	f.set("10 7 "+tNew+"\n7 1 "+tOld+"\n", nil)
	if p, ok := tb.ParentOf(10, tOld); ok {
		t.Fatalf("reused pid inherited old ancestry: ParentOf(10, old) = %d", p)
	}
	if p, ok := tb.ParentOf(10, tNew); !ok || p != 7 {
		t.Fatalf("ParentOf(10, new) = %d,%v want 7,true", p, ok)
	}
}

// A parent that started AFTER its child is a reused pid: the hop is unknown.
func TestProcTable_ParentStartedAfterChildIsReused(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakePS{out: "10 5 " + tOld + "\n5 1 " + tNew + "\n"}
	tb := newTable(f, &now)
	if p, ok := tb.Parent(10); ok {
		t.Fatalf("Parent(10) = %d, want unknown (parent reused)", p)
	}
}

func TestProcTable_FailedSnapshotBoundedReuseThenUnknown(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakePS{out: "10 5 " + tOld + "\n5 1 " + tOld + "\n"}
	tb := newTable(f, &now)
	tb.MaxStale = 30 * time.Second
	if _, ok := tb.Parent(10); !ok {
		t.Fatal("first snapshot should resolve")
	}
	f.set("", errors.New("ps failed"))
	now = now.Add(10 * time.Second)
	if p, ok := tb.Parent(10); !ok || p != 5 {
		t.Fatalf("within bound, previous snapshot must be reused: %d,%v", p, ok)
	}
	if f.calls != 2 {
		t.Fatalf("a failed refresh must be attempted once, execs = %d", f.calls)
	}
	now = now.Add(30 * time.Second) // 40s since last success: past the bound
	if p, ok := tb.Parent(10); ok {
		t.Fatalf("past the bound ancestry must be unknown, got %d", p)
	}
	// Recovery: a good snapshot restores resolution.
	f.set("10 6 "+tOld+"\n6 1 "+tOld+"\n", nil)
	now = now.Add(5 * time.Second)
	if p, ok := tb.Parent(10); !ok || p != 6 {
		t.Fatalf("after recovery Parent(10) = %d,%v want 6,true", p, ok)
	}
}

func TestProcTable_NeverSnapshottedFailureIsUnknown(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakePS{err: errors.New("boom")}
	tb := newTable(f, &now)
	if p, ok := tb.Parent(10); ok {
		t.Fatalf("no snapshot ever: want unknown, got %d", p)
	}
}
