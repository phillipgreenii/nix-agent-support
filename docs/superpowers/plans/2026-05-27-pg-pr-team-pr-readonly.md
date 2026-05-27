# pg-pr Team-PR Read-Only Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `pg-pr sync` from modifying team-mate PRs. The engine currently undrafts any draft+CI-green PR regardless of author (which triggers GitHub-side label-on-ready and reviewer-request cascades) and will post live replies to team-mate threads if a `ReplyDraft` is staged. After this plan: sync only writes upstream for PRs whose `Author == cfg.SelfLogin`; any uncertainty defaults to read-only.

**Architecture:** Three guard sites in `internal/sync/sync.go`. (1) `Sync()` partitions the observed pool into `mine`/`team` by author at the enumerate boundary — only `mine` runs `maybePromoteDraft`. (2) `SyncPR()` adds an inline ownership check before its `maybePromoteDraft` call. (3) `processReplyDrafts()` walks to the parent merge-request and skips any bead whose parent PR isn't self-authored, emitting a warning. A single helper `isSelfAuthoredLogin` centralizes the predicate and defaults to `false` on any uncertainty.

**Tech Stack:** Go 1.22+, `internal/sync` package, `testing` stdlib. Tests use the existing `fakeVCS` and real `bd` workspace pattern.

**Spec:** `docs/superpowers/specs/2026-05-27-pg-pr-team-pr-readonly-design.md`

**Tracking bead:** `beads_pg2-r3t0`

**Working directory for all commands:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

---

## File Structure

**Modified files:**

- `packages/pg-pr/internal/sync/sync.go`
  - Add `Warnings []SummaryError` field on `Summary` struct.
  - Add unexported method `func (e *Engine) isSelfAuthoredLogin(author string) bool`.
  - Refactor `Sync()` loop to partition the observed pool into `mine` and `team`; run upstream writes only over `mine`.
  - Add ownership guard before `maybePromoteDraft` in `SyncPR()`.
  - Add per-bead ownership guard inside `processReplyDrafts()` that emits a warning.

- `packages/pg-pr/internal/sync/sync_test.go`
  - Extend `fakeVCS` to satisfy `DraftToggler` (record `SetDraft` calls).
  - Add `fakeCICD` type satisfying `CICDProvider`.
  - Add `teammatePR(...)` helper (variant of `samplePR` with non-self author).
  - Add `cfgWithCICD(...)` helper (variant of `minimalCfg` with `CICD` providers wired).
  - Add tests: `TestIsSelfAuthoredLogin`, `TestSyncPR_SkipsDraftPromoteForTeammate`, `TestSync_OnlyPromotesDraftForSelfAuthoredPRs`, `TestSummary_WarningsJSONRoundTrip`, `TestSync_SkipsAndWarnsOnTeammateReplyDraft`, `TestSync_TreatsEmptySelfLoginAsTeammate`.

No new files.

---

## Conventions

- All paths relative to `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` unless noted.
- All test commands run from `packages/pg-pr/` unless noted.
- Commits use Conventional Commits, scope `pg-pr`, type matches the change (`feat`, `fix`, `test`, `refactor`). Reference `beads_pg2-r3t0` in commit bodies when relevant.
- Run `go test ./internal/sync/...` after each task to confirm no regression.
- Pre-commit hooks run automatically on `git commit`. If `treefmt` modifies files, re-stage and retry the same commit.

---

## Task 1: Add `isSelfAuthoredLogin` helper

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` — add helper method
- Test: `packages/pg-pr/internal/sync/sync_test.go` — add `TestIsSelfAuthoredLogin`

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestIsSelfAuthoredLogin(t *testing.T) {
	cases := []struct {
		name   string
		self   string
		author string
		want   bool
	}{
		{"matches", "phillipg", "phillipg", true},
		{"different login", "phillipg", "coworker", false},
		{"empty author", "phillipg", "", false},
		{"empty self", "", "phillipg", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{deps: Deps{Cfg: &config.Config{SelfLogin: tc.self}}}
			got := e.isSelfAuthoredLogin(tc.author)
			if got != tc.want {
				t.Fatalf("isSelfAuthoredLogin(%q) with self=%q: got %v want %v",
					tc.author, tc.self, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run from `packages/pg-pr/`:

```bash
go test ./internal/sync/... -run TestIsSelfAuthoredLogin -v
```

Expected: build error `e.isSelfAuthoredLogin undefined`.

- [ ] **Step 3: Implement the helper**

Add this method to `packages/pg-pr/internal/sync/sync.go`. Place it directly after `allTeamMembers` (around line 580) so related self/team helpers cluster together:

```go
// isSelfAuthoredLogin reports whether the given GitHub login matches the
// configured SelfLogin. Empty self or empty author → false (assume
// team-mate; do not modify upstream). Centralizes the ownership
// predicate used by sync's upstream-write guards.
func (e *Engine) isSelfAuthoredLogin(author string) bool {
	self := e.deps.Cfg.SelfLogin
	return self != "" && author != "" && author == self
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/sync/... -run TestIsSelfAuthoredLogin -v
```

Expected: PASS for all five sub-cases.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
feat(pg-pr): add isSelfAuthoredLogin ownership predicate

beads_pg2-r3t0. Centralizes the self-vs-team predicate used by
upstream-write guards. Defaults to false on any uncertainty (empty
author or empty SelfLogin).
EOF
)"
```

---

## Task 2: Test infrastructure — fakeVCS SetDraft recording and fakeCICD

This task adds test plumbing only — no production code change yet. It's a prerequisite for Tasks 3 and 4. Committed separately so the plumbing is reviewable on its own.

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync_test.go` — extend `fakeVCS`, add `fakeCICD`, helpers.

- [ ] **Step 1: Add the test plumbing**

In `packages/pg-pr/internal/sync/sync_test.go`, add the following:

**(a)** Extend the `fakeVCS` struct (currently around line 26). Add these fields to the struct literal:

```go
// SetDraft recording. The engine type-asserts to DraftToggler; with
// setDraftCalls non-nil, the assertion succeeds via the method below.
setDraftCalls []setDraftCall
setDraftErr   error
```

And add the call shape and method below the existing `replyCall` type:

```go
type setDraftCall struct {
	Repo   string
	Number int
	Draft  bool
}

func (f *fakeVCS) SetDraft(_ context.Context, repo string, n int, draft bool) error {
	f.setDraftCalls = append(f.setDraftCalls, setDraftCall{Repo: repo, Number: n, Draft: draft})
	return f.setDraftErr
}
```

**(b)** Add a `fakeCICD` type. Place it after the `fakeVCS` methods, before the bd workspace helpers:

```go
// fakeCICD is a minimal CICDProvider for tests. runs is keyed by
// "repo#prNumber"; missing keys return an empty slice (treated by
// allRunsSuccessful as "no runs" → not promotable).
type fakeCICD struct {
	runs map[string][]api.CIRun
}

func newFakeCICD() *fakeCICD {
	return &fakeCICD{runs: map[string][]api.CIRun{}}
}

func (c *fakeCICD) ListRuns(_ context.Context, repo string, n int) ([]api.CIRun, error) {
	return c.runs[keyOf(repo, n)], nil
}

// successRun returns a single completed+successful CI run, the shape
// allRunsSuccessful requires for draft promotion to fire.
func successRun() api.CIRun {
	return api.CIRun{Status: "completed", Conclusion: "success"}
}
```

**(c)** Add a `teammatePR` helper and a `cfgWithCICD` helper. Place these near `samplePR` and `minimalCfg`:

```go
// teammatePR returns a draft PR authored by someone other than the
// configured SelfLogin ("phillipg"). Used by tests asserting the
// ownership guards.
func teammatePR(n int, repo, branch string) api.PR {
	pr := samplePR(n, repo, branch)
	pr.Author = "coworker"
	pr.Draft = true
	return pr
}

// selfDraftPR returns a self-authored draft PR.
func selfDraftPR(n int, repo, branch string) api.PR {
	pr := samplePR(n, repo, branch)
	pr.Draft = true
	return pr
}

// cfgWithCICD returns a config that wires a single CICD provider name
// ("ci") on the foo/bar repo so maybePromoteDraft can fire in tests.
func cfgWithCICD() *config.Config {
	return &config.Config{
		SelfLogin:    "phillipg",
		WorktreeRoot: "/tmp/wr",
		Repos: []config.RepoConfig{
			{Remote: "foo/bar", VCS: "github", CICD: []string{"ci"}, TeamMembers: []string{"coworker"}},
		},
	}
}
```

- [ ] **Step 2: Verify the test file still compiles**

```bash
cd packages/pg-pr && go build ./internal/sync/...
```

Expected: builds cleanly (no test bodies use the new plumbing yet — that comes in Tasks 3 and 4).

- [ ] **Step 3: Verify existing tests still pass**

```bash
go test ./internal/sync/... -v 2>&1 | tail -20
```

Expected: all existing tests pass. The new fields default to nil/zero and don't affect existing behavior.

- [ ] **Step 4: Commit**

```bash
git add packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
test(pg-pr): extend fakeVCS with SetDraft recording, add fakeCICD

beads_pg2-r3t0. Test plumbing prerequisite for ownership-guard tests in
SyncPR and Sync. fakeVCS now satisfies DraftToggler via setDraftCalls;
fakeCICD provides a minimal CICDProvider. teammatePR / cfgWithCICD
helpers cover the maybePromoteDraft preconditions.
EOF
)"
```

---

## Task 3: Guard `SyncPR` against team-mate draft promotion

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` — add ownership check in `SyncPR()` around line 692.
- Test: `packages/pg-pr/internal/sync/sync_test.go` — add `TestSyncPR_SkipsDraftPromoteForTeammate`.

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestSyncPR_SkipsDraftPromoteForTeammate(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Team-mate's PR — draft, CI green. Without the guard, sync would
	// SetDraft(false).
	pr := teammatePR(99, "foo/bar", "feat/coworker")
	vcs.views[keyOf("foo/bar", 99)] = pr
	ci.runs[keyOf("foo/bar", 99)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.SyncPR(ctx, "foo/bar", 99)
	if err != nil {
		t.Fatalf("SyncPR: %v (errors=%+v)", err, sum.Errors)
	}
	if len(vcs.setDraftCalls) != 0 {
		t.Fatalf("expected no SetDraft calls for team-mate PR; got %+v", vcs.setDraftCalls)
	}
	// Bead is still upserted with Author=coworker.
	if sum.BeadsCreated+sum.BeadsUpdated != 1 {
		t.Fatalf("expected 1 bead upserted; got created=%d updated=%d",
			sum.BeadsCreated, sum.BeadsUpdated)
	}
	if sum.DraftPromoted != 0 {
		t.Fatalf("DraftPromoted: got %d want 0", sum.DraftPromoted)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/pg-pr && go test ./internal/sync/... -run TestSyncPR_SkipsDraftPromoteForTeammate -v
```

Expected: FAIL — `setDraftCalls` has 1 entry, because `maybePromoteDraft` runs unconditionally today.

- [ ] **Step 3: Add the guard in `SyncPR`**

In `packages/pg-pr/internal/sync/sync.go`, find the `maybePromoteDraft` call site in `SyncPR()` (around line 692). The current code:

```go
		if err := e.maybePromoteDraft(ctx, bdc, repo, *pr, prBeadID, summary); err != nil {
```

Replace it with:

```go
		if e.isSelfAuthoredLogin(pr.Author) {
			if err := e.maybePromoteDraft(ctx, bdc, repo, *pr, prBeadID, summary); err != nil {
```

Then close the new `if` block after the existing block. The full replacement (showing context — keep the existing error-handling block intact, just nest it):

```go
		if e.isSelfAuthoredLogin(pr.Author) {
			if err := e.maybePromoteDraft(ctx, bdc, repo, *pr, prBeadID, summary); err != nil {
				summary.Errors = append(summary.Errors, SummaryError{
					Repo: repo, Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
				})
			}
		}
```

(Read lines around 692 first to capture the exact surrounding error-handling shape; preserve it inside the new `if` block.)

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/sync/... -run TestSyncPR_SkipsDraftPromoteForTeammate -v
```

Expected: PASS.

- [ ] **Step 5: Run all sync tests to confirm no regression**

```bash
go test ./internal/sync/... -v 2>&1 | tail -30
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
fix(pg-pr): guard SyncPR against draft-promoting team-mate PRs

beads_pg2-r3t0. Single-PR sync path (used by pg-pr sync --pr and
/check-my-pr) now consults isSelfAuthoredLogin before calling
maybePromoteDraft. Team-mate PRs are still bead-upserted; only the
upstream SetDraft(false) call is suppressed.
EOF
)"
```

---

## Task 4: Partition the `Sync()` loop into mine / team

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` — split observed pool in `Sync()` around lines 316–385.
- Test: `packages/pg-pr/internal/sync/sync_test.go` — add `TestSync_OnlyPromotesDraftForSelfAuthoredPRs`.

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestSync_OnlyPromotesDraftForSelfAuthoredPRs(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Mixed pool: one self draft+green, one team draft+green.
	selfPR := selfDraftPR(10, "foo/bar", "feat/mine")
	teamPR := teammatePR(20, "foo/bar", "feat/theirs")
	vcs.my["foo/bar"] = []api.PR{selfPR}
	vcs.team["foo/bar"] = []api.PR{teamPR}
	ci.runs[keyOf("foo/bar", 10)] = []api.CIRun{successRun()}
	ci.runs[keyOf("foo/bar", 20)] = []api.CIRun{successRun()}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfgWithCICD(),
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// Exactly one SetDraft, and it must be for the self PR (#10).
	if len(vcs.setDraftCalls) != 1 {
		t.Fatalf("expected 1 SetDraft call; got %d: %+v",
			len(vcs.setDraftCalls), vcs.setDraftCalls)
	}
	got := vcs.setDraftCalls[0]
	if got.Number != 10 || got.Draft != false {
		t.Fatalf("expected SetDraft(repo, 10, false); got %+v", got)
	}

	// Both beads must be upserted.
	if sum.BeadsCreated != 2 {
		t.Fatalf("BeadsCreated: got %d want 2", sum.BeadsCreated)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/pg-pr && go test ./internal/sync/... -run TestSync_OnlyPromotesDraftForSelfAuthoredPRs -v
```

Expected: FAIL — 2 SetDraft calls instead of 1 (current code promotes both).

- [ ] **Step 3: Partition the observed pool, then guard upstream writes by partition**

In `packages/pg-pr/internal/sync/sync.go`, the `Sync()` method builds a single `observed map[prKey]api.PR` and then iterates it. Per the spec, partition this into `mine` vs `team` upfront, then run upstream writes only over the `mine` subset.

**(a)** Locate the section right before the existing `for key, pr := range observed` loop (around line 316). Add the partition step:

```go
	// Partition by ownership BEFORE running per-PR phases. Local-bead
	// phases (EnsureMergeRequest, processFeedback) run for both subsets;
	// upstream-write phases (maybePromoteDraft) only run for mine.
	// Empty Author / empty SelfLogin → treated as team (do not modify
	// upstream). Future write-side phases must consciously consult
	// mineSet — defense against the original bug class.
	mineSet := make(map[prKey]bool, len(observed))
	for key, pr := range observed {
		if e.isSelfAuthoredLogin(pr.Author) {
			mineSet[key] = true
		}
	}
```

**(b)** Inside the loop body (around line 376), wrap the `maybePromoteDraft` call in a `mineSet` check. Find this:

```go
				if err := e.maybePromoteDraft(prCtx, bdc, key.Repo, pr, prBeadID, summary); err != nil {
					telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
					recordSpanErr(prSpan, err)
					summary.Errors = append(summary.Errors, SummaryError{
						Repo:    key.Repo,
						Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
					})
				}
```

Replace it with:

```go
				// Upstream-write phase: only for self-authored PRs.
				// See partition above.
				if mineSet[key] {
					if err := e.maybePromoteDraft(prCtx, bdc, key.Repo, pr, prBeadID, summary); err != nil {
						telemetry.SyncErrorsTotal.WithLabelValues(key.Repo).Inc()
						recordSpanErr(prSpan, err)
						summary.Errors = append(summary.Errors, SummaryError{
							Repo:    key.Repo,
							Message: fmt.Sprintf("PR #%d draft-promote: %v", pr.Number, err),
						})
					}
				}
```

The partition computed in (a) is the structural separation the spec calls for: ownership is decided once, at the boundary; the `mineSet` lookup at each write site reads as "is this PR mine?" — a natural read that future write phases will follow by example. Adding a new write phase without consulting `mineSet` will visually stand out next to the existing one.

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/sync/... -run TestSync_OnlyPromotesDraftForSelfAuthoredPRs -v
```

Expected: PASS.

- [ ] **Step 5: Run all sync tests to confirm no regression**

```bash
go test ./internal/sync/... -v 2>&1 | tail -30
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
fix(pg-pr): only auto-promote draft for self-authored PRs in Sync loop

beads_pg2-r3t0. The full Sync() loop now consults isSelfAuthoredLogin
before calling maybePromoteDraft. Team-mate PRs are still bead-upserted
and feedback-ingested; only the upstream SetDraft(false) call is
suppressed. Removes the GitHub-side cascade (label-on-ready,
CODEOWNERS reviewer requests) that was firing on team-mate PRs.
EOF
)"
```

---

## Task 5: Add `Summary.Warnings` field

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` — add `Warnings` field on `Summary`.
- Test: `packages/pg-pr/internal/sync/sync_test.go` — add `TestSummary_WarningsJSONRoundTrip`.

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestSummary_WarningsJSONRoundTrip(t *testing.T) {
	s := Summary{
		Warnings: []SummaryError{
			{Repo: "foo/bar", Message: "example warning"},
		},
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"warnings"`) {
		t.Fatalf("expected warnings key in JSON; got %s", raw)
	}

	// Round-trip back.
	var got Summary
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Message != "example warning" {
		t.Fatalf("round-trip lost warnings: %+v", got.Warnings)
	}

	// Empty warnings should omit the key (omitempty semantics).
	empty, err := json.Marshal(Summary{})
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if strings.Contains(string(empty), `"warnings"`) {
		t.Fatalf("empty Warnings should be omitted; got %s", empty)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/pg-pr && go test ./internal/sync/... -run TestSummary_WarningsJSONRoundTrip -v
```

Expected: build error `unknown field Warnings in struct literal of type Summary`.

- [ ] **Step 3: Add the field**

In `packages/pg-pr/internal/sync/sync.go`, find the `Summary` struct (line 208). Add a new field directly after the existing `Errors` field:

```go
	Errors          []SummaryError `json:"errors,omitempty"`
	Warnings        []SummaryError `json:"warnings,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/sync/... -run TestSummary_WarningsJSONRoundTrip -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
feat(pg-pr): add Summary.Warnings channel separate from Errors

beads_pg2-r3t0. Warnings carry advisory diagnostics about local state
that shouldn't exist (e.g. ReplyDraft on a team-mate's PR feedback
bead). Unlike Errors, Warnings do not count toward
telemetry.SyncErrorsTotal or repoStates[].LastError.
EOF
)"
```

---

## Task 6: Guard `processReplyDrafts` against team-mate posts, emit warning

**Files:**

- Modify: `packages/pg-pr/internal/sync/sync.go` — add ownership check inside `processReplyDrafts` per-bead loop (around line 1161, just after the cross-repo check).
- Test: `packages/pg-pr/internal/sync/sync_test.go` — add `TestSync_SkipsAndWarnsOnTeammateReplyDraft`.

- [ ] **Step 1: Write the failing test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestSync_SkipsAndWarnsOnTeammateReplyDraft(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	// Both PRs need to be in the enumerate set so the repo is healthy.
	vcs.my["foo/bar"] = []api.PR{samplePR(42, "foo/bar", "feat/mine")}
	vcs.team["foo/bar"] = []api.PR{teammatePR(99, "foo/bar", "feat/theirs")}
	vcs.replyResp = &api.Comment{ID: "C_SELF_RESP", Author: "phillipg"}

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      minimalCfg(),
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Seed two MR beads with explicit Author fields and a queued reply
	// on each. EnsureMergeRequest is idempotent on URL — by the time
	// Sync runs, it'll find these existing beads and update the upstream
	// fields onto them.
	selfMRID, _, err := bd.EnsureMergeRequest(ctx,
		"https://github.com/foo/bar/pull/42",
		beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 42, Author: "phillipg"})
	if err != nil {
		t.Fatalf("seed self MR: %v", err)
	}
	teamMRID, _, err := bd.EnsureMergeRequest(ctx,
		"https://github.com/foo/bar/pull/99",
		beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 99, Author: "coworker"})
	if err != nil {
		t.Fatalf("seed team MR: %v", err)
	}

	selfCycle, err := bd.CreateProcessingCycle(ctx, selfMRID, "foo/bar#self-seed")
	if err != nil {
		t.Fatalf("self cycle: %v", err)
	}
	teamCycle, err := bd.CreateProcessingCycle(ctx, teamMRID, "foo/bar#team-seed")
	if err != nil {
		t.Fatalf("team cycle: %v", err)
	}

	selfFB, err := bd.CreateFeedback(ctx, beads.CreateFeedbackInput{
		ProcessingCycleID: selfCycle, Kind: beads.FeedbackKindCommentThread,
		ExternalID: "TH_SELF", Fingerprint: "fp-self", Title: "self",
	})
	if err != nil {
		t.Fatalf("self feedback: %v", err)
	}
	teamFB, err := bd.CreateFeedback(ctx, beads.CreateFeedbackInput{
		ProcessingCycleID: teamCycle, Kind: beads.FeedbackKindCommentThread,
		ExternalID: "TH_TEAM", Fingerprint: "fp-team", Title: "team",
	})
	if err != nil {
		t.Fatalf("team feedback: %v", err)
	}

	if err := bd.SetReplyDraft(ctx, selfFB, "self reply"); err != nil {
		t.Fatalf("SetReplyDraft self: %v", err)
	}
	if err := bd.SetReplyDraft(ctx, teamFB, "team reply — should NOT post"); err != nil {
		t.Fatalf("SetReplyDraft team: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}

	// Exactly one ReplyToThread call — the self one.
	if len(vcs.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyToThread call; got %d: %+v",
			len(vcs.replyCalls), vcs.replyCalls)
	}
	if vcs.replyCalls[0].ThreadID != "TH_SELF" {
		t.Fatalf("expected reply to TH_SELF; got %+v", vcs.replyCalls[0])
	}

	// Self bead got its response_id.
	selfRespID, err := bd.GetResponseID(ctx, selfFB)
	if err != nil {
		t.Fatalf("GetResponseID self: %v", err)
	}
	if selfRespID != "C_SELF_RESP" {
		t.Fatalf("self response_id: got %q want C_SELF_RESP", selfRespID)
	}

	// Team bead untouched: ReplyDraft unchanged, response_id empty.
	teamRespID, err := bd.GetResponseID(ctx, teamFB)
	if err != nil {
		t.Fatalf("GetResponseID team: %v", err)
	}
	if teamRespID != "" {
		t.Fatalf("team response_id should be empty; got %q", teamRespID)
	}

	// Exactly one warning, referencing the team feedback bead.
	if len(sum.Warnings) != 1 {
		t.Fatalf("expected 1 Warning; got %d: %+v", len(sum.Warnings), sum.Warnings)
	}
	w := sum.Warnings[0]
	if w.Repo != "foo/bar" {
		t.Fatalf("warning Repo: %q", w.Repo)
	}
	if !strings.Contains(w.Message, teamFB) {
		t.Fatalf("warning Message should reference team feedback id %q; got %q", teamFB, w.Message)
	}
	if !strings.Contains(w.Message, "coworker") {
		t.Fatalf("warning Message should mention author %q; got %q", "coworker", w.Message)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/pg-pr && go test ./internal/sync/... -run TestSync_SkipsAndWarnsOnTeammateReplyDraft -v
```

Expected: FAIL — `processReplyDrafts` currently posts both replies because there's no ownership check. The test will report 2 reply calls and/or 0 warnings.

- [ ] **Step 3: Add the per-bead ownership guard**

In `packages/pg-pr/internal/sync/sync.go`, locate `processReplyDrafts` (around line 1129). Find the per-bead loop. After the existing cross-repo filter (around line 1161):

```go
		// Scope to current repo — other repos will handle their own beads.
		if mr.Fields.Repo != rcfg.Remote {
			continue
		}
```

Add the ownership guard immediately after:

```go
		// Ownership guard: never post replies to threads on PRs we don't
		// own. ReplyDraft staged on a team-mate's feedback bead is a bug
		// class — emit a warning so the user can investigate, but skip
		// the post. The local ReplyDraft is left in place; the user can
		// inspect / delete / retarget it via bd or pg-pr verbs.
		if !e.isSelfAuthoredLogin(mr.Fields.Author) {
			summary.Warnings = append(summary.Warnings, SummaryError{
				Repo: rcfg.Remote,
				Message: fmt.Sprintf(
					"reply %s skipped: parent PR #%d authored by %q (not self) — ReplyDraft should not have been staged",
					fb.ID, mr.Fields.PRNumber, mr.Fields.Author),
			})
			continue
		}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/sync/... -run TestSync_SkipsAndWarnsOnTeammateReplyDraft -v
```

Expected: PASS.

- [ ] **Step 5: Run all sync tests to confirm no regression**

```bash
go test ./internal/sync/... -v 2>&1 | tail -40
```

Expected: all tests pass. The existing `TestSync_PostsQueuedReplyAndStoresResponseID` and friends use `samplePR` which has `Author: "phillipg" == SelfLogin`, so they continue to post.

- [ ] **Step 6: Commit**

```bash
git add packages/pg-pr/internal/sync/sync.go packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
fix(pg-pr): skip and warn on ReplyDraft for team-mate PRs

beads_pg2-r3t0. processReplyDrafts now consults isSelfAuthoredLogin on
the parent merge-request's Author before posting. A stray ReplyDraft on
a team-mate PR's feedback bead would otherwise post a live, public
reply via the GitHub addPullRequestReviewThreadReply mutation (no draft
state). Skipped beads surface in Summary.Warnings; the local ReplyDraft
is preserved so the user can inspect or retarget it.
EOF
)"
```

---

## Task 7: Confirm empty-SelfLogin / empty-Author edge cases

**Files:**

- Test only: `packages/pg-pr/internal/sync/sync_test.go` — add `TestSync_TreatsEmptySelfLoginAsTeammate`.

This task adds an integration-level regression test for the conservative-default behavior. The unit-level coverage already exists in Task 1's `TestIsSelfAuthoredLogin`, but an end-to-end test confirms the predicate's wiring at the call sites.

- [ ] **Step 1: Write the test**

Append to `packages/pg-pr/internal/sync/sync_test.go`:

```go
func TestSync_TreatsEmptySelfLoginAsTeammate(t *testing.T) {
	ctx := context.Background()
	vcs := newFakeVCS()
	ci := newFakeCICD()

	// Both PRs draft+green. With empty SelfLogin, neither should be promoted.
	pr1 := samplePR(1, "foo/bar", "feat/a")
	pr1.Draft = true
	pr2 := samplePR(2, "foo/bar", "feat/b")
	pr2.Draft = true
	vcs.my["foo/bar"] = []api.PR{pr1, pr2}
	ci.runs[keyOf("foo/bar", 1)] = []api.CIRun{successRun()}
	ci.runs[keyOf("foo/bar", 2)] = []api.CIRun{successRun()}

	cfg := cfgWithCICD()
	cfg.SelfLogin = "" // simulate misconfiguration

	bd := newRealBDClient(t)
	stateDir := t.TempDir()
	e, err := New(Deps{
		Cfg:      cfg,
		VCS:      map[string]VCSProvider{"github": vcs},
		CICD:     map[string]CICDProvider{"ci": ci},
		Beads:    bd,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sum, err := e.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v (errors=%+v)", err, sum.Errors)
	}
	if len(vcs.setDraftCalls) != 0 {
		t.Fatalf("expected NO SetDraft calls when SelfLogin is empty; got %+v",
			vcs.setDraftCalls)
	}
	// Beads still upserted.
	if sum.BeadsCreated != 2 {
		t.Fatalf("BeadsCreated: got %d want 2", sum.BeadsCreated)
	}
}
```

- [ ] **Step 2: Run the test (should PASS immediately after Task 4)**

```bash
cd packages/pg-pr && go test ./internal/sync/... -run TestSync_TreatsEmptySelfLoginAsTeammate -v
```

Expected: PASS — Task 1's predicate returns false for empty self, so Task 4's guard already covers this. This test confirms the wiring.

(If this fails, it indicates a regression in earlier tasks — investigate before continuing.)

- [ ] **Step 3: Commit**

```bash
git add packages/pg-pr/internal/sync/sync_test.go
git commit -m "$(cat <<'EOF'
test(pg-pr): empty SelfLogin makes all PRs read-only

beads_pg2-r3t0. End-to-end regression test confirming that a
misconfigured SelfLogin defaults the engine to "modify nothing"
behavior. Covers the conservative-default contract from the spec at
the integration level.
EOF
)"
```

---

## Task 8: Final validation

- [ ] **Step 1: Run the full pg-pr test suite**

```bash
cd packages/pg-pr && go test ./... 2>&1 | tail -30
```

Expected: all packages pass.

- [ ] **Step 2: Run vet and lint**

```bash
go vet ./...
```

Expected: no diagnostics.

If a `golangci-lint` is available in the repo's pre-commit toolchain:

```bash
golangci-lint run ./...
```

Expected: clean (or only pre-existing warnings unrelated to this change).

- [ ] **Step 3: Sanity-check the diff against the spec**

```bash
git log --oneline -8
git diff --stat $(git log --oneline | grep -m1 "design to scope team-mate" | awk '{print $1}')^.. -- packages/pg-pr/
```

Confirm the changed lines are confined to:

- `packages/pg-pr/internal/sync/sync.go`
- `packages/pg-pr/internal/sync/sync_test.go`

No other files touched.

- [ ] **Step 4: Close the tracking bead**

```bash
bd close beads_pg2-r3t0
```

- [ ] **Step 5 (optional): Open a PR**

If shipping via PR rather than direct merge, use the project's PR creation flow. This plan does not auto-create the PR — that's a human decision per the `pg-pr-workflow` skill's "merges are an explicit human decision" rule.

---

## Spec Coverage Check

| Spec requirement                                                                      | Covered by                                                                                                   |
| ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `isSelfAuthoredLogin` helper, defaults to false on uncertainty                        | Task 1                                                                                                       |
| Empty self / empty author → false                                                     | Task 1 (unit), Task 7 (integration)                                                                          |
| `Sync()` loop: only `mine` runs `maybePromoteDraft`                                   | Task 4                                                                                                       |
| `SyncPR()`: ownership check before `maybePromoteDraft`                                | Task 3                                                                                                       |
| `processReplyDrafts()`: per-bead ownership guard with warning                         | Task 6                                                                                                       |
| Warning order: AFTER orphan + cross-repo checks                                       | Task 6 (guard placed after `mr.Fields.Repo != rcfg.Remote`)                                                  |
| `Summary.Warnings` field, separate from `Errors`, no telemetry impact                 | Task 5                                                                                                       |
| Warning text includes feedback bead ID, PR number, author, repo                       | Task 6 (verified by test assertions)                                                                         |
| Warning fires every sync (no dedup)                                                   | Task 6 (no bead-state writes in the warning path)                                                            |
| Team PRs still get `EnsureMergeRequest` and `processFeedback`                         | Task 3 (`SyncPR`) + Task 4 (`Sync`) — guards wrap only `maybePromoteDraft`; other phases run unconditionally |
| Tests cover mixed pool, SyncPR, processReplyDrafts mix, empty Author, empty SelfLogin | Tasks 1, 3, 4, 6, 7                                                                                          |
| Out of scope: labels / reviewer-requests                                              | Not implemented (correctly — those are GitHub-side cascades)                                                 |
| Out of scope: `pg-pr-review-team-pr` SKILL.md                                         | Untouched                                                                                                    |

---

## Notes for the executor

- **`teammatePR` Draft default**: `teammatePR` sets `Draft: true` (the only state that triggers `maybePromoteDraft`). If a future test needs a non-draft team-mate PR, override the field after construction.
- **`samplePR` Author default**: `samplePR` hardcodes `Author: "phillipg"`. Existing tests use it for self-authored PRs and depend on this. Don't change `samplePR`; use `teammatePR` for the team case.
- **Bd workspace constraints**: tests using `newRealBDClient` skip when `bd` isn't on `$PATH`. CI must have `bd` available, which the existing test suite already requires.
- **`EnsureMergeRequest` idempotency**: tests in Task 6 seed MRs directly before `Sync` runs. `Sync` then re-upserts via the same URL — the bead is updated, not duplicated. The Author from the seed sticks through the update (Sync supplies the same Author from the upstream PR).
- **Don't run `pg-pr` against real GitHub** during development of this plan — these guards exist precisely because the real engine modifies live state. Use only the fakeVCS-driven tests.
