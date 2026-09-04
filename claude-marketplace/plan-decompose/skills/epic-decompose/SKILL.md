---
name: epic-decompose
description: >-
  Use when a program epic's design is too large for one `plan-decompose` pass and needs
  splitting into phases first. Given a program epic id (and its design text), sketches phase
  boundaries for the remaining, not-yet-phased scope, runs an adversarial review and an
  approval gate, then creates each new phase as its own `plan-decompose` docket (an epic-typed
  phase bead) plus a sibling trigger bead that instructs a later claimant to run
  `phase-decompose` against it. Fires on: "this epic is too big to decompose in one pass",
  "split this program epic into phases", "phase this design out", "sketch the next round of
  phases for this epic". May run inline (Skill tool, interactive session) or via the
  `epic-decomposer` agent when offloading context from a heavy session. Do NOT use for
  decomposing an already phase-sized design into work packets (that is `plan-decompose` itself,
  invoked per-phase by `phase-decompose`), and do NOT use to re-verify a phase's design against
  already-landed upstream phases (that is `phase-decompose`'s own step, which dispatches
  `phase-plan-verifier`).
---

# epic-decompose — split a program epic's remaining scope into phases

`epic-decompose` SKETCHES and GATES phase boundaries for a program epic whose design is too
large for one `plan-decompose` pass; it NEVER authors phase content beyond what the design
specifies, and it never itself curates work packets — that happens later, once a person (or an
explicit override) approves the boundaries, via `phase-decompose` and `plan-decompose`
themselves. Design of record for this skill:
`docs/superpowers/specs/2026-09-04-plan-decompose-phasing-design.md` in
`phillipgreenii-nix-agent-support` (provenance only — this skill stands alone).

**Invocation id**: this skill lives in the existing `plan-decompose` plugin (per the design's
Decision 2), matching the convention `plan-decomposer.md` already uses for
`plan-decompose:plan-decompose`. Its plugin-qualified invocation id is
`plan-decompose:epic-decompose` — never the self-referential `epic-decompose:epic-decompose`,
since no plugin named `epic-decompose` exists.

Single mode: `split`. There are no sub-modes.

## Concepts

- **Program epic** — the bead holding a design too large for one decomposition pass. Owns zero
  or more phase beads directly (a fresh epic has none; a second phasing round finds the ones
  the first round created).
- **Phase bead** — one phase's own `plan-decompose` docket: an epic-typed bead (`-t epic`,
  labels `docket,phase`), never claimed for direct work, holding that phase's design slice.
- **Trigger bead** — a plain task, sibling of its phase bead (same `--parent`, not its child),
  labeled `phase-trigger`, whose sole purpose is to instruct a later claimant to run
  `phase-decompose` against the phase bead once its own upstream dependencies are satisfied.
- **`phased-epic` label** — applied to the program epic once at least one round of phases has
  split off it.

## Agent-nesting depth invariant

`epic-decomposer` (the dispatch agent for this skill) is an ORCHESTRATOR — it may hold the
`Agent`/`Skill` tools and dispatch `phase-split-reviewer`. `phase-split-reviewer` itself is a
depth-2 LEAF: its own agent file grants it `Read, Grep, Glob` only (no `Agent`, no `Skill`), so
it cannot recurse even if instructed to. No dispatch this skill makes exceeds depth 2 relative
to whatever invoked `epic-decompose` in the first place.

## Synthetic `pd_source` convention

Each phase bead's `pd_source` is set to `<program-epic-id>#phase<n>` — an issue-id-based
identifier the existing `pd_source` contract already permits ("path or issue id"). This is what
guarantees no two phases, ever, share a `pd_source`, so `find-docket` never again faces an
ambiguous "same source, different intended round" case.

## Mode `split`

1. **Resolve input**: the program epic id, and — when its design is chunked or externally
   referenced — the design text, read via the existing chunk-index convention (the epic's
   design field holds a header plus a numbered index of comment parts that is followed in
   order; see `plan-decompose-beads`'s `create-docket` mapping).

2. **Claim the program epic** for the run's duration: standard bd claim hygiene, explicit
   actor. This prevents two concurrent `epic-decompose` runs from both reading the phase
   inventory and creating overlapping phase/trigger beads. Release it — status open, assignee
   cleared — on EVERY exit path; a run that reaches step 8 (create beads) releases only after
   all beads are created and wired. This claim/release discipline is reused directly from
   `beads-lifecycle`'s existing claim hygiene, not invented here.

3. **`plan-sanity-check` inline**, via the Skill tool (`plan-decompose:plan-sanity-check`,
   never as its own agent dispatch), at level `raw-epic`. A `good_enough: no` verdict halts the
   run: release the claim, and report the gap — to your dispatcher, and (when a tracking bead
   was named) to that bead via `write-report` too. This is the same shape as `plan-decompose`'s
   own mode-`check` gap-report path.

4. **Read the existing phase inventory**: `bd list --parent <program-epic> --label phase
--status all -n 0 --json`. This live query — never a stored "already phased?" flag — is the
   SOLE idempotency signal. A binary flag cannot represent the actual state (some scope
   phased, some not); storing one would reintroduce, one level up, exactly the bug this design
   exists to fix.

5. **Sketch phases for the REMAINING (not-yet-phased) scope only.** Apply the floor: fewer
   than 2 remaining phases ⇒ propose exactly ONE phase covering all remaining scope — NEVER a
   bypass back to calling `plan-decompose` directly against the raw remaining scope, since that
   would skip the synthetic-`pd_source` mechanism above and reproduce this design's own
   motivating bug. Otherwise sketch the full multi-phase split. Boundary rule, reused verbatim
   from `plan-decompose`'s own: split only where a reviewer could reject one phase while
   approving another.

6. **Adversarial review**: dispatch `phase-split-reviewer` (subagent_type
   `plan-decompose:phase-split-reviewer`; a depth-2 leaf — see the invariant above) with the
   full design, the existing phase inventory, and the proposed new phase(s). It checks: every
   design element lands in exactly one phase (existing + new) or is recorded as deliberately
   deferred; no new phase's Consumes cites a Produces shape only a LATER phase would create;
   each boundary is independently reject/approve-able. Findings loop back to step 5, capped at
   2 rounds (mirroring `plan-decompose`'s own semantic post-check cap): a finding recurring
   unresolved into round 2 ⇒ abort — release the claim, create no beads, `write-report` the
   unresolved finding, rather than loop indefinitely.

7. **Approval gate.** Presentation, either mode: a compact table — Phase | one-line scope |
   design elements covered | depends-on (existing or sibling new phases) | reviewer verdict —
   never the full verbatim phase text at this stage (available on request, or after approval,
   from the drafted phase content directly).
   - **Interactive**: present the table; ask a structured question with one option per
     proposed phase ("approve", "revise this phase's boundary"), plus an "approve all" option
     — never a single all-or-nothing yes/no. A "revise" answer loops back to step 5, scoped to
     ONLY the flagged phase(s) — sibling phases the human didn't flag are not re-litigated.
     Cap: after 2 revise-rounds on the SAME phase without full approval, stop, report this as
     a stalled approval (not a productive iteration), and ask the human directly what to do —
     never propose a scripted third option. A stalled approval needs a person's judgment about
     the process itself, not just the phase split.
   - **Background/dispatched**: `write-report` the reviewed table to the program epic AND add
     the label `human` to the program epic (surfacing this exactly like any other "a person
     must decide" state — no separate "hope someone reads the report" mechanism); release the
     claim; STOP with no beads created — UNLESS the brief named an explicit skip-approval
     override, in which case continue directly to step 8 and never apply the `human` label.

8. **Create beads** (only after approval, or under the skip-approval override), per new phase
   `k`, in an order that lets earlier-created phases be named as blockers by later ones. If the
   program epic was labeled `human` at step 7, remove that label in this same sweep — the
   question it marked is now resolved.

   **Choosing `k`**: `k` MUST NOT collide with a phase number already used by an earlier
   phasing round on this same program epic. Read the existing phase inventory's `pd_source`
   values (step 4) and continue numbering from the highest `k` found there on a second-or-later
   round — never restart at 1. This is required to satisfy the synthetic-`pd_source`
   mechanism's own uniqueness guarantee above: a restarted `k` on round 2 would collide with
   round 1's own phase-bead `pd_source` and reproduce the exact bug that mechanism exists to
   prevent.

   **Partial failure**: the design states no rollback/cleanup policy for a failure PARTWAY
   through this step (e.g., some phase/trigger beads already created when a later `bd create`
   or `bd dep add` call fails). Do not invent one. Treat this as "stuck" per your dispatcher's
   escalation ladder: report exactly which beads were created and which step failed, and let a
   human decide whether to clean up or continue by hand; do not silently delete beads or
   silently continue past the failure.
   - **Phase bead**:
     `bd create <title> -t epic --parent <program-epic> --no-inherit-labels --label docket,phase --design-file <phase-slice-file> --metadata 'pd_rev=1,pd_source=<program-epic>#phase<k>,pd_phase=precheck'`
     (plus whatever sizing-policy metadata the brief specifies or the fallback defaults
     supply, per `plan-decompose`'s own metadata table). Deliberately typed `-t epic`: this
     reuses beads' existing epic convention (never claimed for direct work, `bd ready` returns
     it, drain's `--exclude-type epic` keeps its own queue clean) rather than inventing a new
     "phase" bead-type. `--no-inherit-labels` and the explicit `--label` list are REQUIRED:
     without them the phase bead inherits every label the program epic carries, including
     `phased-epic` on a second phasing round, silently polluting the phase bead's label set.
     Title: `Phase <k>: <phase scope>`.
   - **Trigger bead**: a plain task, sibling of the phase bead — SAME `--parent` as the phase
     bead in this same step, NOT a child of it:
     ``bd create <title> -t task --parent <program-epic> --no-inherit-labels --label phase-trigger -p 3 -d "Run \`phase-decompose\` on \`<phase-bead-id>\`."``,
     title `Phase <k> decompose-trigger: <phase scope>`. `--no-inherit-labels` on the
     TRIGGER bead is not itself design-stated for this bead shape — it is this skill's own
     safety analogy to the phase bead's identical hazard: the trigger bead is also created
     with `--parent` in this same step, so without `--no-inherit-labels` it would inherit the
     same unwanted program-epic labels (e.g. `phased-epic`).
   - **Wire**: `bd dep add <phase-bead> --blocked-by <its own trigger>`; for every phase this
     new phase depends on (existing or new, from step 5), `bd dep add <this phase's trigger> --blocked-by <that phase's phase-bead>`. Verify every edge by read-back (`bd dep list`); run
     `bd dep cycles` after the bulk wiring, filtered to this program epic's beads.

9. **Label and report.** Label the program epic `phased-epic` (idempotent) and `write-report`
   the phase-split report (phase index, per-phase design-section coverage, review outcome,
   wiring) to it. Release the claim.

**Abort path**: any early exit (a gap report, review non-convergence at the cap, an
unresolvable ambiguity in step 5, a stalled approval at its own cap) creates no beads, releases
the claim, and leaves the program epic's phase inventory exactly as step 4 found it —
`write-report` states what was attempted.

## Consumers

`epic-decompose` may be run inline by an interactive session, or dispatched via the
`epic-decomposer` agent (this plugin) when a heavy session wants to offload the context cost.
Either path uses the same steps above. Its outputs — phase beads and trigger beads — are
consumed by `phase-decompose` (dispatched per phase-trigger bead) and, in turn, by
`plan-decompose` itself, run once per phase.

## Usage

- "this epic's design is too big for one `plan-decompose` pass — split it into phases" →
  resolve the program epic id, then run mode `split` inline or dispatch `epic-decomposer` with
  the program epic id, absolute repo root(s), and an optional tracking bead for gap reports.
- "sketch the next round of phases for `<program-epic>`" → same entry point; step 4's live
  phase-inventory query is what makes a second round safe to run without re-splitting already
  landed phases.
