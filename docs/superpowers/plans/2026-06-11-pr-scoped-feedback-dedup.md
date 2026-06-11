# PR-Scoped Feedback Dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daemon's per-PR feedback dedup cost one `bd` call (a scoped `dep tree`) instead of `O(cycles × workspace-feedback)` `isChildOf` scans, so refreshes complete and the dashboards populate.

**Architecture:** Add `beads.Client.PRFeedbackFingerprints(prBeadID)` — one `bd dep tree <pr> --direction=up --json`, decoded with the existing `parseBDList`, keeping `issue_type=="feedback"` nodes' `metadata.fingerprint`. The daemon's `existingFeedbackFingerprints` calls it via a narrow `feedbackFingerprinter` type-assert (empty set fallback for fakes). Dedup stays PR-scoped (fingerprints aren't globally unique) and in-memory per event. Cache/full-`Sync` path untouched.

**Tech Stack:** Go (`packages/pg-pr`), `bd`/dolt, standard `go test`.

**Spec:** `docs/superpowers/specs/2026-06-11-pr-scoped-feedback-dedup-design.md`

**Branch:** create a feature branch off `main` (e.g. `pg-pr-feedback-dedup-deptree`); do NOT commit directly to `main`. Run `git` from the repo root; Go commands from `packages/pg-pr`.

**Diagnostics note:** editor/LSP diagnostics are frequently STALE in this workspace — trust `go build`/`go vet`/`go test`, not diagnostics. The full `internal/sync` suite is slow (real bd); iterate with targeted `-run`.

---

## File structure

- `packages/pg-pr/pkg/beads/deptree.go` — add `PRFeedbackFingerprints` (consumes the same `bd dep tree` output as `DepTreeUp`, but decodes via `parseBDList` to get `issue_type` + `metadata`).
- `packages/pg-pr/pkg/beads/deptree_test.go` — add a scriptable-runner unit test (no real bd) plus a small canned `Runner` stub.
- `packages/pg-pr/internal/sync/sync.go` — add the `feedbackFingerprinter` interface; rewrite `existingFeedbackFingerprints` to use it.
- `packages/pg-pr/internal/sync/feedback_dedup_test.go` — migrate the `fpCountBeads` fake to implement `PRFeedbackFingerprints`; update assertions to the one-call contract.

---

## Task 1: `beads.Client.PRFeedbackFingerprints`

**Files:**

- Modify: `packages/pg-pr/pkg/beads/deptree.go`
- Test: `packages/pg-pr/pkg/beads/deptree_test.go`

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/pkg/beads/deptree_test.go` (the package already imports `context`, `strings`, `testing`):

```go
// cannedRunner is a scriptable Runner that records calls and returns a fixed
// stdout — for unit-testing bd-JSON parsing without a real bd workspace.
type cannedRunner struct {
	calls [][]string
	out   string
}

func (r *cannedRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.out, nil
}

func TestPRFeedbackFingerprints_FromDepTree(t *testing.T) {
	// Mimics `bd dep tree <pr> --direction=up --json`: a flat array whose
	// nodes share the bd list node shape (id/issue_type/status/metadata).
	fixture := `[
	  {"id":"pr-1","issue_type":"merge-request","status":"open","metadata":{"repo":"o/r"}},
	  {"id":"cyc-1","issue_type":"task","status":"open","metadata":null},
	  {"id":"fb-1","issue_type":"feedback","status":"open","metadata":{"fingerprint":"fp-aaa","kind":"comment-thread"}},
	  {"id":"fb-2","issue_type":"feedback","status":"closed","metadata":{"fingerprint":"fp-bbb"}},
	  {"id":"fb-3","issue_type":"feedback","status":"open","metadata":{}},
	  {"id":"act-1","issue_type":"task","status":"open","metadata":null}
	]`
	r := &cannedRunner{out: fixture}
	c := NewClientWithRunner(r)

	set, err := c.PRFeedbackFingerprints(context.Background(), "pr-1")
	if err != nil {
		t.Fatalf("PRFeedbackFingerprints: %v", err)
	}

	// Only feedback nodes with a non-empty fingerprint contribute; open+closed
	// both count; the empty-metadata feedback and non-feedback nodes are ignored.
	if len(set) != 2 || !set["fp-aaa"] || !set["fp-bbb"] {
		t.Fatalf("want {fp-aaa, fp-bbb}, got %v", set)
	}
	if len(r.calls) != 1 {
		t.Fatalf("want exactly 1 bd call, got %d: %v", len(r.calls), r.calls)
	}
	if got, want := strings.Join(r.calls[0], " "), "dep tree pr-1 --direction=up --json"; got != want {
		t.Fatalf("bd args: got %q want %q", got, want)
	}
}

func TestPRFeedbackFingerprints_EmptyID(t *testing.T) {
	c := NewClientWithRunner(&cannedRunner{})
	if _, err := c.PRFeedbackFingerprints(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty pr bead id")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./pkg/beads/ -run 'TestPRFeedbackFingerprints' 2>&1 | tail -10`
Expected: compile failure — `c.PRFeedbackFingerprints undefined`.

- [ ] **Step 3: Implement the method**

In `packages/pg-pr/pkg/beads/deptree.go`, add (the file already imports `context`, `encoding/json`, `fmt`, `strings`):

```go
// PRFeedbackFingerprints returns the set of feedback fingerprints present in
// prBeadID's recursive parent-child subtree (MR -> processing-cycle -> feedback),
// from a single `bd dep tree <pr> --direction=up --json` call. This avoids the
// per-bead isChildOf scan that ListFeedback(cycleID) incurs, so the daemon's
// per-PR feedback dedup costs O(1) bd calls regardless of workspace feedback
// volume.
//
// The dep-tree node JSON is the same shape as `bd list --json`, so it is decoded
// with parseBDList; only issue_type=="feedback" nodes contribute, via the same
// feedbackFieldsFromMetadata parser ListFeedback uses. Includes feedback of all
// statuses (open + closed), matching the prior FindFeedbackByFingerprint dedup.
func (c *Client) PRFeedbackFingerprints(ctx context.Context, prBeadID string) (map[string]bool, error) {
	if strings.TrimSpace(prBeadID) == "" {
		return nil, fmt.Errorf("pr feedback fingerprints: pr bead id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "tree", prBeadID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd dep tree --direction=up: %w", err)
	}
	set := map[string]bool{}
	if strings.TrimSpace(out) == "" {
		return set, nil
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, fmt.Errorf("decode bd dep tree json: %w", err)
	}
	for _, iss := range issues {
		if iss.Type != TypeFeedback {
			continue
		}
		if fp := feedbackFieldsFromMetadata(iss.Metadata).Fingerprint; fp != "" {
			set[fp] = true
		}
	}
	return set, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-pr && go test ./pkg/beads/ -run 'TestPRFeedbackFingerprints' -v 2>&1 | grep -E '^(=== RUN|--- (PASS|FAIL)|ok|FAIL)'`
Expected: both `TestPRFeedbackFingerprints_FromDepTree` and `TestPRFeedbackFingerprints_EmptyID` PASS. Also `go build ./...` clean.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/pkg/beads/deptree.go packages/pg-pr/pkg/beads/deptree_test.go
git commit -m "feat(beads): PRFeedbackFingerprints via one scoped dep tree call"
```

NOTE: a pre-commit hook (prek/treefmt) may reformat and abort the first commit; if so, re-`git add` the same files and `git commit` again.

---

## Task 2: route the daemon dedup through `PRFeedbackFingerprints`

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` (`existingFeedbackFingerprints` ~`sync.go:1511-1529`; add the interface near the other narrow capability interfaces ~`sync.go:622`)
- Modify: `packages/pg-pr/internal/sync/feedback_dedup_test.go` (the `fpCountBeads` fake + `TestProcessFeedback_DedupIsHoistedOutOfEventLoop`)

- [ ] **Step 1: Migrate the test fake + assertions (failing test)**

In `packages/pg-pr/internal/sync/feedback_dedup_test.go`, replace the `fpCountBeads` struct and its `ListChildrenOfPR`/`ListFeedback` methods with a `PRFeedbackFingerprints`-based fake. The full struct + methods become:

```go
// fpCountBeads embeds noopBeads and records how many times PRFeedbackFingerprints
// is called (the dedup must consult the PR's feedback once per refresh, not per
// event), plus the feedback it creates. `fingerprints` is the PR's existing
// feedback fingerprint set.
type fpCountBeads struct {
	noopBeads
	fpCalls      int
	fingerprints map[string]bool
	created      []beads.CreateFeedbackInput
}

func (f *fpCountBeads) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "cycle-1", true, nil
}

func (f *fpCountBeads) PRFeedbackFingerprints(context.Context, string) (map[string]bool, error) {
	f.fpCalls++
	return f.fingerprints, nil
}

func (f *fpCountBeads) CreateFeedback(_ context.Context, in beads.CreateFeedbackInput) (string, error) {
	f.created = append(f.created, in)
	return "fb-new", nil
}
```

Then in `TestProcessFeedback_DedupIsHoistedOutOfEventLoop`, change the `bdc` construction and the call-count assertion. Replace:

```go
	dupFP := commentEvent(dup).fingerprint
	bdc := &fpCountBeads{
		feedback: []beads.Feedback{{ID: "fb1", Fields: beads.FeedbackFields{Fingerprint: dupFP}}},
	}
```

with:

```go
	dupFP := commentEvent(dup).fingerprint
	bdc := &fpCountBeads{
		fingerprints: map[string]bool{dupFP: true},
	}
```

and replace the `childrenCalls` assertion block:

```go
	// The hoist: the PR's cycles are listed exactly once, not once per event.
	if bdc.childrenCalls != 1 {
		t.Fatalf("ListChildrenOfPR should be called once (dedup hoisted out of the event loop), got %d", bdc.childrenCalls)
	}
```

with:

```go
	// The PR's existing feedback is read exactly once per refresh, not per event.
	if bdc.fpCalls != 1 {
		t.Fatalf("PRFeedbackFingerprints should be called once per refresh, got %d", bdc.fpCalls)
	}
```

(The `len(bdc.created) != 3` and the dup-fingerprint loop assertions are unchanged. The `beads` import is still used by `CreateFeedbackInput`; the `vcs`/`api` imports are unchanged.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestProcessFeedback_DedupIsHoistedOutOfEventLoop' 2>&1 | tail -10`
Expected: compile failure — `existingFeedbackFingerprints` still calls `ListChildrenOfPR`/`ListFeedback` (the fake no longer overrides them; `noopBeads` returns nil → dedup set empty → assertions would fail), and `bdc.childrenCalls` no longer exists. Either a build error or a failing assertion is an acceptable red.

- [ ] **Step 3: Add the `feedbackFingerprinter` interface**

In `packages/pg-pr/internal/sync/sync.go`, near the `depTreeReader` / `humanLabelReader` interfaces (~`sync.go:622`), add:

```go
// feedbackFingerprinter is the narrow capability for reading a PR's existing
// feedback fingerprints in one scoped bd call. The real *beads.Client satisfies
// it; test fakes that don't are treated as "no existing feedback" (empty set).
type feedbackFingerprinter interface {
	PRFeedbackFingerprints(ctx context.Context, prBeadID string) (map[string]bool, error)
}
```

- [ ] **Step 4: Rewrite `existingFeedbackFingerprints`**

In `packages/pg-pr/internal/sync/sync.go`, replace the whole function (~`sync.go:1500-1529`, including its doc comment) with:

```go
// existingFeedbackFingerprints returns the set of feedback fingerprints already
// present under prBeadID, in one scoped bd call (PRFeedbackFingerprints, a single
// `bd dep tree --direction=up`). Built once per refresh so the per-event dedup in
// processFeedback is an in-memory lookup. A bd client that doesn't implement the
// capability (test fakes) yields an empty set (dedup disabled); the production
// daemon client is *beads.Client, which does implement it.
func (e *Engine) existingFeedbackFingerprints(ctx context.Context, bdc BeadClient, prBeadID string) (map[string]bool, error) {
	fp, ok := bdc.(feedbackFingerprinter)
	if !ok {
		return map[string]bool{}, nil
	}
	return fp.PRFeedbackFingerprints(ctx, prBeadID)
}
```

(If the prior function's doc comment spans the lines just above `func (e *Engine) existingFeedbackFingerprints`, replace it too so no stale comment remains.)

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'TestProcessFeedback_DedupIsHoistedOutOfEventLoop' -v 2>&1 | grep -E '^(--- (PASS|FAIL)|ok|FAIL)' && go build ./... && go vet ./internal/sync/ ./pkg/beads/ 2>&1 | tail -5`
Expected: test PASS; build/vet clean. (Production `*beads.Client` satisfies `feedbackFingerprinter` via Task 1, so the daemon path compiles and dedups for real.)

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/feedback_dedup_test.go
git commit -m "perf(pg-pr): daemon feedback dedup uses one scoped dep tree call"
```

NOTE: re-`git add` + `git commit` again if the pre-commit hook reformats and aborts.

---

## Task 3: full verification

**Files:** none (verification only)

- [ ] **Step 1: Build, vet, and run the affected tests**

Run: `cd packages/pg-pr && go build ./... && go vet ./... 2>&1 | tail -10 && go test ./pkg/beads/ -run 'TestPRFeedbackFingerprints' -count=1 2>&1 | tail -3 && go test ./internal/sync/ -run 'TestProcessFeedback_|TestRefreshPR_|TestMaintenanceCycle_|TestRunSnapshotOwner_|TestRunWorker_|TestSeedDaemon' -count=1 2>&1 | tail -5`
Expected: build/vet clean; all listed tests PASS.

- [ ] **Step 2: Run the feedback-related real-bd tests once**

Run: `cd packages/pg-pr && go test ./internal/sync/ -run 'Feedback' -count=1 2>&1 | tail -5`
Expected: PASS (slow — real bd). If a pre-existing unrelated test (e.g. `TestSync_CreatesBeadsForObservedPRs`) hangs/times out, note it but confirm nothing this plan touched regressed.

- [ ] **Step 3: `prek` hooks on the changed files**

Run from repo root: `prek run --files packages/pg-pr/pkg/beads/deptree.go packages/pg-pr/pkg/beads/deptree_test.go packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/feedback_dedup_test.go 2>&1 | tail -12`
Expected: `gofmt`, `golangci-lint`, `treefmt` PASS.

- [ ] **Step 4: Live verification** (after a `darwin-rebuild switch` deploys the new binary)

Run:

```bash
ps aux | grep '[p]g-pr sync --daemon'
curl -s 127.0.0.1:9818/api/v1/dashboard | jq '{mine:(.mine|length),team:(.team|length),generated_at,sync_interval_seconds}'
curl -s 127.0.0.1:9818/metrics | grep -E 'pg_pr_(refresh_queue_depth|sync_pr_duration_seconds_count|snapshot_present)'
```

Expected: `refresh_queue_depth` drains toward 0; `pg_pr_sync_pr_duration_seconds_count` climbs (refreshes now complete); `snapshot_present` flips to 1; `mine`/`team` populate; `generated_at` advances.

---

## Self-review notes (planner)

- **Spec coverage:** Component 1 (`PRFeedbackFingerprints` via `parseBDList`, `issue_type=="feedback"`, `feedbackFieldsFromMetadata`, empty-id/empty-tree/bd-error handling) → Task 1. Component 2 (`feedbackFingerprinter` interface + `existingFeedbackFingerprints` rewrite + empty-set fallback) → Task 2 Steps 3-4. Test plan (beads scriptable-runner test; one-call sync test migration; the only behavior-changing test is `feedback_dedup_test.go`) → Task 1 Step 1, Task 2 Step 1. Live verification → Task 3 Step 4.
- **Type consistency:** `PRFeedbackFingerprints(ctx, prBeadID string) (map[string]bool, error)` is defined in Task 1 and consumed verbatim by the `feedbackFingerprinter` interface and `existingFeedbackFingerprints` in Task 2. The fake's method matches that signature. `parseBDList`, `bdIssue.Type`/`bdIssue.Metadata`, `TypeFeedback`, `feedbackFieldsFromMetadata` confirmed to exist with these names.
- **Placeholder scan:** none — every code/command step is concrete.
- **Build-green ordering:** Task 1 adds an (initially unused) method — Go compiles unused methods, so build stays green. Task 2 then consumes it; production `*beads.Client` satisfies the interface so the daemon path compiles.
