# Invariants — pg-router-ccpool-handler

Rules this module's implementation MUST hold (see [README](README.md)'s Realization gaps for the
residual the build has not yet caught up to). A rule pg-router's own core
already states (event shape, at-least-once delivery, the offer/decline machinery) is not restated
here — see `packages/pg-router/docs/behavior/invariants.md`. These are the rules specific to this
module as an **implementer** of `INTF-HANDLER`/`INTF-SOURCE`.

- **`INV-CCH-1`** — this module's Go module MUST import `packages/pg-router` (its wire schemas and
  `conformance` package) and `packages/pg-router` MUST NOT import this module back. The dependency
  is one-way (`phillipgreenii-nix-agent-support` ADR 0065's "New module" decision); a reverse
  import would put concrete participant behavior back inside the generic core it was extracted
  out of.
- **`INV-CCH-2`** — a handler session MUST tolerate a duplicate event and MUST support the
  deferred-ack reply form, matching `INTF-HANDLER`'s obligations on every implementer; this module
  states no exception of its own.
- **`INV-CCH-3`** — a handler session's post-accept outcome (`retryable`, `resource-limit`,
  `critical`) MUST be surfaced only on this module's own logs/metrics or turned into a new event —
  never reported back to the core as anything but the opaque completion-outcome string
  `INTF-HANDLER` already grants (`packages/pg-router/docs/behavior/interfaces.md`'s `INTF-HANDLER`
  section, "A handler's run status is not part of this contract").
- **`INV-CCH-4`** — this module's own coverage-thresholds file
  (`packages/pg-router-ccpool-handler/tests/coverage-thresholds.txt`) is never merged with, or copied
  into, `packages/pg-router/tests/coverage-thresholds.txt`. The module boundary is also the
  coverage-gate boundary: a bar activated on one side never silently moves the other side's ratchet
  (`phillipgreenii-nix-agent-support` ADR 0065's "New module" decision).
- **`INV-CCH-5`** — this module MUST NOT name a concrete backing tool (`ccpool`, `bd`, `pg-pr`, or
  any operator-configured command) anywhere in `packages/pg-router`'s own contract surface (its
  `--help` text, config schema, or wire messages). Naming a backing tool is this module's own
  business, per the Floor stated in both this set's and pg-router's own `## Scope`.
- **`INV-CCH-6`** — a handler MUST query pool capacity before preparing isolation or
  launching a session and MUST NOT launch when the pool reports no free slot or cannot be
  read. It signals this ONLY through the transport's pre-accept busy decline
  (`conformance.ExitBusy`), so the core re-offers the event with backoff; it mutates no bead
  and creates no worktree. This is consistent with `INV-CCH-3`: busy is a pre-accept signal,
  not a post-accept outcome.
- **`INV-CCH-7`** — when a session ends before its bead completes, the handler MUST read
  ccpool's recorded close reason. An external close (`idle_ttl`, `cap_eviction`, `operator`)
  MUST release the bead (status open, assignee cleared) with a comment naming the reason,
  regardless of the role's `on_failure`, and MUST escalate to `human` on the second
  consecutive external close of the same bead. Only an unexplained death, or a close the
  handler itself requested (`handler`), applies `on_failure`.
- **`INV-CCH-8`** — when preparing per-bead isolation (`git worktree add`) fails because the
  filesystem has run out of room, the handler MUST NOT treat it as a per-bead launch failure
  (no `pool-launch-fail`/`human` escalation) — a full disk is a transient, system-wide
  condition that would hit whichever bead happened to dispatch next, not a defect in the one
  that hit it first. It MUST signal this ONLY through the transport's pre-accept busy decline
  (`conformance.ExitBusy`, reason `low-disk`), mutating no bead, so the core simply re-offers
  the event later with backoff. This is consistent with `INV-CCH-3`/`INV-CCH-6`: a system-wide
  resource shortage is a pre-accept signal, not a post-accept or per-bead outcome.
