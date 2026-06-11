# Unified PR-Feedback Subtree Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve both of `processFeedback`'s cache-less passes (the CI-success resolver and the dedup) from the single `bd dep tree <pr> --direction=up --json` call, eliminating the remaining `O(F)` `ListFeedback(cycleID)` `isChildOf` scan per refresh.

**Architecture:** Generalize the existing `beads.Client.PRFeedbackFingerprints` (returns a fingerprint set) into `PRFeedbackInSubtree` (returns `[]beads.Feedback`). In `internal/sync`, rename the capability interface + helper and rewrite the `cache == nil` branch of `processFeedback` to fetch the PR's feedback subtree once and feed both passes from that slice in memory. The cache / full-`Sync` path is untouched.

**Tech Stack:** Go; `bd` (beads) CLI subprocess wrappers; standard `go test`.

**Spec:** `docs/superpowers/specs/2026-06-11-unified-pr-feedback-subtree-design.md`. **Bead:** `pg2-wyt8`.

**All paths below are relative to `packages/pg-pr/`. Run all `go`/`prek` commands from `packages/pg-pr/` unless noted.**

---

### Task 1: `beads.PRFeedbackInSubtree` (replace `PRFeedbackFingerprints`)

**Files:**

- Modify: `pkg/beads/deptree.go:150-186` (replace `PRFeedbackFingerprints`)
- Test: `pkg/beads/deptree_test.go:244-279` (migrate the two `TestPRFeedbackFingerprints_*` tests)

- [ ] **Step 1: Migrate the failing tests**

In `pkg/beads/deptree_test.go`, replace the two existing tests — `TestPRFeedbackFingerprints_FromDepTree` (line 244) and `TestPRFeedbackFingerprints_EmptyID` (line 274) — with these. The fixture is the existing one plus `external_id` on `fb-1`:

```go
func TestPRFeedbackInSubtree_FromDepTree(t *testing.T) {
	// Mimics `bd dep tree <pr> --direction=up --json`: a flat array whose
	// nodes share the bd list node shape (id/issue_type/status/metadata).
	fixture := `[
	  {"id":"pr-1","issue_type":"merge-request","status":"open","metadata":{"repo":"o/r"}},
	  {"id":"cyc-1","issue_type":"task","status":"open","metadata":null},
	  {"id":"fb-1","issue_type":"feedback","status":"open","metadata":{"fingerprint":"fp-aaa","kind":"comment-thread","external_id":"ext-1"}},
	  {"id":"fb-2","issue_type":"feedback","status":"closed","metadata":{"fingerprint":"fp-bbb"}},
	  {"id":"fb-3","issue_type":"feedback","status":"open","metadata":{}},
	  {"id":"act-1","issue_type":"task","status":"open","metadata":null}
	]`
	r := &cannedRunner{out: fixture}
	c := NewClientWithRunner(r)

	fbs, err := c.PRFeedbackInSubtree(context.Background(), "pr-1")
	if err != nil {
		t.Fatalf("PRFeedbackInSubtree: %v", err)
	}

	// All three feedback nodes returned (status-agnostic); non-feedback excluded.
	if len(fbs) != 3 {
		t.Fatalf("want 3 feedback nodes, got %d: %+v", len(fbs), fbs)
	}
	byID := map[string]Feedback{}
	for _, fb := range fbs {
		byID[fb.ID] = fb
	}
	if fb1, ok := byID["fb-1"]; !ok {
		t.Fatal("fb-1 missing")
	} else if fb1.Status != "open" || fb1.Fields.Fingerprint != "fp-aaa" || fb1.Fields.ExternalID != "ext-1" || fb1.Fields.Kind != "comment-thread" {
		t.Fatalf("fb-1 parsed wrong: %+v", fb1)
	}
	if fb2, ok := byID["fb-2"]; !ok || fb2.Status != "closed" || fb2.Fields.Fingerprint != "fp-bbb" {
		t.Fatalf("fb-2 parsed wrong: %+v / ok=%v", byID["fb-2"], ok)
	}
	if fb3, ok := byID["fb-3"]; !ok || fb3.Fields.Fingerprint != "" {
		t.Fatalf("fb-3 should have empty fingerprint: %+v", byID["fb-3"])
	}
	if len(r.calls) != 1 {
		t.Fatalf("want exactly 1 bd call, got %d: %v", len(r.calls), r.calls)
	}
	if got, want := strings.Join(r.calls[0], " "), "dep tree pr-1 --direction=up --json"; got != want {
		t.Fatalf("bd args: got %q want %q", got, want)
	}
}

func TestPRFeedbackInSubtree_EmptyID(t *testing.T) {
	c := NewClientWithRunner(&cannedRunner{})
	if _, err := c.PRFeedbackInSubtree(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty pr bead id")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (compile error)**

Run: `go test ./pkg/beads/ -run PRFeedbackInSubtree`
Expected: FAIL — `undefined: c.PRFeedbackInSubtree` (method not yet renamed).

- [ ] **Step 3: Replace `PRFeedbackFingerprints` with `PRFeedbackInSubtree`**

In `pkg/beads/deptree.go`, replace the entire `PRFeedbackFingerprints` function (lines 150-186, including its doc comment) with:

```go
// PRFeedbackInSubtree returns every feedback bead in prBeadID's recursive
// parent-child subtree (MR -> processing-cycle -> feedback), from a single
// `bd dep tree <pr> --direction=up --json` call. This avoids the per-bead
// isChildOf scan that ListFeedback(cycleID) incurs, so the daemon's per-PR
// feedback handling costs O(1) bd calls regardless of workspace feedback
// volume. Includes feedback of all statuses (open + closed); callers filter
// by Status as needed (the CI-success resolver wants open; the dedup wants all
// fingerprints).
//
// The dep-tree node JSON is the same shape as `bd list --json`, so it is
// decoded with parseBDList; only issue_type=="feedback" nodes contribute, via
// the same feedbackFieldsFromMetadata parser ListFeedback uses.
func (c *Client) PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]Feedback, error) {
	if strings.TrimSpace(prBeadID) == "" {
		return nil, fmt.Errorf("pr feedback subtree: pr bead id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "tree", prBeadID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd dep tree --direction=up: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, fmt.Errorf("decode bd dep tree json: %w", err)
	}
	fbs := make([]Feedback, 0, len(issues))
	for _, iss := range issues {
		if iss.Type != TypeFeedback {
			continue
		}
		fbs = append(fbs, Feedback{
			ID:     iss.ID,
			Title:  iss.Title,
			Status: iss.Status,
			Fields: feedbackFieldsFromMetadata(iss.Metadata),
		})
	}
	return fbs, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/beads/ -run PRFeedbackInSubtree -v`
Expected: PASS (both `TestPRFeedbackInSubtree_FromDepTree` and `TestPRFeedbackInSubtree_EmptyID`).

- [ ] **Step 5: Verify the whole tree still builds and tests pass**

Run: `go build ./... && go test ./pkg/beads/`
Expected: PASS. (The `internal/sync` package still compiles: its `feedbackFingerprinter` interface is self-contained and `fpCountBeads` still satisfies it. Production `*beads.Client` no longer satisfies that interface — that gap closes in Task 2; nothing is deployed between tasks.)

- [ ] **Step 6: Commit**

```bash
git add pkg/beads/deptree.go pkg/beads/deptree_test.go
git commit -m "feat(pg-pr/beads): PRFeedbackInSubtree returns []Feedback from one dep tree [pg2-wyt8]"
```

(If the pre-commit hook reformats and aborts, `git add` the same files again and re-run the commit — documented behavior.)

---

### Task 2: `sync` — rename capability + helper, rewrite `processFeedback`

**Files:**

- Modify: `internal/sync/sync.go:642-647` (interface), `internal/sync/sync.go:1384-1438` (processFeedback passes), `internal/sync/sync.go:1507-1519` (helper)
- Test: `internal/sync/feedback_dedup_test.go` (migrate `fpCountBeads` + the once-per-refresh test)

- [ ] **Step 1: Migrate the `fpCountBeads` fake and the once-per-refresh test (red)**

In `internal/sync/feedback_dedup_test.go`, replace the `fpCountBeads` type and its `PRFeedbackFingerprints` method (lines 16-30) with this (renamed method returning `[]beads.Feedback`, `fingerprints map[string]bool` → `subtree []beads.Feedback`, plus a `MarkFeedbackResolvedUpstream` recorder for Task 3):

```go
// fpCountBeads embeds noopBeads and records how many times PRFeedbackInSubtree
// is called (both processFeedback passes must share a single subtree read per
// refresh), the feedback it creates, and the feedback it resolves. `subtree` is
// the PR's existing feedback (what the single dep-tree read returns).
type fpCountBeads struct {
	noopBeads
	fpCalls   int
	subtree   []beads.Feedback
	created   []beads.CreateFeedbackInput
	closedIDs []string
}

func (f *fpCountBeads) FindOpenProcessingCycle(context.Context, string) (string, bool, error) {
	return "cycle-1", true, nil
}

func (f *fpCountBeads) PRFeedbackInSubtree(context.Context, string) ([]beads.Feedback, error) {
	f.fpCalls++
	return f.subtree, nil
}

func (f *fpCountBeads) CreateFeedback(_ context.Context, in beads.CreateFeedbackInput) (string, error) {
	f.created = append(f.created, in)
	return "fb-new", nil
}

func (f *fpCountBeads) MarkFeedbackResolvedUpstream(_ context.Context, id string) error {
	f.closedIDs = append(f.closedIDs, id)
	return nil
}
```

Then in `TestProcessFeedback_DedupIsHoistedOutOfEventLoop`, replace the `bdc` construction (lines 50-53) so the dup fingerprint is seeded as subtree feedback instead of a map, and update the call-count assertion message:

```go
	// The dup comment's fingerprint already exists under the PR's cycle.
	dupFP := commentEvent(dup).fingerprint
	bdc := &fpCountBeads{
		subtree: []beads.Feedback{
			{ID: "fb-dup", Status: "hooked", Fields: beads.FeedbackFields{Fingerprint: dupFP}},
		},
	}
```

And the assertion block at lines 64-67:

```go
	// The PR's existing feedback is read exactly once per refresh, not per event.
	if bdc.fpCalls != 1 {
		t.Fatalf("PRFeedbackInSubtree should be called once per refresh, got %d", bdc.fpCalls)
	}
```

- [ ] **Step 2: Run the test to verify it fails (compile error)**

Run: `go test ./internal/sync/ -run TestProcessFeedback_DedupIsHoistedOutOfEventLoop`
Expected: FAIL — `fpCountBeads` now exposes `PRFeedbackInSubtree` but the still-old `sync.go` never calls it (it calls the renamed-away `feedbackFingerprinter.PRFeedbackFingerprints`, which `fpCountBeads` no longer satisfies). So the assertion trips first: `PRFeedbackInSubtree should be called once per refresh, got 0`. (This is the red state driving Steps 3-4.)

- [ ] **Step 3: Rename the interface and rewrite the helper**

In `internal/sync/sync.go`, replace the `feedbackFingerprinter` interface and its doc comment (lines 642-647) with:

```go
// feedbackSubtreeReader is the narrow capability for reading every feedback
// bead in a PR's recursive parent-child subtree in one scoped bd call. The
// real *beads.Client satisfies it; test fakes that don't are treated as "no
// existing feedback" (empty slice).
type feedbackSubtreeReader interface {
	PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]beads.Feedback, error)
}
```

Then replace `existingFeedbackFingerprints` and its doc comment (lines 1507-1519) with:

```go
// prFeedbackSubtree returns every feedback bead in prBeadID's recursive
// parent-child subtree (MR -> processing-cycle -> feedback) in ONE scoped bd
// call (PRFeedbackInSubtree, a single `bd dep tree --direction=up`). Built once
// per refresh and consulted by both processFeedback passes: the CI-success
// resolver (open feedback) and the dedup (fingerprints of all statuses). A bd
// client that doesn't implement the capability (test fakes) yields an empty
// slice; the production daemon client is *beads.Client, which does implement it.
func (e *Engine) prFeedbackSubtree(ctx context.Context, bdc BeadClient, prBeadID string) ([]beads.Feedback, error) {
	r, ok := bdc.(feedbackSubtreeReader)
	if !ok {
		return nil, nil
	}
	return r.PRFeedbackInSubtree(ctx, prBeadID)
}
```

- [ ] **Step 4: Rewrite the two passes in `processFeedback` to share one subtree read**

In `internal/sync/sync.go`, the current block runs from the "First pass" comment (line 1384) through the `seen` build (line 1438). Replace that entire span with the following. The key changes: (a) a single `subtreeFeedback` fetch is inserted **before** the first pass; (b) the first-pass cache-less branch filters that slice instead of calling `bdc.ListFeedback`; (c) the `seen` build derives from the same slice instead of `e.existingFeedbackFingerprints`. The CI-success resolver loop and the cache (`cache != nil`) branches are unchanged.

```go
	// Read the PR's feedback subtree ONCE (cache-less path) — a single
	// `bd dep tree <pr> --direction=up` — and serve BOTH the first-pass
	// CI-success resolver and the second-pass dedup from it. This replaces the
	// first pass's O(workspace-feedback) ListFeedback(cycleID) isChildOf scan;
	// the cache path keeps reading from the in-memory TickCache.
	var subtreeFeedback []beads.Feedback
	if cache == nil {
		var err error
		subtreeFeedback, err = e.prFeedbackSubtree(ctx, bdc, prBeadID)
		if err != nil {
			// Can't read the PR's feedback — skip this tick rather than risk
			// duplicate beads (second-pass concern) or mis-resolving CI
			// feedback (first-pass concern). A later tick retries. (This
			// unifies the two reads' prior error handling onto the more
			// conservative skip-the-tick behavior.)
			return nil
		}
	}

	// First pass: handle CI events whose conclusion is success and close
	// any matching prior ci-failure feedback (resolved-upstream).
	if found {
		var open []beads.Feedback
		if cache != nil {
			// FeedbackUnder returns open + closed; the ci-failure
			// resolver only wants currently-open beads.
			for _, fb := range cache.FeedbackUnder(cycleID) {
				if fb.Status != "closed" {
					open = append(open, fb)
				}
			}
		} else {
			// Same open-feedback filter, served from the single subtree read
			// above instead of an O(F) ListFeedback(cycleID) scan.
			for _, fb := range subtreeFeedback {
				if fb.Status != "closed" {
					open = append(open, fb)
				}
			}
		}
		{
			closedSet := map[string]bool{}
			for _, ev := range events {
				if ev.kind != beads.FeedbackKindCIFailure || ev.ciConclusion != "success" {
					continue
				}
				for _, fb := range open {
					if closedSet[fb.ID] {
						continue
					}
					// Match by the CI run's "name" carried as external_id
					// or by fingerprint stem.
					if fb.Fields.ExternalID != "" && fb.Fields.ExternalID == ev.externalID {
						_ = bdc.MarkFeedbackResolvedUpstream(ctx, fb.ID)
						summary.FeedbackClosed++
						closedSet[fb.ID] = true
					}
				}
			}
		}
	}

	// Build the PR's existing-feedback fingerprint set for the second-pass
	// dedup. Cache-less path derives it from the single subtree read above (all
	// statuses, non-empty fingerprints) — same contents the prior
	// PRFeedbackFingerprints map produced, now with zero extra bd calls.
	var seen map[string]bool
	if cache == nil {
		seen = make(map[string]bool, len(subtreeFeedback))
		for _, fb := range subtreeFeedback {
			if fb.Fields.Fingerprint != "" {
				seen[fb.Fields.Fingerprint] = true
			}
		}
	}
```

- [ ] **Step 5: Run the migrated test + the full sync package**

Run: `go build ./... && go test ./internal/sync/ -run TestProcessFeedback_DedupIsHoistedOutOfEventLoop -v`
Expected: PASS (`fpCalls == 1`, 3 net-new created, dup skipped).

Run: `go test ./internal/sync/ -run TestRefresh -count=1`
Expected: PASS (no regression in the refresh fakes; `noopBeads`-derived clients hit the `nil, nil` fallback).

- [ ] **Step 6: Commit**

```bash
git add internal/sync/sync.go internal/sync/feedback_dedup_test.go
git commit -m "feat(pg-pr/sync): serve both processFeedback passes from one dep tree read [pg2-wyt8]"
```

---

### Task 3: New unified-path test (first-pass close + second-pass dedup, one read)

**Files:**

- Test: `internal/sync/feedback_dedup_test.go` (append a new test)

- [ ] **Step 1: Add the unified-path test**

Append to `internal/sync/feedback_dedup_test.go`:

```go
// TestProcessFeedback_UnifiedFirstAndSecondPass verifies the cache-less path
// serves BOTH passes from a single PRFeedbackInSubtree read: the first pass
// (CI-success resolver) closes a matching open ci-failure feedback drawn from
// the subtree, and the second pass dedups a duplicate comment against the same
// slice — with exactly one subtree read and no duplicate creation.
func TestProcessFeedback_UnifiedFirstAndSecondPass(t *testing.T) {
	dupComment := api.Comment{ID: "c1", Author: "bot", Body: "duplicate comment"}
	dupFP := commentEvent(dupComment).fingerprint

	// A CI-success run whose ID matches an OPEN ci-failure feedback's
	// external_id. ciRunEvent carries r.ID (NOT the run name) as externalID
	// (sync.go), so the run's ID must equal the seeded feedback's ExternalID.
	successRun := api.CIRun{ID: "run-x", Name: "build", Conclusion: "success", Provider: "gha"}

	bdc := &fpCountBeads{
		subtree: []beads.Feedback{
			// Open ci-failure feedback the success run should resolve (first pass).
			{ID: "fb-ci", Status: "hooked", Fields: beads.FeedbackFields{
				Kind: string(beads.FeedbackKindCIFailure), ExternalID: "run-x",
			}},
			// Existing feedback with the dup comment's fingerprint (second-pass dedup).
			{ID: "fb-dup", Status: "hooked", Fields: beads.FeedbackFields{Fingerprint: dupFP}},
		},
	}

	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	enriched := &vcs.EnrichedPR{
		Comments: []api.Comment{dupComment},
		CIRuns:   []api.CIRun{successRun},
	}

	summary := &Summary{}
	if err := e.processFeedback(context.Background(), bdc, nil /* cache */, enriched, "o/r",
		api.PR{Repo: "o/r", Number: 1}, "pr-bead-1", summary); err != nil {
		t.Fatalf("processFeedback: %v", err)
	}

	// Single subtree read serves both passes.
	if bdc.fpCalls != 1 {
		t.Fatalf("PRFeedbackInSubtree should be called once, got %d", bdc.fpCalls)
	}
	// First pass: the open ci-failure feedback is resolved by the success run.
	if len(bdc.closedIDs) != 1 || bdc.closedIDs[0] != "fb-ci" {
		t.Fatalf("expected fb-ci closed by CI-success resolver, got %v", bdc.closedIDs)
	}
	// Second pass: the duplicate comment is NOT recreated (it dedups against the
	// same subtree slice); the CI-success event is skipped by the create loop.
	if len(bdc.created) != 0 {
		t.Fatalf("expected no feedback created (dup deduped, CI-success skipped), got %d: %+v", len(bdc.created), bdc.created)
	}
}
```

- [ ] **Step 2: Run the new test**

Run: `go test ./internal/sync/ -run TestProcessFeedback_UnifiedFirstAndSecondPass -v`
Expected: PASS — `fpCalls == 1`, `fb-ci` closed, nothing created. (If it fails on the close half, confirm the run's `ID` field — not `Name` — is `"run-x"`.)

- [ ] **Step 3: Commit**

```bash
git add internal/sync/feedback_dedup_test.go
git commit -m "test(pg-pr/sync): unified first+second pass served by one dep tree read [pg2-wyt8]"
```

---

### Task 4: Verification

**No code changes — gates only.**

- [ ] **Step 1: Build + vet**

Run (from `packages/pg-pr/`): `go build ./... && go vet ./...`
Expected: clean, no output.

- [ ] **Step 2: Targeted feedback tests**

Run: `go test ./internal/sync/ -run 'TestProcessFeedback' -v && go test ./pkg/beads/ -run 'PRFeedbackInSubtree' -v`
Expected: all PASS.

- [ ] **Step 3: Real-bd Feedback regression (~3 min)**

Run: `go test ./pkg/beads/ -run Feedback -count=1`
Expected: PASS — exercises `CreateFeedback`/`FindFeedbackByFingerprint`/`ListFeedback`, none of which this change alters. (This subset uses a real bd workspace; the full `internal/sync` suite is NOT required — `TestSync_CreatesBeadsForObservedPRs` hangs on real bd and pre-dates this work.)

- [ ] **Step 4: Pre-commit hooks**

Run (from repo root `phillipgreenii-nix-agent-support/`): `prek run --all-files`
Expected: PASS (gofmt + golangci-lint + treefmt). If a hook reformats, `git add` the changes and re-commit.

- [ ] **Step 5: Live verification (post-deploy — performed after merge to `main`)**

After `darwin-rebuild switch` (re-bootstrap `org.nixos.pg-pr-sync` if 9818 is dead: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/org.nixos.pg-pr-sync.plist`), confirm via the cheat-sheet in the handoff doc:

- `pg_pr_refresh_queue_depth{group="team"}` drains toward 0 (was growing 3→5+).
- `pg_pr_sync_pr_duration_seconds_count` climbs steadily (was plateaued ~40).
- `/api/v1/dashboard` `generated_at` advances; `mine`/`team` stay populated.

```bash
curl -s 127.0.0.1:9818/api/v1/dashboard | jq '{mine:(.mine|length),team:(.team|length),generated_at}'
curl -s 127.0.0.1:9818/metrics | grep -E 'pg_pr_(refresh_queue_depth|sync_pr_duration_seconds_count)'
```

- [ ] **Step 6: Close the bead**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
env -u BEADS_DIR -u WORKSPACE_ROOT bd close pg2-wyt8 --reason="unified both processFeedback passes onto one dep-tree read; O(F) first-pass scan eliminated"
```

---

## Notes for the implementer

- **LSP diagnostics are stale here** — trust `go build` / `go vet` / `go test`, never the editor's "undefined method" warnings.
- **`ListFeedback` stays** — it's still used by `FindFeedbackByFingerprint`, `ListFeedbackPendingReply`, and the full-`Sync`/`TickCache` build. This change only stops the daemon hot path from calling it.
- **The `cache != nil` (full-`Sync`) branch of `processFeedback` must remain untouched** — it already serves both passes from `TickCache`.
- **Do not push** — `main` is unpushed on this repo by the user's choice; integration/push is the user's call.
