# drain-stuck reference: CONVERT-TO-DEPENDENCY

Read in full when STUCK step 3 finds every live blocker is another bead. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## CONVERT-TO-DEPENDENCY (STUCK step 3 found the blocker is another bead)

Reached when EVERY live blocker is another bead. The bead is not waiting on a person, so
it MUST NOT be labeled `escalated` or `human`: the tracker can express this wait exactly, and unlike a
label a dependency edge clears ITSELF. Full contract: the `beads-lifecycle` skill's
`Blocker Modeling` rules (**D-1..D-10**).

1. WIRE one edge per live blocker, FIRST — while the bead is still `in_progress` and
   owned by you, so `bd ready` excludes it and the write lands in a window no peer can
   observe (**D-5**). Prefer the FLAG form: the first id is the BLOCKED bead, the second
   is the BLOCKER, and the bare positional form reads identically written either way
   round, so it is where a reversal hides:

   ```bash
   bd dep add <id> --blocked-by <blocker-id>   # once per blocker; <id> depends on <blocker-id>
   bd dep list <id>                            # CONFIRM each blocker echoes back "(open) via blocks"
   ```

   Leave the type at its default `blocks`. `discovered-from` does NOT gate readiness
   (**D-3**), so the `--deps "discovered-from:<id>"` form used elsewhere in the drain
   loop is the WRONG tool here — it would leave the bead drain-claimable while genuinely
   blocked. Do NOT pass `--no-cycle-check`: a cycle makes BOTH beads permanently unready
   (**D-4**).

2. COMMENT what you found, carrying the step-2 `FRESHNESS:` line. Write NO `PRECONDITION`
   block — the graph IS the precondition, and prose restating it would rot exactly as
   **P-1** describes:

   ```bash
   bd comment <id> "not stuck on a person: blocked on <blocker-id>[, <blocker-id>…], now wired as bd dependencies instead of an escalation. No human input is needed to move this — it returns to the drain queue by itself when the last blocker closes. Work parked on branch <work-branch> in <repo>.
   FRESHNESS: <ISO date> — sibling-open?=<status per blocker> ⇒ premise LIVE
   BLOCKED-BY-BEADS: <blocker-id>[, <blocker-id>…]" --actor "ID"
   ```

3. RECLAIM (below), then RELEASE in ONE call — no `escalated` or `human` label, `status=open`,
   assignee cleared (**B-2**, **B-3**, **D-6**). `--remove-label human` belongs in this same call whenever the bead
   already carries it (from an earlier park) — a safe no-op otherwise:

   ```bash
   bd update <id> --remove-label human --status open --assignee "" --actor "ID"
   ```

   `open`, NOT `blocked`: readiness is DERIVED from the graph, so the bead is already
   absent from `bd ready` with no stored flag needed — and when the last blocker closes it
   re-enters drain's queue on its own, with nobody having to remember to re-open it. A
   stored `blocked` status is a value nothing recomputes, so it would strand the bead
   after the dependency resolved.

4. Do NOT clean up a preserved worktree/branch that holds work — the work resumes there once the
   blockers clear (RECLAIM, at step 3, removed it only if it held nothing). Done — return to
   the caller.

**MIXED blocker (a bead AND a person) — both apply; do not let either fall through**
(**D-7**). Do step 1 above for the bead half, then go BACK to STUCK step 4 and finish the
escalation, because a person may hold part of the answer. Which of two shapes
you have decides where the question goes, and the escalation (or, for a new decision bead,
the `human` label) MUST sit on the bead that actually HOLDS the question:

- **The question is only answerable AFTER the blocker lands** → ESCALATE THIS bead
  (steps 4–8: `escalated`, never `human`). State the consequence in the step-6 comment, because it is not obvious:
  `bd ready` excludes blocked beads, so the bead is now absent from BOTH queues until its
  blockers clear, and only then resurfaces in the triager's `bd ready --label escalated`
  queue. That is CORRECT — the question was not yet answerable — and it is why the comment
  must name the question (the `NEEDS:` line), so the triager inherits it rather than
  re-deriving it.
- **The question is answerable NOW and independent of the blocker** → do NOT bury it
  behind the edge. File it as its OWN `human` bead with no blockers and depend on THAT, so
  the operator sees it immediately; THIS bead then takes the pure conversion path above and
  carries no label of its own (this is the `pg2-l3vdz` shape — the driver held the deps, the
  sub-beads held the questions). That decision bead is NEW planned operator work (LV-2): it
  carries `human` ONLY, never `attention`, and is filed for the operator directly, not
  routed through the triager (the second deliberate direct `human` write of this skill;
  ruling recorded in ADR 0107, bead `pg2-zpj6z`):

  ```bash
  bd create "<the decision or input a person must supply>" --labels human \
    --deps "discovered-from:<id>" --actor "ID" --json
  # capture the new id as <question>, then wire it as a blocker like any other:
  bd dep add <id> --blocked-by <question>
  ```
