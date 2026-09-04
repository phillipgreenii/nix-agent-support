---
name: phase-decompose
description: >-
  Use when a claimed phase-trigger bead is ready to run: verifies the paired phase bead's
  design text against what actually landed from its upstream phases, escalates to a human on
  drift that needs judgment or on a sanity-check failure, and otherwise hands off to
  `plan-decompose` mode `decompose` to run that phase's own decomposition to completion. Fires
  on: claiming or working a `phase-trigger`-labeled bead, "run phase-decompose on this trigger",
  "verify this phase against its upstream phases and decompose it". Normally dispatched via the
  `phase-decomposer` agent by whoever claims the phase-trigger bead (a drain session, an
  interactive session, or a dispatched generic worker reading its instructions). Do NOT use to
  sketch new phase boundaries for a program epic (that is `epic-decompose`, which CREATES the
  phase and trigger beads this skill later consumes), and do NOT use to curate a phase-sized
  design into work packets directly — that is `plan-decompose` itself, which this skill hands
  off to unchanged once verification passes.
---

# phase-decompose — verify a phase against landed upstream state, then decompose it

`phase-decompose` VERIFIES one phase bead's design text against the ACTUAL landed state of the
phases it depends on, and otherwise defers entirely to `plan-decompose`: it never curates work
packets itself. Design of record for this skill:
`docs/superpowers/specs/2026-09-04-plan-decompose-phasing-design.md` in
`phillipgreenii-nix-agent-support` (provenance only — this skill stands alone).

**Invocation id**: this skill lives in the existing `plan-decompose` plugin (per the design's
Decision 2), matching the convention `plan-decomposer.md` already uses for
`plan-decompose:plan-decompose`. Its plugin-qualified invocation id is
`plan-decompose:phase-decompose` — never the self-referential `phase-decompose:phase-decompose`,
since no plugin named `phase-decompose` exists.

Single mode, no sub-modes.

## Concepts

- **Trigger bead** — a plain task, sibling of its phase bead, whose sole purpose is to instruct
  a later claimant to run `phase-decompose` against the paired phase bead once every phase it
  depends on has closed. This skill's whole procedure starts from a claimed trigger bead
  [design: §4 "Trigger bead"].
- **Phase bead** — the paired `plan-decompose` docket (an epic-typed bead) holding that phase's
  design slice, created with `pd_phase=precheck` already set — this skill never creates one,
  it only verifies and then hands one to `plan-decompose` [design: §8 step 5].

## Agent-nesting depth invariant

`phase-decomposer` (the dispatch agent for this skill) is an ORCHESTRATOR — it may hold the
`Agent`/`Skill` tools. It invokes `plan-sanity-check` and `plan-decompose` itself INLINE via the
Skill tool (no new dispatch depth: a skill invoked by whichever agent already holds the context
never creates one), and dispatches `phase-plan-verifier` via the Agent tool as a depth-2 LEAF —
that agent's own file grants it `Read, Grep, Glob, Bash` only (no `Agent`, no `Skill`), so it
cannot recurse even if instructed to. No dispatch this skill makes exceeds depth 2 relative to
whatever invoked the outermost skill [design: §3 Decision 9, §5.3].

## Mode (single, no sub-modes)

1. **Claim the trigger bead** — standard `bd` claim hygiene, explicit actor. This prevents two
   claimants from both running `phase-decompose` against the same phase. Resolve, from the
   trigger bead's own text, the phase-bead id it names, and keep the trigger bead's own id —
   both are needed by every later step, including closeout. Release the claim on EVERY exit
   path except the success closeout (step 7), which closes the bead outright instead of
   releasing it. This claim/release discipline is reused directly from `beads-lifecycle`'s
   existing claim hygiene (B-1..B-6), not invented here [design: §8 step 1; §2
   "beads-lifecycle's dependency-vs-human modeling... and claim hygiene"].

2. **`plan-sanity-check` inline**, via the Skill tool (`plan-decompose:plan-sanity-check`, never
   as its own agent dispatch), at level `phase`, against the phase bead's CURRENT design text.
   A `good_enough: no` verdict routes straight to step 4's escalation mechanism, reason
   `"sanity check failed"` — do not simply leave the trigger open on a failing verdict; a
   silently-reclaimable failing trigger would re-fail identically forever [design: §8 step 2].

3. **Dispatch `phase-plan-verifier`** (depth-2 leaf) with all five of: the phase bead's own id,
   the trigger bead's own id (both already resolved at step 1), the phase bead's design text,
   the ids of the UPSTREAM phases this phase depends on, and the absolute repo root(s).
   - **Resolving the upstream-phase ids**: query `bd dep list <trigger-bead-id>` — NEVER
     `bd dep list <phase-bead-id>`. Per `epic-decompose`'s own wiring, the phase bead itself is
     `--blocked-by` only its own trigger (a single, self-referential edge); the upstream phases
     this phase actually depends on live on the TRIGGER bead's `--blocked-by` list instead.
     Querying the phase bead's own list would return only that one self-referential edge, never
     the upstream set [design: §7 step 8 "Wire" — this resolution path is derived directly from
     that wiring, not itself stated verbatim by the design].
   - **Why both ids are forwarded**: `phase-plan-verifier` needs the phase-bead id to know which
     bead's design field it may direct-edit on unambiguous drift, and needs BOTH ids to fill in
     the escalation-bead template's `Phase: <phase-bead-id> / Trigger: <trigger-bead-id>` header
     (step 4 below) and to wire `bd dep add <trigger-bead-id> --blocked-by <new bead>` on a
     genuine open question — neither id is otherwise available to it, since it is a fresh
     subagent with no other context [design: §8 step 4's template fields; §8 step 1, "the
     phase-bead id... and the trigger bead's own id" already resolved by this point].
   - The subagent reads the ACTUAL landed state of those upstream phases (their closed packets'
     `close_reason`s, and the real repo code/interfaces they produced) and compares it against
     what this phase's design text ASSUMES those phases produced.
   - **Unambiguous drift** (a cited interface/path/shape changed in a way the phase's design
     text can be mechanically corrected to match): the subagent ITSELF — it holds Bash/`bd`
     access for exactly this — direct-edits the phase bead's design field with the correction,
     and records what changed and why in an appended note. No `pd_rev` bump: no packets exist
     yet under this docket, so nothing is stale relative to the old text; bumping the revision
     here would track a distinction that has no consumer.
   - **Genuine open question**: the subagent ITSELF escalates via step 4's mechanism (it also
     holds `bd` access for this), reason `"open question"`, with the evidence it gathered.
   - **How you learn which outcome occurred**: the dispatched subagent's own final text report
     states plainly which of the two it did (naming the escalation bead's id if it escalated) —
     you (phase-decomposer) branch on that report text, not a separate structured return field;
     there is no other channel [design: §8 step 3; §10 agent table row phase-plan-verifier,
     "Bash (repo inspection + bd CLI for its own direct edits/escalation bead)"].

4. **Escalation** (one shared mechanism, reused by steps 2, 3, and 6's abort case — not three
   separate patterns): `bd create` a NEW bead, labeled `human`, with no blockers of its own,
   titled to name the phase and the failure kind. The `human` label goes on this NEW bead,
   NEVER inline on the trigger bead itself — this reuses `beads-lifecycle`'s
   dependency-vs-human modeling directly [design: §2 "beads-lifecycle's dependency-vs-human
   modeling..."]. Reproduce this body template VERBATIM in the escalation bead, so a human
   finding it cold can act on it without having read the design doc:

   ```
   Phase: <phase-bead-id> — <phase title>
   Trigger: <trigger-bead-id>

   What the phase design assumed: <...>
   What actually landed (or: what failed, and how): <...>
   Why this needs a person: <...>

   Options considered: <...>

   To resolve: edit the phase bead's design field to reflect your decision (state what
   you changed and why in your edit), then close this bead. The trigger unblocks automatically
   once you do — no separate step needed.
   ```

   Then `bd dep add <this phase's trigger bead> --blocked-by <new bead>`; release the trigger
   claim — it is now genuinely blocked, so it cannot be reclaimed and re-fail until the human
   bead closes. This dependency edge is what actually stops the loop, not a retry counter.
   Report back to whichever step invoked this: blocked, see `<new-bead-id>`. `phase-decompose`
   halts here. Because resolving REQUIRES editing the phase bead's design as part of closing
   the escalation bead, the next `phase-decompose` run (triggered automatically once the
   dependency clears) sees the updated design text — `phase-plan-verifier` re-verifies against
   the NEW text and, correctly, does not re-raise the same question [design: §8 step 4].

5. **On a clean verify** (no drift, or drift fixed, and no escalation raised at step 3):
   invoke `plan-decompose` mode `decompose` INLINE via the Skill tool — you (phase-decomposer)
   continue as yourself; you do NOT dispatch a second `plan-decomposer` agent. Run it against
   the phase bead, which `find-docket` resolves as an existing docket with `pd_phase=precheck`
   (set explicitly at phase-bead creation time, never absent) and correctly RESUMES from that
   phase, proceeding through `plan-decompose`'s existing, completely unmodified pipeline: its
   own mode `check`, curate, size, pre-filter, cold-read, semantic post-check, wire, release
   [design: §8 step 5].

6. **If that inner `plan-decompose` run ABORTS** (`pd_phase=failed:<phase>`, per its own
   existing, unchanged abort semantics): escalate via step 4's mechanism, reason
   `"decomposition did not converge"`, citing the inner run's own failure report. Do not leave
   the trigger silently open [design: §8 step 6].

7. **Closeout** — only on a genuine `pd_phase=released` result from step 5: `bd close` the
   trigger bead with a reason citing the release. Because `bd` dependency edges self-clear when
   their blocker closes, this automatically un-blocks the phase bead — and any later phase's
   trigger bead that was wired `--blocked-by` this one only unblocks once THIS phase bead
   itself closes (which, being an epic, `bd` refuses until every packet under it is closed) —
   so "the next phase isn't planned until this phase is actually done, not merely decomposed"
   holds by construction [design: §8 step 7].

## Consumers

`phase-decompose` is dispatched, normally via the `phase-decomposer` agent (this plugin), by
whoever claims a `phase-trigger`-labeled bead — a drain session, an interactive session, or a
dispatched generic worker reading its own instructions. Its output is either an escalation bead
(a person's decision blocking the trigger) or a fully released `plan-decompose` docket for that
phase, whose trigger bead closing is what unblocks the next phase's own trigger in turn.

## Usage

- "this phase-trigger bead is ready — run phase-decompose on it" → claim the trigger bead, then
  run this skill's mode inline or dispatch `phase-decomposer` with the trigger bead's id and
  absolute repo root(s). Progress: the trigger bead's claim state, the phase bead's `pd_phase`,
  and any escalation bead created along the way.
