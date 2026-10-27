---
name: plan-sanity-check
description: >-
  Use INLINE (never as a separate agent dispatch) as the shared, cheap "is this plan text
  well-formed enough to act on" guard that `plan-decompose`, `epic-decompose`, and
  `phase-decompose` each invoke before their own deeper checks run. Fires on: a caller about to
  judge or curate a raw epic, a phase plan, or a full decomposition plan, and wanting a fast
  mechanical-first pass before spending a heavier dispatch on it. Single mode, no sub-modes. Do
  NOT use for the deeper decomposability/seam checks themselves (those stay in the calling
  skill) — this only answers whether the text is well-formed enough to be worth checking
  further.
---

# plan-sanity-check — is this plan text well-formed enough to act on?

A single-mode, cheap, mechanical-first guard. It answers one narrow question — is this plan
text well-formed enough to act on — plus one mechanical filter (the `dropped` list of
mutation-run rows, below); sizing, seam consistency, and decomposability judgments stay with
the caller. `plan-decompose`, `epic-decompose`, and
`phase-decompose` each invoke it INLINE, via the Skill tool, before their own deeper checks run.

**Invocation id**: this skill lives in the existing `plan-decompose` plugin (no new plugin), so
its plugin-qualified invocation id follows that plugin's `<plugin-name>:<skill-name>`
convention: `plan-decompose:plan-sanity-check`. Never invoke it as the self-referential
`plan-sanity-check:plan-sanity-check` — there is no `plan-sanity-check` plugin.

**Inline-only**: this skill MUST run inline in the invoking agent's own context via the Skill
tool. It is never marked or dispatched as a background subagent of its own; only its own step-2
judgment fallback (below) is a separate Agent-tool dispatch, and that dispatch is a leaf (it
gets no `Agent`/`Skill` tool grant, so it cannot recurse or invoke this skill again).

## Input / Output

- **Input**: a bead id (or inline plan text) plus the level being judged — `raw-epic` | `phase`
  | `plan`. The level is purely informational in v1 — this skill's judgment logic does not
  branch per level; it is threaded through only so the caller's own report can state what
  altitude was checked. A future revision may add level-specific judgment; v1 does not.
- **Output**: exactly `{ good_enough: yes|no, reasons: [...], dropped: [...] }`. `dropped` lists
  the plan rows the caller MUST NOT carry forward (see "Mutation-run rows" below); it is empty
  when there are none and never affects `good_enough`. There is no sizing/ceiling signal in
  this output — whether a plan is too big, too small, or at the decomposition floor is a
  separate, per-caller judgment call that this skill never makes.

## Mode (single, no sub-modes)

1. **Mechanical scan** — run directly with shell tools (`grep`, `wc`, etc.); no agent dispatch
   for this step. Checks:
   - TBD/TODO/placeholder markers anywhere in the text.
   - Empty or missing anatomy-shaped sections (headings the level's own anatomy expects, present
     but empty, or absent entirely).
   - A byte-length floor: text under some trivially small size is definitionally not "a plan."
   - Mutation-run rows (`grep -n -i -E 'pg-go-mutate|go-test-gaps|mutation (test|run|pass)|survivor|gap analysis'`):
     each hit is a candidate for `dropped`, not a failure of `good_enough`. See "Mutation-run
     rows" below.

   This step is cheap and deterministic, near-zero cost. A clean pass (no markers, no
   empty/missing sections, over the floor) or an unambiguous fail (a marker present, a required
   section empty/missing, or under the floor) resolves the check here — go straight to step 3.

2. **Judgment fallback** — dispatched ONLY when step 1 is inconclusive (neither a clean pass nor
   an unambiguous fail). Dispatch exactly ONE fresh agent via the Agent tool:
   - Model: `haiku` first choice.
   - Fall back to `sonnet` only when the caller's own brief says the mechanical scan alone is
     not trustworthy for this input's shape (for example: prose-heavy design text that a regex
     scan cannot reliably judge).
   - This dispatch is a LEAF under the agent-nesting-depth invariant this skill's callers share:
     it MUST NOT receive an `Agent` or `Skill` tool grant, so it cannot recurse even if
     instructed to.
   - The dispatch is read-only and MUST report fully in one turn (no waiting, no Monitor, no
     further sub-dispatches) — state that constraint in the prompt itself, since a fresh agent
     has no other way to know it. Give it the plan text's absolute FILE PATH, never the text (reuse the plan's existing file when
     there is one; a plan held only in a bead or inline is written to a scratch file first), the level being judged (informational only —
     do not ask it to branch judgment per level), and step 1's inconclusive findings; ask it to
     read that file and return `good_enough: yes|no` plus `reasons: [...]`. Its Read access is
     the only tool it needs.

3. **Output and halt semantics** — output exactly
   `{ good_enough: yes|no, reasons: [...], dropped: [...] }`; no sizing/ceiling signal (that
   stays the caller's own separate judgment). A `no` verdict is
   reported to the CALLER's OWN dispatcher exactly like a `plan-decompose` mode-`check` gap
   report — it halts the caller. This skill itself never retries or escalates on its own; it
   answers once, per invocation, and returns.

## Mutation-run rows

Mutation testing is never part of a planned implementation flow (always-on rule V-5; operator
ruling, Phillip, 2026-10-22: "unless the story is about mutation testing itself, none of the
standard planned work should rely on running mutate tests"; restated 2026-10-26). This is the
same rule `plan-decompose`'s anatomy part 7 applies to a work packet, applied one level up so
that `epic-decompose` (which copies design slices into phase beads) and `phase-decompose` (which
hands the phase design to `plan-decompose`) share ONE guard: both invoke this skill before they
write or hand on any design text.

- A plan row that requires a mutation run (`pg-go-mutate`, the `go-test-gaps` skill), a
  survivor list or a gap analysis, whether as an acceptance criterion, a validation step or a
  planned work item, MUST be listed in `dropped` (quote the row's heading or first line and its
  location).
- The invoking caller MUST NOT carry a `dropped` row into any phase bead design field, phase
  slice, acceptance criterion or work packet it writes or hands on, and MUST note each drop in
  its own gap report (`epic-decompose`: the step 9 `write-report` and the report to its
  dispatcher; `phase-decompose`: its report back to its dispatcher). A drop never halts the
  caller and is never an escalation by itself.
- Carve-outs, which are NOT dropped: a hand-applied negative-control mutant in a test (a
  mutant applied by hand to prove a test fails, not a `pg-go-mutate` run); and a row that
  changes the mutation tooling itself (`pg-go-mutate`, `pg-go-mutate-tui`), which MAY run the
  tool only against a small synthetic fixture project, never against a workspace package and
  never to confirm a closed gap.
- A row that closes gaps a past mutation analysis found is kept only if it names each missed
  path and the test(s) that must exist; it MUST NOT require a re-run of the tool.
- The grep hits are candidates: judge each against the carve-outs above before listing it.

**Note on double-invocation (intentional):** when `phase-decompose` runs this skill at its own
step 2 and later hands off to `plan-decompose` (whose step 0a runs it again), the check runs
twice for one phase. This is deliberate, not an oversight: step 1's mechanical scan is
near-zero-cost in the common case, and running it again before `plan-decompose` commits to a
heavier dispatch (its own semantic post-check machinery) catches a plan that would fail the
deeper check anyway — catching that early, cheaply, is worth the second pass.

## Consumers

`plan-decompose` (mode `check`'s own pre-check), `epic-decompose` (its own step 3), and
`phase-decompose` (its own step 2) each invoke this skill inline via the Skill tool, ahead of
their own deeper decomposability/seam checks. Wiring those callers to invoke it is out of scope
for this skill's own file — each caller's own SKILL.md states where in its sequence this runs.
