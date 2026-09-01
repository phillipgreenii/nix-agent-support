# pr-pool: `INV-PREC-1` conflict resolutions and the participant-model decision

**Status**: Accepted
**Date**: 2026-08-31
**Deciders**: Phillip Green II

## Context

`packages/pr-pool/docs/behavior/invariants.md`'s `INV-PREC-1` declares a precedence ordering over
pr-pool's own invariants — **safety/isolation > continuity (never drop work) > efficiency** — and
its own enforcement clause: a newly-discovered conflict between invariants **MUST** be recorded as
an open question and resolved by a decision, **never** chosen ad hoc by an agent.

The `behavior-docs-impl-conformance` pass over this set found that enforcement clause itself
violated: two conflicts were resolved in shipped code without ever being recorded as an open
question or a decision.

1. **Produce failure vs. delivery of already-queued work.** One failing pull source aborts the
   whole produce tick (`internal/discover/discover.go:175-178`); a failed tick then skips
   `Dispatch` **and** `Expire` in daemon mode (`cmd/pr-pool/run.go:309-314`), and a drain run exits
   `1` **before dispatching anything** (`run.go:246-249`). Events already durably queued —
   including ones pushed in over the socket, unrelated to the failing pull source — are denied the
   delivery opportunity `INV-EVT-1` owes them. Efficiency (fail fast on any source error) was
   placed above continuity (never drop work), inverting `INV-PREC-1`'s declared ordering. The
   realization-gap register records this as `INV-PREC-1`'s own row (bead `pg2-sf520`).

2. **A global operator gate vs. the retry-bound expiry clock.** `INV-LIFE-2` (authored by
   `pg2-84o3m.2`, this docket) is itself the resolution of a second `INV-PREC-1`-shaped conflict:
   while a `quota-paused`/`cicd-down` gate suspends production and new dispatch, does an
   already-queued event's retry-bound clock (`INV-EVT-4`'s `expiresAt`) keep advancing, or pause
   with production? `INV-LIFE-2` already states the answer as authored intent: **expiry MUST
   continue** — "`INV-EVT-4`'s retry-bound clock does not pause with production." This ADR records
   that resolution as the decision `INV-PREC-1`'s enforcement clause requires; it changes nothing
   `INV-LIFE-2` did not already say.

Separately, the same conformance pass raised a third, related question that is not itself an
`INV-PREC-1` conflict but bears on the same code: the docs describe pr-pool's participants (event
handlers, sources, monitoring sinks, storage) as reached over a **message-passing** contract — a
per-call tracking id, an inline-or-deferred reply, a callback for a deferred ack (`INV-INTF-1`,
`INTF-HANDLER`, `INTF-SOURCE`) — while the shipped code reaches every participant kind through a
same-process Go call (`Offer`, `query.Run`) with no tracking id ever minted. The operator must
decide whether to build the documented message-passing architecture or to fix the docs to describe
the in-process shortcut.

## Decision

### 1. Produce failure: never-drop-work wins

A failing pull source's error **MUST** be isolated to that source and **MUST NOT** withhold
delivery, dispatch, or expiry for any other source's or any pushed event's already-queued work:

- A source's query failure (retries exhausted) is recorded against that source only; the produce
  pass continues for every other configured source.
- `Dispatch` and `Expire` run over the queue regardless of any single source's produce failure — a
  source outage suspends **that source's own production**, never the queue's delivery or
  retention-bound cleanup of events already accepted.
- An undeclared-type pull event is rejected and counted, not enqueued (defense-in-depth against a
  misconfigured or misbehaving source), but this rejection is itself per-event and MUST NOT abort
  the pass for other events or sources.
- A drain run still completes the drain before it reports a partial-produce failure; the exit code
  for a partial-produce drain is a generic failure (`1`), deliberately not a branchable, specific
  code (per the repo's coarse exit-code convention, `ADR 0042`) — a caller MUST NOT infer more from
  it than "something did not fully succeed."

This is the decision `INV-PREC-1`'s enforcement clause requires; realizing it in code is Task 1.1
(bead `pg2-84o3m.8`), which this ADR authorizes and whose commit cites it. The register row this
ADR resolves the _decision_ half of (`INV-PREC-1`, bead `pg2-sf520`) stays open, narrowed to the
remaining _build_ gap, until Task 1.1 lands.

### 2. Gate vs. expiry: expiry continues

Per `INV-LIFE-2` as already authored: while a global operator gate is set, the core suspends event
**production** and **new dispatch**, but an already-queued event's retry-bound expiry clock
(`INV-EVT-4`) keeps advancing regardless. A gate is not a pause of wall-clock time for any event's
`expiresAt`.

This is deliberately **not** "freeze every deadline for the duration of the gate": `INV-EVT-4`'s
retry bound is an absolute instant, not a duration counted from an origin the core would have to
track through a pause (`packages/pr-pool/docs/decisions/wire.md`'s `DEC-EVENT-1` records why
expiry is an instant, not a duration — there is no clock origin to choose). Manufacturing a
pause-adjusted deadline would add exactly the kind of core-tracked bookkeeping `GOAL-MIN-1`
forbids. It is also the safety-consistent reading of `INV-PREC-1`'s own ordering: a gate exists to
isolate an external effect (a quota exhaustion, a broken CI/CD path) from every event still queued
behind it, and an event that has gone stale across a long gate is a candidate for exactly the kind
of delivery a resumed core should not blindly retry — letting expiry continue, so a stale event
drops rather than fires the instant the gate lifts, keeps safety/isolation ranked above continuity
for this specific conflict, consistent with `INV-PREC-1`'s ordering rather than an exception to it.

No further build is owed by this half of the decision — `INV-LIFE-2`'s text already states it, and
this ADR's job is only to record it as the decision `INV-PREC-1` requires. `INV-LIFE-2`'s own build
gap (the pause mechanism itself: gate files, `pause`/`resume` subcommands, the halted-vs-quiescent
inspection distinction) is tracked separately (bead `pg2-gkpjz`) and is unaffected by this ADR.

### 3. Participant model: full message-passing is the target; the in-process shortcut is register-held intent

The documented architecture — every core→participant interaction as a message over the common
manager contract (`INV-INTF-1`), with a per-call tracking id, an inline-or-deferred reply, and a
callback for a deferred ack — **is the intended target**, not a description to be relaxed to match
the code. The shipped in-process shortcut (an `Offer` method call standing in for
`handler.dispatch`, `query.Run` standing in for a source query message, no tracking id anywhere) is
**consciously kept for now** and treated as **settled-but-unreached intent**, exactly as the
realization-gap register already records it — the register rows for `INTF-HANDLER`
(bead `pg2-q6tqg`), `INTF-SOURCE` (beads `pg2-nr1xm`, `pg2-u7rzl`),
`INTF-STORE` (bead `pg2-ev34a`), `INTF-MON` (bead `pg2-ov09n`), `INV-OBS-1` (bead `pg2-zqpxj`), and
`INV-CONC-1` (bead `pg2-wwtwk`) already state this correctly and need no correction by this ADR.
(`INV-LIFE-1`'s row, formerly tracked by bead `pg2-jr90c`, was deleted by Task 2.1/2.4 — in-process
registration and lifecycle promotion are now built, so it no longer belongs in this list.)

Concretely: this docket's Phases 0-4 build out the queue-core half of the plan against the
in-process shape (a Go method call is an acceptable stand-in for a message while no seam has yet
forced the distinction to matter). Extracting the participant interfaces into a real
message-passing boundary — minting tracking ids, giving deferred replies a callback, registering
participants, moving the `ccpool`/`beads`/command-tool drivers out of the generic binary — is
**Phase 5**'s job, not any task before it. Nothing before Phase 5 is authorized to claim
message-passing is built; nothing before Phase 5 should invent a partial or ad hoc messaging shim
either — the register rows above are the correct record of this gap until Phase 5 closes it.

## Consequences

### Positive

- `INV-PREC-1`'s own enforcement clause — that a conflict be resolved by a recorded decision, not
  settled ad hoc — is itself now satisfied for both conflicts this pass found. A future reader of
  the register's `INV-PREC-1` row finds a decision, not an unrecorded ad hoc inversion.
- Task 1.1 has an authorized decision to build against and to cite in its commit, rather than
  inventing the resolution itself.
- The participant-model question is closed as a decision (build the documented architecture; the
  in-process shape is intent-not-yet-built) rather than left as an implicit disagreement between
  the docs and the code — the docs are correct as written and stay unchanged by this ADR.

### Negative

- The produce-failure decision constrains Task 1.1 to a specific shape (per-source error
  isolation, `Dispatch`/`Expire` unconditional on a single source's failure, drain completes before
  reporting) rather than leaving the resolution to that packet's own judgment.
- Confirming the participant model as the target (rather than settling for the in-process shape)
  keeps a real, large body of future work (Phase 5) on the books; a deployment relying on the
  current in-process behavior gets no signal from this ADR that anything is changing before Phase 5
  actually lands.

### Neutral

- The gate-vs-expiry decision changes nothing already authored in `INV-LIFE-2` — it exists so the
  decision has an ADR of record, per `INV-PREC-1`'s own requirement, not because the outcome was in
  doubt.
- This ADR records decisions; it builds nothing. The produce-failure isolation is Task 1.1's to
  build; the pause/resume mechanism `INV-LIFE-2` describes is a separate, already-tracked build gap
  (bead `pg2-gkpjz`); the message-passing extraction is Phase 5's.

## Alternatives Considered

### Freeze expiry while gated (pause-adjusted deadlines)

Rejected for the gate-vs-expiry conflict. Requires the core to track elapsed paused time and
retroactively adjust every queued event's `expiresAt` — a form of core-tracked bookkeeping
`GOAL-MIN-1` forbids, and one `DEC-EVENT-1`'s absolute-instant design was never built to support.
See Decision, "Gate vs. expiry: expiry continues".

### Fail-fast stays as documented intent (change `INV-PREC-1`'s ordering, or scope produce failures out of it)

Rejected for the produce-failure conflict. `INV-PREC-1`'s ordering (safety/isolation > continuity >
efficiency) is a set-level, deliberately-declared precedence (`INV-19`'s optional mechanism); a
produce failure has no safety/isolation dimension that would place it above continuity, so there is
no principled carve-out — the current fail-fast behavior is simply the wrong resolution of an
ordinary continuity-vs-efficiency conflict, not evidence the ordering itself needs revisiting.

### Fix the docs to describe the in-process participant shortcut

Rejected for the participant-model question. The global constraints binding this docket (docs MUST
NOT be edited to describe divergent code; intended-behavior changes are operator-approved) name
`INV-PREC-1` conflict resolutions as one of the approved intended-behavior changes carried by this
plan — the participant model is not among them. The operator's direction (recorded here) is to
build toward the documented architecture in Phase 5, not to relax the docs to the shortcut.

## Related Decisions

- Realizes `INV-PREC-1`'s enforcement clause (`packages/pr-pool/docs/behavior/invariants.md`) for
  the two conflicts the `behavior-docs-impl-conformance` pass found.
- Consumes `INV-LIFE-2` as authored by `pg2-84o3m.2` (this docket) — the gate-vs-expiry resolution
  is that invariant's text, recorded here as the decision `INV-PREC-1` requires.
- Authorizes Task 1.1 (bead `pg2-84o3m.8`), whose commit message cites this ADR
  (`ADR per Task 0.6`).
- The participant-model decision is realized incrementally by this docket's register (`INV-23`)
  until Phase 5 extracts the participant interfaces for real; see the
  `INTF-HANDLER`/`INTF-SOURCE`/`INV-LIFE-1`/`INTF-STORE`/`INTF-MON`/`INV-OBS-1`/`INV-CONC-1` rows in
  `packages/pr-pool/docs/behavior/README.md`'s realization-gap register.
- Design spec: `docs/superpowers/specs/2026-08-29-pr-pool-impl-conformance-delta.md` (Theme C for
  the produce-failure conflict; its "Decisions only a human can make" item 1 for the gate/pause
  intent `INV-LIFE-2` already resolved, item 3 for the participant model) and
  `docs/superpowers/specs/2026-08-29-pr-pool-review-digest.md`.
