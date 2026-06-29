---
name: pb-gate-lifecycle
description: Gate a follow-up bead so it stays out of `bd ready` until a `pn workspace apply` actually applies the change it depends on. Use after you finish a unit of work and COMMIT it, when there is post-deploy follow-up (canonically "verify the code works") that MUST NOT be picked up by another agent until the change ships. Triggers: "this needs to be verified after it's applied", "don't let anyone work this until it's deployed", "gate this on apply", filing a verify/follow-up bead after committing. Do NOT use to create ordinary blockers between beads (use `bd dep`), or before you have committed the change (the gate keys on the commit's patch-id).
---

# pb gate lifecycle (pn:applied)

`pb` writes and resolves **`pn:applied` gates**: a gate holds a bead out of
`bd ready` until the workspace has **applied** a specific change. It keys on the
change's `git patch-id` (survives the local rebases this workflow uses), not its
commit SHA. You attach the gate; a later `pn workspace apply` + the apply
post-hook (`pb gate check`) resolves it and the bead surfaces.

## When to gate

Gate a follow-up bead when its work only makes sense **after the change is live**
on the machine — the canonical case is a "verify code works" bead. The gate
governs **when** the bead becomes workable, not its content.

## The lifecycle (do these in order)

You MUST create the follow-up bead **non-workable first**, then attach the gate,
then make it workable. If you create it ready and gate it afterwards, a
concurrent agent draining `bd ready` can grab it in the gap.

1. **Commit the change.** The gate keys on the commit's patch-id, so the commit
   MUST exist first.

   ```bash
   git commit -m "<your change>"
   ```

2. **Create the follow-up bead DEFERRED** (born non-workable):

   ```bash
   bd create "verify <thing> works after apply" --defer +100y --json
   # capture the new bead id, e.g. <BEAD>
   ```

3. **Attach the gate** on the most recent commit (the default, recommended,
   single-gate usage). `--repo` is the `pn workspace info` repo key the change
   lives in:

   ```bash
   pb gate create --blocks <BEAD> --repo <repo>
   # gates --commit HEAD by default; one gate, keyed to the change's tip
   ```

4. **Un-defer** the bead. The gate now holds it instead:

   ```bash
   bd update <BEAD> --defer ""
   ```

The bead stays out of `bd ready` until someone runs `pn workspace apply`; the
apply's post-hook runs `pb gate check`, which resolves the gate once the change's
patch-id is in the applied history. Then the bead surfaces as ordinary work.

## Rules

1. **Commit before gating.** No commit ⇒ no patch-id ⇒ nothing to gate.
2. **Prefer single-commit gating** (`--commit HEAD`, the default). Use
   `--commits <range>` (one gate per commit; the bead unblocks only when **all**
   are applied) only when a change's commits may land/apply separately — it is an
   advanced, rarely-needed option.
3. **Deferred-first ordering is mandatory** (the fleet-race requirement above).
4. **Squash-merges lose the gate.** A squash fuses the diff into a new patch-id,
   so the gate never auto-resolves and falls to stale-handling
   (`pb gate check` converts it to a `human` bead by default). If you expect the
   change to be squash-merged upstream, prefer a plain `bd dep`/human follow-up
   over a `pn:applied` gate.

## When NOT to gate

- The follow-up has no post-deploy dependency → just create an ordinary bead.
- You cannot commit the change yet → gate later, after committing.
- You expect a squash-merge of the gated change → see Rule 4.
