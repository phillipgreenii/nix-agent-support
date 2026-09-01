# Rules

> The section `## Rules for Interactive Sessions Only` applies only when working with the user directly.
> Autonomous agents invoked via `claude -p` (e.g. background workers)
> MUST ignore that section and apply only the rules under `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Mistake Acknowledgment Marker

> Purpose: make agent self-corrections MECHANICALLY COUNTABLE so their rate can be tracked over
> time. It is a WORDING convention only. Adopted 2026-07-30; it generates data FORWARD ONLY and
> cannot be backfilled, which is why it landed ahead of the tooling that will consume it.

- **M-1** When an acknowledgment of the agent's own error is warranted, its first words MUST be
  `Correction:` — one stem, at the start of the sentence, in user-visible text. Thinking blocks are
  excluded.
- **M-2** M-1 MUST NOT change HOW OFTEN the agent acknowledges anything. The threshold for whether
  an acknowledgment is warranted is set elsewhere and is unchanged: correct an earlier statement
  only when the error would change the user's code, conclusions, or decisions. Silent fixes stay
  silent and MUST NOT be marked. If M-1 would increase acknowledgment frequency, M-1 is being
  misapplied.
- **M-3** The agent MUST NOT add a second phrase distinguishing self-caught from user-caught. That
  provenance is derived from transcript structure (whether the preceding turn was a typed user
  prompt), so stating it is redundant and MUST NOT be attempted.

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Absolute-Path Provenance

> Observed 2026-07-30 (8-day census, 924 transcripts): 104 of 152 failed Reads named a root that
> does not exist on this machine — 99 `/home/…`, 4 `/mnt/user-data/…`, 1 `/repo/…` — across 86
> distinct sessions, worst single session 3, and 100% in the main loop rather than subagents. In
> the traced cases the task gave repo-RELATIVE paths and the agent, required to use absolute paths,
> FABRICATED a root instead of resolving against the session cwd. The failure text names the real
> cwd, so each one is a round trip spent asking for something the harness had already answered.

- **A-1** An absolute path MUST be built only from a root OBSERVED this session (the env block's
  working directory, a tool result, or the user's text).@KNOWN_ABSENT_ROOTS_SENTENCE@
- **A-2** Given a repo-relative path, resolve it as `<session-cwd>/<relative>`. If the root is
  uncertain, probe first (`ls` the parent, Glob the suffix, or `git ls-files -- '*<name>'`) — MUST
  NOT Read a guessed absolute path.
- **A-3** When briefing a subagent, the brief MUST state the absolute repo root once; a brief that
  lists relative paths without a root causes exactly this defect.

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has `.pre-commit-config.yaml` (test with `test -f .pre-commit-config.yaml && echo yes || echo no` — an exit-0 probe; do NOT probe by running the tool, and do NOT probe with bare `ls`, which exits nonzero on a missing file and is therefore itself a failed tool call — 19 such failures in the 8 days to 2026-07-30): the pre-commit hooks MUST pass on the **changed** files. The **commit's own hook run is the gate** — a `git commit` fires `prek`/`pre-commit` on the staged files (so `git add -A` first, or a generated change escapes the run). (How to validate before committing without over-running the hook suite: the `nix-how-to` path-rule, `.claude/rules/nix-how-to.md`.)
- If the project has `flake.nix` (same exit-0 probe: `test -f flake.nix && echo yes || echo no`): `nix flake check` MUST pass. (Build-only machine-config validation forms and the `darwin-rebuild check` caveat: the `nix-how-to` path-rule.)
- IF no tests exist for changed code: create them
- NEVER claim code is complete without passing tests

> Both gate OBLIGATIONS above stay here unconditionally (tc-ql0o Stage D, 2026-08-26): they
> trigger on a repo PROPERTY (does `.pre-commit-config.yaml`/`flake.nix` exist), not on reading a
> `.nix` file — a Go-only edit in a flake repo never reads one (the `pg2-3nb2t` class) — so a
> file-glob-triggered path-rule cannot carry the obligation itself, only the HOW-TO detail once
> you're already working with `.nix`/`flake.nix` files.

> Observed 2026-07-30 (8-day census): 127 Bash timeouts across 69 sessions — mostly `git`
> fetch/clone on the monorepo, `nix` builds/checks, and test loops re-issued unchanged after the
> first timeout. 73 of the 127 were subagent calls, which is why **L-3** exists.

- **L-1** A command expected to outlive the 2m default (`nix build` / `nix flake check`,
  `go test ./...`, monorepo `git fetch|clone|push`, `prek`/`pre-commit run --all-files`) MUST set an
  explicit `timeout`, or run via `run_in_background` and be watched with Monitor.
- **L-2** After a timeout, the SAME command MUST NOT be re-issued unchanged; re-run it in the
  background or with a larger explicit timeout, and narrow it if possible.
- **L-3** A subagent brief that instructs a build, check, or full test run MUST state the timeout to
  use, or say to run it in the background.

#### Scratch / Payload File Writes

> Observed 2026-07-30: 125 of 134 Write errors in the 3-month census were "File has not been read
> yet", and the mechanism is unchanged in the 8-day re-measure — 9 of 11 precondition failures were
> regenerated payloads in the scratchpad (`commitmsg.txt`, `pr-body.md`, `*.jsonl` exports)
> overwritten at a path this or a sibling session already wrote. In one session the agent alternated
> between `commitmsg.txt` and `commit-msg.txt` rather than using a fresh name.

- **V-1** A regenerated payload (commit message, PR body, report, export) MUST go to a FRESH unique
  filename in the scratchpad (e.g. `pr-body.2.md`, `mktemp`-style suffix), not overwrite the
  previous revision. Renaming or re-spelling the same file is NOT a fresh name.
- **V-2** If overwriting an existing path is genuinely required, it MUST be Read first in this
  session, immediately before the Write. A ranged Read suffices — verified 2026-07-30, a `limit: 1`
  Read of a 4-line file satisfied the precondition — so the cost is one cheap call, not reading a
  large file in full.

> Exit-code conventions, unit-test isolation, and structured-data-file tooling MOVED to the
> `code-file-standards` path-rule (`.claude/rules/code-file-standards.md`, tc-ql0o Stage D,
> 2026-08-26): each is scoped to a file type (shell/bats, source, or JSON/YAML/TOML), so it now
> rides in only when a matching file is read instead of every session unconditionally.

### Beads & Workflow Lifecycle (see `beads-lifecycle` skill)

> The full ruleset for beads claim/release hygiene (`B-*`), dependency-vs-human blocker
> modeling (`D-*`), handoff preconditions (`P-*`), premise freshness (`F-*`), and the
> worktree-review label lifecycle (`W-*`) MOVED to the `beads-lifecycle` skill (tc-ql0o Stage
> C, operator Decisions 1-3, 2026-08-25/26) to keep this file token-lean — each of those packs
> keys on an observable trigger (a `bd` verb, a park/release/accept action, a label mutation)
> that a MUST-invoke tripwire can gate on, so the full text no longer needs to ride in every
> session unconditionally. `S-1`/`S-2` and `T-1`..`T-3` stay HERE, unmoved: they have no such
> trigger (a conversation-time ruling and a bootstrap-paradox binding, respectively).

- Before running any `bd` command that mutates state (create/update/close/dep), or before
  parking/re-parking/escalating/releasing/accepting a bead, or before applying/checking/removing
  the `worktree-review` or `human` labels, an agent MUST invoke the `beads-lifecycle` skill
  first if it has not already done so this session.
- Rule-ID family map: `B-*`/`D-*`/`F-*`/`P-*`/`W-*` all live in `beads-lifecycle`.
- **B-1/B-2 essence** (always-on regardless of skill invocation, since a tool-restricted
  subagent with Bash but no Skill tool can still violate it): whatever claims a bead MUST
  release it before ending — every exit path MUST end `closed` or released — and a release
  MUST clear the assignee in the SAME `bd update` call as the status change
  (`--status open --assignee ""`; `--status open` alone is NOT a release).
- **F-9 pre-brief clause**: before briefing a subagent to create, restore, or commit a missing
  artifact, invoke `beads-lifecycle` and run its `decided-against?` probe first — an absence
  MAY be a ruling, not missing work.
- **F-1**: before parking, re-parking, escalating, releasing, or accepting work whose premise
  was recorded earlier than now, invoke `beads-lifecycle` and re-verify that premise against
  current reality per its probes.

### Beads Is The Issue Tracker For The Skills That Ask For One

> The `mattpocock-skills` plugin's skills (`/wayfinder`, `/triage`, `/to-tickets`, `/to-spec`)
> each read a per-repo "issue tracker" doc. They ship templates for GitHub, GitLab and local
> markdown only, and DEFAULT SILENTLY to local markdown when no tracker is provided — which
> would put planning state in `.scratch/` files, contradicting the beads-only rule. The beads
> binding is therefore written once and MUST be found from anywhere.

- **T-1** When a skill asks for this repo's "issue tracker", the answer is beads, and the
  binding is the **`wayfinder-beads` skill** — invoke it. It carries the `bd` operation
  mapping, `/wayfinder`'s "Wayfinding operations", and the triage label vocabulary. An agent
  MUST NOT fall back to local markdown, `.scratch/`, or GitHub Issues in a beads repo.
- **T-2** An agent MUST NOT run `/setup-matt-pocock-skills`. It would propose GitHub (a GitHub
  `git remote` is its default posture) and write its own tracker doc over the top. Changing
  trackers is an operator decision.
- **T-3** T-1 names a SKILL, never a path, and that is deliberate: the skill ships in this
  flake's nix-built marketplace, which `homeModules` registers automatically on every machine
  that imports it, so the binding needs no per-machine or per-repo file. An absolute path here
  would bind the rule to one checkout on one machine. MUST NOT reintroduce one.

### Superseding Rulings

> A bead body is what the autonomous queue HANDS to the next agent, so it is the one artifact a
> ruling MUST reach. Observed 2026-07-30 (`pg2-xx1y5`): an operator ruling ("do not commit the
> audit") was written into a doc header and two sibling beads but NOT into the RESUME bead, whose
> entire purpose was to instruct a later session. That bead was released to `/drain-beads` with its
> pre-ruling instruction intact; the drain session believed it and briefed a subagent to do the
> forbidden thing. The session that received the ruling never reached a release — a PEER released it
> — so a release-time duty would never have fired. The duty is at the moment the ruling lands.

- **S-1** When an operator ruling SUPERSEDES an instruction written in a BEAD BODY, that bead body
  MUST be amended in the SAME exchange as the ruling. Recording the ruling in adjacent artifacts — a
  doc header, a sibling bead, a session note — is NOT sufficient and MUST NOT be counted as
  propagation: the queue hands the next agent the BEAD, not the adjacent artifacts. A bead whose
  purpose is to instruct a later session (resume / next-session / handoff / follow-up) MUST be
  amended FIRST, not last.
- **S-2** The amendment MUST SUPERSEDE the instruction, not merely accompany it. Appending the
  ruling while the original instruction still reads as live leaves TWO live instructions and a later
  reader MAY act on either — the outcome S-1 exists to prevent. The superseded text MUST be
  rewritten or struck in the body, and the ruling MUST be recorded verbatim with its provenance (who
  ruled, when) so a later reader can tell an EXECUTED DECISION from an open question. That recorded
  ruling is also what the `beads-lifecycle` skill's F-9 `decided-against?` probe greps for.

### Unpushed Landing Debt (see `wrap-up-session` skill)

> A local ff-merge makes work LANDED, not PUBLISHED, and the debt REGENERATES on every land — so it
> is computable state that no record can hold, and a standing bead for it is a defect (`pg2-5subz`
> nearly orphaned 11 unrelated commits; its replacement `pg2-dawg2` pushed 12, closed correctly, and
> the debt was back within a day). Unpushed commits are NOT in themselves a problem, so the
> OBLIGATION IS TO LOOK WHEN IT MATTERS — not to narrate the count at every session end. The full
> derivation/never-standing-bead/read-only-probe/one-line-reporting contract (U-1..U-4, U-6) MOVED
> to the `session-wrapup:wrap-up-session` skill's `references/unpushed-landing-debt.md`
> (tc-ql0o Stage D, 2026-08-26): session close-out is exactly the observable moment this debt is
> assessed, so the skill invoked at that moment is a sufficient home for the full text.

- **U-5** Discharging the debt is OUTWARD-FACING and operator-authorized. An agent MUST NOT
  `git push`, `pn workspace push`, `pn workspace update`, or `pn workspace apply`, and MUST NOT
  invoke `/pn-workspace-sync` or `/pn-workspace-update`, on its own initiative to clear it.
  REPORTING is in scope; PUBLISHING is not. Trimming the reporting duty (**U-6**) does NOT relax
  this restraint. U-5 stays here, unmoved: it is a bare prohibition against acting on ANY
  push/apply/update at ANY moment, not only at session close-out, so it has no session-close-scoped
  trigger the moved skill could gate on (Design P1).

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.
- When telling the user which file to view/open (design docs, specs, code), ALWAYS give the full
  absolute path, never a repo-relative one — many concurrent worktrees/workforests run across
  sessions, so a relative path is ambiguous about which checkout is meant.
- Prefer NOT publishing work as a Claude Artifact by default. Only publish one when the user
  explicitly asks for it, or a shareable page is clearly the point of the task. This is a
  standing preference, not a ban — undecided whether to relax it later — but the default
  posture MUST be to ask before publishing rather than publish proactively.

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

> R-7 (concurrent-landing races) and R-8 (floating-branch halt) MOVED to the `integrate-branch`
> skill (tc-ql0o Stage D, 2026-08-26): both key on the landing moment itself, which R-9 below
> already forces every integration through, so the skill invoked at exactly that moment is a
> sufficient — and more complete — home for them than a core bullet. `ff-merge-to-main`'s FF-0a
> halt and FF-3 retry-and-stop-at-2 loop already implement R-8 and R-7 respectively in executable
> form.

- **R-9 (integration entry point)** To integrate completed work, the agent MUST invoke the Skill tool with the plugin-qualified id `integrate-branch:integrate-branch` (handlers: `integrate-branch:ff-merge-to-main`, `integrate-branch:pull-request`; session close-out is `session-wrapup:wrap-up-session`). Qualified ids are the form the Skill tool documents for plugin skills, and they are unambiguous where a bare name is not: a bare name can resolve to a different plugin's skill silently, whereas a stale qualified id fails loudly as `Unknown skill: <id>`. Bare names DO currently resolve — verified 2026-07-30: 7 bare `integrate-branch` invocations succeeded among 199 Skill calls over 8 days — so this is a SPECIFICITY requirement, NOT a fix for a live failure, and MUST NOT be cited as evidence of one. The agent MUST NOT use `superpowers:finishing-a-development-branch` (plain non-ff merge, no rebase).

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request — these are user-only commands
- **CRITICAL**: NEVER use `sudo`
- When building/validating nix changes without activation, use a build-only command

#### Version Control

- **ZR monorepo ONLY** (the employer's private org/monorepo): include the Jira issue as `Refs: TICKET-ID` on the line immediately after the subject (before the body). Extract the ticket ID from the branch name (format: `username.TICKET-ID.description`). A valid ticket ID matches `[A-Z]+-\d+` (e.g., `PROJ-9208`, `CI-1494`). If the branch contains `NO-JIRA`, `NOJIRA`, or any variation instead of a real ticket ID, omit the `Refs:` line entirely. In personal/nix repos (the phillipg_mbp workspace and similar), the ticket-branch format does NOT apply: use simple branch names (e.g. `fix-foo`) and never add a `Refs:` line.
- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval
- IF git hooks report violations: MUST fix the violations rather than bypassing hooks
- Agent-authored GitHub PR comments/reviews (ZR repos) MUST include 🤖 in the body — a hook rejects them otherwise (12 rejected-and-retried comment bodies in the 3-month census; 1 in the 8 days to 2026-07-30)

#### Waiting / Polling

> Observed 2026-07-30 (8-day census): 26 foreground-`sleep` blocks across 26 DISTINCT sessions —
> exactly one each, so the reflex is re-learned from scratch every time. 21 of 26 were subagents. 12
> of 26 were `sleep N` followed by `tail`/`cat`/`wc -c` on a background job's scratchpad log, which
> is the exact case Monitor exists for. The Bash tool description already states this prohibition and
> is demonstrably not sufficient on its own.

- **CRITICAL**: NEVER wait by foreground `sleep` — it is policy-blocked, so the call is a guaranteed
  wasted round trip.
- To wait for a background job's output to change or a file to appear: `run_in_background`, then
  Monitor with an until-loop. MUST NOT poll it with `sleep` plus `tail`/`cat`/`wc`.
- To wait on external state (a PR merging, a CI run finishing): Monitor with an until-loop, or a
  single check at a delay matched to how fast that state actually changes — never a `sleep`-then-check
  pair.

#### Subagent Fork Dispatch

> Observed in the improvement retro for 2026-08-17→08-31 (bead `pg2-yeh5f`): "Fork is not
> available inside a forked worker" fired 47 times across 6 sessions (worst 18), 0 main-loop / 47
> subagent. The rejecting condition is being ALREADY a dispatched subagent, not being specifically
> a `fork`-type one — `general-purpose` workers hit the same rejection when they themselves tried
> `subagent_type: "fork"`. 77 retry chains (70 FAILED retries) show workers re-issuing the
> identical rejected call instead of adapting.

- **FK-1** If you are running as a dispatched subagent — of ANY `subagent_type`, fork or
  general-purpose or otherwise — and you face independent sub-tasks, you MUST NOT call the Agent
  tool with `subagent_type: "fork"`. Forking is unavailable from inside any already-dispatched
  subagent and the harness rejects it. Do the sub-tasks directly (yourself, in sequence, with your
  own tools), or dispatch a non-fork agent type (e.g. `general-purpose`) instead.
- **FK-2** A rejected `Fork is not available inside a forked worker` result MUST NOT be re-issued
  unchanged — the identical call fails again every time. Adapt per FK-1 instead.

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
