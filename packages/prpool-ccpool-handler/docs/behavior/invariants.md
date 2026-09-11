# Invariants — prpool-ccpool-handler

Rules this module's implementation MUST hold, once populated (see [README](README.md)'s
Realization gaps — none of the code these rules govern exists yet). A rule pg-router's own core
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
  (`packages/prpool-ccpool-handler/tests/coverage-thresholds.txt`) is never merged with, or copied
  into, `packages/pg-router/tests/coverage-thresholds.txt`. The module boundary is also the
  coverage-gate boundary: a bar activated on one side never silently moves the other side's ratchet
  (`phillipgreenii-nix-agent-support` ADR 0065's "New module" decision).
- **`INV-CCH-5`** — this module MUST NOT name a concrete backing tool (`ccpool`, `bd`, `pg-pr`, or
  any operator-configured command) anywhere in `packages/pg-router`'s own contract surface (its
  `--help` text, config schema, or wire messages). Naming a backing tool is this module's own
  business, per the Floor stated in both this set's and pg-router's own `## Scope`.
