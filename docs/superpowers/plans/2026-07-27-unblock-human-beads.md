# `/unblock-human-beads` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/unblock-human-beads` slash command that drains the
`bd ready --label human` queue by lifting each bead's human blocker and releasing it
back to `/drain-beads`, and give both commands narrow-only `$ARGUMENTS` query
restriction.

**Architecture:** Two prompt-doc command files in the `pb` plugin. The new command
mirrors `/drain-beads`' claim→understand→act→loop shape but its per-bead action is
"do only enough to lift the human blocker, then RELEASE" (never complete). The only
edit to `drain-beads.md` is additive `$ARGUMENTS` support. No executable code — the
"tests" are the repo's `prek` + `nix flake check` gates plus structural greps proving
the spec's load-bearing invariants are present.

**Tech Stack:** Markdown prompt-docs (Claude Code slash commands); the `pb` plugin under
`claude-marketplace/`; `bd` (beads) CLI; nix flake + `prek`/pre-commit + prettier gates.

**Spec (authority for all content):**
`docs/superpowers/specs/2026-07-27-unblock-human-beads-command-design.md` (commit
`fd87d95b`). Every section requirement below traces to that spec; when in doubt, the spec
wins.

## Global Constraints

- **Mirror `drain-beads.md`** in tone, structure, and section style; do not invent a new
  format. Read it first.
- **RFC 2119 language** (MUST/SHOULD/MAY) in the invariants section.
- **Markdown conventions** (repo `CLAUDE.md`): wrap glob patterns, paths with underscores,
  and identifiers in backticks so prettier does not treat them as emphasis. Use one
  fenced ```mermaid block for the loop diagram.
- **Sourcing invariant is load-bearing:** the new command sources work ONLY from
  `bd ready --claim --label human`; it MUST NOT use `bd list --label human` as a work
  source and MUST NOT pass `--include-deferred`. Include the future-editor rationale in
  the doc.
- **Distinct actor id:** the new command uses `${CLAUDE_SESSION_ID}-unblock` (never a bare
  `$CLAUDE_SESSION_ID`).
- **Validation before "done":** `prek run --all-files` (or `pre-commit run --all-files`)
  MUST pass and `nix flake check` MUST pass. Generate the hook config first with
  `nix run .#install-pre-commit-hooks` if `.pre-commit-config.yaml` is absent in the
  worktree.
- **Git:** work on branch `unblock-human-beads` in the worktree
  `.worktrees/unblock-human-beads`; the canonical clone stays on `main`. Commit messages
  omit a `Refs:` line (no ticket in the branch name). Never use `--no-verify`.
- **Landing** (after all tasks pass) is via the `integrate-branch` skill (local
  ff-merge) — NOT part of a task; do it only when the operator says to land.

---

## File Structure

- `claude-marketplace/pb/commands/unblock-human-beads.md` — **new.** The command
  prompt-doc. One responsibility: orchestrate the human-queue unblock loop.
- `claude-marketplace/pb/commands/drain-beads.md` — **modify (additive only).** Gains
  `argument-hint` frontmatter + one "Optional scope arguments" section + a one-line
  pointer in its CLAIM step. No behavioral change otherwise.

No other files change. (The plugin auto-discovers command `.md` files; no manifest edit
is needed — verified: `.claude-plugin/plugin.json` does not enumerate commands.)

---

### Task 1: Author `unblock-human-beads.md`

**Files:**

- Create: `claude-marketplace/pb/commands/unblock-human-beads.md`
- Read first (pattern source): `claude-marketplace/pb/commands/drain-beads.md`
- Spec: `docs/superpowers/specs/2026-07-27-unblock-human-beads-command-design.md`

**Interfaces:**

- Consumes: nothing (leaf prompt-doc).
- Produces: the `/unblock-human-beads` command; and the exact frontmatter `description`
  wording Task 3's discovery check greps for.

- [ ] **Step 1: Read the mirror and the spec.**

Run: `sed -n '1,60p' claude-marketplace/pb/commands/drain-beads.md` and read the full
spec. Match drain's heading style, the actor-id framing, and the mermaid/Rules/limitations
layout.

- [ ] **Step 2: Write the file with this exact frontmatter, then the body.**

Frontmatter (verbatim):

```markdown
---
description: >-
  Drain this pn-workspace's `bd ready --label human` queue by UNBLOCKING — the
  human-queue counterpart to /drain-beads. Loops: atomically claim one parked
  `human` bead under a distinct `-unblock` actor id, do ONLY enough to lift the
  human blocker (any kind of action, reusing drain's parked worktree/set), then
  RELEASE it back to the drain pool. It does NOT complete, land, or close beads
  (except operator-confirmed obsolete, or an in-session-resolved substrate bead).
  Assumes `pn workspace apply` ran before invocation. Parallel-safe via atomic
  claims; accepts optional narrowing $ARGUMENTS.
argument-hint: "[optional narrowing scope: a bead id, --label X, --priority N, --parent ID, or 'one']"
---
```

The body MUST contain these sections, each authored from the correspondingly-named spec
content (no placeholders — write the real prose):

1. **Intro** — one paragraph: this is the `human`-queue counterpart to `/drain-beads`; it
   removes human blockers so a separately-running `/drain-beads` finishes the work; it
   works autonomously until the queue (within any `$ARGUMENTS` scope) is empty; use `bd`
   for all tracking.
2. **Actor id** — `${CLAUDE_SESSION_ID}-unblock` (fallbacks per spec), and the load-bearing
   reason for the `-unblock` suffix (drain's label-unfiltered resume, `drain-beads.md:59`).
3. **Sourcing invariant (with future-editor rationale)** — claim ONLY via
   `bd ready --claim --label human`; never `bd list --label human` as a source; never
   `--include-deferred`; deferred-safety is by construction.
4. **Resume/startup** — `bd prime`; recover own unfinished bead via
   `bd list --status in_progress --assignee "ID" --label human --json`.
5. **Main loop** — CLAIM (`bd ready --claim --label human [+narrowing] --actor "ID"
--json`; empty → STOP; skip-set id → STOP; transient error → retry) → UNDERSTAND
   (`bd show`, read `stuck:` comment + parked isolation) → TRIAGE+UNBLOCK → terminal action
   → loop.
6. **Triage rubric (ordered table, first match wins)** — the 5 classes verbatim from the
   spec: (1) substrate-mutating [recognize incl. the `worktree-review` label; ENGAGE,
   NEVER RELEASE to drain], (2) apply-waiting [RELEASE, trust the pre-apply premise],
   (3) mislabeled/normal-work [RELEASE], (4) genuine decision/input [ENGAGE], (5) uncertain
   [ENGAGE]. Include the "apply-waiting = trust, always" paragraph.
7. **Terminal actions** — RELEASE (single atomic
   `bd update <id> --remove-label human --status open --assignee "" --actor "ID"` after the
   `bd comment` + any commit), CLOSE (operator-confirmed obsolete or in-session-resolved
   substrate; if a worktree is left, file a `worktree-review` follow-up with
   `--add-label human --defer +<window>`), DEFER (operator-initiated / can't-now: comment
   why → `bd update --defer +<window ≥1d> --status open --assignee ""` keeping `human`; add
   id to the session skip-set).
8. **Isolation: reuse vs create** — reuse existing directly (cd in, commit, never clean
   up); create single-repo only at `.worktrees/<id>` on `drain/<id>`; never fork a fresh
   multi-repo set mid-session.
9. **Optional scope arguments (`$ARGUMENTS`)** — narrow-only; never broaden or remove the
   `--label human` / deferred safety filters; the safe specific-bead-id path (confirm the
   id is in `bd ready --label human [scope]`, then claim it).
10. **Invariants (RFC 2119)** — copy the spec's invariant list (sourcing, minimality+stop
    predicate, RELEASE-only-when-drain-can-progress, substrate guard, atomic-release
    ordering, reuse, DEFER-termination [≥`+1d` + skip-set], distinct-actor, close-guard,
    arguments-narrow-only, actor discipline).
11. **Concurrency & known limitations** — parallel-safe claims (with the honest
    operator-serialization caveat), runs alongside `/drain-beads`, stranded orphans,
    apply-waiting trust, in_progress human beads untouched.
12. **Mermaid loop diagram** — the flowchart from the spec (start→resume→claim→understand
    →triage→terminal→loop, incl. the skip-set STOP edge and substrate-never-RELEASE).

- [ ] **Step 3: Ensure the hook config exists, then format.**

Run: `ls .pre-commit-config.yaml || nix run .#install-pre-commit-hooks`
Then: `git add claude-marketplace/pb/commands/unblock-human-beads.md && prek run --files claude-marketplace/pb/commands/unblock-human-beads.md`
Expected: after at most one auto-format pass, all hooks (treefmt/prettier, eof, trailing-ws) PASS. Re-`git add` if treefmt rewrote the file.

- [ ] **Step 4: Structural acceptance checks (the "tests" for a prompt-doc).**

Run each grep; every one MUST match (proves a load-bearing invariant is present):

````bash
f=claude-marketplace/pb/commands/unblock-human-beads.md
grep -q 'argument-hint:' "$f"                                        # args feature
grep -q -- '-unblock' "$f"                                           # distinct actor
grep -q 'bd ready --claim --label human' "$f"                        # sourcing
grep -q -i 'never.*release\|MUST NOT be RELEASEd\|NEVER RELEASE' "$f" # substrate guard
grep -q 'worktree-review' "$f"                                       # substrate recognition
grep -q -- '--remove-label human --status open --assignee' "$f"      # atomic release
grep -q -i 'skip-set\|skip set' "$f"                                 # defer termination
grep -q -i 'MUST NOT.*broaden\|narrow' "$f"                          # args narrow-only
grep -q '```mermaid' "$f"                                            # loop diagram
grep -qi 'do not\|does not\|MUST NOT.*complet' "$f"                  # anti-complete guard
````

Expected: all exit 0. If any fails, the corresponding section is missing content — add it
and re-run Step 3.

- [ ] **Step 5: Commit.**

```bash
git add claude-marketplace/pb/commands/unblock-human-beads.md
git commit -m "feat(pb): add /unblock-human-beads command"
```

---

### Task 2: Add narrow-only `$ARGUMENTS` support to `drain-beads.md`

**Files:**

- Modify: `claude-marketplace/pb/commands/drain-beads.md` (frontmatter + one new section +
  one CLAIM-step pointer)

**Interfaces:**

- Consumes: the argument contract defined in Task 1's spec (identical semantics).
- Produces: no new interface; additive documentation only.

- [ ] **Step 1: Add `argument-hint` to the frontmatter.**

In the `---` frontmatter block, immediately after the `description:` value's closing line,
add:

```markdown
argument-hint: "[optional narrowing scope: a bead id, --label X, --priority N, --parent ID, or 'one']"
```

- [ ] **Step 2: Add a scope-arguments section.**

Insert a new section immediately before `## Rules` (verbatim):

```markdown
## Optional scope arguments

This command MAY be invoked with additional context (`$ARGUMENTS`) that further
**restricts** the work it claims — e.g. an extra label, a priority, a parent/epic, a
type, a specific bead id, or a one-bead / N-bead limit ("just one"). Apply it as extra
`bd ready` filters on the CLAIM query, and honor a specific bead id via the safe path:
confirm the id appears in `bd ready --exclude-label human [scope] --json` (ready,
in-scope, not deferred, not `human`), then claim that id.

Arguments may only NARROW the query. They MUST NOT broaden scope and MUST NOT remove the
safety filters — `--exclude-label human` and the default deferred-exclusion always
remain. With no arguments, behavior is unchanged.
```

- [ ] **Step 3: Point the CLAIM step at the scope arguments.**

In the main-loop CLAIM step, after the existing `bd ready --claim --exclude-label human
--actor "ID" --json` description, add the sentence:

```markdown
If the invocation supplied `$ARGUMENTS`, apply them as additional NARROWING filters here
(see "Optional scope arguments"); they never remove `--exclude-label human` or the
deferred exclusion.
```

- [ ] **Step 4: Format + verify additive-only.**

Run: `git add claude-marketplace/pb/commands/drain-beads.md && prek run --files claude-marketplace/pb/commands/drain-beads.md`
Expected: hooks PASS (re-`git add` if treefmt rewrote it).
Run: `git diff --cached --stat` and confirm ONLY `drain-beads.md` changed, additively.
Run: `grep -q 'argument-hint:' claude-marketplace/pb/commands/drain-beads.md && grep -q 'Optional scope arguments' claude-marketplace/pb/commands/drain-beads.md && grep -q -- '--exclude-label human' claude-marketplace/pb/commands/drain-beads.md`
Expected: exit 0 (safety filter still present; feature added).

- [ ] **Step 5: Commit.**

```bash
git add claude-marketplace/pb/commands/drain-beads.md
git commit -m "feat(pb): accept narrowing \$ARGUMENTS in /drain-beads"
```

---

### Task 3: Whole-repo validation gate

**Files:** none changed — this task only runs the gates that gate "done".

- [ ] **Step 1: Full pre-commit.**

Run: `prek run --all-files` (or `pre-commit run --all-files`)
Expected: all hooks PASS across the repo. If treefmt rewrote either command file, re-add
and re-commit into the owning task's commit (`git commit --amend` on that file only if it
is the tip; otherwise a small `style:` fixup commit).

- [ ] **Step 2: Flake check.**

Run: `nix flake check`
Expected: PASS (the flake, incl. however the `claude-marketplace` plugin set is assembled,
still evaluates and builds). If it fails, read the error — a malformed command frontmatter
or a plugin-manifest assumption is the likely cause — fix in the owning task and re-run.

- [ ] **Step 3: Discovery sanity check.**

Confirm both commands are present and well-formed:

```bash
ls claude-marketplace/pb/commands/unblock-human-beads.md claude-marketplace/pb/commands/drain-beads.md
head -1 claude-marketplace/pb/commands/unblock-human-beads.md   # expect: ---
```

Expected: both files exist; the new file starts with a `---` frontmatter fence.

- [ ] **Step 4: No commit** (validation-only). If Steps 1–2 required a fixup, that commit
      was made in its owning task. Report the final `git log --oneline -3` and that
      `prek run --all-files` and `nix flake check` both pass.

---

## Self-Review

**1. Spec coverage.** Every spec section maps to a task: Context/Goals/rubric/terminal
actions/isolation/invariants/limitations/mermaid → Task 1 section list; the `$ARGUMENTS`
"Shared feature" + the "one change to drain-beads" → Task 2; Validation → Task 3. The
`pg2-umg05` retirement and apply-waiting→gate migration are explicitly out-of-scope in the
spec and correctly have no task.

**2. Placeholder scan.** Frontmatter and the drain-beads insertions are given verbatim;
the new command's body is specified as a concrete, spec-traced section list with grep-based
acceptance checks rather than vague prose — no "TBD"/"add error handling"/"similar to"
placeholders.

**3. Type/name consistency.** The actor-id form (`${CLAUDE_SESSION_ID}-unblock`), the
atomic release command (`bd update <id> --remove-label human --status open --assignee ""`),
the claim query (`bd ready --claim --label human`), and the `worktree-review` label are
identical across the spec, Task 1's section list, and Task 1's grep checks.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-27-unblock-human-beads.md`. Two
execution options:

1. **Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.
