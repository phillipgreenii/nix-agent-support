# Wrapping is a standalone tool, and a pre-ready failure is a decline

**Status**: Accepted (supersedes [0101](0101-roles-and-queries-get-a-minimal-base-env-and-expand-it-with-wrap-entries.md); amends [0065](0065-pr-pool-participant-extraction-prpool-ccpool-handler.md) and [0080](0080-ccpool-usage-limit-admission-gate.md); rulings 30, 31 and 32)
**Date**: 2026-10-22
**Deciders**: Phillip Green II

## Context

ADR 0101 made `wrap` the single primitive that expands a role's or query's minimal base environment: an
ordered list of command prefixes owned by pg-router's configuration, a `PG_WRAP_*` item context, a ready
marker that tells a wrapper failure from a payload failure, a `wrap-failed` failure reason that carries
no strike, and a handler-side role gate that pauses a role whose wrap is broken. Three problems followed
from putting that primitive inside pg-router and its adapters:

- **Coupling.** pg-router, the ccpool adapter and the wrapper mechanism all had to know each other's
  names: core validated wrap entries, the adapters appended a marker stage and set `PG_WRAP_*`, and the
  wrapper contract lived in pg-router's package. A wrapper is a general idea (a chain of command prefixes
  around a payload) that has nothing to do with scheduling work.
- **State to keep consistent.** The ready marker, the stage stderr files, the `wrap-failed` signature and
  its strike exemption, and the role gate were four pieces of state whose only job was to classify a
  failure that is simpler to classify by where it happens: before the payload is ready, or after.
- **A wrong escalation.** A launch that fails before the session is ready escalated the bead to `human`
  through the `pool-launch-fail` stamp (ADR 0080's Decision item 3 even had to carve out the usage-limit
  case). A launch failure is a fault of the environment, not of the item, and the item should be offered
  again.

The design of record is bead `pg2-12gr4` (rulings 30, 31 and 32, made by the operator, Phillip Green II,
on 2026-10-21 and 2026-10-22), cited in the form the behavior sets use: phillipgreenii-nix-agent-support
· bead `pg2-12gr4` · the heading named in each decision below.

## Decision

1. **pg-wrap is a standalone wrapper tool, outside both pg-router and ccpool.** It applies an ordered
   chain of command prefixes around a payload (a Decorator, as chain-loading does in daemontools'
   `chpst`). pg-router, ccpool and pg-wrap are mutually unaware: none imports, names or validates
   another. The wrap spec is pg-wrap's own configuration file, separate from pg-router's configuration;
   a role or query names a spec in its own command or session environment, and pg-router never reads one
   (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "Terms and patterns").
2. **The library and its contract suite move to `packages/pg-wrap`.** The wrap library and the wrapper
   contract suite that lived in `packages/pg-router/wrap` move into pg-wrap's own package. The ready
   marker is deleted and argv templating is stripped from the library. pg-wrap carries its own
   behavior-docs set and this ADR (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "pg-wrap
   (standalone; ruling 30)").
3. **pg-router always runs a command and has no wrap concept.** These are superseded: the `[[query]]`
   `wrap` key, the planned role `wrap`, every `PG_WRAP_*` variable pg-router was to set, the ready marker,
   the `wrap-failed` signature with its strike exemption, and the handler-side role gate
   (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "pg-router's side: commands, base env and item
   context").
4. **pg-router keeps the minimal base environment, the item context and argv templating.** The base
   environment keeps its strict switches. A role or query is a command plus that base environment and the
   item context, and command roles keep rendering their argv through the per-element template engine. The
   `PG_ROUTER_ROLE`, `PG_ROUTER_ITEM_ID` and `PG_ROUTER_ITEM_TYPE` variables carry the item context
   (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "pg-router's side: commands, base env and item
   context").
5. **A ccpool role is wrapped through its pool's `claude.bin` and its session environment.** The pool's
   `claude.bin` is `pg-wrap-exec`, and the role's `sessionEnv` carries `PG_WRAP_SPEC`, which names the
   wrap spec pg-wrap reads. ccpool treats `claude.bin` as opaque and injects only its own markers, so it
   never learns that a wrapper is present (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "Where
   wrapping happens").
6. **A failure before the session is ready is a pre-accept decline, and the launch escalation is
   retired.** A failure before `ccpool new` returns ready is declined as `launch-failed`, kind `fault`,
   without marking the item struck, so core re-offers it. The `pool-launch-fail` stamp and its escalation
   of the bead to `human` are retired. A failure after the session is ready is an accepted failure,
   classified as before (phillipgreenii-nix-agent-support · bead `pg2-12gr4` · "Failure handling and
   observability").
7. **What 0101 decided is superseded as a whole.** Its decisions on the single `wrap` primitive, the two
   entry forms, the layer composition, the absolute-path rule, the wrapper contract WC-1 to WC-9 as a
   pg-router obligation, the `PG_WRAP_*` item context, the ready marker and outcome rule, `wrap-failed`,
   the handler-side role gate, the interim `claudeBaseline` fragment, the carrier before the split and the
   `dispatch_start` wrap fields no longer describe pg-router. The wrapper contract itself survives in
   pg-wrap's own behavior-docs set, where it names no caller. 0101's base-environment decision (its
   Decision item 1, the base list and the strict switches) survives in pg-router, minus the wrap
   expansion.
8. **ADR 0065 and ADR 0080 are amended in place.** ADR 0065's Addendum of 2026-10-20 no longer gives a
   role a wrap: the role keeps its handler and configuration, the "identical wraps" assertion on shared
   pools is withdrawn, `roles.<name>` has no `wrap` key, and the adapters' one validation entry point is
   `validate` rather than `validate-config`. ADR 0080's Decision item 3 is superseded for failures before
   the session is ready: a usage-limit rejection at `ccpool new` is a pre-accept decline like any other
   launch failure, and no `pool-launch-fail` stamp exists to withhold. ADR 0089 is unchanged.

## Consequences

- A wrapper is usable by anything that can run a command, and pg-wrap's behavior set names no caller.
  pg-router's behavior docs name no wrap tool, and the handler docs name pg-wrap only as an external
  tool.
- The failure classes shrink to two that need no marker: declined before ready (offered again, no strike,
  no escalation) and failed after ready (accepted, classified as before). A broken wrap shows up as a
  decline with a kind and detail, and the failure-rate alert and the progress gauges, not a role gate.
- A permanently broken wrapper no longer pauses one role. The brake is the decline's re-offer backoff and
  the pattern alerts on repeated declines; there is no per-role state file to clear.
- A handler that wants a wrapped session needs two pieces of configuration, `claude.bin` on the pool and
  `PG_WRAP_SPEC` in the role's `sessionEnv`, and pg-wrap's own `check` and `show` commands are the way to
  inspect a spec. The runbook for reading a stalled role names them.
- Documents that describe wrap as a pg-router concept (the pg-router and handler behavior sets, the
  consumer checklist, ADR 0085 and the split's ADR text) are corrected by their own packets; this ADR
  supersedes their reading of the wrap, not their text.
- The ready-marker mechanics and the role gate in ADR 0101 are kept there as history and are not to be
  implemented.
