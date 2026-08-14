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
   bd create "verify <thing> works after apply" --defer 2126-01-01 --json
   # capture the new bead id, e.g. <BEAD>
   # far-future ABSOLUTE date: --defer takes --due's formats, which have NO y unit
   ```

   Then CONFIRM the bead is NOT WORKABLE — by READINESS, never by reading `status`.
   `bd create --defer` applies the defer but leaves the new bead at `status: open`
   (confirmed 2026-08-13), and `deferred_until` reads `null` in the `bd show --json`
   projection even after an update that DID set the status — so a field read reports a
   FALSE failure. Assert the OUTCOME the ordering exists to produce (**P-1**):

   ```bash
   # -n 0: bd ready caps at 100 rows by default, so a capped query proves nothing.
   # NO --exclude-label human: absence would then prove the LABEL, not the defer.
   # The --include-deferred half is the positive control -- without it, an erroring
   # or empty bd ready satisfies "absent" vacuously.
   bd ready --json -n 0 --include-deferred | jq -e --arg id "<BEAD>" 'any(.data[]?; .id == $id)' >/dev/null &&
     bd ready --json -n 0 | jq -e --arg id "<BEAD>" 'all(.data[]?; .id != $id)' >/dev/null &&
     echo "OK: <BEAD> is not workable" || echo "FAIL: <BEAD> is workable -- do NOT gate"
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

   Re-run ONLY step 2's second (absence) command: the bead MUST still be absent from
   `bd ready`. Skip the `--include-deferred` positive control here — the gate, not a
   defer, is now what holds the bead, and that flag does not re-admit it.

The bead stays out of `bd ready` until someone runs `pn workspace apply`; the
apply's post-hook runs `pb gate check`, which resolves the gate once the change's
patch-id is in the applied history AND that apply's flake lock contained the commit
(see rule 5). Then the bead surfaces as ordinary work.

## Rules

1. **Commit before gating.** No commit ⇒ no patch-id ⇒ nothing to gate.
2. **Prefer single-commit gating** (`--commit HEAD`, the default). Use
   `--commits <range>` (one gate per commit; the bead unblocks only when **all**
   are applied) only when a change's commits may land/apply separately — it is an
   advanced, rarely-needed option.
3. **Deferred-first ordering is mandatory** (the fleet-race requirement above), and
   it MUST be verified by READINESS, not by reading `status` (step 2). A `status`
   read reports a false failure, and the "repair" for a false failure is to drop or
   re-order the deferred-first step — which OPENS the race this rule closes.
4. **Squash-merges lose the gate.** A squash fuses the diff into a new patch-id,
   so the gate never auto-resolves and falls to stale-handling
   (`pb gate check` converts it to a `human` bead by default). If you expect the
   change to be squash-merged upstream, prefer a plain `bd dep`/human follow-up
   over a `pn:applied` gate.
5. **A flake-pinned repo needs push + relock, not just an apply.** Where the
   workspace terminal pins the repo as a `github:` flake input, an apply builds it
   from the terminal's `flake.lock`, so a commit that is only on local `main` is NOT
   in the built system. `pb gate check` requires the gated commit to be in the rev
   that apply's lock named, so the gate stays blocked (with a reason) until the
   commit is pushed, the terminal relocked, and an apply run. This is deliberate: the
   alternative released verification beads against code no build had seen. Only the
   TERMINAL repo resolves on the apply alone.

## When NOT to gate

- The follow-up has no post-deploy dependency → just create an ordinary bead.
- You cannot commit the change yet → gate later, after committing.
- You expect a squash-merge of the gated change → see Rule 4.
