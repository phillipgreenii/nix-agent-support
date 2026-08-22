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
   NEITHER field is a usable check: on `bd 1.2.2 (dev)`, `bd create --defer` returns
   `status: deferred` with the defer in `defer_until` (observed 2026-08-21; an earlier
   2026-08-13 observation of `status: open` has since drifted), while `deferred_until`
   reads `null` in the `bd show --json` projection — so which field carries the truth
   has already changed once underneath this instruction, and a field read reports a
   FALSE failure. Assert the OUTCOME the ordering exists to produce (**P-1**):

   ```bash
   # -n 0: bd ready caps at 100 rows by default, so a capped query proves nothing.
   # NO --exclude-label human: absence would then prove the LABEL, not the defer.
   # Positive control = a NON-EMPTY .data: it proves bd ready ran and is not vacuously
   # empty, so the absence half carries weight. Both halves read ONE snapshot, so the
   # control cannot pass against a different query than the absence check.
   # NOT --include-deferred: that flag re-admits only status:open beads with a future
   # defer_until, never status:deferred ones -- so it can never see the bead you just
   # created, and as a control it would invert the verdict (bead tc-8x45).
   READY="$(bd ready --json -n 0)"
   jq -e '(.data // []) | length > 0' <<<"$READY" >/dev/null &&
     jq -e --arg id "<BEAD>" 'all(.data[]?; .id != $id)' <<<"$READY" >/dev/null &&
     echo "OK: <BEAD> is not workable" || echo "FAIL: <BEAD> is workable, or bd ready is broken -- do NOT gate"
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

   Re-run step 2's check IN FULL: the bead MUST still be absent from `bd ready`. The
   positive control still applies unchanged — it tests the QUERY, not the defer — and
   the gate, not a defer, is now what holds the bead.

The bead stays out of `bd ready` until someone runs `pn workspace apply`; the
apply's post-hook runs `pb gate check`, which resolves the gate once the change's
patch-id is in the applied history AND — only for an input that apply resolved
through the terminal's flake lock — that lock contained the commit (see rule 5).
Then the bead surfaces as ordinary work.

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
5. **An apply is enough when the apply OVERRODE the repo; only a genuinely
   lock-built input needs push + relock.** `pn workspace apply` passes
   `--override-input <alias> git+file://<clone>` for every workspace repo the
   terminal declares as a flake input whose clone is on disk, so nix builds it from
   the LOCAL CLONE at eval-time HEAD, not from the terminal's `flake.lock`.
   `pb gate check` therefore SKIPS its lock condition for such a repo and the gate
   resolves on the apply alone — as it always has for the TERMINAL repo. So a
   locally-landed, unpushed commit DOES resolve its gate at the next apply, which
   matters because `/drain-beads` lands locally and deliberately does not push.

   The lock condition still applies where the build really did resolve through the
   lock (no clone for nix to be pointed at), and — fail-closed — to an applied-state
   record written by a `pn` predating the override record
   (`applied_state_schema == 2`): there the gate stays blocked with a reason naming
   that assumption until one more apply re-records it. A gate blocked long enough
   still reaches stale-handling. Operator ruling of 2026-08-14 on bead `pg2-14yqh`;
   see ADR 0046's amendment "condition 2 is CONDITIONAL on whether the apply
   OVERRODE the repo".

## When NOT to gate

- The follow-up has no post-deploy dependency → just create an ordinary bead.
- You cannot commit the change yet → gate later, after committing.
- You expect a squash-merge of the gated change → see Rule 4.
