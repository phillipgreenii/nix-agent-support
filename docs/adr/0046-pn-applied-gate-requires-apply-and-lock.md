# A `pn:applied` gate resolves only on BOTH an apply and that apply's lock

**Status**: Accepted
**Date**: 2026-08-14
**Deciders**: Phillip Green II (with Claude)
**Tracking**: pg2-ft60a

## Context

ADR [0018](0018-pb-tool-and-pn-applied-contract.md) defined `pb gate check` to resolve a
`pn:applied` gate when the gated change's `git patch-id` appears in
`applied_baseline..applied_ref`, where `applied_ref` comes from `pn workspace info --json`
(`phillipg-nix-repo-base` ADR 0012). That is one fact, and it answers a narrower question than the
gate asks.

`applied_ref` is the repo's local `git rev-parse HEAD` at apply time. It proves an apply RAN over a
checkout that contained the change. It does **not** prove the applied SYSTEM contains it. When the
terminal pins the repo as a **remote flake input** — a `github:` pin — the code reaches the build
through the terminal's `flake.lock`, so a commit landed on local `main` and never pushed-and-relocked
is absent from the built system while the scan finds its patch-id and resolves the gate.

That is the whole point of the gate, inverted. It exists so a peer draining `bd ready` does not claim
a verification bead and "verify" against un-applied code. A false resolve makes the verifier most
likely conclude "the feature is broken" rather than "it was never deployed" — inviting a revert of
correct work. Three gates did exactly this: `pg2-fci69`, `pg2-06r8r` and `pg2-9s3pm`, releasing
`pg2-c40r4` and `pg2-zbh85`.

Two weaker fixes were considered and rejected:

- **Resolve against the lock only.** Drops the evidence that an apply ever ran, so a bare relock
  would resolve the gate.
- **A creation-time refusal or warning when the target commit is unpushed.** Gating an unpushed
  commit is normal and correct — holding the follow-up until it ships is the gate's job — so the
  check belongs at resolution, not creation.

## Decision

`pb gate check` resolves a `pn:applied` gate only when **both** conditions hold.

### Condition 1 — an apply happened (KEPT verbatim from ADR 0018)

The gated patch-id appears in the scan of `applied_baseline..applied_ref` (or the `--last-n`
fallback). Unchanged, including the range selection and the dirty/`--strict` and stale behaviour.

### Condition 2 — THAT apply's lock contained the commit

The gated commit is an ancestor of `locked_revs[repo]` — the rev the terminal's `flake.lock` pinned
for that repo **at the apply that satisfied condition 1**, published per repo by
`pn workspace info --json` as `locked_rev` (`phillipg-nix-repo-base` ADR 0025).

The rev is the one RECORDED WITH THAT APPLY, never the lock as it stands at check time. Asking "is
the lock NOW past the commit?" re-admits the same false resolve in a narrower window: an apply at T1
followed by a relock at T2 > T1 would satisfy both conditions while the running system was built from
the pre-relock rev.

### The gated commit comes from the condition-1 scan

`git patch-id` prints `<patch-id> <commit-sha>` per patch when fed from `git log -p`, so the commit
is a free by-product of the scan condition 1 already runs. No new gate metadata is recorded and
`pb gate create` is untouched (see below). One patch-id can map to several shas when a diff appears
twice in the range (a cherry-pick); those copies differ in ancestry, so ANY of them being in the lock
means the change shipped.

### Applicability, keyed on the producer's schema version

| `pn` record state                         | condition 2                                                        |
| ----------------------------------------- | ------------------------------------------------------------------ |
| `applied_state_schema < 2`                | SKIPPED — the record predates `locked_revs`; no lock information   |
| schema `>= 2`, `terminal_input` false     | SKIPPED — the terminal does not consume this repo as a flake input |
| schema `>= 2`, `terminal_input`, rev set  | ENFORCED                                                           |
| schema `>= 2`, `terminal_input`, rev `""` | FAIL CLOSED — the apply cannot say what it built that input from   |

The **terminal repo's** gates keep resolving on condition 1 alone with no special case: the apply
builds the terminal from its local directory, so it has no `locked_revs` entry and lands in the
second row.

Keying the first row on the SCHEMA VERSION rather than on "the map is empty" is required. "Old
record, no information" and "current record, this repo genuinely is not an input" are identical if
you probe the map alone, and they are not the same claim. Skipping for old records is a deliberate
bootstrap concession: treating a missing map as unprovable would make every gate unresolvable until a
new `pn` is built, pushed, relocked and applied — and this fix itself ships through that path.

The last row fails closed because falling back to condition 1 there is precisely the unsound case.

### A blocked gate says why, in its own list

`CheckResult` gains `blocked` (optional; nothing renamed or removed). A gate held back by
condition 2 is reported there with an actionable reason naming the gated commit, the locked rev, and
the remedy (push, relock, re-apply), and the non-JSON output prints those lines rather than only a
count. Silence is what made the original defect expensive.

`blocked` is deliberately NOT `skipped`. ADR 0018 made `skipped` mean "undeterminable" and made
`pb gate check` exit non-zero iff it is non-empty; `pb gate check` is the apply post-hook, and a
post-hook failure makes `pn` warn. Routing a determined "this is correctly still gated" into
`skipped` would therefore emit a warning after every apply that has a normal pending gate, which
trains the operator to ignore the warning. A determinable "no" is not the absence of an answer, so it
gets its own list and leaves the exit code alone. The genuinely undeterminable cases — no recorded
rev, no recoverable commit — stay in `skipped` and keep the non-zero exit.

Stale handling still applies to both, so a gate blocked long enough still reaches
`--stale-handler`.

### `pb gate create` is UNCHANGED

No refusal, no warning, no `--allow-unpushed` flag, and no lock or ancestry inspection at create
time. This is asserted by a test, not merely left untested: satisfying the requirement via a
creation-time refusal would be non-conforming.

## Consequences

### Positive

- A verification bead can no longer be released against a change that is committed locally but
  absent from the built system, and the operator is told why in the apply's own output.
- A relock after an apply cannot retroactively resolve that apply's gate.
- Existing behaviour is strictly preserved where it was already sound: condition 1 is untouched, the
  terminal repo needs no special case, and a workspace whose `pn` predates `locked_revs` keeps
  working.
- Recovering the commit from the existing scan means no gate schema change, so gates created before
  this ADR are evaluated by the new rule without migration.

### Negative

- A gate on a flake-input repo now needs push + relock + apply before it resolves; committing and
  applying is no longer enough. The ancestor case (pushed, terminal not yet relocked) stays blocked.
  This is the accepted fail-closed trade-off.
- Condition 2 needs the locked rev present in the local object store. A rev never fetched locally
  makes `git merge-base --is-ancestor` fail, which reads as "not contained" and blocks the gate —
  fail-closed, and reported, but a `git fetch` may be the actual remedy.
- A change rebased between the local commit and the push gets a new sha, so the local sha is not an
  ancestor of the locked rev even though the diff shipped. Condition 1 is patch-id based and survives
  that; condition 2 is sha based and does not, so such a gate falls to the stale-handler.

### Neutral

- ADR 0018 is **amended**, not superseded: its gate grammar (`await_type`, `await_id`), the
  co-location invariant, the multi-DB dedupe key, `pb gate create`'s contract, and the packaging
  decision all stand.
