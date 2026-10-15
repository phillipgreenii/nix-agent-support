# pg-keep-awake is the single holder of the keep-awake assertion

**Status**: Proposed (extends ADR 0095 and ADR 0024; decisions O1 to O9 in the spec were confirmed as proposed on 2026-10-15)
**Date**: 2026-10-14
**Deciders**: Phillip Green II

## Context

ADR 0095 made pa-monitor release its own `caffeinate` on a low battery. It could not cover the other
holders: Claude Code's built-in keep-awake, interactive `caffeinate` commands and wrapper scripts all
hold idle-sleep assertions that no battery rule governs. On 2026-10-09 and 2026-10-10 the Mac drained to
1% to 3% under those holders and stalled for 42 to 46 minutes of awake time each time. Claude Code
spawns `caffeinate` by bare name and offers no switch to turn it off, so the only place a single rule
can apply is the `caffeinate` command itself.

The full design, evidence, schemas, test plan and the reviewers' findings are in
`docs/superpowers/specs/2026-10-14-pg-keep-awake-design.md`.

## Decision

1. **One keeper holds the assertion.** `pg-keep-awake keeper`, a launchd keep-alive agent, is the only
   process that starts `caffeinate`. At steady state one `caffeinate` holds the union of the live
   leases' flags and follows a small `anchor` process with `-w`.
2. **Requests are leases, and the CLI is the only contract.** A client records a lease through
   `pg-keep-awake acquire`, `release` or `run`, or through the `caffeinate`-compatible shim. The lease
   files are private. `pg-keep-awake status --check` reports health. pa-monitor enables and disables
   itself through the CLI, not through a shared library.
3. **A PATH shim covers callers that spawn `caffeinate` by name.** `pg-caffeinate-shim`, installed as
   `caffeinate` by the home-manager module, translates `-t`, `-w` and the utility form into a lease and
   stays alive for the lease's lifetime, so callers that background it and kill its pid need no change.
4. **The battery rule lives only in the keeper and the anchor.** The keeper applies hold, release and
   hysteresis (ADR 0095's values). The anchor is autonomous and exits at the release level, so a wedged
   keeper cannot hold the Mac awake on a low battery. Reaching the release level drops the assertion; it
   never forces sleep or shutdown.
5. **The assertion survives a keeper restart.** The anchor is detached and does not exit when the keeper
   goes away. It exits only at the release level, at the hold level when the battery cannot be read for
   three consecutive reads (the blind rule), or after the watchdog timeout without a heartbeat. A new
   keeper adopts the survivors and reconciles.
6. **No fallback to a bare `caffeinate`.** If the keeper is down nothing holds the Mac awake; launchd
   restarts the keeper and an alert covers a keeper that stays down.
7. **Observability follows the existing OTel pattern.** Only the keeper emits. `status` lists the live
   leases and the external assertion holders, so "what is holding my Mac awake" has an answer.

## Consequences

- Every keep-awake request that goes through `PATH` or the CLI obeys one battery rule. A holder that
  calls `/usr/bin/caffeinate` by absolute path, or any non-`caffeinate` assertion, is not covered;
  `status` lists such holders.
- The shim is a new executable on `PATH` named `caffeinate`. It MUST track the real tool's argument and
  exit-status behavior, which the design pins with parity tests.
- With the keeper down and no held assertion, the Mac can sleep mid-task. The accepted remedy is the
  keeper's restart plus an alert, not a fallback.
- pa-monitor's battery policy and `caffeinate` process management move out of pa-monitor, and its
  battery home-manager options are deprecated. The existing `pa_monitor.caffeinate.*` series keep their
  meaning through `status --json`.
- A behavior-docs set for the CLI and the state invariants is a follow-up.
