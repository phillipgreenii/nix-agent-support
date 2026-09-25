# pg-desk Team/Mine Panel Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace pg-desk's `classifyPanel` act-now/blocked taxonomy with an approval-and-assignment-aware taxonomy (team: `awaiting_owner` / `awaiting_team` / `awaiting_me`; mine: `awaiting_me` / `awaiting_team`), fix the "is this PR still open" gate, and carry both changes through to the served payload and the Grafana dashboard.

**Architecture:** All decision logic stays inside `packages/pg-desk/internal/interpret` (pure functions, no I/O) exactly as today. Two new `Approvals` signals (`SelfApproved`, `HumanChangesRequested`) are computed once in `computeApprovals` and consumed by a rewritten `classifyPanel`. `internal/httpapi/server.go` only renames the panel constants/payload fields it already routes rows through — it gets no new logic. The Grafana dashboard JSON is a mechanical rename plus two new boolean columns using the exact override pattern every existing panel already uses.

**Tech Stack:** Go 1.x (stdlib `testing`, table-driven tests, no testify — matches this package's existing convention), JSON dashboard provisioning (Grafana + `yesoreyeram-infinity-datasource`).

**Spec:** No separate spec doc exists yet — the design below is the operator's own ruling from this conversation (2026-09-25), converged interactively over several turns (see the two decision-tree diagrams reviewed immediately before this plan). Task 4 promotes this ruling into `docs/behavior/pg-desk/interpret.md` and `docs/behavior/pg-desk/serve.md`, which become the durable, canonical description of this predicate from then on.

## Global Constraints

- **No staleness axis exists in pg-desk's Facts today** (documented, pre-existing deviation — see `internal/interpret/interpret.go`'s package doc, "No revision/approval HISTORY"). Every `"non-stale approval"` check in this plan MUST be implemented as "a currently APPROVED review exists," per the operator's own accepted fallback (do not fabricate head-SHA tracking; that is a future bead, not this plan).
- **`mine_awaiting_others` / `mine_awaiting_other_things` are retired, not renamed.** With only a binary "has ≥1 approval" signal and no required-approver-count, "fully approved" and "partially approved" are indistinguishable today, so a third mine bucket would be permanently unreachable dead code. Mine collapses to exactly two panels: `mine_awaiting_me`, `mine_awaiting_team`. This is a scope call made in this plan, not something the operator explicitly re-confirmed after the diagrams — flag back if a placeholder third panel is wanted for forward-compatibility instead.
- **`phillipgreenii-nix-agent-support` is a public repo.** No ZR-specific identifiers, real names, or logins beyond the operator's own may be added to code/tests/docs (existing repo rule). All test fixtures in this plan use generic placeholder logins (`alice`, `bob`, `carol`, `policy-bot`), matching existing test convention.
- **Two independent constant sets stay independent.** `internal/interpret/approvals.go`'s `PanelXxx` constants and `internal/httpapi/server.go`'s own `PanelXxx` constants are two separate, string-identical declarations by existing design (`httpapi` does not import `interpret`). Both MUST be updated in lockstep to the same string values; do not introduce a cross-package import to "fix" this duplication as part of this plan.
- **Do not touch `packages/pg-pr`.** Verified live 2026-09-25: `com.phillipg.pg-desk-serve` already answers `http://127.0.0.1:9818/api/v1/dashboard` with pg-desk's own payload shape — the Phase 11 port flip already happened. `packages/pg-pr`'s parallel implementation is not in the live path and is out of scope.

## Review Focus

- A store row with `pr.State == ""` (empty — e.g. a pre-migration row that predates the `state` field, or a malformed re-read) must resolve to `PanelNone`, the same as an explicitly `"closed"` PR, and must never panic. Task 2's tests cover this explicitly, not just the `"closed"`/`"merged"` cases the operator named.
- On a team PR, a standing bot disapproval or human `CHANGES_REQUESTED` must win over the assignment/approval split even when the current user is the requested reviewer and has _also_ already approved — `blocked` must be checked, and must short-circuit, before any assignment logic runs. Task 2 adds a case with both `SelfApproved: true` and `HumanChangesRequested: true` to pin the precedence.
- `computeApprovals` must not panic or false-positive when `self == ""` (no `SelfLogin` configured — a real zero-value `config.Config`). A review with `Author: ""` must never be treated as a self-approval. Task 1 adds this case explicitly.
- On a mine PR, an unresolved review-thread comment authored by _anyone_ (not just a specific reviewer) must route to `mine_awaiting_me` — the operator's rule ("if there are any open comments, it is also awaits for me") carries no author qualifier, and it is easy to accidentally reuse the old `openConversationOnly`'s narrower gating (which required `HumanApproved` first). Task 2 adds a case with an unresolved thread and `HumanApproved: false` to prove the unresolved-thread check is NOT gated on approval state.
- The bot's own `CHANGES_REQUESTED` review must never double-count as `HumanChangesRequested` — only a non-allowlisted reviewer's `CHANGES_REQUESTED` should set it, or a team PR with a normal bot disapproval (already routed to `awaiting_owner` via `BotVerdict`) would present as blocked twice for two different stated reasons, and a config that ever removes a login from the allowlist would silently change historical rows' classification basis. Task 1's tests pin this against the existing `policy-bot` fixture already in the test file.

---

### Task 1: `Approvals` gains `SelfApproved` and `HumanChangesRequested`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret/approvals.go:58-100` (`Approvals` struct at line ~178-193, `computeApprovals` at line 58)
- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret/interpret.go:285` (call site)
- Test: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret/interpret_test.go:395-418` (existing `TestComputeApprovals`, extended)

**Interfaces:**

- Consumes: nothing new — `prShow.Reviews[].Author`/`.State` (already decoded), the existing `approverAllowlist []string` and `self string` (already computed in `Interpret`, just not yet threaded into this call).
- Produces: `Approvals.SelfApproved bool` (json `self_approved`), `Approvals.HumanChangesRequested bool` (json `human_changes_requested`) — Task 2's `classifyPanel` consumes both by field name; Task 3's Grafana columns read both by their exact json tag.

- [ ] **Step 1: Write the failing tests**

Replace the whole `TestComputeApprovals` function in `internal/interpret/interpret_test.go` (lines 395-418) with:

```go
func TestComputeApprovals(t *testing.T) {
	pr := prShow{Reviews: []prReview{
		{Author: "alice", State: "APPROVED"},
		{Author: "bob", State: "APPROVED"},
		{Author: "policy-bot", State: "CHANGES_REQUESTED"},
	}}
	appr := computeApprovals(pr, "", []string{"policy-bot"}, nil)
	if appr.HumanApprovers != 2 || !appr.HumanApproved {
		t.Fatalf("got HumanApprovers=%d HumanApproved=%v; want 2/true", appr.HumanApprovers, appr.HumanApproved)
	}
	if appr.BotVerdict != BotVerdictDisapproved {
		t.Fatalf("got BotVerdict=%q; want disapproved", appr.BotVerdict)
	}
	if appr.HumanChangesRequested {
		t.Fatalf("got HumanChangesRequested=true; want false (the only CHANGES_REQUESTED review is the allowlisted bot's own, already carried by BotVerdict)")
	}
	if appr.SelfApproved {
		t.Fatalf("got SelfApproved=true; want false (self is \"\", must never match Author: \"\")")
	}

	prApproved := prShow{Reviews: []prReview{{Author: "policy-bot", State: "APPROVED"}}}
	if got := computeApprovals(prApproved, "", []string{"policy-bot"}, nil).BotVerdict; got != BotVerdictApproved {
		t.Fatalf("got BotVerdict=%q; want approved", got)
	}

	prNoDecision := prShow{Reviews: []prReview{{Author: "policy-bot", State: "COMMENTED"}}}
	if got := computeApprovals(prNoDecision, "", []string{"policy-bot"}, nil).BotVerdict; got != BotVerdictNoDecision {
		t.Fatalf("got BotVerdict=%q; want no-decision", got)
	}
}

// TestComputeApprovals_SelfApproved covers the "do I already have a current
// APPROVED review" signal classifyPanel's team branch needs to route an
// assigned reviewer to team_awaiting_owner instead of team_awaiting_me.
func TestComputeApprovals_SelfApproved(t *testing.T) {
	pr := prShow{Reviews: []prReview{
		{Author: "alice", State: "APPROVED"},
		{Author: "me", State: "APPROVED"},
	}}
	if got := computeApprovals(pr, "me", nil, nil).SelfApproved; !got {
		t.Fatalf("got SelfApproved=%v; want true (self has a current APPROVED review)", got)
	}
	if got := computeApprovals(pr, "carol", nil, nil).SelfApproved; got {
		t.Fatalf("got SelfApproved=%v; want false (self never reviewed)", got)
	}
}

// TestComputeApprovals_HumanChangesRequested covers the "a real reviewer,
// not the allowlisted bot, currently disapproves" signal — kept separate
// from BotVerdict so the bot's own disapproval is never double-counted.
func TestComputeApprovals_HumanChangesRequested(t *testing.T) {
	t.Run("non-allowlisted reviewer requests changes", func(t *testing.T) {
		pr := prShow{Reviews: []prReview{{Author: "carol", State: "CHANGES_REQUESTED"}}}
		if got := computeApprovals(pr, "", []string{"policy-bot"}, nil).HumanChangesRequested; !got {
			t.Fatalf("got HumanChangesRequested=%v; want true", got)
		}
	})
	t.Run("only the allowlisted bot requests changes", func(t *testing.T) {
		pr := prShow{Reviews: []prReview{{Author: "policy-bot", State: "CHANGES_REQUESTED"}}}
		if got := computeApprovals(pr, "", []string{"policy-bot"}, nil).HumanChangesRequested; got {
			t.Fatalf("got HumanChangesRequested=%v; want false (that's the bot's own disapproval, already BotVerdict)", got)
		}
	})
	t.Run("no allowlist configured, any CHANGES_REQUESTED counts as human", func(t *testing.T) {
		pr := prShow{Reviews: []prReview{{Author: "carol", State: "CHANGES_REQUESTED"}}}
		if got := computeApprovals(pr, "", nil, nil).HumanChangesRequested; !got {
			t.Fatalf("got HumanChangesRequested=%v; want true (empty allowlist means nobody is exempted)", got)
		}
	})
}
```

Also update every other pre-existing call site so the package still compiles (these are not new tests, just signature-follow fixes — do this in the same step since the file will not compile otherwise):

In `TestComputeApprovals_CommentVerdictGrammar` (same file, currently lines ~428-490+), change every `computeApprovals(pr, []string{"review-bot"}, ...)` call to `computeApprovals(pr, "", []string{"review-bot"}, ...)` — four call sites, at (pre-edit) lines 448, 461, 474, 487. Do not change any assertion in that test; only insert the new `""` argument.

- [ ] **Step 2: Run tests to verify they fail (compile error)**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/interpret/... -run TestComputeApprovals -v`
Expected: FAIL — build error, `not enough arguments in call to computeApprovals` (the test now passes 4 args; the function still takes 3) and/or `appr.HumanChangesRequested`/`appr.SelfApproved` undefined.

- [ ] **Step 3: Implement `SelfApproved` / `HumanChangesRequested`**

In `internal/interpret/approvals.go`, add the two new fields to the `Approvals` struct (insert after the `HumanApproved` field, before `BotVerdict`):

```go
type Approvals struct {
	// HumanApprovers is the count of distinct logins with a currently
	// APPROVED review. Every approver counts as human (see this package's
	// doc comment on the missing agent registry).
	HumanApprovers int  `json:"human_approvers"`
	HumanApproved  bool `json:"human_approved"`
	// SelfApproved is true iff SelfLogin has a currently APPROVED review on
	// this PR — classifyPanel's team branch uses this to route an assigned
	// reviewer who has already approved to team_awaiting_owner instead of
	// team_awaiting_me. No staleness axis (see this package's own doc
	// comment): a self-approval standing from before the PR's latest push
	// still reads true here — a documented, currently-unavoidable gap, not
	// a bug in this field.
	SelfApproved bool `json:"self_approved"`
	// HumanChangesRequested is true iff any reviewer NOT in the configured
	// approver_allowlist currently carries a CHANGES_REQUESTED review.
	// Deliberately excludes allowlisted (bot) reviewers so a bot's own
	// disapproval — already carried by BotVerdict — is never double-counted
	// here as if a second, independent human rejection existed.
	HumanChangesRequested bool `json:"human_changes_requested"`
	// BotVerdict is one of BotVerdictApproved/BotVerdictDisapproved/
	// BotVerdictNoDecision (approvals.go), read from approver_allowlist
	// logins' Review.State only.
	BotVerdict string `json:"bot_verdict"`
	// WaitingOnMe is true iff the anchor work bead's dependency tree
	// (Facts.Deps, `issue deps --full`) has at least one non-closed
	// dependency and every non-closed dependency carries the `human` label
	// — ported from pkg/beads.AllNonClosedHumanLabeled.
	WaitingOnMe bool `json:"waiting_on_me"`
}
```

Replace `computeApprovals` (currently lines 58-100) with:

```go
func computeApprovals(pr prShow, self string, approverAllowlist []string, verdictClassifier *verdict.Classifier) Approvals {
	allow := toSet(approverAllowlist)

	approvers := map[string]struct{}{}
	selfApproved := false
	humanChangesRequested := false
	for _, r := range pr.Reviews {
		switch r.State {
		case "APPROVED":
			approvers[r.Author] = struct{}{}
			if self != "" && r.Author == self {
				selfApproved = true
			}
		case "CHANGES_REQUESTED":
			if _, isBot := allow[r.Author]; !isBot {
				humanChangesRequested = true
			}
		}
	}

	disapproved := false
	approvedByAllowlisted := false
	for _, r := range pr.Reviews {
		if _, ok := allow[r.Author]; !ok {
			continue
		}
		switch r.State {
		case "CHANGES_REQUESTED":
			disapproved = true
		case "APPROVED":
			approvedByAllowlisted = true
		}
	}

	if commentAuthority := classifyCommentVerdict(pr, verdictClassifier); commentAuthority == verdict.Withheld {
		disapproved = true
	} else if commentAuthority == verdict.Approved {
		approvedByAllowlisted = true
	}

	botVerdict := BotVerdictNoDecision
	switch {
	case disapproved:
		botVerdict = BotVerdictDisapproved
	case approvedByAllowlisted:
		botVerdict = BotVerdictApproved
	}

	return Approvals{
		HumanApprovers:        len(approvers),
		HumanApproved:         len(approvers) > 0,
		SelfApproved:          selfApproved,
		HumanChangesRequested: humanChangesRequested,
		BotVerdict:            botVerdict,
	}
}
```

Update the doc comment directly above `computeApprovals` (currently lines 19-57) — append one paragraph after the existing "No staleness axis" paragraph:

```go
//
// SelfApproved and HumanChangesRequested are computed in the SAME pass as
// HumanApprovers/HumanApproved (one loop over pr.Reviews) rather than as
// separate helper functions, since all four read the identical
// State/Author fields — splitting them would mean re-walking pr.Reviews
// for no benefit. HumanChangesRequested excludes approverAllowlist logins
// deliberately: a bot's disapproval is already BotVerdict's concern, and
// double-carrying it here would let one bot rejection present as two
// independent signals to classifyPanel.
```

In `internal/interpret/interpret.go`, update the one call site (line 285):

```go
	approvals := computeApprovals(pr, selfLogin, approverAllowlist, buildVerdictClassifier(verdictGenerations))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/interpret/... -v -run 'TestComputeApprovals|TestComputeApprovals_SelfApproved|TestComputeApprovals_HumanChangesRequested|TestComputeApprovals_CommentVerdictGrammar'`
Expected: PASS, all subtests.

- [ ] **Step 5: Format and commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix fmt -- packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret.go packages/pg-desk/internal/interpret/interpret_test.go
git add packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret.go packages/pg-desk/internal/interpret/interpret_test.go
prek run --files packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret.go packages/pg-desk/internal/interpret/interpret_test.go
git commit -m "pg-desk: add SelfApproved/HumanChangesRequested to Approvals"
```

---

### Task 2: Rewrite `classifyPanel` to the awaiting-owner/team/me taxonomy

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret/approvals.go` (Panel constants ~lines 118-125, `openConversationOnly` ~265-284, `classifyPanel` ~290-326)
- Test: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret/interpret_test.go:588-633` (`TestClassifyPanel`, fully replaced)

**Interfaces:**

- Consumes: `Approvals.SelfApproved`, `Approvals.HumanChangesRequested` (Task 1), `Approvals.HumanApproved`/`.BotVerdict` (existing), `prShow.State`/`.hasConflict()`/`.allComments()` (existing), `ciRollupResult.State` (existing), `matchReasons []string` + `MatchReasonReviewRequested` (existing).
- Produces: `PanelTeamAwaitingOwner = "team_awaiting_owner"`, `PanelTeamAwaitingTeam = "team_awaiting_team"`, `PanelTeamAwaitingMe = "team_awaiting_me"`, `PanelMineAwaitingMe = "mine_awaiting_me"`, `PanelMineAwaitingTeam = "mine_awaiting_team"` — Task 3's `httpapi` package redeclares these same five string values under its own constant names.

- [ ] **Step 1: Write the failing tests**

Replace the whole `TestClassifyPanel` function in `internal/interpret/interpret_test.go` (lines 588-633) with:

```go
func TestClassifyPanel(t *testing.T) {
	openPR := prShow{State: "open"}

	t.Run("not open is excluded regardless of ownership", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			pr   prShow
		}{
			{"merged", prShow{State: "closed", Merged: true}},
			{"closed unmerged (rejected/abandoned)", prShow{State: "closed"}},
			{"empty state (never recorded / stale row)", prShow{State: ""}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := classifyPanel(OwnershipMine, tc.pr, ciRollupResult{State: "success"}, Approvals{}, nil); got != PanelNone {
					t.Errorf("mine: classifyPanel = %q; want PanelNone", got)
				}
				if got := classifyPanel(OwnershipTeam, tc.pr, ciRollupResult{State: "success"}, Approvals{}, []string{MatchReasonTeamAuthored}); got != PanelNone {
					t.Errorf("team: classifyPanel = %q; want PanelNone", got)
				}
			})
		}
	})

	t.Run("mine", func(t *testing.T) {
		tests := []struct {
			name string
			pr   prShow
			ci   ciRollupResult
			appr Approvals
			want string
		}{
			{"conflict -> awaiting me", prShow{State: "open", Mergeable: "CONFLICTING"}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingMe},
			{"ci failure -> awaiting me", openPR, ciRollupResult{State: "failure"}, Approvals{}, PanelMineAwaitingMe},
			{"ci pending -> awaiting me (pending counts as blocked)", openPR, ciRollupResult{State: "pending"}, Approvals{}, PanelMineAwaitingMe},
			{"ci none -> awaiting me (no countable run is not green)", openPR, ciRollupResult{State: "none"}, Approvals{}, PanelMineAwaitingMe},
			{"bot disapproved -> awaiting me", openPR, ciRollupResult{State: "success"}, Approvals{BotVerdict: BotVerdictDisapproved}, PanelMineAwaitingMe},
			{"human changes requested -> awaiting me", openPR, ciRollupResult{State: "success"}, Approvals{HumanChangesRequested: true}, PanelMineAwaitingMe},
			{
				"unresolved thread -> awaiting me, even with zero approvals",
				prShow{State: "open", Comments: []prComment{{ID: "c1", ThreadID: "t1", Resolved: false}}},
				ciRollupResult{State: "success"}, Approvals{HumanApproved: false}, PanelMineAwaitingMe,
			},
			{"resolved thread only, no approval -> awaiting team", prShow{State: "open", Comments: []prComment{{ID: "c1", ThreadID: "t1", Resolved: true}}}, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingTeam},
			{"clean + approved -> awaiting me (ready to merge)", openPR, ciRollupResult{State: "success"}, Approvals{HumanApproved: true}, PanelMineAwaitingMe},
			{"clean + not yet approved -> awaiting team", openPR, ciRollupResult{State: "success"}, Approvals{}, PanelMineAwaitingTeam},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := classifyPanel(OwnershipMine, tt.pr, tt.ci, tt.appr, nil); got != tt.want {
					t.Errorf("classifyPanel = %q; want %q", got, tt.want)
				}
			})
		}
		t.Run("co-owned acts as mine", func(t *testing.T) {
			if got := classifyPanel(OwnershipCoOwned, openPR, ciRollupResult{State: "success"}, Approvals{}, nil); got != PanelMineAwaitingTeam {
				t.Errorf("classifyPanel = %q; want %q", got, PanelMineAwaitingTeam)
			}
		})
	})

	t.Run("team", func(t *testing.T) {
		t.Run("draft -> none", func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, prShow{State: "open", Draft: true}, ciRollupResult{State: "success"}, Approvals{}, []string{MatchReasonTeamAuthored}); got != PanelNone {
				t.Errorf("got %q; want none", got)
			}
		})
		t.Run("no match reasons -> none", func(t *testing.T) {
			if got := classifyPanel(OwnershipTeam, openPR, ciRollupResult{State: "success"}, Approvals{}, nil); got != PanelNone {
				t.Errorf("got %q; want none", got)
			}
		})

		tests := []struct {
			name string
			pr   prShow
			ci   ciRollupResult
			appr Approvals
			want string
		}{
			{"ci failing -> awaiting owner", openPR, ciRollupResult{State: "failure"}, Approvals{}, PanelTeamAwaitingOwner},
			{"ci pending -> awaiting owner", openPR, ciRollupResult{State: "pending"}, Approvals{}, PanelTeamAwaitingOwner},
			{"conflict -> awaiting owner", prShow{State: "open", Mergeable: "CONFLICTING"}, ciRollupResult{State: "success"}, Approvals{}, PanelTeamAwaitingOwner},
			{"bot disapproved -> awaiting owner", openPR, ciRollupResult{State: "success"}, Approvals{BotVerdict: BotVerdictDisapproved}, PanelTeamAwaitingOwner},
			{"human changes requested -> awaiting owner", openPR, ciRollupResult{State: "success"}, Approvals{HumanChangesRequested: true}, PanelTeamAwaitingOwner},
			{
				"blocked wins even if I'm assigned and already approved",
				openPR, ciRollupResult{State: "success"},
				Approvals{SelfApproved: true, HumanChangesRequested: true}, PanelTeamAwaitingOwner,
			},
			{"clean, I'm requested, I haven't approved -> awaiting me", openPR, ciRollupResult{State: "success"}, Approvals{}, PanelTeamAwaitingMe},
			{"clean, I'm requested, I already approved -> awaiting owner", openPR, ciRollupResult{State: "success"}, Approvals{SelfApproved: true}, PanelTeamAwaitingOwner},
			{"clean, not requested, nobody approved -> awaiting team", openPR, ciRollupResult{State: "success"}, Approvals{}, PanelTeamAwaitingTeam},
			{"clean, not requested, someone else approved -> awaiting owner", openPR, ciRollupResult{State: "success"}, Approvals{HumanApproved: true}, PanelTeamAwaitingOwner},
		}
		for _, tt := range tests {
			// Every case with SelfApproved:true is deliberately a
			// requested-reviewer case (see the two rows this fires for:
			// "blocked wins even if I'm assigned and already approved" and
			// "clean, I'm requested, I already approved"). No row sets
			// SelfApproved:true while intending a not-requested reading, so
			// this alone is a safe, unambiguous selector.
			matchReasons := []string{MatchReasonTeamAuthored}
			if tt.want == PanelTeamAwaitingMe || tt.appr.SelfApproved {
				matchReasons = []string{MatchReasonReviewRequested}
			}
			t.Run(tt.name, func(t *testing.T) {
				if got := classifyPanel(OwnershipTeam, tt.pr, tt.ci, tt.appr, matchReasons); got != tt.want {
					t.Errorf("classifyPanel = %q; want %q (matchReasons=%v)", got, tt.want, matchReasons)
				}
			})
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/interpret/... -run TestClassifyPanel -v`
Expected: FAIL — compile error (`PanelTeamAwaitingOwner` etc. undefined) and/or wrong-value assertions once it compiles against a stub.

- [ ] **Step 3: Implement the new constants and `classifyPanel`**

Replace the Panel name constants block (currently lines 118-125) with:

```go
const (
	PanelTeamAwaitingOwner = "team_awaiting_owner"
	PanelTeamAwaitingTeam  = "team_awaiting_team"
	PanelTeamAwaitingMe    = "team_awaiting_me"
	PanelMineAwaitingMe    = "mine_awaiting_me"
	PanelMineAwaitingTeam  = "mine_awaiting_team"
	PanelNone              = ""
)
```

Delete `openConversationOnly` entirely (currently lines 262-284, including its doc comment) — it is superseded by the simpler, ungated `hasUnresolvedThread` below.

Replace `classifyPanel` (currently lines 286-326, including its doc comment) with:

```go
// classifyPanel places one entity into exactly one of the five named
// panels, or PanelNone when it is not currently in flight at all (not
// open, a draft/reasonless team PR).
//
// Operator ruling, 2026-09-25 (superseding the earlier act-now/blocked
// taxonomy this function carried through Phase 9-13): "Act Now" conflated
// "nothing is stopping you from looking at this" with "this needs YOUR
// action," which was misleading — a team PR already carrying two human
// approvals and a bot approval showed as Act Now solely because it matched
// a watch label, with nothing left for the operator to actually do.
//
//   - blocked (ci not green, bot disapproved, a real human
//     CHANGES_REQUESTED, or a merge conflict) always resolves to
//     team_awaiting_owner / mine_awaiting_me FIRST, before any
//     assignment/approval check — see this file's TestClassifyPanel
//     "blocked wins even if I'm assigned and already approved".
//   - Team, once not blocked: if I'm a requested reviewer
//     (MatchReasonReviewRequested) and haven't approved yet ->
//     team_awaiting_me; if I have -> team_awaiting_owner (the ball is back
//     with the PR's owner/other reviewers). If I'm not requested: any
//     existing human approval -> team_awaiting_owner, otherwise ->
//     team_awaiting_team.
//   - Mine, once not blocked: any unresolved review-thread comment ->
//     mine_awaiting_me (no author qualifier — any open thread is on me).
//     Otherwise, already having a human approval -> mine_awaiting_me
//     (nothing left to do but merge). Otherwise -> mine_awaiting_team.
//
// No staleness axis (package doc): "approved" here means "a currently
// APPROVED review exists," not "a non-stale one" — pg-desk has no
// per-review head-SHA history to tell the two apart yet.
func classifyPanel(own Ownership, pr prShow, ci ciRollupResult, appr Approvals, matchReasons []string) string {
	if pr.State != "open" {
		return PanelNone
	}

	blocked := ci.State != "success" ||
		appr.BotVerdict == BotVerdictDisapproved ||
		appr.HumanChangesRequested ||
		pr.hasConflict()

	if own.ActsAsMine() {
		switch {
		case blocked:
			return PanelMineAwaitingMe
		case hasUnresolvedThread(pr.allComments()):
			return PanelMineAwaitingMe
		case appr.HumanApproved:
			return PanelMineAwaitingMe
		default:
			return PanelMineAwaitingTeam
		}
	}

	// Team: admitted only when non-draft and carrying at least one live
	// match reason (unchanged from the prior taxonomy).
	if pr.Draft || len(matchReasons) == 0 {
		return PanelNone
	}
	if blocked {
		return PanelTeamAwaitingOwner
	}

	_, requested := toSet(matchReasons)[MatchReasonReviewRequested]
	if requested {
		if appr.SelfApproved {
			return PanelTeamAwaitingOwner
		}
		return PanelTeamAwaitingMe
	}
	if appr.HumanApproved {
		return PanelTeamAwaitingOwner
	}
	return PanelTeamAwaitingTeam
}

// hasUnresolvedThread reports whether any review-thread comment is still
// open — ported from the old openConversationOnly's inner loop, but no
// longer gated on approval/CI/conflict state: the 2026-09-25 ruling makes
// an open thread on a mine PR actionable on its own, regardless of
// anything else about the PR.
func hasUnresolvedThread(comments []prComment) bool {
	for _, c := range comments {
		if c.ThreadID != "" && !c.Resolved {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/interpret/... -v -run TestClassifyPanel`
Expected: PASS, all subtests.

Then run the whole package to catch any other breakage (e.g. `computeReadyToPromote`'s own tests, which reference `Approvals{}` literals unaffected by this change, and `TestInterpret_DeterministicWithFixedClock`, whose fixture has no `"state"` key and will now interpret as `PanelNone` — this is expected, since that test never asserted a `Panel` value):

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/interpret/... -v`
Expected: PASS, entire package.

- [ ] **Step 5: Format and commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix fmt -- packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret_test.go
git add packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret_test.go
prek run --files packages/pg-desk/internal/interpret/approvals.go packages/pg-desk/internal/interpret/interpret_test.go
git commit -m "pg-desk: replace act-now/blocked with awaiting-owner/team/me taxonomy"
```

---

### Task 3: Propagate the new panel names through `httpapi`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/httpapi/server.go:53-66` (constants), `:202-260` (`Payload` struct + `BuildPayload`)
- Test: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk/internal/httpapi/server_test.go` (multiple call sites), `testdata/dashboard_golden.json`

**Interfaces:**

- Consumes: Task 2's five `PanelXxx` string values (re-declared here under this package's own constants, per the Global Constraints note on the two independent constant sets — copy the exact strings, do not import `interpret`).
- Produces: `Payload.TeamAwaitingOwner`/`.TeamAwaitingTeam`/`.TeamAwaitingMe`/`.MineAwaitingMe`/`.MineAwaitingTeam` (json `team_awaiting_owner`/`team_awaiting_team`/`team_awaiting_me`/`mine_awaiting_me`/`mine_awaiting_team`) — Task 5's Grafana panels read these exact json keys via `root_selector`.

- [ ] **Step 1: Write the failing tests**

In `server_test.go`, apply these mechanical renames (same test behavior, new names — no test is asserting different _content_, only different _panel keys_):

Replace every `PanelMineActNow` literal used as a fixture's `Panel:` value with `PanelMineAwaitingMe`, and every `PanelMineAwaitingOthers` fixture value with `PanelMineAwaitingTeam` (these are the two panels actually exercised by name in the file today — `TestDashboard200AfterFirstInterpretation`, `TestStaleFlag`, `TestHiddenArrayExcludesFromPanel`, `TestMetricsSmoke`, `TestMetricsSmoke_StalePolarity`).

In `TestDashboard200AfterFirstInterpretation` (lines 81-91), replace the map with:

```go
	for name, got := range map[string][]Row{
		"team_awaiting_owner": payload.TeamAwaitingOwner,
		"team_awaiting_team":  payload.TeamAwaitingTeam,
		"team_awaiting_me":    payload.TeamAwaitingMe,
		"mine_awaiting_team":  payload.MineAwaitingTeam,
		"hidden":              payload.Hidden,
	} {
		if got == nil || len(got) != 0 {
			t.Fatalf("%s = %#v, want a non-nil empty array", name, got)
		}
	}
```

(This test's own fixture uses `PanelMineAwaitingMe`, so `payload.MineAwaitingMe` is the one array expected to be non-empty and is checked separately just above this block — leave that `len(payload.MineActNow) != 1` line, but rename it to `len(payload.MineAwaitingMe) != 1`.)

Replace `TestPayloadGoldenMatchesGrafanaSelectors` (lines 267-426) entirely with:

```go
// TestPayloadGoldenMatchesGrafanaSelectors is the acceptance criterion
// "Payload golden matches the Grafana selector list byte-for-byte on the
// fields Grafana reads." Column selectors below were read directly from
// phillipgreenii-nix-support-apps's
// darwin/modules/observability/dashboards/pg-desk.json (read-only
// reference; that repo's own file is never touched here) as of the
// 2026-09-25 awaiting-owner/team/me redesign.
//
//   - Team panels (team_awaiting_owner, team_awaiting_team,
//     team_awaiting_me) read: entity_id, human_approved, self_approved,
//     human_changes_requested, bot_verdict, match_team_authored,
//     match_review_requested, match_has_watch_label, ready_to_promote,
//     degraded, sync_error.
//   - Mine panels (mine_awaiting_me, mine_awaiting_team) read: entity_id,
//     human_approved, human_changes_requested, bot_verdict,
//     ready_to_promote, degraded, sync_error.
//   - The hidden panel (hidden) reads: entity_id, category,
//     ready_to_promote, degraded, sync_error.
//   - The root reads: dropped_count, age_seconds.
//
// sync_error is exercised via the hidden-panel fixture (entity 303) rather
// than duplicated on every fixture below: buildRow sets it through the
// same setIfNonEmpty call regardless of which panel the row lands in (see
// buildRow above), so proving it once is sufficient.
func TestPayloadGoldenMatchesGrafanaSelectors(t *testing.T) {
	wantPanelKeys := []string{
		PanelTeamAwaitingOwner, PanelTeamAwaitingTeam, PanelTeamAwaitingMe,
		PanelMineAwaitingMe, PanelMineAwaitingTeam, "hidden",
	}
	for _, want := range []string{
		"team_awaiting_owner", "team_awaiting_team", "team_awaiting_me",
		"mine_awaiting_me", "mine_awaiting_team",
	} {
		found := false
		for _, k := range wantPanelKeys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("panel constant list is missing the Grafana root_selector %q", want)
		}
	}

	s := store.OpenForTest(t)

	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "101",
		Ownership: "mine", Category: "bug", GateState: "satisfied",
		Approvals: `{"human_approved":true,"human_changes_requested":false,"bot_verdict":"no_decision"}`,
		Panel:     PanelMineAwaitingMe, ReadyToPromote: true, Degraded: false,
		AsOf: "2026-09-16T12:00:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "202",
		Ownership: "team", Category: "feature", GateState: "unsatisfied",
		Approvals:    `{"human_approved":false,"self_approved":false,"human_changes_requested":false,"bot_verdict":"disapproved"}`,
		MatchReasons: `["team-authored","label:urgent"]`,
		Panel:        PanelTeamAwaitingOwner, ReadyToPromote: false, Degraded: true,
		AsOf: "2026-09-16T11:55:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "303",
		Ownership: "mine", Category: "chore",
		Panel: PanelMineAwaitingTeam, ReadyToPromote: false, Degraded: false,
		// SyncError is the one row exercising the sync_error Grafana
		// selector (see the doc comment above): buildRow sets it uniformly
		// regardless of destination panel, so a single fixture suffices.
		SyncError: "sync timeout",
		AsOf:      "2026-09-16T10:00:00Z",
	})
	hidden := true
	if err := s.UpsertAnnotation(store.Annotation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "303",
		Hidden: &hidden, HiddenReason: "not ready",
		SetBy: "operator", SetAt: "2026-09-16T09:55:00Z",
	}); err != nil {
		t.Fatalf("UpsertAnnotation: %v", err)
	}

	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	mustSetMeta(t, s, store.MetaKeyLastRun, "2026-09-16T11:59:00Z")
	mustSetMeta(t, s, store.MetaKeyLastSweep, "2026-09-16T11:00:00Z")

	fixedNow := time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC)
	payload, err := BuildPayload(s, testConfig(), fixedNow)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	gotBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	mineColumns := []string{"entity_id", "human_approved", "human_changes_requested", "bot_verdict", "ready_to_promote", "degraded"}
	teamColumns := []string{
		"entity_id", "human_approved", "self_approved", "human_changes_requested", "bot_verdict",
		"match_team_authored", "match_review_requested", "match_has_watch_label", "ready_to_promote", "degraded",
	}
	hiddenColumns := []string{"entity_id", "category", "ready_to_promote", "degraded", "sync_error"}

	if len(payload.MineAwaitingMe) != 1 {
		t.Fatalf("MineAwaitingMe = %+v, want exactly 1 row", payload.MineAwaitingMe)
	}
	requireColumns(t, "mine_awaiting_me[0]", payload.MineAwaitingMe[0], mineColumns)

	if len(payload.TeamAwaitingOwner) != 1 {
		t.Fatalf("TeamAwaitingOwner = %+v, want exactly 1 row", payload.TeamAwaitingOwner)
	}
	requireColumns(t, "team_awaiting_owner[0]", payload.TeamAwaitingOwner[0], teamColumns)

	if len(payload.Hidden) != 1 {
		t.Fatalf("Hidden = %+v, want exactly 1 row (entity 303, excluded from mine_awaiting_team)", payload.Hidden)
	}
	requireColumns(t, "hidden[0]", payload.Hidden[0], hiddenColumns)
	if len(payload.MineAwaitingTeam) != 0 {
		t.Fatalf("MineAwaitingTeam = %+v, want empty (entity 303 is hidden)", payload.MineAwaitingTeam)
	}

	// dropped_count is the one root-level Grafana selector.
	if payload.DroppedCount != 0 {
		t.Fatalf("DroppedCount = %d, want 0", payload.DroppedCount)
	}

	goldenPath := filepath.Join("testdata", "dashboard_golden.json")
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}

	var got, want any
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatalf("unmarshal actual payload: %v", err)
	}
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("unmarshal golden %s: %v", goldenPath, err)
	}
	if !reflect.DeepEqual(got, want) {
		gotPretty, _ := json.MarshalIndent(got, "", "  ")
		wantPretty, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("payload does not match golden %s:\n--- got ---\n%s\n--- want ---\n%s", goldenPath, gotPretty, wantPretty)
	}
}
```

Overwrite `internal/httpapi/testdata/dashboard_golden.json` with:

```json
{
  "generated_at": "2026-09-16T12:00:00Z",
  "age_seconds": 30,
  "stale": false,
  "stale_after_seconds": 120,
  "sync_interval_seconds": 60,
  "dropped_count": 0,
  "team_awaiting_owner": [
    {
      "human_approved": false,
      "self_approved": false,
      "human_changes_requested": false,
      "bot_verdict": "disapproved",
      "match_reasons": ["team-authored", "label:urgent"],
      "match_team_authored": true,
      "match_review_requested": false,
      "match_has_watch_label": true,
      "repo": "acme/widgets",
      "entity_type": "pull_request",
      "entity_id": "202",
      "ownership": "team",
      "category": "feature",
      "gate_state": "unsatisfied",
      "as_of": "2026-09-16T11:55:00Z",
      "ready_to_promote": false,
      "degraded": true
    }
  ],
  "team_awaiting_team": [],
  "team_awaiting_me": [],
  "mine_awaiting_me": [
    {
      "human_approved": true,
      "human_changes_requested": false,
      "bot_verdict": "no_decision",
      "repo": "acme/widgets",
      "entity_type": "pull_request",
      "entity_id": "101",
      "ownership": "mine",
      "category": "bug",
      "gate_state": "satisfied",
      "as_of": "2026-09-16T12:00:00Z",
      "ready_to_promote": true,
      "degraded": false
    }
  ],
  "mine_awaiting_team": [],
  "hidden": [
    {
      "repo": "acme/widgets",
      "entity_type": "pull_request",
      "entity_id": "303",
      "ownership": "mine",
      "category": "chore",
      "as_of": "2026-09-16T10:00:00Z",
      "ready_to_promote": false,
      "degraded": false,
      "sync_error": "sync timeout"
    }
  ],
  "last_run_at": "2026-09-16T11:59:00Z",
  "last_sweep_at": "2026-09-16T11:00:00Z",
  "runs_failed_24h": 1,
  "errors": [
    {
      "repo": "acme/widgets",
      "entity_type": "pull_request",
      "entity_id": "303",
      "error": "sync timeout"
    }
  ]
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/httpapi/... -v`
Expected: FAIL — compile errors (`PanelTeamAwaitingOwner` etc. and `payload.TeamAwaitingOwner` etc. undefined).

- [ ] **Step 3: Implement**

Replace the Panel name constants block in `server.go` (currently lines 53-66, including its doc comment) with:

```go
// Panel name constants — the five panels of the 2026-09-25
// awaiting-owner/team/me taxonomy (operator ruling; superseded the earlier
// act-now/blocked split). Cross-checked against the Grafana dashboard
// JSON's root_selector values in phillipgreenii-nix-support-apps
// (darwin/modules/observability/dashboards/pg-desk.json) and against
// docs/behavior/pg-desk/interpret.md's "Panel placement" section — every
// byte here MUST match both, or the Infinity datasource panel finds
// nothing.
const (
	PanelTeamAwaitingOwner = "team_awaiting_owner"
	PanelTeamAwaitingTeam  = "team_awaiting_team"
	PanelTeamAwaitingMe    = "team_awaiting_me"
	PanelMineAwaitingMe    = "mine_awaiting_me"
	PanelMineAwaitingTeam  = "mine_awaiting_team"
)
```

Replace the `Payload` struct's panel fields (currently lines 215-219) with:

```go
	TeamAwaitingOwner []Row `json:"team_awaiting_owner"`
	TeamAwaitingTeam  []Row `json:"team_awaiting_team"`
	TeamAwaitingMe    []Row `json:"team_awaiting_me"`
	MineAwaitingMe    []Row `json:"mine_awaiting_me"`
	MineAwaitingTeam  []Row `json:"mine_awaiting_team"`
```

Update the `Payload` struct's own doc comment (currently lines 197-201) — replace "the five named panel arrays... pinned verbatim from the design doc's section 7.7" with:

```go
// Payload is the GET /api/v1/dashboard response body: the five named panel
// arrays, the hidden array, and root freshness/counter fields — see
// docs/behavior/pg-desk/interpret.md's "Panel placement" section and
// serve.md for the canonical description. Every array field is always
// non-nil (serializes as "[]", never "null").
```

Replace `BuildPayload`'s initialization block (currently lines 244-260) with:

```go
	p := &Payload{
		TeamAwaitingOwner: []Row{},
		TeamAwaitingTeam:  []Row{},
		TeamAwaitingMe:    []Row{},
		MineAwaitingMe:    []Row{},
		MineAwaitingTeam:  []Row{},
		Hidden:            []Row{},
		Errors:            []PayloadError{},
	}

	panels := map[string]*[]Row{
		PanelTeamAwaitingOwner: &p.TeamAwaitingOwner,
		PanelTeamAwaitingTeam:  &p.TeamAwaitingTeam,
		PanelTeamAwaitingMe:    &p.TeamAwaitingMe,
		PanelMineAwaitingMe:    &p.MineAwaitingMe,
		PanelMineAwaitingTeam:  &p.MineAwaitingTeam,
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go test ./internal/httpapi/... -v`
Expected: PASS, entire package.

Then run the whole module to confirm nothing else references the retired names:

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-desk && go build ./... && go test ./...`
Expected: PASS. If `go build` fails, grep for remaining references: `grep -rn "PanelMineActNow\|PanelMineAwaitingOthers\|PanelMineAwaitingOtherThings\|PanelTeamActNow\|PanelTeamBlocked" --include='*.go' .` and fix each (likely `cmd/pg-desk/*.go` — check `open.go`, `open_json_test.go`, `open_test.go`, `show_test.go`, which the initial repo-wide grep for `"act now"` at the start of this conversation also matched).

- [ ] **Step 5: Format and commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix fmt -- packages/pg-desk/internal/httpapi/server.go packages/pg-desk/internal/httpapi/server_test.go
git add packages/pg-desk/internal/httpapi/server.go packages/pg-desk/internal/httpapi/server_test.go packages/pg-desk/internal/httpapi/testdata/dashboard_golden.json
prek run --files packages/pg-desk/internal/httpapi/server.go packages/pg-desk/internal/httpapi/server_test.go packages/pg-desk/internal/httpapi/testdata/dashboard_golden.json
git commit -m "pg-desk: rename served panels to the awaiting-owner/team/me taxonomy"
```

**Note for the implementer:** Step 4's `go build ./...` may surface panel-name references in `cmd/pg-desk/{open,show}*.go` and their tests that this plan did not enumerate (the initial investigation in this conversation found five files matching "act now" beyond `approvals.go`/`interpret.go`/`server.go`: `desk.go`, `open_json_test.go`, `open_test.go`, `show_test.go`, `store_test.go`). Fix each compile/test break the same way — rename the panel string/constant reference to its new equivalent — and fold that into this task's commit rather than opening a new task, since it is the same rename propagating further than this plan's authors traced by hand.

---

### Task 4: Update behavior docs

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/docs/behavior/pg-desk/interpret.md`
- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/docs/behavior/pg-desk/serve.md`

**Interfaces:**

- Consumes: nothing (documentation only).
- Produces: the durable, canonical description Task 3's new `server.go` doc comment cites in place of the retired ephemeral-spec citation.

- [ ] **Step 1: Add a "Panel placement" section to `interpret.md`**

There is no test to write first — this task is documentation, not code. Insert a new section into `interpret.md` immediately after the existing bulleted list (after the "Ready-to-promote" bullet, currently ending at line 34) and before the "Hidden and WIP are explicitly NOT interpreted" paragraph:

```markdown
- **Panel placement** — five named panels (`team_awaiting_owner`, `team_awaiting_team`,
  `team_awaiting_me`, `mine_awaiting_me`, `mine_awaiting_team`), or no panel at all for a PR that
  is not open, a draft team PR, or a team PR with zero match reasons. Operator ruling, 2026-09-25
  (superseding the earlier act-now/blocked taxonomy): "Act Now" conflated "nothing is stopping you
  from looking at this" with "this needs YOUR action."

  A PR is **blocked** when CI is not green (`failure`, `pending`, or `none` all count — only
  `success` passes), the bot verdict is disapproved, a non-bot reviewer currently carries a
  `CHANGES_REQUESTED` review, or there is a merge conflict. Blocked always wins over every
  assignment/approval check below.
  - **Team**, once not blocked: if the operator is a requested reviewer and has not yet approved
    → `team_awaiting_me`; if the operator has already approved → `team_awaiting_owner` (the ball
    is back with the PR's owner or other reviewers). If the operator is not a requested reviewer:
    any existing human approval → `team_awaiting_owner`, otherwise → `team_awaiting_team`. Blocked
    → `team_awaiting_owner` (fixing CI/conflicts/disapprovals is the PR owner's job, not the
    reviewer's).
  - **Mine**, once not blocked: any unresolved review-thread comment → `mine_awaiting_me` (no
    author qualifier — an open thread is on the operator regardless of who left it). Otherwise, an
    existing human approval → `mine_awaiting_me` (nothing left to do but merge). Otherwise →
    `mine_awaiting_team`. Blocked → `mine_awaiting_me` (it's the operator's own PR to fix).

  **Known data gap:** pg-desk has no per-review head-SHA history, so "approved" here means "a
  currently `APPROVED` review exists," not "a non-stale one" — a self- or team-approval from
  before the PR's latest push still reads as satisfied. A future gather/store change to add
  per-review staleness would change this without changing the taxonomy above.

  **Known scope gap:** the taxonomy cannot distinguish "fully approved" from "partially approved"
  (no required-approver-count signal — no CODEOWNERS/branch-protection data is gathered), so mine
  has only two panels rather than a third "waiting on more approvals" bucket.
```

- [ ] **Step 2: Update `serve.md`'s named-selector list**

In `serve.md`, replace the parenthetical panel list (currently: `` `mine_act_now`, `mine_awaiting_others`, `mine_awaiting_other_things`, `team_act_now`, `team_blocked` ``) with:

```markdown
(`team_awaiting_owner`, `team_awaiting_team`, `team_awaiting_me`, `mine_awaiting_me`,
`mine_awaiting_team`)
```

- [ ] **Step 3: Verify (grep, no test runner for docs)**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support && grep -rn "mine_act_now\|team_act_now\|team_blocked\|mine_awaiting_other" docs/behavior/pg-desk/`
Expected: no output (every old panel name is gone from these two files).

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add docs/behavior/pg-desk/interpret.md docs/behavior/pg-desk/serve.md
prek run --files docs/behavior/pg-desk/interpret.md docs/behavior/pg-desk/serve.md
git commit -m "docs(pg-desk): document the awaiting-owner/team/me panel taxonomy"
```

---

### Task 5: Update the Grafana dashboard

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-desk.json` (panels with `id` 1-5, lines 51-750 as of this plan's writing)

**Interfaces:**

- Consumes: Task 3's five `root_selector` values (`team_awaiting_owner`, `team_awaiting_team`, `team_awaiting_me`, `mine_awaiting_me`, `mine_awaiting_team`) and the two new row fields (`self_approved`, `human_changes_requested`).
- Produces: nothing further downstream — this is the leaf of the chain.

- [ ] **Step 1: Replace panels 1 through 5**

There is no test framework for this JSON file; "failing test" here is `jq empty` on the current file (already valid, so skip straight to the edit) followed by the same check after editing (Step 2 below is the closest analogue to "run the test").

In `darwin/modules/observability/dashboards/pg-desk.json`, replace the five panel objects with `"id": 1` through `"id": 5` (currently the JSON array elements spanning line 51's `{` through line 750's matching `},`, i.e. everything between the `"panels": [` opener and the `"id": 8` ("Data Age (s)") panel) with:

```json
    {
      "id": 1,
      "title": "Team PRs — Awaiting Me",
      "type": "table",
      "gridPos": { "x": 0, "y": 0, "w": 24, "h": 8 },
      "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "pg-pr-infinity" },
      "targets": [
        {
          "refId": "A",
          "type": "json",
          "parser": "backend",
          "source": "url",
          "format": "table",
          "url": "http://127.0.0.1:9818/api/v1/dashboard",
          "url_options": { "method": "GET" },
          "root_selector": "team_awaiting_me",
          "columns": [
            { "selector": "entity_id", "text": "Entity", "type": "string" },
            { "selector": "title", "text": "Title", "type": "string" },
            { "selector": "url", "text": "url", "type": "string" },
            { "selector": "human_approved", "text": "Human✓", "type": "boolean" },
            { "selector": "self_approved", "text": "Self✓", "type": "boolean" },
            { "selector": "human_changes_requested", "text": "Chg Req", "type": "boolean" },
            { "selector": "bot_verdict", "text": "Bot", "type": "string" },
            { "selector": "match_team_authored", "text": "Team-Authored", "type": "boolean" },
            { "selector": "match_review_requested", "text": "Requested", "type": "boolean" },
            { "selector": "match_has_watch_label", "text": "Watch Label", "type": "boolean" },
            { "selector": "ready_to_promote", "text": "Promote Ready", "type": "boolean" },
            { "selector": "degraded", "text": "Degraded", "type": "boolean" },
            { "selector": "sync_error", "text": "Sync Error", "type": "string" }
          ]
        }
      ],
      "fieldConfig": {
        "defaults": {},
        "overrides": [
          {
            "matcher": { "id": "byName", "options": "Entity" },
            "properties": [
              { "id": "custom.width", "value": 280 },
              { "id": "links", "value": [{ "title": "Open PR", "url": "${__data.fields.url}", "targetBlank": true }] }
            ]
          },
          { "matcher": { "id": "byName", "options": "Bot" }, "properties": [{ "id": "custom.width", "value": 90 }] },
          { "matcher": { "id": "byName", "options": "Title" }, "properties": [{ "id": "custom.width", "value": 400 }] },
          { "matcher": { "id": "byName", "options": "url" }, "properties": [{ "id": "custom.hidden", "value": true }] }
        ]
      }
    },
    {
      "id": 2,
      "title": "Team PRs — Awaiting Team",
      "type": "table",
      "gridPos": { "x": 0, "y": 8, "w": 24, "h": 8 },
      "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "pg-pr-infinity" },
      "targets": [
        {
          "refId": "A",
          "type": "json",
          "parser": "backend",
          "source": "url",
          "format": "table",
          "url": "http://127.0.0.1:9818/api/v1/dashboard",
          "url_options": { "method": "GET" },
          "root_selector": "team_awaiting_team",
          "columns": [
            { "selector": "entity_id", "text": "Entity", "type": "string" },
            { "selector": "title", "text": "Title", "type": "string" },
            { "selector": "url", "text": "url", "type": "string" },
            { "selector": "human_approved", "text": "Human✓", "type": "boolean" },
            { "selector": "self_approved", "text": "Self✓", "type": "boolean" },
            { "selector": "human_changes_requested", "text": "Chg Req", "type": "boolean" },
            { "selector": "bot_verdict", "text": "Bot", "type": "string" },
            { "selector": "match_team_authored", "text": "Team-Authored", "type": "boolean" },
            { "selector": "match_review_requested", "text": "Requested", "type": "boolean" },
            { "selector": "match_has_watch_label", "text": "Watch Label", "type": "boolean" },
            { "selector": "ready_to_promote", "text": "Promote Ready", "type": "boolean" },
            { "selector": "degraded", "text": "Degraded", "type": "boolean" },
            { "selector": "sync_error", "text": "Sync Error", "type": "string" }
          ]
        }
      ],
      "fieldConfig": {
        "defaults": {},
        "overrides": [
          {
            "matcher": { "id": "byName", "options": "Entity" },
            "properties": [
              { "id": "custom.width", "value": 280 },
              { "id": "links", "value": [{ "title": "Open PR", "url": "${__data.fields.url}", "targetBlank": true }] }
            ]
          },
          { "matcher": { "id": "byName", "options": "Bot" }, "properties": [{ "id": "custom.width", "value": 90 }] },
          { "matcher": { "id": "byName", "options": "Title" }, "properties": [{ "id": "custom.width", "value": 400 }] },
          { "matcher": { "id": "byName", "options": "url" }, "properties": [{ "id": "custom.hidden", "value": true }] }
        ]
      }
    },
    {
      "id": 3,
      "title": "Team PRs — Awaiting Owner",
      "type": "table",
      "gridPos": { "x": 0, "y": 16, "w": 24, "h": 8 },
      "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "pg-pr-infinity" },
      "targets": [
        {
          "refId": "A",
          "type": "json",
          "parser": "backend",
          "source": "url",
          "format": "table",
          "url": "http://127.0.0.1:9818/api/v1/dashboard",
          "url_options": { "method": "GET" },
          "root_selector": "team_awaiting_owner",
          "columns": [
            { "selector": "entity_id", "text": "Entity", "type": "string" },
            { "selector": "title", "text": "Title", "type": "string" },
            { "selector": "url", "text": "url", "type": "string" },
            { "selector": "human_approved", "text": "Human✓", "type": "boolean" },
            { "selector": "self_approved", "text": "Self✓", "type": "boolean" },
            { "selector": "human_changes_requested", "text": "Chg Req", "type": "boolean" },
            { "selector": "bot_verdict", "text": "Bot", "type": "string" },
            { "selector": "match_team_authored", "text": "Team-Authored", "type": "boolean" },
            { "selector": "match_review_requested", "text": "Requested", "type": "boolean" },
            { "selector": "match_has_watch_label", "text": "Watch Label", "type": "boolean" },
            { "selector": "ready_to_promote", "text": "Promote Ready", "type": "boolean" },
            { "selector": "degraded", "text": "Degraded", "type": "boolean" },
            { "selector": "sync_error", "text": "Sync Error", "type": "string" }
          ]
        }
      ],
      "fieldConfig": {
        "defaults": {},
        "overrides": [
          {
            "matcher": { "id": "byName", "options": "Entity" },
            "properties": [
              { "id": "custom.width", "value": 280 },
              { "id": "links", "value": [{ "title": "Open PR", "url": "${__data.fields.url}", "targetBlank": true }] }
            ]
          },
          { "matcher": { "id": "byName", "options": "Bot" }, "properties": [{ "id": "custom.width", "value": 90 }] },
          { "matcher": { "id": "byName", "options": "Title" }, "properties": [{ "id": "custom.width", "value": 400 }] },
          { "matcher": { "id": "byName", "options": "url" }, "properties": [{ "id": "custom.hidden", "value": true }] }
        ]
      }
    },
    {
      "id": 4,
      "title": "My PRs — Awaiting Me",
      "type": "table",
      "gridPos": { "x": 0, "y": 24, "w": 24, "h": 8 },
      "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "pg-pr-infinity" },
      "targets": [
        {
          "refId": "A",
          "type": "json",
          "parser": "backend",
          "source": "url",
          "format": "table",
          "url": "http://127.0.0.1:9818/api/v1/dashboard",
          "url_options": { "method": "GET" },
          "root_selector": "mine_awaiting_me",
          "columns": [
            { "selector": "entity_id", "text": "Entity", "type": "string" },
            { "selector": "title", "text": "Title", "type": "string" },
            { "selector": "url", "text": "url", "type": "string" },
            { "selector": "human_approved", "text": "Human✓", "type": "boolean" },
            { "selector": "human_changes_requested", "text": "Chg Req", "type": "boolean" },
            { "selector": "bot_verdict", "text": "Bot", "type": "string" },
            { "selector": "ready_to_promote", "text": "Promote Ready", "type": "boolean" },
            { "selector": "degraded", "text": "Degraded", "type": "boolean" },
            { "selector": "sync_error", "text": "Sync Error", "type": "string" }
          ]
        }
      ],
      "fieldConfig": {
        "defaults": {},
        "overrides": [
          {
            "matcher": { "id": "byName", "options": "Entity" },
            "properties": [
              { "id": "custom.width", "value": 280 },
              { "id": "links", "value": [{ "title": "Open PR", "url": "${__data.fields.url}", "targetBlank": true }] }
            ]
          },
          { "matcher": { "id": "byName", "options": "Bot" }, "properties": [{ "id": "custom.width", "value": 90 }] },
          { "matcher": { "id": "byName", "options": "Title" }, "properties": [{ "id": "custom.width", "value": 400 }] },
          { "matcher": { "id": "byName", "options": "url" }, "properties": [{ "id": "custom.hidden", "value": true }] }
        ]
      }
    },
    {
      "id": 5,
      "title": "My PRs — Awaiting Team",
      "type": "table",
      "gridPos": { "x": 0, "y": 32, "w": 24, "h": 8 },
      "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "pg-pr-infinity" },
      "targets": [
        {
          "refId": "A",
          "type": "json",
          "parser": "backend",
          "source": "url",
          "format": "table",
          "url": "http://127.0.0.1:9818/api/v1/dashboard",
          "url_options": { "method": "GET" },
          "root_selector": "mine_awaiting_team",
          "columns": [
            { "selector": "entity_id", "text": "Entity", "type": "string" },
            { "selector": "title", "text": "Title", "type": "string" },
            { "selector": "url", "text": "url", "type": "string" },
            { "selector": "human_approved", "text": "Human✓", "type": "boolean" },
            { "selector": "human_changes_requested", "text": "Chg Req", "type": "boolean" },
            { "selector": "bot_verdict", "text": "Bot", "type": "string" },
            { "selector": "ready_to_promote", "text": "Promote Ready", "type": "boolean" },
            { "selector": "degraded", "text": "Degraded", "type": "boolean" },
            { "selector": "sync_error", "text": "Sync Error", "type": "string" }
          ]
        }
      ],
      "fieldConfig": {
        "defaults": {},
        "overrides": [
          {
            "matcher": { "id": "byName", "options": "Entity" },
            "properties": [
              { "id": "custom.width", "value": 280 },
              { "id": "links", "value": [{ "title": "Open PR", "url": "${__data.fields.url}", "targetBlank": true }] }
            ]
          },
          { "matcher": { "id": "byName", "options": "Bot" }, "properties": [{ "id": "custom.width", "value": 90 }] },
          { "matcher": { "id": "byName", "options": "Title" }, "properties": [{ "id": "custom.width", "value": 400 }] },
          { "matcher": { "id": "byName", "options": "url" }, "properties": [{ "id": "custom.hidden", "value": true }] }
        ]
      }
    },
```

Leave every panel from `"id": 8` ("Data Age (s)") onward untouched — their `gridPos.y` values (40, 44, 45, 49) already sit below `y: 32, h: 8` (i.e. below `y: 40`), so no downstream repositioning is needed.

- [ ] **Step 2: Validate**

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && jq empty darwin/modules/observability/dashboards/pg-desk.json && echo "valid JSON"`
Expected: `valid JSON`, no parse error.

Run: `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && jq -r '.panels[] | select(.type=="table") | .targets[0].root_selector' darwin/modules/observability/dashboards/pg-desk.json`
Expected, in order: `team_awaiting_me`, `team_awaiting_team`, `team_awaiting_owner`, `mine_awaiting_me`, `mine_awaiting_team`, `hidden` — matching Task 3's five `PanelXxx` constants (plus the pre-existing `hidden` panel, untouched by this plan) exactly.

- [ ] **Step 3: Format and commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps
nix fmt -- darwin/modules/observability/dashboards/pg-desk.json
git add darwin/modules/observability/dashboards/pg-desk.json
prek run --all-files
git commit -m "pg-desk dashboard: rename panels to the awaiting-owner/team/me taxonomy"
```

(This repo's own `CLAUDE.md` convention is `prek run --all-files` for this gate — heavier than the narrower per-file convention used in `phillipgreenii-nix-agent-support` above, but that is this repo's own documented gate, not a plan deviation.)

---

## Deployment note (not a plan task — do not run without explicit request)

None of the five tasks above activate the change. `pg-desk-serve` is a live launchd agent (`com.phillipg.pg-desk-serve`) running an already-built Nix package; picking up Task 1-3's Go changes requires rebuilding and re-activating home-manager (and the Grafana provisioning module for Task 5, likely via `darwin-rebuild switch` or the workspace's own `pn workspace apply`). Per this session's standing rule, do not run any system-activation command on your own initiative — hand the finished, committed, tested branch back and let the operator apply it themselves, or ask explicitly before activating.
