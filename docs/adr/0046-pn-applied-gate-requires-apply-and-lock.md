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

## Amendment: condition 2 is CONDITIONAL on whether the apply OVERRODE the repo (bd pg2-14yqh)

**Status**: Accepted
**Date**: 2026-08-14
**Tracking**: pg2-14yqh
**Provenance**: operator ruling by `phillipg@ziprecruiter.com`, 2026-08-14, recorded on bead
`pg2-14yqh` (via `/unblock-human-beads`, actor `16b010d6-8af6-4256-a14b-f4969e4ab412-unblock`).

### What changed under this ADR's feet

The Context above rests on "the terminal consumes these repos as REMOTE flake inputs, so the build
resolves them through the terminal's `flake.lock`". Bead `pg2-xg9wp` **disproved** that for
`pn workspace apply`, and `phillipg-nix-repo-base` ADR 0025's own amendment records the byte-level
evidence:

- `pn`'s `Apply` computes `--override-input` **unconditionally**, appending one
  `--override-input <alias> git+file://<clone>` per workspace lock edge. So every non-terminal
  workspace repo with a clone on disk is built from that **local clone at eval-time HEAD**, not from
  the terminal's pinned rev.
- Generation `794` is decisive alone: its CETA source is `phillipgreenii-nix-agent-support` at
  `9e3bb00f`, which is **not** an ancestor of the terminal's pinned `2b18e16`, while the terminal's
  `flake.lock` was last touched over an hour earlier. A lock-built system could not contain it.

So condition 2 as landed is **stricter than the build requires**. It stays fail-SAFE — a `blocked`
verdict means "not provable from the lock", never "not in the running system" — but the cost lands
on exactly one workflow: `/drain-beads` lands locally and deliberately does not push (always-on rule
U-5), so every verification bead it gated sat blocked until someone pushed **and** relocked, and the
stale handler then converted it into operator traffic.

### The ruling

**Condition 2 becomes CONDITIONAL on whether the repo was actually OVERRIDDEN in that apply.**

- **OVERRIDDEN repo** — condition 2 is **SKIPPED**. Its built code is the local eval-time HEAD, so
  condition 1 already covers it, exactly as it already does for the terminal.
- **Genuinely lock-built input** — condition 2 applies **unchanged**.

Two alternatives were put to the operator and **REJECTED**; neither is to be re-proposed:

- **(a) Keep condition 2 unconditional.** Declined: the operator traffic is real and lands
  specifically on the `/drain-beads` workflow.
- **(b) Relax condition 2 to compare against the OVERRIDDEN eval-time HEAD** (what the build
  actually used). Declined although it matches the mechanism most directly, because it depends on
  bead `pg2-0782j` (the `markApplied` TOCTOU, still OPEN) being fixed first: eval-time HEAD is
  precisely the value `markApplied` currently mis-samples, so keying the gate to it now would key it
  to a known-wrong reading.

Also **not reopened**, and unchanged by this amendment:

- **Condition 1** — untouched.
- **`pb gate create`** — still says NOTHING about an unpushed target. No warning, no refusal, no
  `--allow-unpushed` flag. The original rejection of a creation-side check stands.
- **`locked_revs`** — stays exactly as recorded. It remains useful, correct, fail-closed evidence,
  and `phillipg-nix-repo-base` ADR 0025's decision stands. It is now qualified, not withdrawn: it is
  what the build carries **only for an input that was not overridden**.

### The producer had to record what it overrode

Nothing in the applied-state carried the override fact, so `phillipg-nix-repo-base` extends the
record again — schema **3** adds `overridden_inputs` (workspace repo key → the `git+file://<dir>`
flake URL the `--override-input` flag carried), published per repo by `pn workspace info --json` as
`overridden`. Both projections of the override set — the flags nix is given and the map recorded —
are derived from **one** resolution inside `Apply`, so the record cannot describe a different build
than the one that ran.

### Applicability, revised

| `pn` record state                                         | condition 2                                                          |
| --------------------------------------------------------- | -------------------------------------------------------------------- |
| `applied_state_schema < 2`                                | SKIPPED — the record predates `locked_revs`; no lock information     |
| schema `>= 2`, `terminal_input` false                     | SKIPPED — the terminal does not consume this repo as a flake input   |
| schema `>= 3`, `overridden` true                          | SKIPPED — built from the local clone; condition 1 is the whole truth |
| schema `>= 2`, `terminal_input`, NOT overridden, rev set  | ENFORCED                                                             |
| schema `>= 2`, `terminal_input`, NOT overridden, rev `""` | FAIL CLOSED — the apply cannot say what it built that input from     |

Branch ORDER is load-bearing: the override row precedes the empty-rev row. An overridden input whose
locked rev could not be established must SKIP, because nothing was built from that rev and its
absence therefore says nothing about whether the change shipped. Ordered the other way, such a gate
would be permanently unresolvable while the change is demonstrably in the running system.

`overridden` true implies `terminal_input` true — both derive from the terminal's lock edges, and an
override additionally requires the clone to be present — so the new row only ever REMOVES repos from
the condition's scope; it can never widen it.

### The older-record fallback leans FAIL-CLOSED, and that is the opposite of the schema `< 2` row

A **schema-2** record carries `locked_revs` but no override set, so `overridden` reads `false` —
indistinguishable on the wire from "recorded, and genuinely lock-built". That absence is read as NOT
overridden, i.e. condition 2 is **ENFORCED**. The asymmetry against the first row is deliberate:

- At schema `< 2` condition 2 is **unevaluable**. The only alternative to skipping is that no gate
  resolves anywhere until a new `pn` is built, pushed, relocked and applied — an unbounded bootstrap
  stall, and the fix ships through that very path.
- At schema 2 condition 2 is **fully evaluable** and its verdict is DETERMINATE (`lockMissing` →
  `blocked`, not `skipped`, so `pb gate check` still exits 0 and the apply post-hook does not warn).
  Leaning open there would **assert** an override the record does not evidence, and a false "the
  change shipped" is the expensive direction — it is the `pg2-ft60a` harm. A false "still blocked"
  costs one stale-handler escalation to a person.
- The window is **bounded to one apply**: the next successful apply rewrites every record at schema 3. It is not the unbounded stall the first row exists to prevent.

That fail-closed verdict names its own assumption in the reason string, so a conservative answer is
never mistaken for a wrong one.

### Consequences of the amendment

- A `/drain-beads` verification bead gated on a locally-landed commit now resolves at the next apply,
  which is what the gate was always meant to mean for these repos.
- Condition 2 is NOT dead code, but in this workspace it is now **rarely reached**: `Apply` overrides
  every terminal lock edge whose clone exists, so the ENFORCED row is reached only for an input with
  no clone on disk, for a record written before schema 3, or for a future `pn` that does not
  override. The ruling accepted that knowingly — the condition remains the fail-closed anchor for a
  build that really did resolve through the lock.
- `markApplied`'s "locked rev unresolvable ⇒ your gate stays blocked" warning is now conditioned on
  the input NOT having been overridden. The claim is false for an overridden input, and warning
  anyway would fire on every apply — which is the same "trains the operator to ignore the warning"
  failure this ADR argues against for `blocked` vs `skipped`.
- ADR 0018 and the body of this ADR are **amended**, not superseded. Condition 1, the gate grammar,
  the `blocked`/`skipped` split, the stale handling, and `pb gate create`'s untouched contract all
  stand.
