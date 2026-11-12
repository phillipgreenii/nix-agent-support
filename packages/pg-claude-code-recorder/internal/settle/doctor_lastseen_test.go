package settle

import (
	"sync"
	"testing"
	"time"
)

// Doctor reads a file's last-seen time while Tick, under the run lock, fills and
// clears the Settler's per-tick last-seen cache. Doctor holds only its own mutex and
// the copier lock, so its read MUST NOT go through that cache.
//
// This test is deterministic. It supersedes the earlier tripwire that ran 30
// Ticks against 30 Doctors and hoped for an unordered overlap: the copier lock
// ordered most of those overlaps, so with the fix reverted it caught the race in
// only 1 of 5 processes at 10 iterations, 0 of 5 at 20 and at 30, and 3 of 5 at
// 200. Here the test holds Tick open between its write of the cache and its
// clear while the Doctor reads, and neither side waits for a signal the other
// sent after its own access, so the race detector sees the two accesses
// unordered every time.
func TestDoctorReadsLastSeenWithoutRacingTick(t *testing.T) {
	// waitLimit bounds every wait on a channel so a regression that deadlocks
	// the barrier fails the test instead of hanging it. It is far above the
	// time one Tick or Doctor run takes (milliseconds), even on a loaded host.
	const waitLimit = 60 * time.Second

	e := newEnv(t, false)
	// A young Conversation that stays quiet, so the Tick evaluates it and
	// prefetches the last-seen time of its files.
	e.seed(3, time.Minute)
	rel := "projects/example-project/" + sess + "/subagents/agent-1.meta.json"
	e.write(rel, `{"agentType":"example"}`, e.clk.Now().Add(-time.Minute), false)
	e.ingest(rel)
	// Change the original after it was recorded, with an older mtime, so the
	// snapshot comparison differs and the Doctor asks for the file's last-seen time.
	e.write(rel, `{"agentType":"changed"}`, e.clk.Now().Add(-48*time.Hour), false)

	// tickAtPrefetch is closed by Tick just before it writes the cache; the
	// Doctor's read waits for it so the read cannot finish before Tick has
	// reached the cache (which would let the copier lock order read before write).
	tickAtPrefetch := make(chan struct{})
	// doctorRead is closed after the Doctor's read; Tick stays paused, after
	// its write and before its clear, until then.
	doctorRead := make(chan struct{})

	prefetched := false
	// The Doctor may read more than one file; each channel closes once.
	var atOnce, readOnce sync.Once
	e.sett.beforePrefetch = func() { atOnce.Do(func() { close(tickAtPrefetch) }) }
	e.sett.afterPrefetch = func() {
		prefetched = true
		select {
		case <-doctorRead:
		case <-time.After(waitLimit):
			t.Error("the Doctor never read the file's last-seen time while Tick held the cache open")
		}
	}
	seam := e.sett.doctorSeamRef()
	seam.lastSeenBefore = func() {
		select {
		case <-tickAtPrefetch:
		case <-time.After(waitLimit):
			t.Error("Tick never reached its last-seen prefetch")
		}
	}
	seam.lastSeenAfter = func() { readOnce.Do(func() { close(doctorRead) }) }

	tickDone := make(chan error, 1)
	go func() {
		_, err := e.sett.Tick()
		tickDone <- err
	}()
	if _, err := e.sett.Doctor(); err != nil {
		t.Error(err)
	}
	if err := <-tickDone; err != nil {
		t.Error(err)
	}
	if !prefetched {
		t.Error("Tick never prefetched last-seen times, so the test did not exercise the cache")
	}
}
