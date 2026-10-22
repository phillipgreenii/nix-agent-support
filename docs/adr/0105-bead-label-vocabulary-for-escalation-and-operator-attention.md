# Bead label vocabulary for escalation and operator attention

**Status**: Accepted (extends 0079 and 0081; resolves `pg2-ikkpk`)
**Date**: 2026-10-22
**Deciders**: Phillip Green II (operator rulings, 2026-10-21 and 2026-10-22, recorded in the bead)

## Context

An agent that has a problem escalates to the escalation-triager; if the triager cannot resolve it,
it comes to the operator. Only those beads should show up as the operator's attention. The labels
that carry this had grown by habit:

- `human` means "a person is the blocker" and is also what many agent paths apply (drain-stuck
  parks, verify and checkpoint beads, handoffs, decision beads), so the human queue is the
  operator's whole work queue, far wider than "an agent tried and failed".
- Nothing wrote `attention` or `human-focus`, so the beads half of the attention list was empty;
  ADR 0081 named `human-focus` as an initial attention label, but only `human-focus-required`
  exists, so the name was a mismatch.
- The two escalation-triager prompts disagreed at the hand-off: one added `human` and removed
  `escalated`, the other added `human` and kept `escalated` so the probe's dedup query still saw the
  bead.
- `docs/superpowers/specs/2026-10-10-escalations-design.md` already uses "escalation" for a menu-bar
  reminder item, which is unrelated to the `escalated` label.

The operator ruled (2026-10-22) that the vocabulary must be explicit, with two label SETS, strict
writes and open reads, and that a hand-off to a person MAY remove `escalated` provided the human
labels are on the bead no later than the removal, so no agent sees a bead with neither label.

## Decision

1. **Two label sets, defined once** in `docs/guides/bead-label-vocabulary.md`.
   `ESCALATED_LABELS` is `escalated`. `HUMAN_LABELS` is `attention`, `human-focus`,
   `human-focus-required`, `human`. They are READ sets: every filter accepts the whole set, so a bead
   labeled by habit or by a bug still filters correctly.
2. **Writes are strict.** An agent writes one canonical label per role: `escalated` (a problem for
   the triager), `human` (a person is the blocker, which includes planned operator work), and the
   pair `human` + `attention` (the escalation path could not resolve it and the operator must
   act). `attention` is written only at the hand-off; planned operator work carries `human` only.
   `human-focus` is read, never written.
3. **Filters follow the sets.** Normal work queues exclude both sets. The escalation handler
   includes `ESCALATED_LABELS` and excludes `HUMAN_LABELS`. The person queue includes the human
   labels. The attention list reads `attention_labels`, which equals `HUMAN_LABELS`. A dedup lookup
   keyed on `escalated` reads the union of both sets.
4. **One helper writes the hand-off.** `hand-to-human` (`packages/hand-to-human`) adds `human` and
   `attention` and removes `escalated` in ONE update call, then reads the bead back. Ordering is
   load-bearing: the human labels are on the bead no later than the `escalated` removal (the same
   reasoning as `beads-lifecycle` `D-5`). Go code that writes through the `bd` CLI uses
   `beads.HandToHuman`, which issues the same single call. A tool that cannot make one call MUST add
   first and remove second. Agents never hand-write these labels (`beads-lifecycle` `LV-3`).
5. **The triager prompts and the other direct-to-human producers route through the helper** (the
   operator's blanket rule, 2026-10-22). The disposition of each producer is recorded in the
   convention page.
6. **The queue mirror carries the sets.** The two drain queues in `claude-marketplace/pb/queues.json`
   exclude every member of both sets, and `tests/pb-queues-mirror.bats` checks the markdown and the
   mirror against each other and against the sets (ADR 0079's drift check).
7. **Terminology.** The convention page disambiguates the `escalated` label from the menu-bar
   "escalation" of the 2026-10-10 escalations design (`beads-lifecycle` `LV-6`).

## Consequences

### Positive

- Only a bead nobody could resolve carries `attention`, so the attention list means what it says.
- The two triager prompts behave identically, and the dedup lookup survives the removal of
  `escalated`.
- A new tool has one place to read to know which label to write and which to filter on.

### Negative

- A producer that creates a bead directly for a person (the probe's `escalation="human"` route) cannot
  use an update-style command; it writes the pair in its create call and keeps `escalated` so a
  dedup query that reads only `escalated` still sees it.
- Other writers of `human` that are not escalation hand-offs (the handler's failure and park paths)
  still write `human` only. Until a ruling settles them, `human` MUST stay in the attention list's
  read set.
- The helper's label names are fixed; a workflow with configurable label names (`pg-wi-flow`) writes
  the same pair through its own single update call instead.

## Alternatives Considered

### Keep `escalated` at the hand-off (the earlier ruling)

Rejected by the later 2026-10-22 ruling: removing `escalated` is allowed when the human labels are
added first or in the same update, and it keeps a handed-off bead out of the handler's queue without
a second exclusion.

### One label for intent plus a level label

Preferring one label for INTENT (design, implementation, ...) augmented by `escalated` or `human` for
the LEVEL, over a cross-product of labels, was recorded by the operator as a non-goal for this
change. Not implemented.

### A per-tracker rule for keep versus remove

Rejected: the helper picks one behavior (single call, removing `escalated`) for every tracker.

## Related Decisions

- [0079](0079-queue-definitions-mirror-contract.md): the queue mirror and its drift check, extended
  here with the two sets.
- [0081](0081-entity-attention-evaluated-at-read-time-in-pg-desk.md): pg-desk attention, a separate
  mechanism from the bead attention list; its `human-focus` initial label was a name mismatch.
- [0089](0089-drain-roles-apply-the-shared-pb-drain-one-skill.md): the drain roles whose claim queries
  exclude both sets.
