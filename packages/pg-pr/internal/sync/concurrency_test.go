package sync

// concurrency_test.go proves the load-bearing correctness invariant of the
// event-ownership refactor under CONCURRENT outbox drainers.
//
// Invariant: a PR's pr.opened event is enqueued+committed BEFORE that PR's
// feedback.created event (lower outbox id). RunOutbox dispatches in FIFO id
// order (SELECT ... ORDER BY id, see internal/store/outbox.go). Therefore the
// beadsbridge feedback handler (ensureProcessFeedbackBead) must ALWAYS find the
// merge-request bead already projected — it must NEVER hit the "no merge-request
// bead for X" error path (internal/beadsbridge/bridge.go) — even when multiple
// RunOutbox drainers run concurrently against ONE shared store/outbox (two
// workers + a maintenance flusher in the daemon).
//
// pg2-scl9p: this test caught a genuine ordering bug in RunOutbox
// (internal/store/outbox.go) — a caller that lost the claim race on a
// still-in-flight lower-id row used to fall through and dispatch a later,
// unclaimed row from its own snapshot regardless, which could dispatch
// feedback.created before a concurrently-dispatching pr.opened finished
// projecting the bead. RunOutbox now stops its pass instead of skipping ahead
// in that case, leaving the rest for a later drain. This test is what proved
// the bug reproducible (a plain, non-sandboxed `go test -count=1` failed on
// the very first attempt) and now guards the fix.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// concurrentBeadClient is a CONCURRENCY-SAFE beadsbridge.BeadClient. Its bead
// state is guarded by a mutex so concurrent drainers can hammer it without data
// races. It mirrors the real backend: ReconcileMergeRequest records
// (idempotently) the open merge-request bead for a PR; FindByRepoAndNumber and
// FindByRepoAndNumberUncached return it if present (else nil); the
// processing-cycle calls track open cycles safely.
type concurrentBeadClient struct {
	mu sync.Mutex
	// beads keyed by "repo#number" → the projected merge-request bead.
	beads map[string]*beads.MergeRequest
	// openCycle keyed by prBeadID → the open processing cycle, when one exists.
	openCycle map[string]*beads.ProcessingCycle
	nextID    int
}

func newConcurrentBeadClient() *concurrentBeadClient {
	return &concurrentBeadClient{
		beads:     map[string]*beads.MergeRequest{},
		openCycle: map[string]*beads.ProcessingCycle{},
	}
}

func beadKey(repo string, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

// ReconcileMergeRequest records the merge-request bead (idempotently) — the
// read-once + single-write projection (pg2-pz7y8). existing is whatever the
// caller's own FindByRepoAndNumberUncached read returned; this fake trusts it
// exactly like the real Client does (no re-read), so a concurrent creator
// racing another goroutine's read is exactly what this test wants to exercise.
func (c *concurrentBeadClient) ReconcileMergeRequest(_ context.Context, existing *beads.MergeRequest, _ string, f beads.MergeRequestFields, _, _, _ bool) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := beadKey(f.Repo, f.PRNumber)
	if mr, ok := c.beads[key]; ok {
		return mr.ID, mr.Status == "closed", nil
	}
	if existing != nil {
		return existing.ID, existing.Status == "closed", nil
	}
	c.nextID++
	id := fmt.Sprintf("mr-%d", c.nextID)
	c.beads[key] = &beads.MergeRequest{ID: id, Status: "open"}
	return id, false, nil
}

// FindByRepoAndNumber returns the recorded bead, or nil if not yet projected.
func (c *concurrentBeadClient) FindByRepoAndNumber(_ context.Context, repo string, number int) (*beads.MergeRequest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mr, ok := c.beads[beadKey(repo, number)]; ok {
		// Return a copy so callers can't mutate our state under the lock.
		cp := *mr
		return &cp, nil
	}
	return nil, nil
}

// FindByRepoAndNumberUncached is the same lookup as FindByRepoAndNumber: this
// fake has no cache to bypass.
func (c *concurrentBeadClient) FindByRepoAndNumberUncached(ctx context.Context, repo string, number int) (*beads.MergeRequest, error) {
	return c.FindByRepoAndNumber(ctx, repo, number)
}

func (c *concurrentBeadClient) CloseMergeRequest(context.Context, string, string) error { return nil }

func (c *concurrentBeadClient) ListChildrenOfPR(context.Context, string) ([]string, error) {
	return nil, nil
}

func (c *concurrentBeadClient) CreateProcessingCycle(_ context.Context, in beads.CreateProcessingCycleInput) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := fmt.Sprintf("cycle-%d", c.nextID)
	c.openCycle[in.PRBeadID] = &beads.ProcessingCycle{ID: id, Status: "open"}
	return id, nil
}

func (c *concurrentBeadClient) ResolveProcessingCycle(_ context.Context, _, prBeadID string) (beads.ProcessingCycleState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return beads.ProcessingCycleState{Open: c.openCycle[prBeadID]}, nil
}

func (c *concurrentBeadClient) AppendProcessingCycleNote(context.Context, string, string, string, []string) error {
	return nil
}

func (c *concurrentBeadClient) CloseProcessingCycle(context.Context, string, string) error {
	return nil
}

func (c *concurrentBeadClient) CloseFeedback(context.Context, string, string) error { return nil }

func (c *concurrentBeadClient) ListFeedbackChildrenOfCycle(context.Context, string) ([]string, error) {
	return nil, nil
}

// compile-time check.
var _ beadsbridge.BeadClient = (*concurrentBeadClient)(nil)

// TestConcurrentFlushNeverMissesPRBead drives MANY iterations, each with a FRESH
// store. For each iteration it enqueues pr.opened (committed) THEN that PR's
// feedback.created (committed, higher id), then runs multiple RunOutbox drainers
// CONCURRENTLY against the shared *store.DB with the REAL beadsbridge handler.
// It asserts the feedback handler NEVER hits the "no merge-request bead" path.
func TestConcurrentFlushNeverMissesPRBead(t *testing.T) {
	const iterations = 50
	const drainers = 3 // two workers + one maintenance flusher

	for i := range iterations {
		t.Run(fmt.Sprintf("iter-%02d", i), func(t *testing.T) {
			ctx := context.Background()
			db := store.OpenForTest(t)

			repo := "o/r"
			number := 100 + i

			// Enqueue pr.opened (committed) FIRST so it gets the lower outbox id.
			prPayload, err := json.Marshal(store.PRPayload{
				Repo: repo, Number: number, Title: "https://github.com/o/r/pull",
				State: "open", Branch: "feat", Base: "main", Author: "alice",
			})
			if err != nil {
				t.Fatalf("marshal pr payload: %v", err)
			}
			if err := db.InTx(ctx, func(tx *store.Tx) error {
				return tx.EnqueueEvent(store.EventPROpened, prPayload)
			}); err != nil {
				t.Fatalf("enqueue pr.opened: %v", err)
			}

			// THEN enqueue feedback.created (committed) — higher outbox id. This
			// mirrors the real ingest order: the PR bead must be projected before
			// the feedback handler runs.
			fbPayload, err := json.Marshal(beadsbridge.FeedbackPayload{
				Repo: repo, Number: number, Mine: false,
			})
			if err != nil {
				t.Fatalf("marshal feedback payload: %v", err)
			}
			if err := db.InTx(ctx, func(tx *store.Tx) error {
				return tx.EnqueueEvent(store.EventFeedbackCreated, fbPayload)
			}); err != nil {
				t.Fatalf("enqueue feedback.created: %v", err)
			}

			// REAL bridge handler over a concurrency-safe fake client.
			client := newConcurrentBeadClient()
			handle := beadsbridge.New(client).Handle

			// missedBead flips true if the feedback handler ever saw a nil PR bead.
			var missedBead atomic.Bool

			// dispatch wraps the real handler: RunOutbox IGNORES dispatch errors,
			// so we inspect the returned error here. A "no merge-request bead"
			// error is the invariant violation we are hunting; we flip the flag
			// and swallow the error (return nil) so RunOutbox proceeds.
			dispatch := func(ctx context.Context, e store.Event) error {
				if err := handle(ctx, e); err != nil {
					if strings.Contains(err.Error(), "no merge-request bead") {
						missedBead.Store(true)
					}
					// Any other handler error is not the invariant under test;
					// swallow it (matches RunOutbox's fire-once semantics).
				}
				return nil
			}

			// Run multiple RunOutbox drainers concurrently against the shared DB.
			var wg sync.WaitGroup
			for range drainers {
				wg.Go(func() {
					// SQLite I/O contention ("database is locked") is NOT the
					// invariant under test; rows simply stay pending and a later
					// drain completes them. Ignore RunOutbox's returned I/O error.
					_ = db.RunOutbox(ctx, dispatch)
				})
			}
			wg.Wait()

			// Drain to completion synchronously to mop up any rows left pending by
			// concurrent contention, then assert the invariant once more. This
			// makes the test deterministic regardless of SQLite locking timing.
			if err := db.RunOutbox(ctx, dispatch); err != nil {
				t.Fatalf("final drain: %v", err)
			}

			if missedBead.Load() {
				t.Fatalf("invariant violated: feedback handler hit the \"no merge-request bead\" path for %s — the PR bead was not projected before feedback.created was dispatched", beadKey(repo, number))
			}
		})
	}
}
