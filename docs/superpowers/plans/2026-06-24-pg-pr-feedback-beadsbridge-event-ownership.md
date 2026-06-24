# beadsbridge PR-lifecycle Event Ownership (#5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the store `pull_request` row authoritative and the `beadsbridge` handler the single owner of the PR (merge-request) bead, by having `sync`/`refresh` emit `pr.opened/updated/closed/merged` events instead of writing the bead inline. Fold in follow-up #6 (`Summary.RepliesPosted`).

**Architecture:** Approach "A2" (targeted event emission) from `docs/superpowers/specs/2026-06-24-pg-pr-feedback-beadsbridge-event-ownership-design.md`. Build the new projection capability additively (Phase A), switch the one-shot `Sync` path (Phase B), switch the daemon `refresh`/`applyFetchedPR` path (Phase C), then prove the correctness invariants and clean up (Phase D). The load-bearing invariant: a PR's `pr.opened`/`pr.updated` is enqueued+committed (in one goroutine, before that PR's `feedback.created`) so the FIFO outbox always projects the PR bead before the feedback handler runs.

**Tech Stack:** Go; SQLite event store + transactional outbox (`internal/store`); in-process dispatcher (`internal/event`); `bd` beads client (`pkg/beads`). Module path: `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr`. All commands run from `packages/pg-pr/`.

**Read first:** the design doc (above) and `internal/beadsbridge/bridge.go`, `internal/sync/sync.go`, `internal/sync/refresh.go`, `internal/store/outbox.go`, `internal/store/event.go`, `internal/store/pull_request.go`.

---

## Phase A — Build the new projection capability (additive, non-breaking)

These tasks add/enrich code the new path needs. Production still uses the inline path until Phase B, so the tree stays green throughout.

### Task 1: Move + enrich the PR event payload; bridge writes full fields

Today `beadsbridge.PRPayload` carries only `{Repo, Number, Title, Ownership, Merged}` and the handler passes only `{Repo, PRNumber}` to `EnsureMergeRequest` — a degraded bead. Move the payload to the shared `store` package (both emitter and handler depend on `store`) and enrich it; have the handler write all fields.

**Files:**

- Modify: `internal/store/event.go` (add `PRPayload`)
- Modify: `internal/beadsbridge/bridge.go` (use `store.PRPayload`; pass full fields)
- Test: `internal/beadsbridge/bridge_test.go`

- [ ] **Step 1: Write the failing test** — assert every enriched field lands on the bead.

In `internal/beadsbridge/bridge_test.go` add:

```go
func TestPROpenedWritesFullFields(t *testing.T) {
	var got beads.MergeRequestFields
	client := &capturingClient{onEnsure: func(f beads.MergeRequestFields) { got = f }}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{
		Repo: "o/r", Number: 7, Title: "t", Ownership: "team",
		State: "open", Branch: "feat", Base: "main", Author: "alice",
		URL: "https://x/7", Draft: true, LastSyncedAt: "2026-06-24T00:00:00Z",
	})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.State != "open" || got.Branch != "feat" || got.Base != "main" ||
		got.Author != "alice" || got.URL != "https://x/7" || !got.Draft {
		t.Fatalf("fields not propagated: %+v", got)
	}
}

// capturingClient is a minimal BeadClient capturing EnsureMergeRequest fields.
type capturingClient struct{ onEnsure func(beads.MergeRequestFields) }

func (c *capturingClient) EnsureMergeRequest(_ context.Context, _ string, f beads.MergeRequestFields) (string, bool, error) {
	if c.onEnsure != nil { c.onEnsure(f) }
	return "mr-1", false, nil
}
func (c *capturingClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) { return nil, nil }
func (c *capturingClient) CloseMergeRequest(context.Context, string, string) error          { return nil }
func (c *capturingClient) ListChildrenOfPR(context.Context, string) ([]string, error)       { return nil, nil }
func (c *capturingClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) { return "", nil }
func (c *capturingClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error)       { return "", false, nil }
func (c *capturingClient) CloseProcessingCycle(context.Context, string, string) error       { return nil }
func (c *capturingClient) CloseFeedback(context.Context, string, string) error              { return nil }
```

- [ ] **Step 2: Run it; verify it fails to compile** (`store.PRPayload` undefined).

Run: `go test ./internal/beadsbridge/ -run TestPROpenedWritesFullFields`
Expected: build error `undefined: store.PRPayload`.

- [ ] **Step 3: Add `store.PRPayload`** to `internal/store/event.go`:

```go
// PRPayload is the JSON body of pr.* lifecycle events. It carries everything
// the beadsbridge handler needs to project a complete merge-request bead.
type PRPayload struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Ownership    string `json:"ownership"`
	Merged       bool   `json:"merged"`
	State        string `json:"state,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Base         string `json:"base,omitempty"`
	Author       string `json:"author,omitempty"`
	URL          string `json:"url,omitempty"`
	Draft        bool   `json:"draft,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
}
```

- [ ] **Step 4: Update `internal/beadsbridge/bridge.go`** — delete the local `PRPayload` type (lines 36-42); replace all `PRPayload` references with `store.PRPayload`; in the `EventPROpened/EventPRUpdated` case build the full fields:

```go
case store.EventPROpened, store.EventPRUpdated:
	var p store.PRPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
	}
	_, _, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
		Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
		Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
		LastSyncedAt: p.LastSyncedAt,
	})
	return err
```

- [ ] **Step 5: Run tests, verify pass.**

Run: `go test ./internal/beadsbridge/`
Expected: PASS (existing `TestPROpenedCreatesPRBead` still passes; new test passes).

- [ ] **Step 6: Commit.**

```bash
git add internal/store/event.go internal/beadsbridge/bridge.go internal/beadsbridge/bridge_test.go
git commit -m "feat(pg-pr): enrich PR event payload; bridge writes full merge-request fields"
```

### Task 2: No-resurrection guard — skip feedback cycle under a closed PR bead

Today the inline `EnsureMergeRequest` returns `alreadyClosed` and `sync` returns early, so a closed-bead PR never gets feedback processed. Under A2 the feedback handler must enforce that: skip cycle creation when the parent PR bead is closed.

**Files:**

- Modify: `internal/beadsbridge/bridge.go` (`ensureProcessFeedbackBead`)
- Test: `internal/beadsbridge/bridge_test.go`

- [ ] **Step 1: Write the failing test.**

```go
func TestFeedbackSkippedWhenParentClosed(t *testing.T) {
	created := 0
	client := &closedParentClient{createInc: func() { created++ }}
	h := New(client)
	payload, _ := json.Marshal(store.FeedbackPayload{Repo: "o/r", Number: 7, Mine: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if created != 0 {
		t.Fatalf("expected no cycle created under a closed parent, got %d", created)
	}
}

// closedParentClient returns a closed merge-request from FindByRepoAndNumber.
type closedParentClient struct{ createInc func() }

func (c *closedParentClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) { return "mr-1", true, nil }
func (c *closedParentClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return &beads.MergeRequest{ID: "mr-1", Status: "closed"}, nil
}
func (c *closedParentClient) CloseMergeRequest(context.Context, string, string) error    { return nil }
func (c *closedParentClient) ListChildrenOfPR(context.Context, string) ([]string, error) { return nil, nil }
func (c *closedParentClient) CreateProcessingCycle(context.Context, string, string, bool) (string, error) {
	c.createInc()
	return "cycle-1", nil
}
func (c *closedParentClient) FindOpenProcessingCycle(context.Context, string) (string, bool, error) { return "", false, nil }
func (c *closedParentClient) CloseProcessingCycle(context.Context, string, string) error { return nil }
func (c *closedParentClient) CloseFeedback(context.Context, string, string) error        { return nil }
```

Note: if `store.FeedbackPayload` does not yet exist, this task also moves `FeedbackPayload` from `bridge.go` into `internal/store/event.go` (mirror Task 1's move). If it already lives in `bridge.go`, use `beadsbridge`'s type in the test instead — check `bridge.go` first.

- [ ] **Step 2: Run it; verify it fails** (a cycle is created → `created == 1`).

Run: `go test ./internal/beadsbridge/ -run TestFeedbackSkippedWhenParentClosed`
Expected: FAIL — `expected no cycle created under a closed parent, got 1`.

- [ ] **Step 3: Add the guard** in `ensureProcessFeedbackBead` (after the `mr == nil` check, ~`bridge.go:90`):

```go
	if mr.Status == "closed" {
		return nil // do not attach a live cycle under a closed PR bead
	}
```

- [ ] **Step 4: Run tests, verify pass.**

Run: `go test ./internal/beadsbridge/`
Expected: PASS.

- [ ] **Step 5: Add bridge coverage for the close path** (the spec flags `EventPRClosed`/`EventPRMerged`/`cascadeClose` as currently untested). Add a test that dispatches `EventPRMerged` and asserts `cascadeClose` closes the children **and** the MR with reason `upstream-merged`, and one for `EventPRClosed` → reason `pr-closed`:

```go
func TestPRMergedCascadeCloses(t *testing.T) {
	var closedReason string
	closedChildren := 0
	client := &cascadeClient{
		find:        &beads.MergeRequest{ID: "mr-1", Status: "open"},
		children:    []string{"c1", "c2"},
		onCloseMR:   func(_ , reason string) { closedReason = reason },
		onCloseChild: func() { closedChildren++ },
	}
	h := New(client)
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Merged: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRMerged, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if closedReason != "upstream-merged" || closedChildren != 2 {
		t.Fatalf("reason=%q children=%d", closedReason, closedChildren)
	}
}
```

Define `cascadeClient` as a `BeadClient` whose `FindByRepoAndNumber` returns `find`, `ListChildrenOfPR` returns `children`, `CloseProcessingCycle`/`CloseFeedback` call `onCloseChild`, and `CloseMergeRequest` calls `onCloseMR`. Add a sibling `TestPRClosedReasonPRClosed` with `Merged:false` asserting reason `pr-closed`. Run `go test ./internal/beadsbridge/`; expected PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/beadsbridge/bridge.go internal/beadsbridge/bridge_test.go internal/store/event.go
git commit -m "feat(pg-pr): bridge skips feedback cycle under a closed PR bead (no-resurrection) + close-path coverage"
```

### Task 3: `store.ListOpenPRs` query

Close-detection will read open PR rows from the store instead of `bd ListMergeRequests`.

**Files:**

- Modify: `internal/store/pull_request.go`
- Test: `internal/store/pull_request_test.go`

- [ ] **Step 1: Write the failing test.**

```go
func TestListOpenPRs(t *testing.T) {
	db := openTestDB(t) // existing helper in this package; confirm its name
	ctx := context.Background()
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 1, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/r", Number: 2, Ownership: "team", State: "closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertPR(ctx, PullRequest{Repo: "o/other", Number: 3, Ownership: "mine", State: "open"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListOpenPRs(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("want only o/r#1 open, got %+v", got)
	}
}
```

(Confirm the package's test-DB helper name — likely `openTestDB`/`newTestDB` in `store_test.go`; reuse it.)

- [ ] **Step 2: Run it; verify it fails** (`ListOpenPRs` undefined).

Run: `go test ./internal/store/ -run TestListOpenPRs`
Expected: build error `db.ListOpenPRs undefined`.

- [ ] **Step 3: Implement `ListOpenPRs`** in `internal/store/pull_request.go`:

```go
// ListOpenPRs returns the open/draft PRs for a repo — used by sync to detect
// PRs that have disappeared upstream so it can emit pr.closed/pr.merged.
func (db *DB) ListOpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, repo, number, ownership, author, state, branch, base, url, head_sha, last_synced_at
FROM pull_request WHERE repo=? AND state IN ('open','draft')`, repo)
	if err != nil {
		return nil, fmt.Errorf("store: list open prs %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PullRequest
	for rows.Next() {
		var pr PullRequest
		if err := rows.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
			&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("store: scan open pr: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests, verify pass.**

Run: `go test ./internal/store/`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/store/pull_request.go internal/store/pull_request_test.go
git commit -m "feat(pg-pr): store.ListOpenPRs for event-driven close detection"
```

### Task 4: PR-event emission helper in `sync`

A single helper builds the enriched payload and enqueues a `pr.*` event in its own committed transaction. Nil-store safe.

**Files:**

- Create: `internal/sync/prevents.go`
- Test: `internal/sync/prevents_test.go`

- [ ] **Step 1: Write the failing test.**

```go
func TestEmitPREvent_EnqueuesPayload(t *testing.T) {
	db := store.OpenInMemory(t) // use this package's store-test constructor; confirm name
	e := &Engine{deps: Deps{Store: db}}
	pr := api.PR{Number: 9, Title: "t", Branch: "b", Base: "main", Author: "me", URL: "u", Draft: true, State: "open"}
	if err := e.emitPREvent(context.Background(), store.EventPROpened, "o/r", pr, "mine"); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var seen store.PRPayload
	var typ string
	_ = db.RunOutbox(context.Background(), func(_ context.Context, ev store.Event) error {
		typ = ev.Type
		return json.Unmarshal(ev.Payload, &seen)
	})
	if typ != store.EventPROpened || seen.Number != 9 || seen.Ownership != "mine" || !seen.Draft {
		t.Fatalf("bad event: type=%s payload=%+v", typ, seen)
	}
}

func TestEmitPREvent_NilStoreNoop(t *testing.T) {
	e := &Engine{deps: Deps{}}
	if err := e.emitPREvent(context.Background(), store.EventPROpened, "o/r", api.PR{Number: 1}, "mine"); err != nil {
		t.Fatalf("nil-store emit should be a no-op, got %v", err)
	}
}
```

(Confirm the store package's in-test DB constructor; if there is no exported in-memory helper, open a temp-file DB via the same path the other `sync` tests use.)

- [ ] **Step 2: Run it; verify it fails** (`emitPREvent` undefined).

Run: `go test ./internal/sync/ -run TestEmitPREvent`
Expected: build error.

- [ ] **Step 3: Create `internal/sync/prevents.go`:**

```go
package sync

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// prPayload builds the enriched bridge payload for an observed PR.
func (e *Engine) prPayload(repo string, pr api.PR, ownership string) store.PRPayload {
	return store.PRPayload{
		Repo: repo, Number: pr.Number, Title: pr.Title, Ownership: ownership,
		Merged: pr.Merged, State: stateForPR(pr), Branch: pr.Branch, Base: pr.Base,
		Author: pr.Author, URL: pr.URL, Draft: pr.Draft,
		LastSyncedAt: e.deps.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// emitPREvent enqueues a pr.* lifecycle event in its own committed transaction.
// No-op when the store is nil (test/legacy configs without event projection).
func (e *Engine) emitPREvent(ctx context.Context, eventType, repo string, pr api.PR, ownership string) error {
	if e.deps.Store == nil {
		return nil
	}
	payload, err := json.Marshal(e.prPayload(repo, pr, ownership))
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(eventType, payload)
	})
}

// emitPRClosed enqueues pr.closed (or pr.merged when merged) for a stored PR row
// that is no longer observed upstream.
func (e *Engine) emitPRClosed(ctx context.Context, row store.PullRequest, merged bool) error {
	if e.deps.Store == nil {
		return nil
	}
	eventType := store.EventPRClosed
	if merged {
		eventType = store.EventPRMerged
	}
	payload, err := json.Marshal(store.PRPayload{
		Repo: row.Repo, Number: row.Number, Ownership: row.Ownership, Merged: merged,
	})
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(eventType, payload)
	})
}
```

(Use the same RFC3339 layout the codebase uses — `time.RFC3339`. Import `time` and use `e.deps.Now().UTC().Format(time.RFC3339)` to match `applyFetchedPR:997`.)

- [ ] **Step 4: Run tests, verify pass.**

Run: `go test ./internal/sync/ -run TestEmitPREvent`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/sync/prevents.go internal/sync/prevents_test.go
git commit -m "feat(pg-pr): emitPREvent/emitPRClosed helpers for pr.* lifecycle events"
```

### Task 5: `replyposter.Reconcile` returns a count; wire `Summary.RepliesPosted` (folds #6)

**Files:**

- Modify: `internal/replyposter/poster.go` (`Reconcile` signature)
- Modify: `internal/sync/sync.go` (`reconcileReplies:1274`, call sites `:475`, `:958`)
- Modify: `internal/sync/daemon.go:345` (maintenance call site)
- Test: `internal/replyposter/poster_test.go`

- [ ] **Step 1: Write/extend the failing test** in `poster_test.go` — assert `Reconcile` returns the number of replies posted (e.g. 2 pending → returns 2).

```go
func TestReconcileReturnsCount(t *testing.T) {
	// Arrange a store with 2 pending replies + a stub Replier that succeeds.
	// (Mirror the existing poster_test setup; assert the returned int.)
	n, err := New(db, stubReplier).Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 replies posted, got %d", n)
	}
}
```

(Adapt to the existing `poster_test.go` fixtures — reuse its store + Replier stubs.)

- [ ] **Step 2: Run it; verify it fails** (Reconcile returns only `error`).

Run: `go test ./internal/replyposter/ -run TestReconcileReturnsCount`
Expected: build error / assignment mismatch.

- [ ] **Step 3: Change `Reconcile` to `(int, error)`** — increment a counter on each successfully posted reply; return it. Update the body at `poster.go:46`.

- [ ] **Step 4: Update `reconcileReplies`** (`sync.go:1274`) to `func (e *Engine) reconcileReplies(ctx context.Context) (int, error)`; the early-return cases return `0, nil`; the final line becomes `return replyposter.New(e.deps.Store, replier).Reconcile(ctx)`.

- [ ] **Step 5: Update call sites.** At `sync.go:475`:

```go
	if n, err := e.reconcileReplies(ctx); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: "(replies)", Message: fmt.Sprintf("reply pipeline: %v", err)})
	} else {
		summary.RepliesPosted += n
	}
```

At `sync.go:958` (SyncPR path) mirror the same; at `daemon.go:345` (maintenance) the count is discarded — `if _, err := e.reconcileReplies(ctx); err != nil { log.Warn(...) }`. Update `maintenanceCycle`'s call too (`sync.go` near `reconcileReplies`).

- [ ] **Step 6: Run package + sync tests, verify pass + compile.**

Run: `go build ./... && go test ./internal/replyposter/ ./internal/sync/`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/replyposter/ internal/sync/sync.go internal/sync/daemon.go
git commit -m "fix(pg-pr): replyposter.Reconcile returns posted count; Summary.RepliesPosted wired (#6)"
```

---

## Phase B — Switch the one-shot `Sync` path to events

### Task 6: Emit `pr.opened`/`pr.updated` from `Sync`; unconditional `UpsertPR`; emit-time counts; fix gate

**Files:**

- Modify: `internal/sync/sync.go` (per-PR block `:405-436`, `processFeedback:1347`)
- Modify: `internal/sync/ingest.go` (make `UpsertPR` not depend on the bead)
- Test: `internal/sync/sync_test.go` (or the integration test in Task 11)

- [ ] **Step 1: Write the failing test** — a one-shot `Sync` over one observed PR creates the PR bead **via the outbox** (the bead client receives `EnsureMergeRequest` only through dispatch), and `Summary.BeadsCreated == 1`.

Place it in `sync_test.go` using the existing engine-test harness (store wired, in-memory dispatcher+bridge, fake VCS returning one PR). Assert: no inline `EnsureMergeRequest` call occurs before `flushOutbox`; after Sync, `summary.BeadsCreated == 1`.

- [ ] **Step 2: Run it; verify it fails** (today the bead is created inline, not via outbox).

Run: `go test ./internal/sync/ -run TestSyncCreatesBeadViaOutbox`
Expected: FAIL.

- [ ] **Step 3: Replace the inline create** in the per-PR block (`sync.go:405-436`). Remove the `fields := beads.MergeRequestFields{…}` + `EnsureMergeRequest` + `alreadyClosed` early-return + the `repoPreExisting` count increments. Replace with:

```go
	ownership := "team"
	if mineSet[key] {
		ownership = "mine"
	}
	eventType := store.EventPROpened
	if _, was := repoPreExisting[key.Repo][key]; was {
		eventType = store.EventPRUpdated
	}
	if err := e.emitPREvent(prCtx, eventType, key.Repo, pr, ownership); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: key.Repo, Message: fmt.Sprintf("PR #%d emit: %v", pr.Number, err)})
		return
	}
	if eventType == store.EventPROpened {
		summary.BeadsCreated++
	} else {
		summary.BeadsUpdated++
	}
```

Downstream `processFeedback`/`maybePromoteDraft` are now called **without** a `prBeadID` argument — see Steps 4-5. Keep `prCtx`/span handling.

- [ ] **Step 4: Make `UpsertPR` unconditional and drop the `prBeadID` gate.** In `ingest.go`, `ingestFeedbackToStore` already calls `UpsertPR`; ensure it runs for every observed PR regardless of any bead id (it is called from `processFeedback`, which is still invoked per PR). In `processFeedback` (`sync.go:1347`), change the signature to drop `prBeadID string`, and replace the gate:

```go
func (e *Engine) processFeedback(ctx context.Context, _ BeadClient, _ *beads.TickCache, enriched *vcs.EnrichedPR, repo string, pr api.PR, summary *Summary) error {
	if _, err := e.repoConfig(repo); err != nil {
		return nil // not configured (ad-hoc single PR)
	}
	if e.deps.Store != nil {
		if err := e.ingestFeedbackToStore(ctx, repo, pr, enriched); err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: "ingestFeedbackToStore: " + err.Error()})
		}
	}
	return nil
}
```

Update the call at `sync.go:448` to drop the `prBeadID` arg.

- [ ] **Step 5: Keep the build green for `maybePromoteDraft`.** Removing the inline `EnsureMergeRequest` removes the `prBeadID` that `maybePromoteDraft` consumes, so its signature must change now (the event emission lands in Task 7). Change `maybePromoteDraft`'s signature to drop the `bdc BeadClient` and `prBeadID string` params; **delete** the `bdc.UpdateMergeRequest(ctx, prBeadID, …)` line at `sync.go:1418` (Task 7 replaces it with an emit); keep `SetDraft(false)` + `summary.DraftPromoted++`. Update the call site at `sync.go:459` to `e.maybePromoteDraft(prCtx, prEnriched, key.Repo, pr, summary)`. Between this task and Task 7 the draft promotion simply doesn't persist a bead update; the next tick re-emits `pr.updated` for the now-non-draft PR, so it self-heals — and no test asserts the bead update until Task 7.

- [ ] **Step 6: Run tests, verify pass + build.**

Run: `go build ./... && go test ./internal/sync/ -run TestSyncCreatesBeadViaOutbox`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/sync/sync.go internal/sync/ingest.go
git commit -m "feat(pg-pr): Sync emits pr.opened/updated via outbox; unconditional UpsertPR; emit-time counts"
```

### Task 7: `maybePromoteDraft` emits `pr.updated{State:open}`

**Files:**

- Modify: `internal/sync/sync.go` (`maybePromoteDraft:1374-1424`)
- Test: `internal/sync/sync_test.go`

- [ ] **Step 1: Write the failing test** — when a draft PR's CI is all-green, `maybePromoteDraft` calls `SetDraft(false)` AND enqueues a `pr.updated` event with `State == "open"` (assert via draining the outbox), not an inline `UpdateMergeRequest`.

- [ ] **Step 2: Run it; verify it fails.**

Run: `go test ./internal/sync/ -run TestMaybePromoteDraftEmitsUpdate`
Expected: FAIL.

- [ ] **Step 3: Replace** the `bdc.UpdateMergeRequest(ctx, prBeadID, …)` call (`sync.go:1418`) with:

```go
	promoted := pr
	promoted.Draft = false
	promoted.State = "open"
	ownership := "mine" // maybePromoteDraft only runs for self-authored PRs
	if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, promoted, ownership); err != nil {
		return fmt.Errorf("emit pr.updated (draft-promote): %w", err)
	}
	summary.DraftPromoted++
```

Remove the now-unused `bdc BeadClient` / `prBeadID` params if nothing else uses them (the `SetDraft` path uses `provider`, not `bdc`). Adjust the signature + call site accordingly.

- [ ] **Step 4: Run tests, verify pass + build.**

Run: `go build ./... && go test ./internal/sync/ -run TestMaybePromoteDraftEmitsUpdate`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/sync/sync.go
git commit -m "feat(pg-pr): draft-promote emits pr.updated{State:open} instead of inline UpdateMergeRequest"
```

### Task 8: Close phase via `store.ListOpenPRs`; emit `pr.closed`/`pr.merged`

**Files:**

- Modify: `internal/sync/sync.go` (close phase `:489-525`, `cascadeClose` call removal)
- Test: `internal/sync/sync_test.go`

- [ ] **Step 1: Write the failing test** — a stored open PR that is NOT in the observed set causes a `pr.closed` event (assert via outbox) and `Summary.BeadsClosed == 1`; the inline `CloseMergeRequest` is no longer called directly.

- [ ] **Step 2: Run it; verify it fails.**

Run: `go test ./internal/sync/ -run TestSyncEmitsCloseForDisappearedPR`
Expected: FAIL.

- [ ] **Step 3: Replace the close phase** (`sync.go:489-525`). For each healthy repo, query the store, skip observed, emit close:

```go
	for repo := range healthyRepos {
		open, err := e.deps.Store.ListOpenPRs(ctx, repo)
		if err != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: fmt.Sprintf("list open prs: %v", err)})
			continue
		}
		for _, row := range open {
			k := prKey{Repo: row.Repo, Number: row.Number}
			if _, watched := observed[k]; watched {
				continue
			}
			merged := row.State == "merged"
			if err := e.emitPRClosed(ctx, row, merged); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: fmt.Sprintf("emit close %s#%d: %v", row.Repo, row.Number, err)})
				continue
			}
			summary.BeadsClosed++
		}
	}
```

Guard the whole block on `e.deps.Store != nil` (skip when no store). Delete the old `ListMergeRequests`/`CloseMergeRequest`/`e.cascadeClose(...)` body it replaces.

- [ ] **Step 4: Run tests, verify pass + build.**

Run: `go build ./... && go test ./internal/sync/ -run TestSyncEmitsCloseForDisappearedPR`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/sync/sync.go
git commit -m "feat(pg-pr): Sync close-detection reads store.ListOpenPRs and emits pr.closed/merged"
```

---

## Phase C — Switch the daemon path (`refresh.go` + `applyFetchedPR`)

### Task 9: `applyFetchedPR` emits `pr.opened`/`pr.updated`

**Files:**

- Modify: `internal/sync/sync.go` (`applyFetchedPR:988-1010+`)
- Test: `internal/sync/refresh_test.go` (or `sync_test.go`)

- [ ] **Step 1: Write the failing test** — `refreshPR` on an active PR enqueues `pr.opened`/`pr.updated` (drain outbox) rather than calling `EnsureMergeRequest` inline; the returned `PRInput` is still produced.

- [ ] **Step 2: Run it; verify it fails.**

Run: `go test ./internal/sync/ -run TestRefreshPREmitsOpen`
Expected: FAIL.

- [ ] **Step 3: Rewrite `applyFetchedPR`** to emit instead of calling `EnsureMergeRequest`. It no longer returns `(id, alreadyClosed)` — change its signature to `(error)` and update `refreshPR:76` and `buildPRInput`'s use of `id` (pass `""`; `buildPRInput` falls back to `FindByRepoAndNumber`, which now finds the bridge-projected bead after the per-PR `flushOutbox` at `refresh.go:81`). Choose opened-vs-updated by checking whether a bead/row already exists (use `e.deps.Store`'s `GetPR`):

```go
func (e *Engine) applyFetchedPR(ctx context.Context, _ BeadClient, rcfg config.RepoConfig, pr *api.PR, enriched *vcs.EnrichedPR, summary *Summary) error {
	ownership := "team"
	if e.isSelfAuthored(pr.Author) {
		ownership = "mine"
	}
	eventType := store.EventPROpened
	if e.deps.Store != nil {
		if existing, _ := e.deps.Store.GetPR(ctx, rcfg.Remote, pr.Number); existing != nil {
			eventType = store.EventPRUpdated
		}
	}
	if err := e.emitPREvent(ctx, eventType, rcfg.Remote, *pr, ownership); err != nil {
		return err
	}
	if err := e.processFeedback(ctx, nil, nil, enriched, rcfg.Remote, *pr, summary); err != nil {
		return err
	}
	return e.maybePromoteDraft(ctx, enriched, rcfg.Remote, *pr, summary)
}
```

Because the per-PR `flushOutbox` (`refresh.go:81`) runs after this, `pr.opened` is committed before it; and `feedback.created` (enqueued inside `processFeedback`) has a higher id — preserving the ordering invariant within the single goroutine.

- [ ] **Step 4: Update `refreshPR`** (`refresh.go:76`) to the new signature: `if err := e.applyFetchedPR(ctx, bdc, rcfg, pr, enriched, summary); err != nil { return nil, err }`, then `in := e.buildPRInput(ctx, *pr, enriched, bdc, nil, rcfg, "")`.

- [ ] **Step 5: Run tests, verify pass + build.**

Run: `go build ./... && go test ./internal/sync/ -run TestRefreshPREmitsOpen`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/sync/sync.go internal/sync/refresh.go
git commit -m "feat(pg-pr): applyFetchedPR emits pr.opened/updated (daemon path)"
```

### Task 10: `refresh.go` draft + close branches emit events; remove inline close/cascade

**Files:**

- Modify: `internal/sync/refresh.go` (`:41-52` close branch, `:56-70` hidden-draft branch)
- Test: `internal/sync/refresh_test.go`

- [ ] **Step 1: Write the failing tests** — (a) a closed/merged PR causes `refreshPR` to emit `pr.closed`/`pr.merged` (not inline `CloseMergeRequest`); (b) a hidden team draft emits `pr.updated` with `State == "draft"`.

- [ ] **Step 2: Run them; verify they fail.**

Run: `go test ./internal/sync/ -run TestRefreshPRClose`
Expected: FAIL.

- [ ] **Step 3: Rewrite the closed/merged branch** (`refresh.go:41-52`):

```go
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		row := store.PullRequest{Repo: repo, Number: pr.Number, Ownership: ownershipFor(pr), State: pr.State}
		_ = e.emitPRClosed(ctx, row, pr.Merged || pr.State == "merged")
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}
```

(Define `ownershipFor(pr)` inline as `"mine"`/`"team"` via `e.isSelfAuthored(pr.Author)`. Remove `findBeadByPR`+`CloseMergeRequest`+`cascadeClose` here — the bridge cascades.)

- [ ] **Step 4: Rewrite the hidden-team-draft branch** (`refresh.go:56-70`) to emit `pr.updated` with `State:"draft"`:

```go
	if !e.isSelfAuthored(pr.Author) && pr.Draft {
		draft := *pr
		draft.State = "draft"
		if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, draft, "team"); err != nil {
			return nil, err
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}
```

(`stateForPR` may already map a draft to `"draft"`; if so, setting `draft.State` is redundant but harmless — confirm `stateForPR`.)

- [ ] **Step 5: Run tests, verify pass + build.**

Run: `go build ./... && go test ./internal/sync/ -run TestRefreshPRClose`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/sync/refresh.go
git commit -m "feat(pg-pr): refresh close/draft branches emit pr.* events; bridge is sole closer"
```

---

## Phase D — Correctness invariants & cleanup

### Task 11: Rewrite the full-chain integration test to drive the real path

The existing `TestFullChain_IngestOutboxDispatchBridge` hard-codes `findResult` non-nil (`integration_test.go:124`), masking the ordering dependency. Replace the fake's PR-bead state with one the bridge actually populates.

**Files:**

- Modify: `internal/sync/integration_test.go`

- [ ] **Step 1: Rewrite the test** so `fullChainBeadClient` starts with `findResult == nil` and `EnsureMergeRequest` records the created bead so a subsequent `FindByRepoAndNumber` returns it (open):

```go
func (f *fullChainBeadClient) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.ensureCalls = append(f.ensureCalls, fields)
	f.findResult = &beads.MergeRequest{ID: "mr-chain-1", Status: "open"} // bridge created it
	return "mr-chain-1", false, nil
}
```

Then drive: emit `pr.opened` (via `emitPREvent`) → ingest feedback (enqueues `feedback.created`) → `RunOutbox` → assert the bridge received `EnsureMergeRequest` **before** the feedback handler found the bead, and `createCycles == 1`. Assert the outbox processed `pr.opened` (lower id) before `feedback.created`.

- [ ] **Step 2: Run it; verify it passes** (proves the real ordering, not the hard-coded shortcut).

Run: `go test ./internal/sync/ -run TestFullChain`
Expected: PASS.

- [ ] **Step 3: Commit.**

```bash
git add internal/sync/integration_test.go
git commit -m "test(pg-pr): full-chain test drives real pr.opened→feedback ordering (no hard-coded bead)"
```

### Task 12: Concurrency test — competing `RunOutbox` never misses the PR bead

**Files:**

- Create/Modify: `internal/sync/concurrency_test.go`

- [ ] **Step 1: Write the test** — enqueue `pr.opened` + `feedback.created` for the same PR in the design's order, then run two `RunOutbox` goroutines concurrently against the shared store with a bridge whose feedback handler records any "no merge-request bead" error. Assert zero such errors across many iterations.

```go
func TestConcurrentFlushNeverMissesPRBead(t *testing.T) {
	for i := 0; i < 50; i++ {
		db := newSyncTestStore(t)
		// emit pr.opened (committed) THEN feedback.created (committed), same goroutine order.
		// ... enqueue both ...
		var wg sync.WaitGroup
		var missErr atomic.Bool
		bridge := newRecordingBridge(&missErr) // sets missErr if FindByRepoAndNumber==nil in feedback path
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = db.RunOutbox(context.Background(), bridge.Dispatch) }()
		}
		wg.Wait()
		if missErr.Load() {
			t.Fatalf("iteration %d: feedback handler ran before PR bead existed", i)
		}
	}
}
```

(Wire a bridge whose `EnsureMergeRequest` records the bead so `FindByRepoAndNumber` returns it; the feedback handler sets `missErr` if it finds nil. Use the in-package store test constructor.)

- [ ] **Step 2: Run it; verify it passes.**

Run: `go test ./internal/sync/ -run TestConcurrentFlush -race -count=1`
Expected: PASS (no races, no misses).

- [ ] **Step 3: Commit.**

```bash
git add internal/sync/concurrency_test.go
git commit -m "test(pg-pr): concurrent RunOutbox never projects feedback before PR bead"
```

### Task 13: Idempotency + no-resurrection scenario tests

**Files:**

- Modify: `internal/sync/integration_test.go` or new `internal/sync/resurrection_test.go`

- [ ] **Step 1: Write the tests.**
  - **Idempotency**: dispatch the same `pr.opened` twice → exactly one bead (bridge fake counts creates; assert 1).
  - **No-resurrection (close→reappear)**: bead is closed; emit `pr.opened` + `feedback.created` for that PR → bead not reopened (EnsureMergeRequest returns alreadyClosed) AND no new cycle created (Task 2 guard). Assert `createCycles == 0`.
  - **Reopen/force-push**: an observed PR whose store row is `open` again → `pr.opened`/`pr.updated` projects normally; assert a cycle is created when the bead is open.

- [ ] **Step 2: Run them; verify pass.**

Run: `go test ./internal/sync/ -run 'TestIdempotent|TestNoResurrection'`
Expected: PASS.

- [ ] **Step 3: Commit.**

```bash
git add internal/sync/
git commit -m "test(pg-pr): idempotent re-dispatch + no-resurrection scenarios"
```

### Task 14: Counts test, full build, lint, dead-code sweep, close #6

**Files:**

- Modify: `internal/sync/sync_test.go` (counts); possibly delete now-unused inline helpers.

- [ ] **Step 1: Write the counts test** — one-shot `Sync` over a fixed observed set (1 new, 1 pre-existing, 1 disappeared) yields `BeadsCreated==1`, `BeadsUpdated==1`, `BeadsClosed==1`; a draft-promote on the new PR does NOT bump `BeadsUpdated` past its created count (dedupe).

- [ ] **Step 2: Run it; verify pass.**

Run: `go test ./internal/sync/ -run TestSyncSummaryCounts`
Expected: PASS.

- [ ] **Step 3: Dead-code sweep.** Search for now-unused symbols and remove them: the engine `cascadeClose` (`sync.go:1447`) if no longer called from `sync`/`refresh`; any `BeadClient` interface methods (`UpdateMergeRequest`, `ListChildrenOfPR`, `CloseMergeRequest`) only reachable from deleted inline code. Keep methods still used by the bridge's `BeadClient`. Verify with:

```bash
go vet ./... 2>&1 | grep -i "unused" || true
grep -rn "cascadeClose\|UpdateMergeRequest\|ListMergeRequests" internal/sync --include=*.go | grep -v _test
```

Remove only what is genuinely unreferenced (compiler/linter will confirm).

- [ ] **Step 4: Full verification.**

Run: `go build ./... && go test ./... && golangci-lint run ./...`
Expected: all PASS. (If `golangci-lint` isn't on PATH, run `prek run --all-files` from the repo root.)

- [ ] **Step 5: Close folded bead #6 and record completion.**

```bash
bd close pg2-4c5i.14 --reason "Subsumed and delivered in #5 (pg2-4c5i.9): replyposter.Reconcile now returns a count and Summary.RepliesPosted is wired. See plan task 5."
```

- [ ] **Step 6: Commit.**

```bash
git add -A
git commit -m "test(pg-pr): Sync summary counts; remove dead inline bead-write code (#5 complete)"
```

---

## Notes for the executor

- **Ordering invariant is the whole game.** In every path, a PR's `pr.opened`/`pr.updated` must be enqueued+committed (same goroutine) before that PR's `feedback.created`. The helpers commit each event in its own `InTx`, and per-PR processing is sequential, so this holds — Task 12 proves it under concurrency.
- **Nil store/dispatch** must never panic — `emitPREvent`/`emitPRClosed` no-op, and the close phase is store-guarded. Pre-existing tests without a store will no longer get a PR bead; that is expected (see design "Store/Dispatch become required").
- **Don't reintroduce inline bead writes.** After Phase C, the only place `EnsureMergeRequest`/`cascadeClose`/`CloseMergeRequest` run is inside `beadsbridge`.
- **Confirm helper/constructor names** flagged with "(confirm …)" against the actual test files before writing — they're the package's existing fixtures, names may differ.
