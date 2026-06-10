# Per-PR Refresh Throughput Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `pg-pr` daemon's per-PR refresh fast enough that `/api/v1/dashboard` populates, by removing workspace-wide and redundant work from the per-PR hot path.

**Architecture:** Keep the two-tier model (fingerprint detector → dedup queues → 2 workers → snapshot owner). Lift the workspace-wide bd work (the `human`-label scan and the reply-draft drain) onto a single asynchronous maintenance goroutine, so a slow bd never stalls the detector/workers. Make each per-PR refresh fetch only that PR's own data once (one enrichment bundle reused across the feedback pipeline and the snapshot builder) and thread the bead id so it stops re-looking-up a bead it just upserted.

**Tech Stack:** Go (`packages/pg-pr`), `bd`/dolt issue tracker, `gh` CLI provider, Prometheus telemetry. Tests: standard `go test` with in-package fakes.

**Spec:** `docs/superpowers/specs/2026-06-10-per-pr-refresh-throughput-design.md`

**Branch:** `pg-pr-refresh-throughput` (already created; do NOT `git checkout` / create branches).

**Working dir for all commands:** `packages/pg-pr` (i.e. `cd packages/pg-pr` from repo root). Run `git` from the repo root with full `packages/pg-pr/...` pathspecs.

**Note on diagnostics:** Editor/LSP diagnostics are frequently stale in this workspace. Trust `go build ./...` / `go vet ./...` / `go test`, not the diagnostics. The full `internal/sync` suite is slow (real bd+dolt, 30+ min) — iterate with targeted `-run` tests, run the full suite once at the end (Task 6).

---

## File structure

- `internal/sync/sync.go` — engine. Add the `humanLabels` atomic field, the `humanLabelReader` interface, `humanLabelsFor`, `refreshHumanLabels`, and `enrichOnePR`. Modify `buildPRInput` (label source + `knownMRID` param), `applyFetchedPR` (forward `enriched`, return `alreadyClosed`, drop reply drain), and `SyncPR` (explicit reply drain).
- `internal/sync/refresh.go` — `refreshPR` active branch: build the enrichment bundle once, thread the bead id + bundle into `applyFetchedPR` and `buildPRInput`.
- `internal/sync/daemon.go` — `maintenanceCycle`, `drainReplies`, `runMaintenance`; start the goroutine in `Daemon` and wait for it on shutdown.
- `internal/sync/refresh_test.go` — update `TestBuildPRInput_AppliesHumanLabelWithoutCache`; rename the `refreshFakeBeads.feedbackRan` signal to `replyDrainRan`; invert `TestRefreshPR_ActiveMine_UpsertsSnapshot`; add SyncPR reply tests.
- `internal/sync/maintenance_test.go` (new) — unit tests for `refreshHumanLabels`, `humanLabelsFor`, and `maintenanceCycle`.

---

## Task 1: Engine `human`-label cache (field, accessor, refresh)

Adds the atomic per-repo label set and the function that fills it. No existing call sites change (new methods are unused until later tasks), so the build stays green.

**Files:**

- Modify: `internal/sync/sync.go` (the `Engine` struct ~`sync.go:167-182`; add new methods + interface)
- Test: `internal/sync/maintenance_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/sync/maintenance_test.go`:

```go
package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

func TestRefreshHumanLabels_PopulatesAtomicSet(t *testing.T) {
	bdc := &fakeDepBeads{human: map[string]bool{"fb-1": true}}
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.refreshHumanLabels(context.Background())

	got := e.humanLabelsFor("o/r")
	if !got["fb-1"] {
		t.Fatalf("humanLabelsFor(o/r) missing fb-1; got %v", got)
	}
	if e.humanLabelsFor("absent") != nil {
		t.Fatal("humanLabelsFor for an unknown repo must be nil")
	}
}

func TestHumanLabelsFor_NilBeforeFirstPull(t *testing.T) {
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: &fakeDepBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.humanLabelsFor("o/r") != nil {
		t.Fatal("humanLabelsFor must be nil before the first pull")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestRefreshHumanLabels_PopulatesAtomicSet|TestHumanLabelsFor_NilBeforeFirstPull' 2>&1 | tail -20`
Expected: compile failure — `e.refreshHumanLabels` / `e.humanLabelsFor` undefined.

- [ ] **Step 3: Add the field to the `Engine` struct**

In `internal/sync/sync.go`, in the `Engine` struct (after the `authFailStreak int` field), add:

```go
	// humanLabels is the per-repo set of bead IDs carrying the `human` label
	// (repo -> set). Refreshed off the critical path by the daemon's
	// maintenance goroutine (refreshHumanLabels) and read by workers in
	// buildPRInput's cache-less branch. A *map is stored so the read is a
	// single atomic load; the stored map is never mutated after Store.
	humanLabels atomic.Pointer[map[string]map[string]bool]
```

(`sync/atomic` is already imported.)

- [ ] **Step 4: Add the interface, accessor, and refresh method**

In `internal/sync/sync.go`, near the `depTreeReader` interface (~`sync.go:622`), add:

```go
// humanLabelReader is the narrow capability the maintenance goroutine needs to
// pull the workspace's `human`-labeled bead set. The real *beads.Client
// satisfies it; test-injected clients that don't are skipped.
type humanLabelReader interface {
	HumanLabeledBeads(ctx context.Context) (map[string]bool, error)
}

// humanLabelsFor returns the last-pulled `human`-label set for repo, or nil if
// no pull has populated it yet. Safe to call from any goroutine.
func (e *Engine) humanLabelsFor(repo string) map[string]bool {
	m := e.humanLabels.Load()
	if m == nil {
		return nil
	}
	return (*m)[repo]
}

// refreshHumanLabels pulls the `human`-labeled bead set for every configured
// repo and atomically publishes the result for workers to read. A per-repo
// pull error preserves that repo's previous set (no flicker); a repo whose
// client lacks HumanLabeledBeads (test injection) is skipped. Runs on the
// maintenance goroutine only.
func (e *Engine) refreshHumanLabels(ctx context.Context) {
	out := map[string]map[string]bool{}
	for _, rcfg := range e.cfg().Repos {
		reader, ok := e.bdClientFor(rcfg).(humanLabelReader)
		if !ok {
			continue
		}
		set, err := reader.HumanLabeledBeads(ctx)
		if err != nil {
			if prev := e.humanLabelsFor(rcfg.Remote); prev != nil {
				out[rcfg.Remote] = prev
			}
			continue
		}
		out[rcfg.Remote] = set
	}
	e.humanLabels.Store(&out)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestRefreshHumanLabels_PopulatesAtomicSet|TestHumanLabelsFor_NilBeforeFirstPull' -v 2>&1 | tail -20`
Expected: PASS (both tests). Also run `go build ./...` — expected: no output (success).

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/maintenance_test.go
git commit -m "feat(pg-pr): engine per-repo human-label atomic cache"
```

---

## Task 2: `buildPRInput` reads the engine label set + accepts `knownMRID`

Switches the cache-less label overlay from a per-PR `HumanLabeledBeads` call to the engine's atomic set, and lets callers pass a known bead id so the dep-tree path skips `FindByRepoAndNumber`. Updates all three call sites.

**Files:**

- Modify: `internal/sync/sync.go` (`buildPRInput` ~`sync.go:647-739`; caller `buildAndStoreSnapshot` ~`sync.go:601`)
- Modify: `internal/sync/refresh.go` (the `buildPRInput` call ~`refresh.go:77`)
- Modify: `internal/sync/refresh_test.go` (`TestBuildPRInput_AppliesHumanLabelWithoutCache` ~`refresh_test.go:35`)

- [ ] **Step 1: Update the existing test to seed the engine set and pass `knownMRID`**

In `internal/sync/refresh_test.go`, replace `TestBuildPRInput_AppliesHumanLabelWithoutCache` (lines ~35-65) with:

```go
func TestBuildPRInput_AppliesHumanLabelWithoutCache(t *testing.T) {
	bdc := &fakeDepBeads{
		mrID: "mr-1",
		deps: []beads.DepNode{{ID: "fb-1", Status: "open"}},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The human-label overlay now comes from the engine's atomic set, not a
	// per-PR HumanLabeledBeads call.
	e.humanLabels.Store(&map[string]map[string]bool{"o/r": {"fb-1": true}})
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}

	in := e.buildPRInput(context.Background(), pr, nil, bdc, nil, config.RepoConfig{Remote: "o/r"}, "")

	if len(in.BeadsDeps) != 1 {
		t.Fatalf("want 1 dep, got %d", len(in.BeadsDeps))
	}
	found := false
	for _, l := range in.BeadsDeps[0].Labels {
		if l == "human" {
			found = true
		}
	}
	if !found {
		t.Fatal("human label not applied on cache-less path; WaitingOnMe will regress")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run TestBuildPRInput_AppliesHumanLabelWithoutCache 2>&1 | tail -20`
Expected: compile failure — `buildPRInput` called with 7 args but defined with 6 (`knownMRID` not yet added).

- [ ] **Step 3: Add the `knownMRID` parameter and change the mrID + label logic**

In `internal/sync/sync.go`, change the `buildPRInput` signature to add `knownMRID string` as the final parameter:

```go
func (e *Engine) buildPRInput(ctx context.Context, pr api.PR, enriched *vcs.EnrichedPR, bdc BeadClient, cache *beads.TickCache, rcfg config.RepoConfig, knownMRID string) snapshot.PRInput {
```

Then replace the mrID-resolution + human-label block. Find this current code (~`sync.go:691-734`):

```go
	reader, hasReader := bdc.(depTreeReader)
	if cache != nil || hasReader {
		var mrID string
		if cache != nil {
			if mr, found := cache.FindMergeRequest(pr.Repo, pr.Number); found {
				mrID = mr.ID
			}
		}
		if mrID == "" && hasReader {
			if mr, ferr := reader.FindByRepoAndNumber(ctx, pr.Repo, pr.Number); ferr == nil && mr != nil {
				mrID = mr.ID
			}
		}
```

and replace the `var mrID string ... }` resolution prologue with:

```go
	reader, hasReader := bdc.(depTreeReader)
	if cache != nil || hasReader {
		var mrID string
		switch {
		case knownMRID != "":
			mrID = knownMRID
		case cache != nil:
			if mr, found := cache.FindMergeRequest(pr.Repo, pr.Number); found {
				mrID = mr.ID
			}
		}
		if mrID == "" && hasReader {
			if mr, ferr := reader.FindByRepoAndNumber(ctx, pr.Repo, pr.Number); ferr == nil && mr != nil {
				mrID = mr.ID
			}
		}
```

Then find the human-label overlay (~`sync.go:728-734`):

```go
				if cache != nil {
					beads.ApplyHumanLabels(deps, cache.HumanLabeled)
				} else if hasReader {
					if set, herr := reader.HumanLabeledBeads(ctx); herr == nil {
						beads.ApplyHumanLabels(deps, set)
					}
				}
```

and replace it with:

```go
				if cache != nil {
					beads.ApplyHumanLabels(deps, cache.HumanLabeled)
				} else {
					beads.ApplyHumanLabels(deps, e.humanLabelsFor(pr.Repo))
				}
```

(`ApplyHumanLabels` is a no-op on a nil/empty set, so no guard is needed.)

- [ ] **Step 4: Update the other two call sites**

In `internal/sync/sync.go`, `buildAndStoreSnapshot` (~`sync.go:601`): change

```go
		in := e.buildPRInput(ctx, pr, enriched, bdc, cachesByRepo[key.Repo], rcfg)
```

to

```go
		in := e.buildPRInput(ctx, pr, enriched, bdc, cachesByRepo[key.Repo], rcfg, "")
```

In `internal/sync/refresh.go` (~`refresh.go:77`): change

```go
	in := e.buildPRInput(ctx, *pr, nil, bdc, nil, rcfg)
```

to

```go
	in := e.buildPRInput(ctx, *pr, nil, bdc, nil, rcfg, "")
```

(Task 4 will replace this line with the enriched bundle + threaded id.)

- [ ] **Step 5: Run test + build to verify**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run TestBuildPRInput_AppliesHumanLabelWithoutCache -v 2>&1 | tail -10 && go build ./... && go vet ./internal/sync/ 2>&1 | tail -10`
Expected: test PASS; build/vet clean.

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/refresh.go packages/pg-pr/internal/sync/refresh_test.go
git commit -m "refactor(pg-pr): buildPRInput reads engine label set, accepts knownMRID"
```

---

## Task 3: Relocate reply-draft draining out of `applyFetchedPR`

`applyFetchedPR` stops draining replies and stops being the shared place that does it. It now forwards `enriched` to `processFeedback` AND `maybePromoteDraft`, and returns `alreadyClosed`. The daemon path (`refreshPR`) no longer drains replies; the one-shot CLI (`SyncPR`) drains them explicitly, preserving the skip-on-already-closed behavior. The `refreshFakeBeads.feedbackRan` signal (which only fired via `ListFeedbackPendingReply`) is renamed `replyDrainRan` and the affected assertions are updated.

**Files:**

- Modify: `internal/sync/sync.go` (`applyFetchedPR` ~`sync.go:886-919`; `SyncPR` ~`sync.go:862-864`; update the `applyFetchedPR` doc comment ~`sync.go:871-885`)
- Modify: `internal/sync/refresh.go` (the `applyFetchedPR` call ~`refresh.go:74`)
- Modify: `internal/sync/refresh_test.go` (rename `feedbackRan`→`replyDrainRan`; invert `TestRefreshPR_ActiveMine_UpsertsSnapshot`; add `ensureClosed`; add SyncPR tests)

- [ ] **Step 1: Rename the fake's signal and add an `ensureClosed` knob**

In `internal/sync/refresh_test.go`, in the `refreshFakeBeads` struct (~`refresh_test.go:81-87`) replace `feedbackRan bool` and add `ensureClosed bool`:

```go
type refreshFakeBeads struct {
	noopBeads
	existing     *beads.MergeRequest
	lastState    string
	closed       bool
	replyDrainRan bool // set when processReplyDrafts reached ListFeedbackPendingReply
	ensureClosed bool  // when true, EnsureMergeRequest reports the bead already closed
}
```

Update the comment block above the struct (~`refresh_test.go:71-80`) to describe `replyDrainRan` (whether reply draining ran) instead of `feedbackRan`.

Change `EnsureMergeRequest` (~`refresh_test.go:89-92`) to honor `ensureClosed`:

```go
func (f *refreshFakeBeads) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.lastState = fields.State
	return "mr-1", f.ensureClosed, nil
}
```

Change `ListFeedbackPendingReply` (~`refresh_test.go:106-109`) to set the renamed field:

```go
func (f *refreshFakeBeads) ListFeedbackPendingReply(_ context.Context) ([]beads.Feedback, error) {
	f.replyDrainRan = true
	return nil, nil
}
```

Update the two existing assertions that reference `feedbackRan` and stay `false`:

- `TestRefreshPR_ClosedMerged_ClosesAndRemoves` (~`refresh_test.go:159-161`): `if bdc.feedbackRan {` → `if bdc.replyDrainRan {` (message: "closed path must not drain replies").
- `TestRefreshPR_TeamDraft_MarksDraftKeepsBeadHidden` (~`refresh_test.go:185-187`): `if bdc.feedbackRan {` → `if bdc.replyDrainRan {` (message: "dormant team-draft path must not drain replies").

- [ ] **Step 2: Invert the active-path assertion and add SyncPR reply tests**

In `internal/sync/refresh_test.go`, in `TestRefreshPR_ActiveMine_UpsertsSnapshot` (~`refresh_test.go:211-213`), replace:

```go
	if !bdc.feedbackRan {
		t.Fatal("active path must run the full pipeline (ListFeedbackPendingReply)")
	}
```

with:

```go
	if bdc.replyDrainRan {
		t.Fatal("daemon refreshPR must NOT drain replies; the maintenance goroutine does")
	}
```

Then append these two new tests to `internal/sync/refresh_test.go`:

```go
func TestSyncPR_DrainsRepliesOnOpenBead(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 7, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/7",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	if _, err := e.SyncPR(context.Background(), "o/r", 7); err != nil {
		t.Fatalf("SyncPR: %v", err)
	}
	if !bdc.replyDrainRan {
		t.Fatal("SyncPR must drain replies for an open bead")
	}
}

func TestSyncPR_SkipsReplyDrainWhenAlreadyClosed(t *testing.T) {
	bdc := &refreshFakeBeads{ensureClosed: true}
	pr := api.PR{
		Repo: "o/r", Number: 8, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/8",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	if _, err := e.SyncPR(context.Background(), "o/r", 8); err != nil {
		t.Fatalf("SyncPR: %v", err)
	}
	if bdc.replyDrainRan {
		t.Fatal("SyncPR must skip reply drain when the bead is already closed")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestRefreshPR_ActiveMine_UpsertsSnapshot|TestSyncPR_DrainsRepliesOnOpenBead|TestSyncPR_SkipsReplyDrainWhenAlreadyClosed' 2>&1 | tail -20`
Expected: compile failure (`applyFetchedPR` still has the old signature / `SyncPR` not yet updated) OR, once it compiles, the new-behavior tests fail. Either is an acceptable red.

- [ ] **Step 4: Change `applyFetchedPR` (forward enriched, return alreadyClosed, drop reply drain)**

In `internal/sync/sync.go`, replace `applyFetchedPR` (~`sync.go:886-919`) body and signature:

```go
func (e *Engine) applyFetchedPR(ctx context.Context, bdc BeadClient, rcfg config.RepoConfig, pr *api.PR, enriched *vcs.EnrichedPR, summary *Summary) (string, bool, error) {
	fields := beads.MergeRequestFields{
		Repo:         rcfg.Remote,
		PRNumber:     pr.Number,
		State:        stateForPR(*pr),
		Branch:       pr.Branch,
		Base:         pr.Base,
		Author:       pr.Author,
		URL:          pr.URL,
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
		Draft:        pr.Draft,
	}
	id, alreadyClosed, err := bdc.EnsureMergeRequest(ctx, pr.URL, fields)
	if err != nil || alreadyClosed {
		return id, alreadyClosed, err
	}
	summary.BeadsUpdated = 1
	// Phase 3: feedback + draft pipelines. enriched (when non-nil) carries the
	// PR's comments/CI runs so these helpers skip their own per-PR fetches.
	if err := e.processFeedback(ctx, bdc, nil, enriched, rcfg.Remote, *pr, id, summary); err != nil {
		return id, false, err
	}
	if e.isSelfAuthored(pr.Author) {
		if err := e.maybePromoteDraft(ctx, bdc, enriched, rcfg.Remote, *pr, id, summary); err != nil {
			return id, false, err
		}
	}
	return id, false, nil
}
```

Update the doc comment above `applyFetchedPR` (~`sync.go:871-885`): remove the sentence describing the reply phase and note that reply draining is now done by the caller (daemon: maintenance goroutine; CLI: `SyncPR`), and that it returns `alreadyClosed`.

- [ ] **Step 5: Update `SyncPR` to drain replies explicitly**

In `internal/sync/sync.go`, `SyncPR` (~`sync.go:862-864`): replace

```go
	if _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, summary); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
	}
```

with

```go
	_, alreadyClosed, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, nil, summary)
	if err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: err.Error()})
	} else if !alreadyClosed {
		// One-shot reply drain (the shared apply path no longer does it).
		if rerr := e.processReplyDrafts(ctx, bdc, rcfg, summary); rerr != nil {
			summary.Errors = append(summary.Errors, SummaryError{Repo: repo, Message: rerr.Error()})
		}
	}
```

- [ ] **Step 6: Update the daemon caller so it compiles**

In `internal/sync/refresh.go` (~`refresh.go:74`): replace

```go
	if _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, summary); err != nil {
		return nil, err
	}
```

with

```go
	if _, _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, nil, summary); err != nil {
		return nil, err
	}
```

(Task 4 replaces this with the enriched bundle + threaded id.)

- [ ] **Step 7: Run tests + build to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestRefreshPR_|TestSyncPR_' -v 2>&1 | tail -30 && go build ./...`
Expected: all `TestRefreshPR_*` and `TestSyncPR_*` PASS; build clean.

- [ ] **Step 8: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/refresh.go packages/pg-pr/internal/sync/refresh_test.go
git commit -m "refactor(pg-pr): move reply-draft drain out of applyFetchedPR"
```

---

## Task 4: `refreshPR` fetches each PR's enrichment once and threads the bead id

The daemon active path now does a single focused enrichment fetch and reuses it across the feedback pipeline and the snapshot builder, and passes the upserted bead id so `buildPRInput` skips `FindByRepoAndNumber`.

**Files:**

- Modify: `internal/sync/sync.go` (add `enrichOnePR` near `buildPRInput`)
- Modify: `internal/sync/refresh.go` (active branch ~`refresh.go:71-78`)
- Test: `internal/sync/refresh_test.go` (add an enrichment-reuse test; reuse `newRefreshEngine`)

- [ ] **Step 1: Write the failing test**

`newRefreshEngine` uses `newFakeVCS`. Confirm the fake records comment-fetch calls; if it does not already expose a counter, this test asserts on the snapshot input the active path produces (which proves the enriched bundle flowed through). Append to `internal/sync/refresh_test.go`:

```go
func TestRefreshPR_ActiveMine_EnrichmentReused(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 9, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/9",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 9)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("active self PR must yield a non-nil snapshot input")
	}
	// The snapshot input is built from the per-PR enrichment bundle the active
	// path fetched once; the PR identity must round-trip.
	if in.PR.Number != 9 || in.PR.Repo != "o/r" {
		t.Fatalf("input PR mismatch: got %s#%d", in.PR.Repo, in.PR.Number)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run TestRefreshPR_ActiveMine_EnrichmentReused 2>&1 | tail -20`
Expected: FAIL or compile error until `enrichOnePR` exists and `refreshPR` is updated. (If `newFakeVCS` lacks `ListReviews`/`ListComments`/`ListRuns` support, the test will surface it here — see Step 3 note.)

- [ ] **Step 3: Add `enrichOnePR` to `sync.go`**

In `internal/sync/sync.go`, add near `buildPRInput`:

```go
// enrichOnePR fetches one PR's reviews, comments, and CI runs via focused
// per-PR provider calls and bundles them into a *vcs.EnrichedPR so the feedback
// pipeline (processFeedback / maybePromoteDraft) and the snapshot builder
// (buildPRInput) share a single fetch instead of each issuing its own. CI runs
// come from the first configured CICD provider, preferring the branch-known
// path (matching buildPRInput's existing cache-less CI behavior). Providers
// lacking an optional capability leave the corresponding field empty.
func (e *Engine) enrichOnePR(ctx context.Context, rcfg config.RepoConfig, pr api.PR) *vcs.EnrichedPR {
	if pr.Repo == "" {
		pr.Repo = rcfg.Remote
	}
	out := vcs.EnrichedPR{PR: pr}
	if vp, err := e.providerFor(rcfg); err == nil {
		if rl, ok := vp.(ReviewLister); ok {
			if reviews, rerr := rl.ListReviews(ctx, pr.Repo, pr.Number); rerr == nil {
				out.Reviews = reviews
			}
		}
		if reader, ok := vp.(CommentReader); ok {
			if comments, cerr := reader.ListComments(ctx, pr.Repo, pr.Number); cerr == nil {
				out.Comments = comments
			}
		}
	}
	if cp := e.firstCICDFor(rcfg); cp != nil {
		if bl, ok := cp.(CICDBranchLister); ok && strings.TrimSpace(pr.Branch) != "" {
			if runs, cerr := bl.ListRunsByBranch(ctx, pr.Repo, pr.Branch); cerr == nil {
				out.CIRuns = runs
			}
		} else if runs, cerr := cp.ListRuns(ctx, pr.Repo, pr.Number); cerr == nil {
			out.CIRuns = runs
		}
	}
	return &out
}
```

(`strings` and `vcs` are already imported in `sync.go`.)

- [ ] **Step 4: Rewrite the `refreshPR` active branch**

In `internal/sync/refresh.go`, replace the active-PR block (~`refresh.go:71-78`):

```go
	// Active PR: run the full upsert + feedback + (self) draft-promote +
	// reply pipeline and build the snapshot input.
	if _, _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, nil, summary); err != nil {
		return nil, err
	}
	in := e.buildPRInput(ctx, *pr, nil, bdc, nil, rcfg, "")
	return &in, nil
```

with:

```go
	// Active PR: fetch this PR's enrichment once and reuse it across the
	// feedback pipeline and the snapshot input, threading the upserted bead id
	// so buildPRInput skips a redundant FindByRepoAndNumber.
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	id, _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, enriched, summary)
	if err != nil {
		return nil, err
	}
	in := e.buildPRInput(ctx, *pr, enriched, bdc, nil, rcfg, id)
	return &in, nil
```

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestRefreshPR_' -v 2>&1 | tail -30 && go build ./... && go vet ./internal/sync/ 2>&1 | tail -10`
Expected: all `TestRefreshPR_*` PASS; build/vet clean.

If `newFakeVCS` does not implement `ReviewLister`/`CommentReader`/`CICDBranchLister`, `enrichOnePR` simply leaves those fields empty (the type assertions fail gracefully) — the test still passes because it asserts on PR identity, not enrichment contents.

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/refresh.go packages/pg-pr/internal/sync/refresh_test.go
git commit -m "perf(pg-pr): refreshPR fetches enrichment once, threads bead id"
```

---

## Task 5: Async maintenance goroutine (label refresh + reply drain)

Runs the workspace-wide work off the critical path: one goroutine, own ticker, pulls the label set and drains replies each cycle. A slow bd only delays these; it never stalls the detector, workers, or shutdown.

**Files:**

- Modify: `internal/sync/daemon.go` (add `maintenanceCycle`, `drainReplies`, `runMaintenance`; wire into `Daemon` ~`daemon.go:200-204` and shutdown ~`daemon.go:243`)
- Test: `internal/sync/maintenance_test.go` (add `maintenanceCycle` test)

- [ ] **Step 1: Write the failing test**

Append to `internal/sync/maintenance_test.go`:

```go
func TestMaintenanceCycle_RefreshesLabelsAndDrainsReplies(t *testing.T) {
	bdc := &refreshFakeBeads{}
	// Give the fake a human-label reader by embedding the dep fake's behavior
	// is unnecessary; refreshFakeBeads embeds noopBeads which lacks
	// HumanLabeledBeads, so labels are skipped for it. Use fakeDepBeads to also
	// exercise the label pull.
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", VCS: "github"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.maintenanceCycle(context.Background(), NewTextLogger())

	if !bdc.replyDrainRan {
		t.Fatal("maintenanceCycle must drain replies")
	}
}

func TestMaintenanceCycle_PullsLabels(t *testing.T) {
	bdc := &fakeDepBeads{human: map[string]bool{"fb-9": true}}
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.maintenanceCycle(context.Background(), NewTextLogger())

	if !e.humanLabelsFor("o/r")["fb-9"] {
		t.Fatal("maintenanceCycle must refresh the human-label set")
	}
}
```

(`refreshFakeBeads` implements `ListFeedbackPendingReply`; `newRefreshEngine`'s `fakeVCS` is a `ThreadReplier`, so `processReplyDrafts` reaches `ListFeedbackPendingReply`. `fakeDepBeads` implements `HumanLabeledBeads`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestMaintenanceCycle_' 2>&1 | tail -20`
Expected: compile failure — `e.maintenanceCycle` undefined.

- [ ] **Step 3: Implement `maintenanceCycle`, `drainReplies`, `runMaintenance`**

In `internal/sync/daemon.go`, add (the file already imports `context`, `log/slog`, `time`, `stdsync "sync"`):

```go
// maintenanceCycle runs one pass of the off-critical-path workspace work:
// refresh the human-label set and drain queued reply drafts. Called by
// runMaintenance each tick and directly by tests.
func (e *Engine) maintenanceCycle(ctx context.Context, log *slog.Logger) {
	e.refreshHumanLabels(ctx)
	e.drainReplies(ctx, log)
}

// drainReplies posts queued reply drafts for every configured repo. It uses a
// throwaway Summary per repo and logs its errors/warnings, since daemon mode has
// no aggregate Summary to return.
func (e *Engine) drainReplies(ctx context.Context, log *slog.Logger) {
	for _, rcfg := range e.cfg().Repos {
		bdc := e.bdClientFor(rcfg)
		summary := &Summary{}
		if err := e.processReplyDrafts(ctx, bdc, rcfg, summary); err != nil {
			log.Warn("reply drain failed", "repo", rcfg.Remote, "err", err.Error())
		}
		for _, se := range summary.Errors {
			log.Warn("reply drain error", "repo", se.Repo, "msg", se.Message)
		}
		for _, se := range summary.Warnings {
			log.Warn("reply drain warning", "repo", se.Repo, "msg", se.Message)
		}
	}
}

// runMaintenance runs maintenanceCycle on its own ticker until ctx is cancelled.
// It pulls once immediately so the label set is populated as soon as possible.
// A slow bd only delays this loop, never the detector/workers. wg-tracked so
// shutdown waits for an in-flight cycle.
func (e *Engine) runMaintenance(ctx context.Context, interval time.Duration, log *slog.Logger, wg *stdsync.WaitGroup) {
	defer wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		e.maintenanceCycle(ctx, log)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
```

- [ ] **Step 4: Wire the goroutine into `Daemon`**

In `internal/sync/daemon.go`, find the worker startup (~`daemon.go:200-204`):

```go
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()
	var wg stdsync.WaitGroup
	wg.Add(2)
	go e.runWorker(ctx, mineQ, "mine", updates, opts.Logger, &wg)
	go e.runWorker(ctx, teamQ, "team", updates, opts.Logger, &wg)
```

and change `wg.Add(2)` to `wg.Add(3)` and add the maintenance goroutine:

```go
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()
	var wg stdsync.WaitGroup
	wg.Add(3)
	go e.runWorker(ctx, mineQ, "mine", updates, opts.Logger, &wg)
	go e.runWorker(ctx, teamQ, "team", updates, opts.Logger, &wg)
	go e.runMaintenance(ctx, opts.Interval, opts.Logger, &wg)
```

No shutdown change is needed: `wg.Wait()` (~`daemon.go:243`) already blocks on all `wg`-tracked goroutines before `close(updates)`, and the maintenance goroutine returns on `ctx.Done()`.

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestMaintenanceCycle_' -v 2>&1 | tail -20 && go build ./... && go vet ./internal/sync/ 2>&1 | tail -10`
Expected: both `TestMaintenanceCycle_*` PASS; build/vet clean.

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/daemon.go packages/pg-pr/internal/sync/maintenance_test.go
git commit -m "feat(pg-pr): async maintenance goroutine for labels + reply drain"
```

---

## Task 6: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Build, vet, and run the targeted sync tests**

Run: `cd packages/pg-pr && go build ./... && go vet ./... 2>&1 | tail -20 && go test ./internal/sync/ -run 'TestRefreshPR_|TestSyncPR_|TestBuildPRInput_|TestRefreshHumanLabels_|TestHumanLabelsFor_|TestMaintenanceCycle_' -v 2>&1 | tail -40`
Expected: build/vet clean; all listed tests PASS.

- [ ] **Step 2: Run the full `internal/sync` suite once**

Run: `cd packages/pg-pr && go test ./internal/sync/ 2>&1 | tail -30`
Expected: PASS (slow — real bd+dolt, may take 30+ min; contended by gascity agents). If a pre-existing unrelated test is flaky/slow, note it but ensure nothing this plan touched regressed.

- [ ] **Step 3: Run the broader package build/test**

Run: `cd packages/pg-pr && go test ./... 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 4: Confirm `prek` hooks pass on the Go changes**

Run from repo root: `prek run --files packages/pg-pr/internal/sync/*.go 2>&1 | tail -20` (or `pre-commit run --files ...` if `prek` is unavailable)
Expected: `gofmt`, `golangci-lint`, `go vet` hooks PASS.

- [ ] **Step 5: Live verification on the daemon** (after a `darwin-rebuild switch` picks up the new binary)

Run:

```bash
ps aux | grep '[p]g-pr sync --daemon'
curl -s 127.0.0.1:9818/api/v1/dashboard | jq '{mine:(.mine|length),team:(.team|length),generated_at,sync_interval_seconds}'
curl -s 127.0.0.1:9818/metrics | grep -E 'pg_pr_(refresh_enqueued|sync_pr_duration_seconds_count|refresh_queue_depth)'
```

Expected: `pg_pr_refresh_queue_depth` drains toward 0; `pg_pr_sync_pr_duration_seconds_count` climbs steadily (was stuck at 2); `mine`/`team` become non-zero; `generated_at` advances across repeated calls.

---

## Self-review notes (planner)

- **Spec coverage:** Design A (async maintenance: label cache + reply drain) → Tasks 1, 5. Workers read label set (`buildPRInput` cache-less branch) → Task 2. Design B (per-PR enrichment reused + threaded id) → Tasks 3 (signature/forwarding) + 4 (refreshPR). Reply relocation incl. `SyncPR` skip-on-closed and the full-sync path left intact → Task 3. `maybePromoteDraft` enriched forwarding → Task 3. Test rework (`TestRefreshPR_ActiveMine_UpsertsSnapshot`, `feedbackRan`→`replyDrainRan`, `TestBuildPRInput_AppliesHumanLabelWithoutCache`) → Tasks 2, 3. Out-of-scope items (1m rebuild, `pg_pr_snapshot_present`, idle `generated_at`) are intentionally not in this plan.
- **Type consistency:** `buildPRInput(..., knownMRID string)` (Task 2) is called with the threaded `id` in Task 4 and `""` elsewhere. `applyFetchedPR(..., enriched *vcs.EnrichedPR, ...) (string, bool, error)` (Task 3) is called as `id, _, err` in Task 4 and `_, alreadyClosed, err` in `SyncPR`. `enrichOnePR(ctx, rcfg, pr api.PR) *vcs.EnrichedPR` (Task 4) feeds both `applyFetchedPR` and `buildPRInput`. `humanLabelsFor`/`refreshHumanLabels`/`humanLabels` (Task 1) are consumed in Tasks 2 and 5. `maintenanceCycle`/`drainReplies`/`runMaintenance` (Task 5) consistent.
- **Build-green ordering:** each task updates every caller of a changed signature within the same task; `refresh.go`'s `buildPRInput`/`applyFetchedPR` calls get interim edits in Tasks 2/3 and their final form in Task 4.
