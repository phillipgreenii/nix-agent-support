package settle

import (
	"sync"
	"testing"
	"time"
)

// Doctor reads a file's last-seen time while Tick, under the run lock, fills and
// clears the Settler's per-tick last-seen cache. Doctor holds only its own mutex and
// the copier lock, so its read MUST NOT go through that cache.
func TestDoctorReadsLastSeenWithoutRacingTick(t *testing.T) {
	// iterations is how many times each goroutine runs. Each Tick writes the
	// cache twice (prefetch, then clear) and each Doctor reads it once for the
	// changed snapshot file. The race detector needs one unsynchronised
	// read/write pair inside its history window, and the overlap of the two
	// loops is a matter of scheduling, so the count has to be large enough that
	// the loops overlap many times. More would only slow the test.
	//
	// Cost measured 2026-11-10: 0.48 s plain, 5.27 s under -race at 30.
	// Hand-applied negative control (doctorFull and doctorOne pointed back at
	// lastSeen, five separate `go test -race -count=1` processes per count; a
	// process counts only if it reports a DATA RACE): 10 iterations failed 1 of
	// 5, 20 failed 0 of 5, 30 failed 0 of 5, 200 failed 3 of 5. So no smaller
	// count is reliable, and 30 is kept only as the existing cost/benefit
	// compromise: it is NOT a reliable detector of the old defect (the copier
	// lock orders most Tick/Doctor overlaps, leaving few racy ones), only a
	// cheap tripwire.
	const iterations = 30
	e := newEnv(t, false)
	// A young Conversation that stays quiet, so every Tick evaluates it and
	// prefetches the last-seen time of its files.
	e.seed(3, time.Minute)
	rel := "projects/example-project/" + sess + "/subagents/agent-1.meta.json"
	e.write(rel, `{"agentType":"example"}`, e.clk.Now().Add(-time.Minute), false)
	e.ingest(rel)
	// Change the original after it was recorded, with an older mtime, so the
	// snapshot comparison differs and doctor asks for the file's last-seen time.
	e.write(rel, `{"agentType":"changed"}`, e.clk.Now().Add(-48*time.Hour), false)

	var wg sync.WaitGroup
	errs := make(chan error, 2*iterations)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := e.sett.Tick(); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := e.sett.Doctor(); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
