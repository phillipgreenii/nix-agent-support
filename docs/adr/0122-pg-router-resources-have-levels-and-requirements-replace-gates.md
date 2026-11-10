# pg-router resources have levels, and requirements replace gates

**Status**: Accepted (operator ruled 2026-11-10; supersedes the 2026-11-04 resource-pressure spec)
**Date**: 2026-11-10
**Deciders**: Phillip Green II

## Context

pg-router held back work with **gates**: a TYPE-keyed on/off record, set by an operator or by an
external watchdog, that a registration either blocked on or listed as exempt. Three problems showed up
in use:

- A gate has no scale. "Disk is tight" and "disk is exhausted" had to be two gates sequenced by a
  watchdog, and CPU and RAM pressure shared the disk gate because there was nowhere else to put them.
- A gate has no staleness. A watchdog that died left its gate set, and work stayed held until a
  person cleared it.
- A registration named the conditions it did NOT care about (an exemption list), which grows with every
  new gate, and an unlisted gate silently stopped it.

On 2026-11-03 the kernel watchdog panicked while root-volume free space fell from 44.8 GB to 2.56 GB in
86 minutes and the load average reached 7 to 26 per core. The disk watchdog that followed
(a package, a role and a timer) set one pressure gate and had to be tuned by hand. The operator ruled,
between 2026-11-09 and 2026-11-10, that gates become levels of resources, enforced from the first
deploy and tuned afterwards.

## Decision

1. **A resource has an ordered scale and a level** (`router`, `disk`, `cpu`, `ram`, a network, a tracker
   server, a credential, the Claude pace). The graded scale is `PLENTY > OK > TIGHT > EXHAUSTED`;
   two-level scales are `RUNNING > PAUSED`, `UP > DOWN` and `AVAILABLE > UNAVAILABLE`.
2. **A registration declares what it requires** (`requires = [ "<resource> <op> <LEVEL>", ... ]`, AND
   only). A resource it does not name is one it does not care about (`ignored`). A clause is added only
   when its absence causes a problem and its facts are known.
3. **Levels are their own durable log records**, not queue events, appended without passing admission.
   A **sensor** (one generic binary, five subcommands) reports a level on a pull query or a push, owns
   its thresholds, hysteresis and dwell, and keeps working while the router is paused. The operator can
   override a level for at most 24 hours, or until cleared.
4. **A stale level is held for a grace period, then reads `unknown`**: best for every resource except
   credentials, which read the worst, so a dead sensor never starts workers without credentials.
5. **Levels govern admission only** and never stop, signal or deprioritise running work.
6. **The gate mechanism is removed in the same landing** (the Gate Registry, gate TYPEs and verbs,
   the per-registration exemption list, the gate metrics and the disk watchdog), with no alias and no period in which
   both exist. Every alert, rule test, dashboard and runbook that named a gate is converted, and a
   check searches every workspace repository for the retired names.
7. **Enforcement starts at the first deploy**; there is no observe mode. A read-only report of what the
   current readings would pause (`doctor --clauses`) is a command, not a mode. Thresholds are
   configurable and are tuned from the metrics.

The rules are `INV-LIFE-2` (amended), `INV-LVL-1` to `INV-LVL-6`, `INV-SEN-1` to `INV-SEN-7`,
`INV-ADM-1` to `INV-ADM-8`, `INV-REQ-1`, `INV-STALE-1`, `INV-GATE-1` and `INV-ALR-1` in
`packages/pg-router/docs/behavior/invariants.md`; the realization is `DEC-LEVEL-1` to `DEC-LEVEL-6` in
`packages/pg-router/docs/decisions/level.md`. The metric catalog of `INV-OBS-1` is amended in the same
change. The durable home of the design is this record and those invariants.

## Consequences

- One mechanism replaces gates, the pause gate and the disk watchdog; a new resource is a sensor and a
  scale, and a new registration names only what it needs.
- The levels and the readings behind them are metrics, so the thresholds can be tuned over time and a
  sensor that goes silent is itself alertable.
- The change is breaking for every consumer of a gate name, so it lands as one coordinated set across
  the workspace repositories. An ACTIVE legacy gate in the log at the first start is deleted and logged
  (`DEC-LEVEL-6`); the operator lists and records any gate before the apply.
- A level can be minutes stale at best; the router pulls before its first dispatch and after a wake
  from sleep, and the grace period bounds how long a silent sensor is believed.
- `level history` and `explain --at` are deferred; history comes from the metrics.
- The thresholds in `DEC-LEVEL-2` are strawmen. They need a follow-up after the first deploy to confirm
  that the chosen numbers work and that roles are not starving while they wait on a requirement.

## Alternatives

- **Keep gates and add scales to them.** Rejected: the registry, the exemption list and the
  external-setter model are the problem, and two mechanisms would coexist for a period.
- **An observe-only first deploy.** Rejected by the operator: enforce from the first deploy and tune,
  with a read-only report available as a command.
- **Per-sensor daemons with the router holding thresholds.** Rejected: the router holds no thresholds,
  and a one-shot sensor with a state file is enough and cheaper to run and test.
- **Translate legacy gates at replay.** Rejected: gate TYPEs have no resource to map to, and a
  compatibility path is what the one-landing rule forbids.
