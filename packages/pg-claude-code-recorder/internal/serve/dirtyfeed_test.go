package serve

import (
	"sync"
	"testing"
	"time"
)

const feedSess = "0a1b2c3d-0000-4000-8000-0000000000aa"

// fakeDirty is a DirtySink that records what the daemon feeds it.
type fakeDirty struct {
	mu          sync.Mutex
	dirty       map[int64]int64
	liveChanges int
}

func (f *fakeDirty) Dirty(file, copyOffset int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirty == nil {
		f.dirty = map[int64]int64{}
	}
	f.dirty[file] = copyOffset
}

func (f *fakeDirty) LiveChanged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.liveChanges++
}

func (f *fakeDirty) snapshot() (map[int64]int64, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]int64{}
	for k, v := range f.dirty {
		out[k] = v
	}
	return out, f.liveChanges
}

// The daemon feeds the dirty sink from an ingest and from the lifecycle
// tracker's change notification.
func TestDaemonFeedsTheDirtySinkAndLiveChanges(t *testing.T) {
	e := newEnv(t, false)
	content := "{\"type\":\"user\"}\n"
	e.transcript(feedSess+".jsonl", content, time.Now().Add(-time.Hour))
	d := e.open(t)
	fake := &fakeDirty{}
	d.dirty = fake
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	got, live := fake.snapshot()
	if len(got) != 1 {
		t.Fatalf("dirty set %v, want one file", got)
	}
	for _, off := range got {
		if off != int64(len(content)) {
			t.Fatalf("dirty set %v, want offset %d", got, len(content))
		}
	}
	if live != 0 {
		t.Fatal("a live change before any Process")
	}
	start := time.Now().Add(-time.Hour)
	e.registry(t, 4711, regJSON(4711, feedSess, start, "idle", 1))
	e.procs.Add(4711, start, nil)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, live = fake.snapshot(); live == 0 {
		t.Fatal("discovering a Process did not notify the sink")
	}
}
