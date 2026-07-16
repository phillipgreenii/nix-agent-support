# pg-pr Conflict Urgency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a merge conflict drive urgency: for a PR I can fix (`mine`/`co-owned`) raise it (dashboard "resolve conflicts" flag + merge-request bead priority −1); for a `team` PR I only review, lower it (dampen dashboard/attention + bead priority +1).

**Architecture:** A pure `api.PR.HasConflict()` predicate feeds three consumers off the same signal: the dashboard rows (new conflict fields), the shared `NeedsAttention` predicate (dampens a conflicting team PR — dashboard + bead together), and a beadsbridge priority reconciler (±1 with the pre-adjustment priority stashed in a `pbase:<n>` bead label so it reverts cleanly). Builds on Plan A (`internal/ownership`, 3-way `Ownership`).

**Tech Stack:** Go 1.25, `internal/snapshot`, `internal/sync`, `pkg/beads` (bd CLI), `internal/beadsbridge`.

## Global Constraints

- **Depends on Plan A** (`2026-07-16-pg-pr-3way-ownership.md`): `internal/ownership`, `PRInput.Ownership`, `emitAttention(…, own)` signature, `store.PRPayload.Ownership`. Implement Plan A first.
- Conflict = `pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY"`. `UNKNOWN`/`MERGEABLE` are NOT conflicts.
- bd priority = int **0–4** (P0 highest … P4 lowest), default 2. Raise = decrement (clamp 0); lower = increment (clamp 4).
- Priority nudge is stateless/idempotent: baseline stashed in a `pbase:<n>` label on first conflicting tick; repeated conflicting ticks are no-ops; on clear, restore baseline and remove the label.
- Team-side stays a single composable signal — NO team-PR tiers/panels/ordering here (that is pg2-4dz88).
- No new attention-**bead** machinery — dampening rides the shared `NeedsAttention` predicate (pg2-ynhr FORK2a).
- Spec: `docs/superpowers/specs/2026-07-16-pg-pr-ownership-and-conflict-urgency-design.md` §5.
- Commit style: `<type>(pg-pr): <subject> (pg2-tsgkj)`.

---

### Task 1: `api.PR.HasConflict()` predicate

**Files:**

- Modify: `pkg/api/pr.go` (method on `PR`)
- Test: `pkg/api/pr_test.go`

**Interfaces:**

- Produces: `func (pr PR) HasConflict() bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestPR_HasConflict(t *testing.T) {
	tests := []struct {
		name string
		pr   PR
		want bool
	}{
		{"conflicting mergeable", PR{Mergeable: "CONFLICTING"}, true},
		{"dirty merge state", PR{MergeStateStatus: "DIRTY"}, true},
		{"mergeable clean", PR{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, false},
		{"unknown is not a conflict", PR{Mergeable: "UNKNOWN"}, false},
		{"zero value", PR{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pr.HasConflict(); got != tt.want {
				t.Errorf("HasConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/api/ -run TestPR_HasConflict`
Expected: FAIL — `HasConflict` undefined.

- [ ] **Step 3: Implement** (`pr.go`, after the `Mergeable`/`MergeStateStatus` field docs)

```go
// HasConflict reports whether GitHub signals a merge conflict on this PR, via
// either the mergeability enum (CONFLICTING) or the merge-state status (DIRTY).
// UNKNOWN (GitHub still computing) is deliberately NOT a conflict.
func (pr PR) HasConflict() bool {
	return pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY"
}
```

- [ ] **Step 4: Run**

Run: `go test ./pkg/api/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/api/pr.go pkg/api/pr_test.go
git commit -m "feat(pg-pr): add PR.HasConflict predicate (pg2-tsgkj)"
```

---

### Task 2: Dashboard conflict fields

**Files:**

- Modify: `internal/snapshot/snapshot.go` (`MineRow`: `HasConflicts`, `NeedsConflictResolution`; `TeamRow`: `HasConflicts`)
- Modify: `internal/snapshot/builder.go` (`buildMineRow`, `buildTeamRow`)
- Test: `internal/snapshot/builder_test.go`

**Interfaces:**

- Consumes: `api.PR.HasConflict()`.
- Produces: `MineRow.HasConflicts`/`.NeedsConflictResolution`, `TeamRow.HasConflicts`.

- [ ] **Step 1: Write the failing test** (append to `builder_test.go`)

```go
func TestBuild_MineConflictFlags(t *testing.T) {
	in := BuilderInput{
		Self: "me",
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 1, Author: "me", Mergeable: "CONFLICTING"},
			Ownership: ownership.Mine,
		}},
	}
	out := Build(in)
	if len(out.Mine) != 1 {
		t.Fatalf("want 1 mine row, got %d", len(out.Mine))
	}
	if !out.Mine[0].HasConflicts || !out.Mine[0].NeedsConflictResolution {
		t.Errorf("mine conflict flags = (%v,%v), want (true,true)",
			out.Mine[0].HasConflicts, out.Mine[0].NeedsConflictResolution)
	}
}

func TestBuild_TeamConflictFlag(t *testing.T) {
	in := BuilderInput{
		Self:        "me",
		TeamMembers: []string{"you"},
		PRs: []PRInput{{
			PR:        api.PR{Repo: "o/r", Number: 2, Author: "you", Draft: false, MergeStateStatus: "DIRTY"},
			Ownership: ownership.Team,
		}},
	}
	out := Build(in)
	if len(out.Team) != 1 || !out.Team[0].HasConflicts {
		t.Fatalf("want 1 team row with HasConflicts; got %d rows", len(out.Team))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/snapshot/ -run 'TestBuild_MineConflictFlags|TestBuild_TeamConflictFlag'`
Expected: FAIL — fields undefined.

- [ ] **Step 3a: Add fields** (`snapshot.go`)

`MineRow`:

```go
	// HasConflicts is true when GitHub signals a merge conflict (CONFLICTING/DIRTY).
	HasConflicts bool `json:"has_conflicts,omitempty"`
	// NeedsConflictResolution nudges me to rebase/resolve MY (or co-owned) PR —
	// a PR I can fix. Mirrors the NeedsMergeReminder idiom.
	NeedsConflictResolution bool `json:"needs_conflict_resolution,omitempty"`
```

`TeamRow`:

```go
	// HasConflicts is true when GitHub signals a merge conflict; a conflicting
	// team PR is also dampened out of NeedsAttention (not worth reviewing until
	// the author rebases).
	HasConflicts bool `json:"has_conflicts,omitempty"`
```

- [ ] **Step 3b: Wire in the builders** (`builder.go`)

`buildMineRow` return literal — add:

```go
		HasConflicts:            p.PR.HasConflict(),
		NeedsConflictResolution: p.PR.HasConflict(),
```

`buildTeamRow` return literal — add:

```go
		HasConflicts: p.PR.HasConflict(),
```

- [ ] **Step 4: Run**

Run: `go test ./internal/snapshot/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/snapshot.go internal/snapshot/builder.go internal/snapshot/builder_test.go
git commit -m "feat(pg-pr): surface conflict flags on dashboard rows (pg2-tsgkj)"
```

---

### Task 3: Dampen a conflicting team PR's attention (shared predicate)

**Files:**

- Modify: `internal/snapshot/attention.go` (`NeedsAttention` gains `hasConflict bool`)
- Modify: `internal/snapshot/builder.go` (`buildTeamRow` passes `p.PR.HasConflict()`)
- Modify: `internal/sync/prevents.go` (`emitAttention` team path passes `hasConflict`)
- Modify: `internal/sync/refresh.go` (pass `pr.HasConflict()` into `emitAttention`)
- Test: `internal/snapshot/attention_test.go`, `internal/snapshot/builder_test.go`

**Interfaces:**

- Consumes: `api.PR.HasConflict()`.
- Produces: `NeedsAttention(revs, draftReviewClosed, hasConflict) (bool, string)` — returns `(false,"")` when `hasConflict`.

- [ ] **Step 1: Write the failing test** (append to `attention_test.go`)

```go
func TestNeedsAttention_ConflictDampens(t *testing.T) {
	// A revision set that WOULD need attention (draft review ready, unapproved).
	revs := []store.Revision{{Seq: 1, HeadSHA: "h"}}
	if need, _ := NeedsAttention(revs, true, false); !need {
		t.Fatal("precondition: expected need=true without conflict")
	}
	if need, reason := NeedsAttention(revs, true, true); need || reason != "" {
		t.Errorf("with conflict: need=%v reason=%q, want false/\"\"", need, reason)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/snapshot/ -run TestNeedsAttention_ConflictDampens`
Expected: FAIL — `NeedsAttention` takes 2 args.

- [ ] **Step 3a: Add the parameter** (`attention.go`)

```go
func NeedsAttention(revs []store.Revision, draftReviewClosed bool, hasConflict bool) (need bool, reason string) {
	if len(revs) == 0 {
		return false, ""
	}
	// A conflicting team PR is not worth reviewing until the author rebases —
	// dampen it out of the attention signal entirely (dashboard + bead both, via
	// this shared predicate). (pg2-tsgkj)
	if hasConflict {
		return false, ""
	}
	// ... rest unchanged ...
}
```

- [ ] **Step 3b: Update `buildTeamRow`** (`builder.go`)

```go
	need, reason := NeedsAttention(p.Revisions, p.DraftReviewClosed, p.PR.HasConflict())
```

- [ ] **Step 3c: Update `emitAttention` team path** (`prevents.go`) — it now needs the conflict signal. Add a `hasConflict bool` parameter and pass it to `NeedsAttention`:

```go
func (e *Engine) emitAttention(ctx context.Context, bdc BeadClient, repo string, number int, prID int64, own ownership.Ownership, hasConflict bool) error {
	// ... co-owned/non-team short-circuit (Plan A) unchanged ...
	// team path:
	need, reason := snapshot.NeedsAttention(revs, draftReviewClosed, hasConflict)
	// ...
}
```

- [ ] **Step 3d: Update the `refreshPR` caller** (`refresh.go`, the attention block from Plan A Task 7)

```go
			if aerr := e.emitAttention(ctx, bdc, rcfg.Remote, pr.Number, stored.ID, own, pr.HasConflict()); aerr != nil {
```

- [ ] **Step 4: Build + run**

Run: `go build ./... && go test ./internal/snapshot/ ./internal/sync/ -run 'TestNeedsAttention|TestBuild|TestRefresh|TestAttention'`
Expected: PASS. (Update any other `NeedsAttention` / `emitAttention` call sites the compiler flags.)

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/attention.go internal/snapshot/builder.go internal/sync/prevents.go internal/sync/refresh.go internal/snapshot/attention_test.go
git commit -m "feat(pg-pr): dampen conflicting team PR attention via shared predicate (pg2-tsgkj)"
```

---

### Task 4: bd priority read/write surface

**Files:**

- Modify: `pkg/beads/mergerequest.go` (`bdIssue.Priority`; `MergeRequest.Priority`+`.Labels`; `bdIssueToMergeRequest`; new `SetPriority`)
- Test: `pkg/beads/mergerequest_test.go`

**Interfaces:**

- Produces: `MergeRequest.Priority int`, `MergeRequest.Labels []string`; `func (c *Client) SetPriority(ctx context.Context, id string, p int) error`.

- [ ] **Step 1: Write the failing test** (append to `mergerequest_test.go`, using the fake `Runner` pattern from existing tests)

```go
func TestSetPriority(t *testing.T) {
	fr := &fakeRunner{} // existing test double; records args, returns ""
	c := NewClientWithRunner(fr)
	if err := c.SetPriority(context.Background(), "bd-1", 1); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	fr.assertCalled(t, "update", "bd-1", "-p", "1") // adapt to fakeRunner's API
}

func TestBdIssueToMergeRequest_ParsesPriorityAndLabels(t *testing.T) {
	iss := bdIssue{ID: "bd-2", Priority: 3, Labels: []string{"co-owned", "pbase:2"},
		Metadata: map[string]any{"repo": "o/r", "pr_number": float64(5)}}
	mr := bdIssueToMergeRequest(iss)
	if mr.Priority != 3 {
		t.Errorf("Priority = %d, want 3", mr.Priority)
	}
	if len(mr.Labels) != 2 {
		t.Errorf("Labels = %v, want 2", mr.Labels)
	}
}
```

(Adapt `fakeRunner`/`assertCalled` to the existing test double in this package.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/beads/ -run 'TestSetPriority|TestBdIssueToMergeRequest_ParsesPriorityAndLabels'`
Expected: FAIL — `Priority`/`Labels`/`SetPriority` undefined.

- [ ] **Step 3a: Add fields** — `bdIssue` already has `Labels`; add `Priority int` with tag `json:"priority"`. Add to `MergeRequest`:

```go
	Priority int      `json:"-"`
	Labels   []string `json:"-"`
```

- [ ] **Step 3b: Populate in `bdIssueToMergeRequest`**

```go
	return MergeRequest{
		ID: iss.ID, Title: iss.Title, Status: iss.Status, Type: iss.Type,
		Fields: f, Priority: iss.Priority, Labels: iss.Labels,
	}
```

- [ ] **Step 3c: Add `SetPriority`**

```go
// SetPriority sets the bead's priority (0=highest … 4=lowest). Used by the
// conflict-urgency reconciler (pg2-tsgkj).
func (c *Client) SetPriority(ctx context.Context, id string, p int) error {
	if id == "" {
		return errors.New("merge-request: id required")
	}
	if p < 0 {
		p = 0
	}
	if p > 4 {
		p = 4
	}
	_, err := c.Runner.Run(ctx, "update", id, "-p", strconv.Itoa(p))
	return err
}
```

Add `"strconv"` to imports.

- [ ] **Step 4: Run**

Run: `go test ./pkg/beads/ -run 'TestSetPriority|TestBdIssue'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/beads/mergerequest.go pkg/beads/mergerequest_test.go
git commit -m "feat(pg-pr): add bead priority read/write to beads client (pg2-tsgkj)"
```

---

### Task 5: beadsbridge conflict→priority reconciliation (±1, baseline in `pbase:` label)

**Files:**

- Modify: `internal/store/event.go` (`PRPayload` gains `HasConflict`)
- Modify: `internal/sync/prevents.go` (`prPayload` sets `HasConflict: pr.HasConflict()`)
- Modify: `pkg/beads/mergerequest.go` (`GetMergeRequest` already returns Priority+Labels via Task 4; add label add/remove helpers if not reusing `--add-label`/`--remove-label` directly)
- Modify: `internal/beadsbridge/bridge.go` (`BeadClient` + `reconcilePriority` on pr.opened/updated)
- Test: `internal/beadsbridge/bridge_test.go`

**Interfaces:**

- Consumes: `store.PRPayload.Ownership`, `store.PRPayload.HasConflict`, `MergeRequest.Priority`/`.Labels`, `Client.SetPriority`.
- Produces: MR bead priority nudged ±1 with baseline stashed in `pbase:<n>` label; reverts on clear.

- [ ] **Step 1: Add `HasConflict` to the payload** (`event.go` `PRPayload`)

```go
	HasConflict bool `json:"has_conflict,omitempty"`
```

and set it in `prPayload` (`prevents.go`):

```go
		Author: pr.Author, URL: pr.URL, Draft: pr.Draft, HasConflict: pr.HasConflict(),
```

- [ ] **Step 2: Add bead-client capabilities to the interface** (`bridge.go` `BeadClient`)

```go
	GetMergeRequest(ctx context.Context, id string) (*beads.MergeRequest, error)
	SetPriority(ctx context.Context, id string, p int) error
	AddLabel(ctx context.Context, id, label string) error
	RemoveLabel(ctx context.Context, id, label string) error
```

Add `AddLabel`/`RemoveLabel` to `pkg/beads` (thin wrappers over `bd update <id> --add-label`/`--remove-label`) if not already present:

```go
func (c *Client) AddLabel(ctx context.Context, id, label string) error {
	_, err := c.Runner.Run(ctx, "update", id, "--add-label", label)
	return err
}
func (c *Client) RemoveLabel(ctx context.Context, id, label string) error {
	_, err := c.Runner.Run(ctx, "update", id, "--remove-label", label)
	return err
}
```

- [ ] **Step 3: Write the failing test** (`bridge_test.go`, extend the fake `BeadClient` to serve `GetMergeRequest` and record `SetPriority`/labels)

```go
func TestHandle_ConflictRaisesMinePriorityAndStashesBaseline(t *testing.T) {
	// Fake: GetMergeRequest(mr-1) -> {Priority:2, Labels:nil}. Dispatch
	// EventPRUpdated {Ownership:"mine", HasConflict:true}. Assert:
	//   SetPriority(mr-1, 1) called AND AddLabel(mr-1, "pbase:2") called.
}
func TestHandle_ConflictClearedRestoresBaseline(t *testing.T) {
	// Fake: GetMergeRequest(mr-1) -> {Priority:1, Labels:["pbase:2"]}. Dispatch
	// EventPRUpdated {Ownership:"mine", HasConflict:false}. Assert:
	//   SetPriority(mr-1, 2) AND RemoveLabel(mr-1, "pbase:2").
}
func TestHandle_ConflictIdempotentNoDoubleNudge(t *testing.T) {
	// Fake: GetMergeRequest(mr-1) -> {Priority:1, Labels:["pbase:2"]}. Dispatch
	// EventPRUpdated {Ownership:"mine", HasConflict:true}. Assert NO SetPriority
	// (baseline already stashed; already adjusted).
}
func TestHandle_TeamConflictLowersPriority(t *testing.T) {
	// GetMergeRequest -> {Priority:2}. {Ownership:"team", HasConflict:true} ->
	// SetPriority(mr-1, 3) + AddLabel("pbase:2").
}
```

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./internal/beadsbridge/ -run TestHandle_Conflict`
Expected: FAIL — reconciliation not implemented; fake methods missing.

- [ ] **Step 5: Implement `reconcilePriority` + call it** (`bridge.go`)

Call site (pr.opened/updated case, after `SetMergeRequestCoOwned`, before the draft-review block; skip when `alreadyClosed`):

```go
			if err := h.reconcilePriority(ctx, mrID, p.Ownership, p.HasConflict); err != nil {
				return err
			}
```

Implementation:

```go
const pbaseLabelPrefix = "pbase:"

// reconcilePriority nudges the merge-request bead's priority on conflict and
// reverts it when the conflict clears, statelessly. The pre-adjustment priority
// is stashed in a `pbase:<n>` label so a repeated conflicting tick is a no-op
// and a clear restores the exact baseline. mine/co-owned raise (−1, clamp 0);
// team lowers (+1, clamp 4). (pg2-tsgkj)
func (h *Handler) reconcilePriority(ctx context.Context, mrID, ownershipStr string, hasConflict bool) error {
	mr, err := h.client.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	if mr == nil {
		return nil
	}
	baseline, hasBaseline := parsePbase(mr.Labels)

	switch {
	case hasConflict && !hasBaseline:
		// First conflicting tick: stash current priority, then nudge.
		desired := nudged(mr.Priority, ownershipStr)
		if desired == mr.Priority {
			// Already clamped at the boundary: still stash so a later clear is a no-op-safe restore.
		}
		if err := h.client.AddLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(mr.Priority)); err != nil {
			return err
		}
		if desired != mr.Priority {
			return h.client.SetPriority(ctx, mrID, desired)
		}
		return nil
	case hasConflict && hasBaseline:
		return nil // already adjusted this conflict episode — idempotent no-op
	case !hasConflict && hasBaseline:
		// Conflict cleared: restore baseline, drop the marker.
		if mr.Priority != baseline {
			if err := h.client.SetPriority(ctx, mrID, baseline); err != nil {
				return err
			}
		}
		return h.client.RemoveLabel(ctx, mrID, pbaseLabelPrefix+strconv.Itoa(baseline))
	default:
		return nil // no conflict, no baseline — nothing to do
	}
}

// nudged returns the conflict-adjusted priority: mine/co-owned raise (toward 0),
// team lower (toward 4). Clamped to [0,4].
func nudged(p int, ownershipStr string) int {
	if ownership.Ownership(ownershipStr).ActsAsMine() {
		if p > 0 {
			return p - 1
		}
		return 0
	}
	if p < 4 {
		return p + 1
	}
	return 4
}

// parsePbase extracts the stashed baseline priority from a `pbase:<n>` label.
func parsePbase(labels []string) (int, bool) {
	for _, l := range labels {
		if strings.HasPrefix(l, pbaseLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, pbaseLabelPrefix)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
```

Add imports: `strconv`, `strings`, and `internal/ownership`. Add the new methods to every `BeadClient` test fake (no-op / recorded).

- [ ] **Step 6: Build + run**

Run: `go build ./... && go test ./internal/beadsbridge/ ./pkg/beads/ ./internal/sync/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/event.go internal/sync/prevents.go pkg/beads/mergerequest.go internal/beadsbridge/bridge.go internal/beadsbridge/bridge_test.go
git commit -m "feat(pg-pr): nudge merge-request bead priority on conflict, revert on clear (pg2-tsgkj)"
```

---

### Task 6: Package gate + acceptance spot-check

- [ ] **Step 1: Full package suite**

Run: `go test ./...` (from `packages/pg-pr`)
Expected: PASS.

- [ ] **Step 2: AC spot-check** against spec §5.4:
  - AC-B1 → Task 1. AC-B2 → Tasks 2, 5 (raise+clamp). AC-B3 → Tasks 2, 3, 5 (dampen + lower+clamp). AC-B4 → Task 5 (restore + remove label). AC-B5 → Task 5 (idempotent). AC-B6 → all task tests.

- [ ] **Step 3: Repo completion gates** (shared with Plan A; run once at repo root after both plans):

```bash
nix flake check
pre-commit run --all-files   # or prek run --all-files
```

## Self-Review Notes

- Spec coverage: §5.1→T1, §5.2→T2/T3, §5.3→T4/T5, §5.4→T6. Covered.
- Baseline stored as a `pbase:<n>` label (not metadata) — sidesteps bd metadata zero-omission/removal; P0 baseline survives ("pbase:0" is a real label). Clamp handled in `nudged`; a boundary PR still stashes a baseline so a later clear restores exactly.
- Type consistency: `PR.HasConflict()`, `NeedsAttention(revs, closed, hasConflict)`, `emitAttention(…, own, hasConflict)`, `MergeRequest.Priority/.Labels`, `SetPriority`, `AddLabel`/`RemoveLabel`, `reconcilePriority(ctx, mrID, ownershipStr, hasConflict)` consistent across tasks and with Plan A's `emitAttention(…, own)`.
- Team-side scope: only dampening + −/+1 nudge; no tiers/panels (pg2-4dz88 owns those).
