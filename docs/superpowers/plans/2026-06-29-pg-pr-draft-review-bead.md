# pg-pr draft-review bead Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When pg-pr's sync detects a PR, project a per-PR `draft-review` bead (child of the merge-request bead) so an agent can review it — for my PRs always, for teammate PRs only once they leave GitHub-draft.

**Architecture:** Extend the existing emit→`beadsbridge` projection (settled by `#5`). The bridge's `EventPROpened`/`EventPRUpdated` handler, after `EnsureMergeRequest`, ensures a child `draft-review` bead gated by `ownership=="mine" || !draft`. The bead is a builtin `task` discriminated by a `"draft-review: "` title prefix (no custom-type config), deduped by scanning the PR bead's children **including closed** (idempotent + no-resurrection).

**Tech Stack:** Go; the `bd` (beads) CLI via `pkg/beads` shell-out wrappers (`Runner` injectable for tests); Nix flake build.

## Global Constraints

- Reuse the builtin bd `task` type + title prefix `"draft-review: "` — do **NOT** introduce a custom `draft-review` bd type (that needs out-of-repo `types.custom` config; silent-miss risk). Mirror `pkg/beads/processingcycle.go`.
- Dedup key is the **parent PR bead** (one draft-review child per PR), scanned **including closed** beads. **Never** key on head SHA.
- A dedup lookup error MUST propagate (caller skips, retries next tick) — it MUST NOT be treated as "none exists" (the documented duplicate-cycle bug, `processingcycle.go:84-90`).
- Closed-parent guard: skip creating a draft-review bead when `EnsureMergeRequest` returns `alreadyClosed == true`.
- Module path: `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr`. All commands run from `packages/pg-pr` unless noted.
- Spec: `docs/superpowers/specs/2026-06-29-pg-pr-draft-review-bead-design.md`.

---

## File Structure

- **Create** `packages/pg-pr/pkg/beads/draftreview.go` — `EnsureDraftReviewBead` + `findDraftReviewChild` + `draftReviewTitlePrefix`. Mirrors `processingcycle.go`. One responsibility: the draft-review bead wrapper.
- **Create** `packages/pg-pr/pkg/beads/draftreview_test.go` — unit tests via a scripted `Runner` (no real bd).
- **Modify** `packages/pg-pr/internal/beadsbridge/bridge.go` — add `EnsureDraftReviewBead` to the `BeadClient` interface; in the `EventPROpened`/`EventPRUpdated` branch capture `(mrID, alreadyClosed)`, apply the closed-parent guard, and ensure the child bead under the gate.
- **Modify** `packages/pg-pr/internal/beadsbridge/bridge_test.go` — add the new method to `noopBeadClient`; add bridge-level gate tests.

The compile-time assertion `var _ BeadClient = (*beads.Client)(nil)` (`bridge.go:119`) forces `*beads.Client` to implement the new method.

---

### Task 1: `pkg/beads` — `EnsureDraftReviewBead`

**Files:**

- Create: `packages/pg-pr/pkg/beads/draftreview.go`
- Test: `packages/pg-pr/pkg/beads/draftreview_test.go`

**Interfaces:**

- Consumes: existing `*beads.Client` (`Runner`), `ListChildrenOfPR` (`processingcycle.go:158`), `parseBDList` (`mergerequest.go:340`).
- Produces:
  - `const draftReviewTitlePrefix = "draft-review: "`
  - `func (c *Client) EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error)` — ensures one draft-review child of `prBeadID`; idempotent; no-resurrection; returns the bead ID.
  - `func (c *Client) findDraftReviewChild(ctx context.Context, prBeadID string) (string, error)` — returns the ID of an existing draft-review child (open OR closed), or `""`.

- [ ] **Step 1: Write the failing tests**

Create `packages/pg-pr/pkg/beads/draftreview_test.go`:

```go
package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedRunner returns canned output per bd subcommand and records calls.
type scriptedRunner struct {
	calls     [][]string
	children  string // output for `dep list <id> --direction=up --json`
	tasks     string // output for `list --type=task ...`
	tasksErr  error  // if set, the `list --type=task` call errors
	createID  string // ID returned for `create`
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	switch {
	case len(args) >= 2 && args[0] == "dep" && args[1] == "list":
		return r.children, nil
	case len(args) >= 2 && args[0] == "dep" && args[1] == "add":
		return "", nil
	case len(args) >= 1 && args[0] == "list":
		if r.tasksErr != nil {
			return "", r.tasksErr
		}
		return r.tasks, nil
	case len(args) >= 1 && args[0] == "create":
		return r.createID, nil
	}
	return "", nil
}

func (r *scriptedRunner) sawCreate() bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "create" {
			return true
		}
	}
	return false
}

func (r *scriptedRunner) sawDepAdd() bool {
	for _, c := range r.calls {
		if len(c) >= 2 && c[0] == "dep" && c[1] == "add" {
			return true
		}
	}
	return false
}

func TestEnsureDraftReviewCreatesWhenNoChild(t *testing.T) {
	r := &scriptedRunner{children: "[]", createID: "dr-1"}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", true)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want dr-1", id)
	}
	if !r.sawCreate() {
		t.Fatalf("expected a create, calls: %v", r.calls)
	}
	if !r.sawDepAdd() {
		t.Fatalf("expected a parent-child dep add, calls: %v", r.calls)
	}
	// Title carries the prefix; mine adds the label.
	var createCall []string
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "create" {
			createCall = c
		}
	}
	joined := strings.Join(createCall, " ")
	if !strings.Contains(joined, draftReviewTitlePrefix+"o/r#7") {
		t.Fatalf("create title missing prefix: %v", createCall)
	}
	if !strings.Contains(joined, "-l mine") {
		t.Fatalf("expected mine label, got: %v", createCall)
	}
}

func TestEnsureDraftReviewDedupsExistingChild(t *testing.T) {
	// An open draft-review child already exists → no create.
	r := &scriptedRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#7","status":"open"}]}`,
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want existing dr-1", id)
	}
	if r.sawCreate() {
		t.Fatalf("must not create when a child exists, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewDoesNotResurrectClosedChild(t *testing.T) {
	// A CLOSED draft-review child exists (visible because we list --all) → no create.
	r := &scriptedRunner{
		children: `[{"id":"dr-1"}]`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#7","status":"closed"}]}`,
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	id, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err != nil {
		t.Fatalf("EnsureDraftReviewBead: %v", err)
	}
	if id != "dr-1" {
		t.Fatalf("id = %q, want closed dr-1 (no resurrection)", id)
	}
	if r.sawCreate() {
		t.Fatalf("must not resurrect a closed child, calls: %v", r.calls)
	}
}

func TestEnsureDraftReviewPropagatesLookupError(t *testing.T) {
	// Children exist, but the task-list lookup errors → must NOT create.
	r := &scriptedRunner{
		children: `[{"id":"dr-1"}]`,
		tasksErr: errors.New("boom"),
		createID: "should-not-be-used",
	}
	c := NewClientWithRunner(r)
	_, err := c.EnsureDraftReviewBead(context.Background(), "mr-1", "o/r#7", false)
	if err == nil {
		t.Fatal("expected lookup error to propagate, got nil (would risk a duplicate bead)")
	}
	if r.sawCreate() {
		t.Fatalf("must not create on lookup error, calls: %v", r.calls)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd packages/pg-pr && go test ./pkg/beads/ -run TestEnsureDraftReview -v`
Expected: compile error / FAIL — `EnsureDraftReviewBead` and `draftReviewTitlePrefix` undefined.

- [ ] **Step 3: Implement `draftreview.go`**

Create `packages/pg-pr/pkg/beads/draftreview.go`:

```go
// Package beads — draft-review bead wrappers (bd type=task).
//
// A draft-review bead represents the obligation to review one PR. It is a
// child of the merge-request bead, created by the beadsbridge when sync first
// detects a PR (for my PRs, or teammate PRs once out of draft). An agent claims
// it and produces the review; routing the review output is handled separately
// (pg2-4c5i.34 / .35).
//
// It reuses the builtin bd `task` type and is discriminated by a title prefix,
// exactly like the processing-cycle bead — so no custom-type config is needed.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// draftReviewTitlePrefix is matched verbatim by findDraftReviewChild.
const draftReviewTitlePrefix = "draft-review: "

// EnsureDraftReviewBead ensures exactly one draft-review bead (open OR closed)
// exists as a child of prBeadID, and returns its ID. title is appended after
// the canonical prefix; mine adds the `mine` ownership label so downstream
// routing (pg2-4c5i.34/.35) can distinguish my PRs from teammate PRs.
//
// Idempotent on re-delivery: if a draft-review child already exists it is
// returned without creating a second. It MUST NOT resurrect a closed
// draft-review bead — a completed review (closed bead) suppresses recreation.
// A lookup error PROPAGATES (the caller skips and retries next tick); it is
// never treated as "none exists" (that is the duplicate-cycle bug,
// processingcycle.go:84-90).
func (c *Client) EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	if prBeadID == "" {
		return "", errors.New("draft-review: pr bead id required")
	}
	existing, err := c.findDraftReviewChild(ctx, prBeadID)
	if err != nil {
		return "", err // propagate — do NOT treat as "none exists"
	}
	if existing != "" {
		return existing, nil // open or closed → do not recreate
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := draftReviewTitlePrefix + title
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	}
	if mine {
		createArgs = append(createArgs, "-l", "mine")
	}
	out, err := c.Runner.Run(ctx, createArgs...)
	if err != nil {
		return "", fmt.Errorf("create draft-review: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the draft-review bead depends on (is a child of) the
	// merge-request bead.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, prBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link draft-review %s to pr %s: %w", id, prBeadID, err)
	}
	return id, nil
}

// findDraftReviewChild returns the ID of an existing draft-review child of
// prBeadID (open OR closed), or "" when none. Strategy mirrors
// FindOpenProcessingCycle but INCLUDES closed beads (so a completed review
// suppresses recreation): resolve the PR's children once (ListChildrenOfPR),
// then intersect with `task` beads (open + closed, via --all) whose title
// carries the draft-review prefix. Every bd error PROPAGATES.
func (c *Client) findDraftReviewChild(ctx context.Context, prBeadID string) (string, error) {
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return "", nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(ctx,
		"list",
		"--type=task",
		"--all", // include closed — required for no-resurrection
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, nil
		}
	}
	return "", nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd packages/pg-pr && go test ./pkg/beads/ -run TestEnsureDraftReview -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/pkg/beads/draftreview.go packages/pg-pr/pkg/beads/draftreview_test.go
git commit -m "feat(pg-pr): EnsureDraftReviewBead wrapper (task + draft-review: prefix) [pg2-4c5i.12]"
```

---

### Task 2: `beadsbridge` — project the draft-review bead on `pr.opened`/`pr.updated`

**Files:**

- Modify: `packages/pg-pr/internal/beadsbridge/bridge.go` (interface `:18-27`; handler branch `:46-56`)
- Test: `packages/pg-pr/internal/beadsbridge/bridge_test.go` (`noopBeadClient` `:49-66`; new tests)

**Interfaces:**

- Consumes: `(*beads.Client).EnsureDraftReviewBead(ctx, prBeadID, title string, mine bool) (string, error)` from Task 1; `EnsureMergeRequest` returning `(id, alreadyClosed, err)` (`mergerequest.go:227`); `store.PRPayload` fields `Ownership`, `Draft`, `Repo`, `Number` (`store/event.go`, `sync/prevents.go:32-38`).
- Produces: no new exported symbols; behavior change in `Handle`.

- [ ] **Step 1: Add the new method to `noopBeadClient` so the package keeps compiling**

In `bridge_test.go`, after the other `noopBeadClient` methods (around `:66`), add:

```go
func (noopBeadClient) EnsureDraftReviewBead(context.Context, string, string, bool) (string, error) {
	return "", nil
}
```

- [ ] **Step 2: Write the failing bridge tests**

Append to `bridge_test.go`:

```go
// draftReviewClient records EnsureDraftReviewBead calls and controls the
// alreadyClosed result of EnsureMergeRequest.
type draftReviewClient struct {
	noopBeadClient
	alreadyClosed bool
	drCalls       int
	lastPRBeadID  string
	lastTitle     string
	lastMine      bool
}

func (c *draftReviewClient) EnsureMergeRequest(context.Context, string, beads.MergeRequestFields) (string, bool, error) {
	return "mr-1", c.alreadyClosed, nil
}

func (c *draftReviewClient) EnsureDraftReviewBead(_ context.Context, prBeadID, title string, mine bool) (string, error) {
	c.drCalls++
	c.lastPRBeadID = prBeadID
	c.lastTitle = title
	c.lastMine = mine
	return "dr-1", nil
}

func TestPROpenedMinePRCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// My PR, still a GitHub draft → review bead is still created.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure, got %d", c.drCalls)
	}
	if !c.lastMine {
		t.Fatalf("expected mine=true for my PR")
	}
	if c.lastPRBeadID != "mr-1" {
		t.Fatalf("expected parent bead id mr-1, got %q", c.lastPRBeadID)
	}
	if c.lastTitle != "o/r#7" {
		t.Fatalf("expected title o/r#7, got %q", c.lastTitle)
	}
}

func TestPROpenedTeamDraftSkipsDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// Teammate PR still in draft → NO review bead yet.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("expected no draft-review for a teammate draft PR, got %d", c.drCalls)
	}
}

func TestPROpenedTeamReadyCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// Teammate PR, not a draft → review bead created.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure for a ready teammate PR, got %d", c.drCalls)
	}
	if c.lastMine {
		t.Fatalf("expected mine=false for a teammate PR")
	}
}

func TestPRUpdatedTeamDraftToReadyCreatesDraftReview(t *testing.T) {
	c := &draftReviewClient{}
	h := New(c)
	// First observation: teammate draft → no bead.
	draftPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: true})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: draftPayload}); err != nil {
		t.Fatalf("Handle (draft): %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("expected no draft-review while still draft, got %d", c.drCalls)
	}
	// Draft flag removed → pr.updated with Draft=false → bead created.
	readyPayload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "team", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPRUpdated, Payload: readyPayload}); err != nil {
		t.Fatalf("Handle (ready): %v", err)
	}
	if c.drCalls != 1 {
		t.Fatalf("expected 1 draft-review ensure after draft→ready, got %d", c.drCalls)
	}
}

func TestPROpenedClosedParentSkipsDraftReview(t *testing.T) {
	c := &draftReviewClient{alreadyClosed: true}
	h := New(c)
	// PR bead already closed → no review bead even for my PR.
	payload, _ := json.Marshal(store.PRPayload{Repo: "o/r", Number: 7, Ownership: "mine", Draft: false})
	if err := h.Handle(context.Background(), store.Event{Type: store.EventPROpened, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if c.drCalls != 0 {
		t.Fatalf("closed-parent guard failed: expected 0 draft-review ensures, got %d", c.drCalls)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd packages/pg-pr && go test ./internal/beadsbridge/ -run 'TestPROpened|TestPRUpdated' -v`
Expected: the new tests FAIL — `Handle` does not yet call `EnsureDraftReviewBead`, so `drCalls` stays 0 where 1 is expected (and the interface lacks the method until Step 4). Existing tests still pass.

- [ ] **Step 4: Add the method to the `BeadClient` interface**

In `bridge.go`, inside the `BeadClient` interface (after `CloseFeedback`, `:26`), add:

```go
	EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error)
```

- [ ] **Step 5: Wire the draft-review ensure into the handler**

In `bridge.go`, replace the `EventPROpened`/`EventPRUpdated` branch (`:46-56`):

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

with:

```go
	case store.EventPROpened, store.EventPRUpdated:
		var p store.PRPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("beadsbridge: decode pr payload: %w", err)
		}
		mrID, alreadyClosed, err := h.client.EnsureMergeRequest(ctx, p.Title, beads.MergeRequestFields{
			Repo: p.Repo, PRNumber: p.Number, State: p.State, Branch: p.Branch,
			Base: p.Base, Author: p.Author, URL: p.URL, Draft: p.Draft,
			LastSyncedAt: p.LastSyncedAt,
		})
		if err != nil {
			return err
		}
		if alreadyClosed {
			return nil // closed PR bead: do not attach a draft-review under it
		}
		// Emit the review work item. My PRs are reviewed even while a GitHub
		// draft; teammate PRs wait until the draft flag is removed (which fires
		// on the pr.updated that flips it). EnsureDraftReviewBead is idempotent.
		if p.Ownership == "mine" || !p.Draft {
			_, err := h.client.EnsureDraftReviewBead(ctx, mrID, fmt.Sprintf("%s#%d", p.Repo, p.Number), p.Ownership == "mine")
			return err
		}
		return nil
```

- [ ] **Step 6: Run the bridge tests to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/beadsbridge/ -v`
Expected: PASS — new gate tests pass, and all pre-existing tests (`TestPROpenedCreatesPRBead`, `TestPROpenedWritesFullFields`, `TestPROpenedIdempotentSingleBead`, `TestClosedBeadNotResurrectedByReappearance`, etc.) still pass.

- [ ] **Step 7: Commit**

```bash
git add packages/pg-pr/internal/beadsbridge/bridge.go packages/pg-pr/internal/beadsbridge/bridge_test.go
git commit -m "feat(pg-pr): project draft-review bead on pr.opened/updated (gated by ownership/draft) [pg2-4c5i.12]"
```

---

### Task 3: Verification gate

**Files:** none (verification only).

- [ ] **Step 1: Full package test suite**

Run: `cd packages/pg-pr && go test ./...`
Expected: all packages PASS.

- [ ] **Step 2: Vet + format + lint**

Run:

```bash
cd packages/pg-pr && go vet ./... && gofmt -l .
```

Expected: `go vet` clean; `gofmt -l .` prints nothing (no unformatted files). If `gofmt -l` lists files, run `gofmt -w <files>` and re-run.

- [ ] **Step 3: Pre-commit hooks across the repo**

From the worktree root, run the configured hooks (golangci-lint for pg-pr, treefmt, etc.):

```bash
cd "$(git rev-parse --show-toplevel)" && pre-commit run --all-files
```

Expected: all hooks Pass. Fix any reported issues (do NOT use `--no-verify`).

- [ ] **Step 4: Flake check (builds pg-pr)**

Run: `cd "$(git rev-parse --show-toplevel)" && nix flake check`
Expected: succeeds.

- [ ] **Step 5: Consumer build note**

The full `pn workspace build` gate builds the **canonical** workspace checkouts, not this worktree (`PN_WORKSPACE_OVERRIDE_PATHS` is not honored by `pn workspace build` — bd memory `pn-build-single-repo-worktree-override`). Run it **after merge to `main`**, or build the worktree directly with `darwin-rebuild build` replicating pn's sibling `--override-input` flags with this worktree's path swapped in. This is a post-merge / pre-existing-workflow step, not part of the per-task TDD loop.

- [ ] **Step 6: Update the bead**

```bash
bd -C /Users/phillipg/phillipg_mbp update pg2-4c5i.12 --status in_progress
# After merge: bd -C /Users/phillipg/phillipg_mbp close pg2-4c5i.12
```

---

## Self-Review

**Spec coverage:**

- Trigger + gate (`ownership=="mine" || !draft`) → Task 2, Step 5; tests Task 2 Steps 2 (`mine`/`team-draft`/`team-ready`/`draft→ready`).
- Same-handler projection after `EnsureMergeRequest` → Task 2, Step 5.
- Builtin `task` + `"draft-review: "` prefix (no custom type) → Task 1, Step 3 (`draftReviewTitlePrefix`, `--type=task`).
- Idempotency + no-resurrection (scan children incl. closed) → Task 1, Step 3 (`findDraftReviewChild` with `--all`); tests `Dedups`/`DoesNotResurrect`.
- Lookup-error propagation → Task 1, Step 3 (`return "", err`); test `PropagatesLookupError`.
- Closed-parent guard via `alreadyClosed` → Task 2, Step 5; test `ClosedParentSkips`.
- Cascade close of an open draft-review child → already covered by existing `cascadeClose` + `ListChildrenOfPR` (enumerates all children regardless of type) and `CloseProcessingCycle`/`bd close` (works on any type); no new code. Verified by the existing `TestPRMergedCascadeCloses` pattern; the new bead type is closed by the same loop.
- Ownership label for downstream routing → Task 1, Step 3 (`-l mine`).
- Reopened-PR / draft→ready→draft limitations → documented in spec; no code (intentional).

**Placeholder scan:** none — every code step contains complete code; every run step has an exact command + expected outcome.

**Type consistency:** `EnsureDraftReviewBead(ctx, prBeadID, title string, mine bool) (string, error)` is identical in the interface (Task 2 Step 4), the `*beads.Client` method (Task 1 Step 3), the `noopBeadClient` stub (Task 2 Step 1), and the `draftReviewClient` fake (Task 2 Step 2). `draftReviewTitlePrefix` is defined once (Task 1) and referenced in tests. `EnsureMergeRequest`'s `(id, alreadyClosed, err)` return matches `mergerequest.go:227`.
