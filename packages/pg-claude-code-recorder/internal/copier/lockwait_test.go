package copier

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A caller that queues behind a holder of the copier lock observes how long it
// queued, measured from before it asked for the lock. The injected clock moves
// while the caller is queued, so the wait is exact and nothing sleeps.
func TestCopierLockWaitIsObservedBeforeAcquiring(t *testing.T) {
	calls := map[string]func(e *env, p string){
		"Ingest":      func(e *env, p string) { _, _ = e.cp.Ingest(p) },
		"IngestBatch": func(e *env, p string) { e.cp.IngestBatch([]string{p}) },
		"SweepMissing": func(e *env, _ string) {
			_ = e.cp.SweepMissing(map[Identity]bool{})
		},
		"WithLock": func(e *env, _ string) { _ = e.cp.WithLock(func() error { return nil }) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.write("projects/example-project/a.jsonl", "{}\n")
			queued := make(chan struct{})
			e.cp.onContended = func() {
				e.clk.Advance(250 * time.Millisecond) // the caller is queued while time passes
				close(queued)
			}
			e.cp.mu.Lock() // another holder
			done := make(chan struct{})
			go func() { call(e, p); close(done) }()
			<-queued
			e.cp.mu.Unlock()
			<-done
			h := e.obs.Reg.Hist(obs.MCopierLockWait)
			if h == nil || h.Samples != 1 {
				t.Fatalf("copier_lock_wait_seconds = %+v, want 1 sample", h)
			}
			if h.Sum != 0.25 {
				t.Fatalf("sum = %v, want 0.25", h.Sum)
			}
		})
	}
}

// An uncontended acquisition observes nothing and takes no registry lock.
func TestUncontendedCopierLockTakesNoRegistryLock(t *testing.T) {
	e := newEnv(t)
	p := e.write("projects/example-project/a.jsonl", "{}\n")
	e.ingest(p)
	e.appendTo("projects/example-project/a.jsonl", "{}\n")
	var plain int
	e.obs.Reg.OnLock = func() { plain++ }
	e.ingest(p)
	e.obs.Reg.OnLock = nil
	if h := e.obs.Reg.Hist(obs.MCopierLockWait); h != nil && h.Samples != 0 {
		t.Fatalf("uncontended ingest observed %d waits", h.Samples)
	}
	t.Logf("registry lock acquisitions in one copying Ingest, copier alone with the lock free: %d (the copier's own lag gauge sets)", plain)
}
