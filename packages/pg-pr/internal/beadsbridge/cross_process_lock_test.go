package beadsbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// TestMain gives every test in this package an isolated cross-process lock
// directory instead of prLock's production default
// ($XDG_RUNTIME_DIR/pg-pr/locks). Without this, ANY test that calls
// Handler.Handle (there are dozens across this package's test files) would
// take a REAL flock under that path — contending with, or worse silently
// synchronizing against, an actual pg-pr daemon running on the same machine.
// See prlock.Options.LockDir's own doc comment: "Tests MUST inject a
// t.TempDir() value here instead of relying on the default." TestMain runs
// outside any *testing.T, so it uses os.MkdirTemp directly.
//
// Individual tests below further override prLock (with t.Cleanup restoring
// this default) when they need a short Timeout or a lock already held by a
// simulated second process.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beadsbridge-prlock-test-*")
	if err != nil {
		panic("beadsbridge: create test lock dir: " + err.Error())
	}
	prLock = prlock.New(prlock.Options{LockDir: dir})
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// pausingRunner wraps a beads.Runner and blocks the FIRST invocation matching
// pauseOn until the test sends on proceed, closing paused right before it
// blocks. This lets a test observe state (here: whether a cross-process lock
// is held) while a Handler.Handle call is suspended mid-projection, rather
// than only before or after it runs.
type pausingRunner struct {
	inner   beads.Runner
	pauseOn func([]string) bool
	paused  chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (p *pausingRunner) Run(ctx context.Context, args ...string) (string, error) {
	if p.pauseOn(args) {
		p.once.Do(func() { close(p.paused) })
		<-p.proceed
	}
	return p.inner.Run(ctx, args...)
}

// TestHandle_CrossProcessLockSerializesSamePRAcrossOSProcesses proves the
// prlock wiring in Handle (bead pg2-4dz88.6.3) — not just the pre-existing
// in-process keyedLock, which projection_race_test.go's
// TestConcurrentPRProjectionCreatesOneMergeRequestBead already covers —
// actually gates the SAME per-PR key against a SEPARATE Locker instance
// simulating a second OS process. A genuinely different process would have
// its own address space (hence its own empty keyedLock and its own
// prlock.Locker value) but the SAME on-disk lock file under the default
// LockDir; this test mirrors that with two independently-constructed
// Lockers pointed at the same directory — exactly prlock's own cross-process
// proof technique (see prlock_test.go's TestAcquire_SameKeySerializes: "Each
// Acquire call opens its own fd ... exercises real cross-process BSD flock
// semantics").
//
// The pause point is the SAME "list --type=merge-request" read the existing
// in-process race tests pause on, which matters here for a different reason:
// Handle acquires both locks BEFORE calling project (see Handle's doc
// comment — "Both locks are taken ONCE for the whole event"), so observing
// the second process's Acquire fail while paused at that read proves the
// cross-process lock is held across the ENTIRE critical section, not just
// around the eventual write.
func TestHandle_CrossProcessLockSerializesSamePRAcrossOSProcesses(t *testing.T) {
	dir := t.TempDir()
	prevLock := prLock
	prLock = prlock.New(prlock.Options{LockDir: dir})
	t.Cleanup(func() { prLock = prevLock })

	ws := newFakeWorkspace(nil)
	pr := &pausingRunner{
		inner: ws,
		pauseOn: func(args []string) bool {
			return len(args) > 0 && args[0] == "list" && hasArg(args, "--type=merge-request")
		},
		paused:  make(chan struct{}),
		proceed: make(chan struct{}),
	}

	payload, err := json.Marshal(store.PRPayload{
		Repo: "o/r", Number: 42, Title: "add a thing", Ownership: "mine",
		State: "open", Branch: "feat", Base: "main", Author: "alice",
		LastSyncedAt: "2026-08-24T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal pr payload: %v", err)
	}

	h := New(beads.NewClientWithRunner(pr))
	handleDone := make(chan error, 1)
	go func() {
		handleDone <- h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload})
	}()

	select {
	case <-pr.paused:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle never reached the paused read — test setup is broken")
	}

	// A SEPARATE Locker, simulating a second OS process, must NOT be able to
	// acquire the SAME key while Handle's projection is still in flight.
	secondProcess := prlock.New(prlock.Options{LockDir: dir, Timeout: 100 * time.Millisecond})
	if release, err := secondProcess.Acquire(context.Background(), "o/r#42"); err == nil {
		release()
		t.Fatal("a second process acquired the per-PR lock while Handle's projection was still in flight")
	} else if !errors.Is(err, prlock.ErrTimeout) {
		t.Fatalf("second process Acquire error = %v, want an error wrapping prlock.ErrTimeout", err)
	}

	close(pr.proceed)

	if err := <-handleDone; err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Now that Handle has released, the second process must acquire promptly.
	release, err := secondProcess.Acquire(context.Background(), "o/r#42")
	if err != nil {
		t.Fatalf("second process Acquire after Handle released: %v", err)
	}
	release()
}

// TestHandle_CrossProcessLockGiveUp_ReturnsErrTimeout proves the OTHER half
// of the wiring: when the cross-process lock cannot be acquired because a
// simulated second process already holds it, Handle returns an error
// wrapping prlock.ErrTimeout — the typed give-up cmd/pg-pr/main.go's
// exitCodeFor detects via errors.Is to route to exitBusy (bead pg2-4dz88.6.3).
func TestHandle_CrossProcessLockGiveUp_ReturnsErrTimeout(t *testing.T) {
	dir := t.TempDir()
	prevLock := prLock
	prLock = prlock.New(prlock.Options{LockDir: dir, Timeout: 50 * time.Millisecond})
	t.Cleanup(func() { prLock = prevLock })

	holder := prlock.New(prlock.Options{LockDir: dir})
	release, err := holder.Acquire(context.Background(), "o/r#5")
	if err != nil {
		t.Fatalf("pre-acquire (simulated second process): %v", err)
	}
	defer release()

	payload, err := json.Marshal(store.PRPayload{
		Repo: "o/r", Number: 5, Title: "t", Ownership: "mine",
		State: "open", Branch: "feat", Base: "main", Author: "alice",
		LastSyncedAt: "2026-08-24T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal pr payload: %v", err)
	}

	h := New(beads.NewClientWithRunner(newFakeWorkspace(nil)))
	err = h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload})
	if err == nil {
		t.Fatal("expected Handle to fail while the cross-process lock is held elsewhere")
	}
	if !errors.Is(err, prlock.ErrTimeout) {
		t.Fatalf("Handle error = %v, want an error wrapping prlock.ErrTimeout", err)
	}
}
