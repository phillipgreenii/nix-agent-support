package serve

import (
	"testing"
	"time"
)

const feedSess = "0a1b2c3d-0000-4000-8000-0000000000aa"

// The daemon feeds the settler's dirty set from an ingest and from the
// lifecycle tracker's change notification.
func TestDaemonFeedsTheSettlerDirtySetAndLiveChanges(t *testing.T) {
	e := newEnv(t, false)
	content := "{\"type\":\"user\"}\n"
	e.transcript(feedSess+".jsonl", content, time.Now().Add(-time.Hour))
	d := e.open(t)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	got := d.Settler().TakeDirty()
	if got[feedSess] != int64(len(content)) {
		t.Fatalf("dirty set %v, want %s at %d", got, feedSess, len(content))
	}

	if d.Settler().TakeLiveChanged() {
		t.Fatal("a live change before any Process")
	}
	start := time.Now().Add(-time.Hour)
	e.registry(t, 4711, regJSON(4711, feedSess, start, "idle", 1))
	e.procs.Add(4711, start, nil)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if !d.Settler().TakeLiveChanged() {
		t.Fatal("discovering a Process did not notify the settler")
	}
}
