# Rules

Rationale, census data and provenance for these rules: `docs/agent-rules-rationale.md` in `nix-agent-support` (not loaded at runtime).
The section `## Rules for Interactive Sessions Only` applies only when working with the user directly. Autonomous agents (`claude -p`, background workers) MUST ignore it and apply only `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Mistake Acknowledgment Marker

- **M-1** When acknowledging the agent's own error is warranted, the first words MUST be `Correction:` (user-visible text only, not thinking blocks).
- **M-2** M-1 MUST NOT change how often the agent acknowledges anything: correct an earlier statement only when the error would change the user's code, conclusions, or decisions. Silent fixes stay silent and unmarked.
- **M-3** MUST NOT add a phrase distinguishing self-caught from user-caught errors.

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Absolute-Path Provenance

- **A-1** An absolute path MUST be built only from a root OBSERVED this session (env block working directory, a tool result, or the user's text).@KNOWN_ABSENT_ROOTS_SENTENCE@
- **A-2** Resolve a repo-relative path as `<session-cwd>/<relative>`. If the root is uncertain, probe first (`ls` the parent, Glob the suffix, or `git ls-files -- '*<name>'`); MUST NOT Read a guessed absolute path.
- **A-3** A subagent brief MUST state the absolute repo root once; relative paths without a root cause this defect.

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has pre-commit hooks (probe with `pg-hooks status --porcelain || true` and read the `state=` line, not the exit code. Do NOT probe with `test -f .pre-commit-config.yaml` or bare `ls`): the hooks MUST pass on the **changed** files.
  - The commit's own hook run is the gate: `git add -A` first, run the formatter in write mode (`nix fmt -- <files>`, `gofumpt -w`, `prettier --write`), then `pg-hooks fix`, then `git commit`. Pre-validate with `pg-hooks run pre-commit <changed files>`; never `--all-files` per change. Before landing: `pg-hooks run pre-land`.
  - State `missing`/`broken`/`unreachable`/`relocated`: commit hooks do NOT run; say so in the report instead of claiming they passed, and never link, copy or regenerate a hook config. (State reference and rebuild steps: the `nix-how-to` path-rule, `pg-hooks status`, `docs/hooks.md` in `phillipg-nix-repo-base`.)
  - Exit `127` or no `state=` line: report `pg-hooks not installed on this machine; ask the operator to run pn workspace apply`, then fall back to `test -f .pre-commit-config.yaml && echo yes || echo no`.
- If the project has `flake.nix`: a full `nix flake check` is NOT a per-change or land-time gate (operator ruling 2026-10-01); you MAY (SHOULD for shared infrastructure) build targeted `nix build .#checks.<system>.<name>` in the background.
- IF no tests exist for changed code: create them. NEVER claim code is complete without passing tests.
- **V-3** A check or verification of DETERMINISTIC code (CLI behaviour, parsing, state transitions, anything reproducible against a throwaway fixture) MUST live in an automated test that stays in the repo, run against the real tool in a hermetic environment where the tool's own semantics matter (a mock of that tool does not count). A one-off manual probe MAY diagnose, but the regression guard MUST land as a test in the same change. A post-deploy "verify" bead is for what a test cannot prove (that the installed artifact changed on a machine), MUST NOT be filed for logic a test can encode, and MUST cite the test as its evidence.

- **L-1** A command expected to outlive the 2m default (`nix build`/`nix flake check`, `go test ./...`, monorepo `git fetch|clone|push`, any `--all-files` hook run) MUST set an explicit `timeout` or use `run_in_background` + Monitor.
- **L-2** After a timeout, MUST NOT re-issue the SAME command unchanged; re-run in the background or with a larger timeout, narrowed if possible.
- **L-3** A subagent brief instructing a build, check, or full test run MUST state the timeout or say to run in the background. A brief instructing a nix run (`nix build|flake check|eval|run|fmt`, `darwin-rebuild`) MUST also name `pg-nix-log-wrapped` as the command prefix.

#### Scratch / Payload File Writes

- **V-1** A regenerated payload (commit message, PR body, report, export) MUST go to a FRESH unique scratchpad filename (`pr-body.2.md`, `mktemp`-style); renaming or re-spelling is NOT fresh.
- **V-2** If overwriting an existing path is required, MUST Read it first in this session, immediately before the Write (a `limit: 1` Read suffices).

### Beads & Workflow Lifecycle (see `beads-lifecycle` skill)

- Before any state-mutating `bd` command (create/update/close/dep), before parking/re-parking/escalating/releasing/accepting a bead, or before applying/checking/removing the `worktree-review` or `human` labels, MUST invoke the `beads-lifecycle` skill if not already done this session. `B-*`/`D-*`/`F-*`/`P-*`/`W-*` live there.
- **Handoff beads**: before reading, claiming, working, or closing a bead of type `handoff`, or before creating one (e.g. at wrap-up), invoke `beads-lifecycle:handoff-bead` (sole contract; an ambiguous bead is NOT a handoff).
- **B-1/B-2** Whatever claims a bead MUST release it before ending (every exit path ends `closed` or released); a release MUST clear the assignee in the SAME call: `bd update <id> --status open --assignee ""` (`--status open` alone is NOT a release).
- **B-5** Every agent/daemon claim (`--claim`, `bd ready --claim`, `--status in_progress`, `--assignee`) MUST carry `--actor "<session-id>[-<role>]"` (or `BEADS_ACTOR`) and MUST NOT resolve to the operator's name.
- **F-9** Before briefing a subagent to create, restore, or commit a missing artifact, invoke `beads-lifecycle` and run its `decided-against?` probe first (an absence MAY be a ruling).
- **F-1** Before parking, re-parking, escalating, releasing, or accepting work whose premise predates now, invoke `beads-lifecycle` and re-verify the premise per its probes.

### Beads Is The Issue Tracker For The Skills That Ask For One

- **T-1** When a skill asks for this repo's "issue tracker", the answer is beads; the binding is the `wayfinder-beads` skill (invoke it). MUST NOT fall back to local markdown, `.scratch/`, or GitHub Issues in a beads repo.
- **T-2** MUST NOT run `/setup-matt-pocock-skills`; changing trackers is an operator decision.
- **T-3** T-1 names a SKILL, never a path; MUST NOT reintroduce an absolute path.

### File Beads In The Tracker Of The Repo Where The Fix Lands

- **BF-1** File a bead in the tracker of the repo where the fix will LAND, not the cwd's repo: determine that repo first (work subject, repo `CLAUDE.md`/workspace lookup table), then run `bd -C <tracker-root> ...` against it.
- **BF-2** If the correct tracker is unreachable, MUST NOT fall back to another repo's tracker; report/escalate (or park the bead text in the session report) and retry later.
- **BF-3/BF-4** (recreate misfiled beads; read-back verification before reporting filed): see `beads-lifecycle`.

### Superseding Rulings

- **S-1** When an operator ruling SUPERSEDES an instruction in a BEAD BODY, amend that bead body in the SAME exchange as the ruling; recording it in a doc header, sibling bead, or session note does NOT count. A bead meant to instruct a later session (resume/next-session/handoff/follow-up) MUST be amended FIRST.
- **S-2** The amendment MUST SUPERSEDE, not accompany: rewrite or strike the old instruction (never leave two live instructions) and record the ruling verbatim with provenance (who ruled, when), so the F-9 `decided-against?` probe can grep it.

### Unpushed Landing Debt (see `session-wrapup:wrap-up-session`)

- **U-5** MUST NOT `git push`, `pn workspace push|update|apply`, or invoke `/pn-workspace-sync` or `/pn-workspace-update` on own initiative to clear unpushed work; REPORTING is in scope, PUBLISHING is not. (U-1..U-4, U-6: the wrap-up skill.)

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.
- When telling the user which file to view/open, ALWAYS give the full absolute path (many concurrent worktrees run across sessions).
- Default to NOT publishing work as a Claude Artifact; only on explicit request or when a shareable page is clearly the point, and ask before publishing proactively.

### Git Workflow

- Always commit to the correct branch: run `git branch --show-current` first; if changes landed on the wrong branch, alert the user before proceeding.
- When pre-commit hooks exist, run `git diff --cached --no-ext-diff` and address formatting/lint issues before committing; ensure subagent-generated changes are staged.
- Agent-run `git diff`/`git show`/`git log -p` MUST pass `--no-ext-diff` (`--stat`, `--name-only`, `--numstat`, `--check` and plumbing are unaffected).

### Git Worktree / Integration Discipline

Primary branch = `pgii-integrate-branch.primaryBranch` (git config) → `git symbolic-ref refs/remotes/origin/HEAD` → `main`.

- **R-1** The canonical clone MUST have its primary branch checked out as steady state.
- **R-2** Only the canonical clone MAY have the primary branch checked out; a worktree/workforest member MUST use a feature branch.
- **R-3** MUST NOT switch the canonical clone off its primary branch or leave it dirty in steady state; on finding it off-branch/dirty, stop and report (no reset, re-checkout, stash, or workaround).
- **R-4** By default an isolated single-repo change MUST be done in a git worktree.
- **R-5** The worktree (R-4) and workforest requirements MAY be overridden when the user explicitly says so.
- **R-6** For a very small/quick change the agent MAY commit directly on the primary branch in the canonical clone, but MUST first ask the user.
- **R-9** To integrate completed work, MUST invoke the Skill tool with the plugin-qualified id `integrate-branch:integrate-branch` (handlers `integrate-branch:ff-merge-to-main`, `integrate-branch:pull-request`; close-out `session-wrapup:wrap-up-session`). MUST NOT use `superpowers:finishing-a-development-branch`. (R-7/R-8: the `integrate-branch` skill.)

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request
- **CRITICAL**: NEVER use `sudo`
- Validate nix changes without activation using a build-only command

#### Version Control

- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval; MUST fix hook violations rather than bypass hooks.
- ZR monorepo ONLY: add `Refs: TICKET-ID` (`[A-Z]+-\d+`, from branch `username.TICKET-ID.description`) on the line after the commit subject; omit it for `NO-JIRA`/`NOJIRA` branches. Personal/nix repos: simple branch names, never a `Refs:` line. Agent-authored GitHub PR comments/reviews in ZR repos MUST include 🤖.

#### Waiting / Polling

- **CRITICAL**: NEVER wait by foreground `sleep` (policy-blocked).
- To wait on a background job's output or a file: `run_in_background`, then Monitor with an until-loop; MUST NOT poll with `sleep` plus `tail`/`cat`/`wc`.
- To wait on external state (PR merge, CI): Monitor with an until-loop, or one check at a delay matched to the state's change rate; never `sleep`-then-check.

#### Subagent Fork Dispatch

- **FK-1** A dispatched subagent (ANY `subagent_type`) MUST NOT call the Agent tool with `subagent_type: "fork"`; do the sub-tasks directly, or dispatch a non-fork type (e.g. `general-purpose`).
- **FK-2** A rejected `Fork is not available inside a forked worker` call MUST NOT be re-issued unchanged; adapt per FK-1.

#### Numeric Data

- **CRITICAL**: NEVER include calculated numbers without showing calculation method

#### Estimates

- **CRITICAL**: NEVER provide time estimates; signal effort with t-shirt sizes (S/M/L/XL)

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
