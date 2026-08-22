# pr-pool: the durable event queue is the universal intermediary; internal/eventbus is retired

**Status**: Accepted
**Date**: 2026-08-21
**Deciders**: Phillip Green II

## Context

pr-pool grew **two** in-process delivery mechanisms side by side:

- `internal/eventqueue` (`ADR 0031`) — a durable, ordered, de-duped, retention-bounded queue.
  Delivery is offer/accept: the core offers a listener its head event; capacity is
  **listener-enforced** (a pre-accept `busy` decline), never a core-tracked number; one outstanding
  offer per listener (per-handler serial FIFO).
- `internal/eventbus` — an **in-pass** broker (`Bus`), scoped to one drain pass and never
  persisted. Its own package doc called this "a deliberately different, lighter altitude ... [that]
  serves a separate feature" from the durable queue. It carried `Subscribe`/`Lease`/`Ack`/`Nack`/
  `Inflight`, and an opt-in `Aggregator` (`SubscribeAggregate`) correlating events by
  `CorrelationID`.

`docs/behavior/invariants.md` and `interfaces.md` were written against the **durable-queue**
model throughout: `INV-CONC-1` states plainly that "capacity is handler-enforced and declared
nowhere, never a core-tracked number," and the dispatch flowchart (`invariants.md`'s "Dispatch"
section) is the offer/accept/re-offer loop, not a lease/ack/inflight one. The shipped code
disagreed with its own docs: `internal/orchestrator.go`'s `discoverViaBus` drove role dispatch
through `eventbus`, computing `n := r.Cap - bus.Inflight(r.Name)` — exactly the core-tracked
ceiling `INV-CONC-1` says must not exist. `docs/behavior/README.md`'s realization-gap register
(`INV-23`) recorded this divergence explicitly (two rows keyed to this bead, `pg2-f3mcb.2`): the
`Cap`/`Inflight` gap against `INV-CONC-1`, and a second gap against `INV-LIFE-1` — nothing outside
`internal/core`'s own tests ever called `Listen`+`Accept`, because `cmd/pr-pool` had no `run` /
`run-until-idle` subcommand to boot a real core from.

Source-side push/pull modes (`INTF-SOURCE`) and the handler contract (`INTF-HANDLER`) were never
part of this divergence — a source declares its invocation and mode exactly as before, and a
handler is dispatched exactly as before. The disagreement was entirely **core-internal**: which
in-process mechanism actually carried an event from a producer to a role.

## Decision

**The durable event queue (`internal/eventqueue`) is the universal intermediary for core-internal
delivery.** Every event — however it arrived, pull or push — is enqueued there, and **only**
events the queue delivers are ever offered to a handler. `internal/eventbus` is deleted outright
(`eventbus.go`, `aggregate.go`, `Subscribe`/`SubscribeAggregate`/`Lease`/`Ack`/`Nack`/`Inflight`),
not ported or wrapped. This supersedes the "deliberately different, lighter altitude ... serves a
separate feature" framing eventbus's own package doc carried, and the ADR 0026 / ADR 0031 division
of labor that let two delivery mechanisms coexist.

Concretely:

1. **Per-role `Cap` is removed as a concept, not just as a field.** `roles.Role.Cap`, the TOML
   `cap` key, the example-config generator's `cap = N` line, and `config --show`'s `cap=%d` column
   are all gone. `INV-CONC-1` gives a handler exactly two legitimate responses to an offer: decline
   `busy` (the core re-offers within the event's `expiresAt`), or accept and self-manage its own
   concurrency. No configuration surface, in pr-pool or a downstream deployment, states a per-role
   ceiling.
2. **A queue->executor Listener bridge** (`internal/orchestrator.NewListener`) implements
   `eventqueue.Listener` per configured role: `Matches` checks the event's `type` against the
   role's declared `Binds`; `Offer` runs `executor.For(role.Type).Dispatch` **synchronously** and
   always reports acceptance — the offer call itself _is_ the handler session (an inline
   completion, never a deferred ack, on this bridge). It deliberately holds no in-flight counter of
   its own; the only concurrency control is the queue's own one-outstanding-offer-per-listener
   structure, which is exactly what removing `Cap` requires (a second core-side counter under a
   different name would silently reintroduce the divergence).
3. **A discovery->enqueue producer**: `internal/discover.Produce` (rewritten off `eventbus.Bus`
   onto `*eventqueue.Queue`) fires each configured query's trigger and enqueues its emitted events
   directly — `ThresholdTrigger` settling now reads `Queue.DepthByType()` in place of the retired
   `Bus.Depth`.
4. **`cmd/pr-pool` gains `run` (long-running daemon) and `run-until-idle` (discover once, drain to
   idle, exit — `INV-LIFE-1`'s two modes) subcommands.** Both boot `internal/core.Listen` +
   `Accept` (previously exercised only by that package's own tests) over the SAME queue the
   producer and the Listener bridge share, so a push participant reaching the socket mid-run is
   picked up on the very next dispatch pass.
5. **Bare `pr-pool` (no subcommand) now prints usage and exits non-zero.** It previously defaulted
   to a `drain` pass; that default is a deliberate compatibility break. `drain` itself is kept as a
   **deprecated alias for `run-until-idle`** (identical behavior) rather than removed outright, so
   the handful of places that still spell it (documentation, an operator's muscle memory) keep
   working. No launchd/systemd unit or script in this repo invoked bare `pr-pool` or `pr-pool
drain` as a scheduled command at the time of this change — grepped for and confirmed absent — so
   there was no internal caller left to migrate.
6. **Correlation/aggregation is deleted, not ported.** `event.CorrelationSpec`, `Completeness`/
   `AllOf`/`CountOf`, `event.Event.CorrelationID`, the `[role.correlation]` TOML table,
   `roles.Role.Correlation`, and `config.buildCorrelation` are all removed. Nothing shipped ever
   used them in a built-in role, and the durable queue's per-listener offer/accept model has no
   drop-in seam for "hold N correlated events, then fire one aggregate" — porting it well is a
   design question in its own right, not a mechanical move.

## Consequences

### Positive

- One delivery path. The shipped code now matches `invariants.md` / `interfaces.md` as written,
  closing both `pg2-f3mcb.2`-tracked rows in the realization-gap register (`INV-CONC-1`,
  `INV-LIFE-1`) — there is no longer a "the docs describe the target, the code runs something
  else" seam for a later reader to trip on.
- Push AND pull events are now durable, ordered, de-duped, and retried through the SAME queue
  (`ADR 0031`) regardless of source. A push source's event surviving a core restart, or a pull
  source's re-emit being deduped within retention, both now hold for every configured role, not
  just the ones `eventbus` happened to route.
- The core's socket service (`internal/core`) is finally reachable in production: `run-until-idle`
  and `run` are the first callers outside its own test suite, so `ingest-event`, `self-status`,
  `push-inject`, and future inspection commands all have a real running core to reach.

### Negative

- **Per-role declared concurrency is gone.** A deployment that relied on `cap > 1` to run several
  sessions of one role in parallel within a single drain pass no longer can — every configured
  role, ccpool or command, is dispatched exactly one head per `Dispatch()` pass now (structurally
  serial). All ready work still gets processed, just paced across repeated dispatch ticks rather
  than fanned out within one tick. A role that genuinely wants internal concurrency must implement
  it itself (accept the offer, buffer the event, and run several buffered events concurrently on
  its own) — nothing shipped today does this, so every current role is effectively serial. This is
  the INTENDED shape of `INV-CONC-1`, not an oversight, but it is a real throughput change for any
  deployment whose `cap` was set above 1.
- **Correlation/aggregation has no replacement.** A deployment that declared `[role.correlation]`
  loses that behavior outright on upgrade; there is no migration path other than re-deriving the
  need against the durable queue from scratch.
- **`drain` is deprecated**, and bare `pr-pool` now fails where it used to run a pass — a breaking
  change for any caller (script, muscle memory, stale documentation) that never named a
  subcommand.

### Neutral

- The queue's own storage mechanism, retention/expiry model (`at`/`expiresAt`, `DEC-EVENT-1`), and
  per-handler retry cadence (`INV-FAIL-2`) are all unchanged — this decision is about which
  in-process mechanism carries an event, not how the queue itself behaves.
- `INTF-SOURCE`'s push/pull modes and `INTF-HANDLER`'s dispatch contract are both invisible to this
  change: a source still declares its own invocation and mode, and a handler is still dispatched
  through the same manager contract as before. Nothing here is observable from either side of the
  interface — it is core-internal delivery only.

## Alternatives Considered

### Keep `eventbus` as a thin adapter over `eventqueue`

Rejected. It would add a third hybrid shape (bus-fronted-by-queue) instead of resolving the
docs-vs-code divergence, doubles the delivery surface for no behavior gained, and leaves
`Inflight`-shaped bookkeeping one refactor away from silently reintroducing the very core-tracked
counter `INV-CONC-1` forbids.

### Keep `cap` as a non-enforced advisory hint

Rejected. `INV-CONC-1` says capacity is "declared nowhere" — an advisory `cap` the core reads but
does not enforce is still a declaration, and it invites a future change to start enforcing it
"since it's already there."

### Port correlation/aggregation onto the durable queue

Deferred, not rejected outright. No shipped role uses it, the docs don't require it, and the
queue's per-listener offer/accept model has no obvious seam for "hold N correlated events, then
emit one aggregate" — a future bead can design that properly against the durable queue if a real
need appears, rather than mechanically translating the per-pass bus's version.

## Related Decisions

- Realizes `INV-CONC-1` and `INV-LIFE-1` as `packages/pr-pool/docs/behavior/invariants.md` already
  stated them; closes the two `pg2-f3mcb.2`-tracked rows in
  `packages/pr-pool/docs/behavior/README.md`'s realization-gap register.
- Supersedes the eventbus/eventqueue division of labor implicit in `ADR 0031`'s Context (the
  "deliberately different, lighter altitude ... serves a separate feature" framing) and in
  `ADR 0026`'s bare-orchestrator scoping.
- Builds on `ADR 0031` (the durable queue itself, amended by `DEC-EVENT-1`) and `ADR 0036` (the
  CLI never auto-starts a core) — this decision is what actually boots that core from `cmd/pr-pool`
  in production.
- The per-type "serialize" mark `INV-CONC-1` also names (events of one type never running in
  parallel) is explicitly **out of scope** here — tracked separately (bead `pg2-cl9jz`), and this
  decision does not touch it.
