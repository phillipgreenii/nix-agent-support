---
name: integrate-branch
description: Use when integrating/landing completed work on the current branch — runs integrate-branch-support, then delegates to the right handler (ff-merge-to-main / pull-request / a declared custom handler). Adapts to the repo automatically.
---

# Integrate the current branch

This skill is the **single entry point** for landing completed work on the current
branch. It is a **Strategy-pattern dispatcher**: it runs an advisory tool to gather
facts, decides which integration method applies, and delegates to a **Command-style
handler skill** that actually does the work. Do not hand-roll a rebase + merge, and
do not use `superpowers:finishing-a-development-branch` (plain non-fast-forward
merge, no rebase) — this skill is the sanctioned replacement.

**The advisory tool never decides.** `integrate-branch-support` reports facts and,
when it can, a recommended `strategy` — it never returns `ask`, `halt`, or `none`.
Those are judgment calls this skill makes, using the report **plus whatever else you
already know** about the repo and the conversation. Read the whole report before
acting; do not act on `strategy` alone.

## Step 1 — Run the advisory tool

Run the bare command (it is on `PATH`):

```bash
integrate-branch-support
```

It prints one JSON object and exits nonzero only when it could not even determine
the repo's facts (not a git repo, hard error) — treat a nonzero exit as "could not
determine," surface that, and fall back to asking. On success you get:

```json
{
  "strategy": "pull-request", // or null when the tool can't determine one
  "reason": "open PR #42 found for feat-x",
  "primary_branch": "main",
  "canonical": { "branch": "main", "dirty": false },
  "remote": "origin", // null when the repo has no remote
  "open_pr": { "number": 42, "state": "open", "url": "…" }, // null when none
  "mr_bead": "pg2-abcd" // null when beads is unavailable or none exists
}
```

| Field              | Meaning                                        | Why it matters here                                                               |
| ------------------ | ---------------------------------------------- | --------------------------------------------------------------------------------- |
| `strategy`         | recommended method, or `null`                  | advisory only; `null` means you must decide (Step 5)                              |
| `reason`           | human-readable explanation                     | summarize to the user; also tells you "declared" vs "inferred" provenance         |
| `primary_branch`   | the repo's resolved primary branch             | compare against your own current branch; the handler's target                     |
| `canonical.branch` | what the canonical clone (main worktree) is on | anomaly check (Step 3) — MUST equal `primary_branch` in steady state (Tier R R-1) |
| `canonical.dirty`  | whether the canonical clone has local changes  | anomaly check (Step 3) — a canonical clone MUST be clean in steady state (R-3)    |
| `remote`           | remote name, or `null`                         | no remote ⇒ `pull-request` is infeasible                                          |
| `open_pr`          | PR number/state/url, or `null`                 | corroborates a `pull-request` strategy                                            |
| `mr_bead`          | merge-request tracker bead id, or `null`       | optional corroborating signal; absence never fails the tool                       |

## Step 2 — Nothing to integrate?

Before anything else, check whether there is actually work to land. Report and
**stop** — do not run a handler — when any of these hold:

- Your current branch **is** `primary_branch` (compare `git rev-parse --abbrev-ref
HEAD` to the report's `primary_branch`).
- `HEAD` is **detached** (`git rev-parse --abbrev-ref HEAD` prints `HEAD`) — there is
  no feature branch to integrate.
- You have **0 commits ahead** of `primary_branch`
  (`git rev-list --count <primary_branch>..HEAD` is `0`).

Report this plainly ("already on `main`", "nothing to land — 0 commits ahead of
`main`") and end here. Do not invoke a handler for a no-op.

## Step 3 — Surface canonical anomalies

If `canonical.branch != primary_branch` or `canonical.dirty` is `true`, the
canonical clone (the repo's main working tree, not necessarily where you're
standing) is in a state Tier R says it MUST NOT be in during steady state (R-1,
R-3). **Always surface this to the user** — never silently normalize it.

Whether it also **blocks** integration depends on the method, and that is a
property of the handler, not of this skill:

- A **canonical-advancing** method (one that lands work by moving the canonical
  clone's primary branch, e.g. `ff-merge-to-main`) MUST halt on this anomaly rather
  than merge onto the wrong branch or into a dirty tree (R-8). Its own precondition
  check (e.g. `ff-merge-to-main`'s FF-0) re-verifies and enforces this — you do not
  need to duplicate the check here, but you MUST NOT talk yourself past it or invoke
  the handler expecting it to "just work around" the anomaly.
- A method that never touches the canonical primary branch (e.g. `pull-request`)
  surfaces the anomaly and **proceeds** — it has nothing to lose by the canonical
  clone being off-branch or dirty.

So: report the anomaly now; let the handler you invoke in Step 4 decide (per its own
contract) whether that report ends in a halt.

## Step 4 — `strategy` is set: invoke the handler, if it's feasible

When the report names a `strategy`:

1. **Confirm it names an installed handler skill.** The `strategy` string IS the
   handler skill's name (e.g. `ff-merge-to-main`, `pull-request`, or an org-declared
   custom handler). Check it's actually available before invoking it — an unknown
   or misspelled `strategy` (e.g. from a stale or hand-edited `git config
pgii-integrate-branch.strategy`) is a fail-safe you MUST catch here, not something
   to guess your way past.
   - **Unknown/not installed** → do not invoke anything. Surface the conflict
     ("declared strategy `foo` has no matching handler skill") and ask the user.
2. **Confirm it's feasible given the facts.** A declared strategy can be correctly
   spelled but impossible right now — e.g. `strategy: "pull-request"` with
   `remote: null` (no remote, so there is nothing to push to and nowhere to open a
   PR).
   - **Infeasible** → do not invoke it. Surface the conflict and ask the user (they
     may want to add a remote, or re-declare a different strategy).
3. **Installed and feasible** → invoke that handler skill (via the `Skill` tool,
   using the strategy string as the skill name). Relay the handler's outcome
   (`landed | pr-opened | stopped:<reason>`) to the user, and when `reason` in the
   report indicated the strategy was _inferred_ rather than _declared_, say so —
   the user should know the method wasn't pinned by config.

Today's shipped handlers are `ff-merge-to-main` (rebase-first fast-forward merge
into the canonical clone's primary branch, then retire the worktree/branch) and
`pull-request` (push the branch, open or update a PR, never auto-merge). Both are
sibling skills in this marketplace; this skill does not implement their mechanics —
it only decides which one applies and hands off. A repo MAY declare a third-party or
org-specific handler by name via `git config pgii-integrate-branch.strategy`; as
long as a same-named skill is installed, this dispatcher treats it identically to
the two built-ins.

## Step 5 — `strategy` is `null`: decide, or ask

A `null` strategy means the tool could not infer one from remote/PR/bead signals and
nothing was declared (`git config` unset). This does **not** mean you're stuck:

1. **Decide from the facts plus your own context.** You may know things the tool
   doesn't — e.g. the user just said "open a PR for this," or the conversation
   established this is a scratch/local-only repo. If that context clearly resolves
   the ambiguity, proceed as if that were the `strategy` (re-running Step 4's
   feasibility check against it).
2. **Still unsure → ask the user.** Summarize the report's `reason` (why the tool
   couldn't infer one) and ask which method to use. Offer to persist the answer so
   future runs in this repo don't hit the same ambiguity:

   ```bash
   git config pgii-integrate-branch.strategy ff-merge-to-main   # or: pull-request
   ```

Never guess silently on a `null` strategy — an incorrect guess here either lands
work the wrong way (see Step 3's anomaly discussion) or pushes/opens a PR the user
didn't ask for.

## Decision flow

```mermaid
flowchart TD
    U["User: integrate-branch"] --> S["integrate-branch skill"]
    S --> T["run integrate-branch-support → advisory report"]
    T --> DEC{"agent decides (report + its own context)"}
    DEC -->|nothing to integrate: on primary / detached / 0 ahead| NONE["report: nothing to land"]
    DEC -->|strategy resolved (declared, inferred, or agent-decided) & feasible| H["invoke matching handler skill"]
    DEC -->|declared-but-infeasible, or cannot decide| ASK["ask the user"]
    H --> H1["ff-merge-to-main (§4.5) — FF-0 halts if canonical off-primary/dirty (R-8/R-3)"]
    H --> H2["pull-request (push+PR; never auto-merge; surface canonical anomaly)"]
```

## Rules this skill enforces (Tier R, RFC 2119)

- The agent MUST run `integrate-branch-support` before deciding anything — never
  skip straight to a handler on assumption.
- The agent MUST treat the tool's output as advisory: it MUST NOT invoke a handler
  whose `strategy` is unset, unknown, or infeasible without first asking the user.
- The agent MUST surface a canonical-clone anomaly (`canonical.branch !=
primary_branch`, or `canonical.dirty`) whenever the report shows one, regardless of
  which handler runs (R-3).
- The agent MUST NOT paper over a canonical anomaly to force a canonical-advancing
  method through — that decision belongs to the handler's own precondition check
  (R-8), and this skill MUST NOT second-guess it.
- The agent MUST NOT use `superpowers:finishing-a-development-branch` to integrate a
  branch in a repo where this skill applies (R-9).
- The agent SHOULD offer to persist a user's answer via `git config
pgii-integrate-branch.strategy` when it resolves a `null`/ambiguous strategy, so the
  next integration in this repo doesn't repeat the question.
