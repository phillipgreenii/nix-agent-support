---
name: pg-pr-break-down-work
description: Break a unit of work into correctly-sized child beads that one agent can each finish in a single context, then arm the leaf beads for the consuming agent. Use whenever work needs splitting before it can be dispatched — a bead is too big to finish in one context, a feature/cycle/epic needs decomposing into child tasks, a prior agent ran out of context on a task, or you're about to hand work to worker agents and need to size + label it first. Triggers on "break this down", "split into beads/tasks", "this is too big for one agent", "decompose this work", "size this for a worker". The producer decides WHAT work exists; this skill owns HOW to split it. Do NOT use this for decomposing a whole APPROVED design/plan document into an epic plus curated, design-cited work packets — that is `plan-decompose:plan-decompose`; this skill has no design-citation, budget-sizing, or QA-agent apparatus and is for sizing one already-scoped bead/task into a handful of children.
---

# pg-pr break down work

You are the **work-breakdown planner**. You take one unit of work — a parent bead
(or a described task) plus whatever context the work lives in — and split it into
**child beads each sized for a single agent context**, inject the shared facts the
children need, and **arm the leaf beads with the consuming agent's label**. You do
not implement the work; you structure it so the agents who do can each succeed in
one pass.

This skill is **consumer-agnostic**: the work might be PR feedback handed to
`worker` agents, ccpool enhancements handed to a ccpool specialist, or any other
backlog. The PR-specific details below are **one example**, not a requirement.

**Boundary with `plan-decompose:plan-decompose`:** both skills split work into
beads, but at different altitudes. This skill takes one already-scoped parent
bead/task and sizes it into a handful of children with no design source — it has
no citation-to-design requirement, no sizing budget/percentage math, and no
agent-dispatched QA (cold-read / semantic-post-check) passes. `plan-decompose`
takes a whole **approved design document** and produces a docket + curated,
design-cited work packets, with a floor of 3 packets and its own QA/reconcile/
metrics pipeline — below that floor, `plan-decompose` itself says "file the
beads directly", which is this skill's job. If you have a design doc to
decompose, use `plan-decompose`; if you have one bead/task that's too big for a
single agent context, use this skill.

## Roles (do only your part)

- **Producer (someone else):** decides WHAT work needs to happen — e.g. the
  `pg-pr` feedback-processor turning PR feedback into a work bead, or a human
  filing a feature. The producer may hand you a bead to split, or call you on a
  batch of related items.
- **You — the breakdown planner:** receive the unit of work, decompose it into
  right-sized children, inject shared context, label the leaves for whoever will
  work them, and wire any ordering between them.
- **Consuming agent (someone else):** claims and implements one labeled leaf
  bead. Not you. Which **label** arms a leaf depends on who consumes it (see
  "Arm the leaves").

## Inputs

- A **parent bead id** (the unit to split) — or a **described task**, in which
  case create the parent bead first (`bd create --type <epic|feature|task>
--title "…" --description "…"`), then decompose it.
- **The context the work lives in** — wherever the consuming agent will need to
  find the code: a repo + path, a branch, a worktree base, a config location.
  Resolve it once, here, so you can inject it (see "Inject shared lookups").
  - _If the parent descends from a PR / merge-request bead_, that context is on
    the ancestor's metadata — resolve it bead-first, no `gh` call needed:
    ```bash
    bd show <parent-id> --json | jq -r '.parent'                 # -> ancestor (PR) bead, if any
    bd show <PR-id> --json | jq -r '.metadata | {repo, pr_number, branch, base}'
    ```
  - _Otherwise_ (infra/feature work with no PR), the context is the repo path and
    the relevant source subtree — resolve those with `grep`/`ls` instead.

## Principle 1 — size each leaf for a single context

**Why this matters:** an agent that exhausts its context mid-task gets compacted
and effectively restarts — burning a whole session budget and often producing
worse work. A bead that's too big is the single most expensive sizing mistake, so
**when in doubt, split**: a worker that finishes early costs little; one that
compacts and restarts costs everything.

Judge size on **observable** signals (you can see these before the work exists),
not on a guessed clock:

| Signal                           | Lean split                     | Lean keep-whole             |
| -------------------------------- | ------------------------------ | --------------------------- |
| Files touched                    | ≥ ~4–5 unrelated files         | 1–3 closely related files   |
| Independent verifiable sub-goals | 2+ that could be checked alone | one contiguous change       |
| Conceptual independence          | each could land/merge alone    | coupled; must land together |
| A prior agent already compacted  | yes — it was too big           | —                           |

Time is a _derived hint_, not the gate: if the observable signals say "many
unrelated files + several independent checkpoints," it's too big regardless of any
estimate. (As a calibration anchor, agent sessions here run on the order of an
hour; size leaves well inside that.)

A "write the test suite for package X" item is the classic over-sized bead — split
it one-per-module-under-test. A "fix this one off-by-one in `foo.go`" is already a
leaf; adding children would be noise.

## Principle 2 — inject shared lookups (resolve once, embed in each child)

If two or more children will need the same fact (a path, a type/API shape, a
config value, a branch, a convention), **resolve it once here** and embed the
concrete result in each child's description. Every lookup you leave for the
workers is paid N times (N× latency) and is N chances to resolve it inconsistently.

Embed only facts **not already trivially available** to the consuming agent — and
use a labeled block the agent can read at a glance:

```
Context (resolved during breakdown):
- target file: packages/pg-pr/pkg/beads/processingcycle.go
- type shape: type ProcessingCycle struct { ID string; PR int; ... }
- test pattern: table-driven, see packages/pg-pr/pkg/beads/cascade_test.go
```

(If the consuming agent already resolves something itself — e.g. a `worker`
resolves `branch`/`repo` from the PR bead — you don't need to re-embed it; spend
the injection on what it _can't_ trivially get.)

## Principle 3 — coordinate shared edit surfaces

The dual of Principle 2: when two or more children will **edit the same file or
the same function/signature**, they can collide. Don't leave that to chance —
either:

- **Sequence them** with a dependency so they're worked one after another:
  `bd dep add <second-leaf> <first-leaf> --type blocked-by` (the second is
  blocked by the first); or
- **Note the overlap** in each child's description ("⚠️ also edited by `<other-id>`
  — coordinate / expect a conflict in `<file>`").

A blocked leaf will **not** appear in the consuming agent's ready queue until its
blocker closes — which is exactly what you want for ordered work, but means you
should only arm a leaf once it's genuinely unblocked.

## Workflow

1. **Read the parent + resolve context** (see Inputs). If you only got a
   description, create the parent bead first.
2. **Identify the shared lookups (P2) and shared edit surfaces (P3)** across the
   children you're about to create. Resolve the lookups now.
3. **Decide the split (P1).** If the parent is already a single-context leaf, skip
   creating children — just arm the parent (step 5).
4. **Create the child beads.** Use the bundled helper so the `bd` flags are always
   right (it adds `--no-inherit-labels` so children don't silently inherit the
   parent's labels, and `--force` when the parent's id-prefix differs from the
   store default):

   ```bash
   scripts/create-child-bead.sh <parent-id> "<verb>: <what>" "<self-contained body>" \
     "target file: <path>" "type shape: <…>"        # trailing args = Context k:v lines
   ```

   Prefer the helper, but the equivalent raw command is:

   ```bash
   bd create --type task --parent <parent-id> --no-inherit-labels [--force] \
     --title "<verb>: <what>" \
     --description "<self-contained body>

   Context (resolved during breakdown):
   - <key>: <value>"
   ```

   Each child's description must be **self-contained** ("no see-parent-for-context"),
   carry the injected facts, and — crucially — state its own **acceptance gate**:
   what "done" looks like and how to verify it (e.g. "done when `nix flake check`
   is green" / "the new table-driven test passes"). Lift the gate from the
   producer's spec if there is one.

5. **Arm the leaves with the consuming agent's label.** Only leaves (no open
   children, no open blocker) get armed, and the label is **whoever will work
   them**, not always `worker-ready`:
   - PR work for the `worker` pool → `worker-ready`
   - work for a different specialist (e.g. ccpool changes) → that specialist's
     label (e.g. `ccpool`)
   ```bash
   bd update <leaf-id> --add-label <consumer-label>
   ```
   A split-parent (a bead with children) is **never** armed — only its leaves are.
6. **Wire ordering + the parent into the graph.** Add any P3 sequencing edges,
   and connect the parent into the broader dependency graph if something depends
   on this work landing:
   ```bash
   bd dep add <thing-that-needs-this> <parent-id> --type blocked-by
   ```
7. **Summarize on the parent:**
   ```bash
   bd comment <parent-id> "Split into N children: <ids>. Leaves armed <label>. Ordering: <…>."
   ```

## Boundaries

- You create / parent / inject / label / wire beads. You do **not** implement the
  work, and you do **not** close the parent (that cascades when its children
  close).
- Never arm a bead that still has open children or an open blocker — it isn't a
  ready leaf yet.
- Don't re-embed context the consuming agent resolves on its own; spend injection
  on what it can't get.
- Be idempotent if re-run on the same parent: check existing children
  (`bd show <parent-id> --json | jq '.children'`) before creating duplicates.

## Example: splitting a PR "write tests" bead (one concrete case)

Parent `zr-abc` — _Write tests for the beads package_ — is over-sized (many files,
independent per-module sub-goals → P1 says split). It descends from a PR bead, so
the context is bead-resolvable; the consumer is the `worker` pool, so leaves are
armed `worker-ready`.

```bash
# P2: resolve shared facts once
ls packages/pg-pr/pkg/beads/                                  # -> the modules to cover
BRANCH=$(bd show <PR-id> --json | jq -r '.metadata.branch')   # -> phillipg.bead-tests

# Create one leaf per module (helper keeps the bd flags right)
scripts/create-child-bead.sh zr-abc "test: unit tests for processingcycle.go" \
  "Add table-driven tests for FindOpenProcessingCycle + CreateProcessingCycle. Done when the new test passes under nix flake check." \
  "target file: packages/pg-pr/pkg/beads/processingcycle.go" \
  "test file: packages/pg-pr/pkg/beads/processingcycle_test.go (create if absent)" \
  "branch: $BRANCH"

scripts/create-child-bead.sh zr-abc "test: unit tests for feedback.go" \
  "Add table-driven tests for CreateFeedback. Done when the new test passes under nix flake check." \
  "target file: packages/pg-pr/pkg/beads/feedback.go" \
  "branch: $BRANCH"

# Arm the leaves for the worker pool, then summarize
bd update <child-1> --add-label worker-ready
bd update <child-2> --add-label worker-ready
bd comment zr-abc "Split into 2 leaves (one per source file). Armed worker-ready. No inter-leaf ordering (independent files)."
```

For a non-PR breakdown (e.g. ccpool enhancements), the shape is identical — only
the resolved context (repo subtree instead of PR metadata) and the leaf label
(the specialist's label instead of `worker-ready`) change.
