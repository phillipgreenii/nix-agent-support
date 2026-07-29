# Rules

> The section `## Rules for Interactive Sessions Only` applies only when working with the user directly.
> Autonomous agents invoked via `claude -p` (e.g. background workers, polecats, dogs)
> MUST ignore that section and apply only the rules under `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has `.pre-commit-config.yaml`: `pre-commit run --all-files` MUST pass
- If the project has `flake.nix`: `nix flake check && darwin-rebuild check --flake .` MUST pass
- IF no tests exist for changed code: create them
- NEVER claim code is complete without passing tests

#### Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).

#### Unit Tests

MUST be isolated; if they modify files directly, the test MUST generate the scenario in a temp directory.

### Beads Claim Hygiene

> `bd` has NO `unclaim` verb. A release MUST be synthesised, and the `--assignee ""` half is
> the one that gets forgotten. A bead left `status=open` with a non-empty `assignee` is
> **stranded**: `bd ready --claim` correctly skips it (it is claimed), `bd update <id> --claim`
> rejects it ("issue already claimed by …"), and no stale-`in_progress` sweep can see it —
> so it sits at the top of the queue, unclaimable and invisible.

- **B-1** Whatever claims a bead MUST release it. Every exit path — success, hand-back, park,
  escalate, defer, give up, out of context — MUST end with the bead either `closed` or
  released. MUST NOT end a session still holding a claim.
- **B-2** A release MUST clear the assignee, not just the status:
  `bd update <id> --status open --assignee ""`. `--status open` alone is NOT a release.
- **B-3** The status change and the assignee clear MUST be a SINGLE `bd update` call. Two
  calls leave a window in which the bead is `open` but still claimed.
- **B-4** Any transition out of `in_progress` that is not a `bd close` MUST clear the assignee
  — including `blocked`, `deferred`, and re-`open`. A `bd close` MAY leave the assignee (it
  records who did the work), so anything that later RE-OPENS a closed bead MUST clear it then.
- **B-5** MUST prefer an explicit `--actor "<session-id>"` on every claim. Without it the
  assignee resolves to the human's display name, which makes an abandoned claim look like the
  operator deliberately took the bead.
- **B-6** On finding a bead that is `open` with a non-empty assignee, an agent MUST report it
  rather than silently steal or clear it — it is this defect, and the operator decides.

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.

### Git Workflow

- Always commit to the correct branch. Before committing, run `git branch --show-current` to verify. If changes were made on the wrong branch, alert the user before proceeding.
- When pre-commit hooks exist, always run `git diff --cached` and address any formatting/lint issues before attempting to commit. If subagents generate changes, ensure files are properly staged.

### Git Worktree / Integration Discipline

> The "primary branch" is the repo's default integration branch, resolved as
> `pgii-integrate-branch.primaryBranch` (git config) → `git symbolic-ref refs/remotes/origin/HEAD`
> (git standard) → `main`.

- **R-1** The canonical clone MUST have its primary branch checked out as steady state.
- **R-2** Only the canonical clone MAY have the primary branch checked out; a worktree/workforest member MUST use a feature branch.
- **R-3** An agent MUST NOT switch the canonical clone off its primary branch or leave it dirty in steady state. On finding it unexpectedly off-branch/dirty, the agent MUST stop and report — not reset, re-checkout, stash, or work around it.
- **R-4** By default an isolated single-repo change MUST be done in a git worktree.
- **R-5** The worktree (R-4) and workforest requirements MAY be overridden when the user explicitly says so.
- **R-6** For a change judged very small/quick, the agent MAY take the direct-commit path (commit on the primary branch in the canonical clone) — but if it does, it MUST first ask the user.
- **R-7** Concurrent agents in different worktrees are expected; the primary branch advancing during work is absorbed by the rebase. Only a rebase conflict or a persistent ff-race during landing warrants attention.
- **R-8 (floating-branch halt)** If an integration would advance the canonical primary branch (e.g. a local ff-merge) and the canonical clone is not on its primary branch, the agent MUST halt and report — merging then advances the wrong branch and orphans work into hanging branches. (For methods that do not touch the canonical primary — e.g. `pull-request` — an off-primary/dirty canonical is an R-3 anomaly to surface, not necessarily to halt.)
- **R-9 (integration entry point)** To integrate completed work, the agent MUST use the `integrate-branch` skill. The agent MUST NOT use `superpowers:finishing-a-development-branch` (plain non-ff merge, no rebase).

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request — these are user-only commands
- **CRITICAL**: NEVER use `sudo`
- When building/validating nix changes without activation, use a build-only command

#### Version Control

- Include the Jira issue as `Refs: TICKET-ID` on the line immediately after the subject (before the body). Extract the ticket ID from the branch name (format: `username.TICKET-ID.description`). A valid ticket ID matches `[A-Z]+-\d+` (e.g., `FINDEV-9208`, `CI-1494`). If the branch contains `NO-JIRA`, `NOJIRA`, or any variation instead of a real ticket ID, omit the `Refs:` line entirely.
- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval
- IF git hooks report violations: MUST fix the violations rather than bypassing hooks

#### Numeric Data

- **CRITICAL**: NEVER include calculated numbers without showing calculation method

#### Estimates

- **CRITICAL**: NEVER provide time estimates
- IF signaling effort needed: use t-shirt sizes (S/M/L/XL)

## Rules for Interactive Sessions Only

### Interaction Protocol

- MUST provide direct answers to questions without making code/file changes
- IF question implies work: confirm intent before proceeding
- MUST question assumptions, offer counterpoints, and state problems directly — prioritize correctness over agreement

### Development Standards

#### Planning & Design

- DEFAULT: iterative discussion → plan approval → implementation
- MUST NOT start coding without confirmation
- EXCEPTION: MAY proceed immediately when explicitly provided an implementation plan
- MUST critique non-trivial plans via independent subagent; iterate until no adjustments needed
- IF user input required during critique: ask before continuing
