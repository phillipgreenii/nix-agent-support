# pg-keep-awake — the keeper

The keeper is the single holder: the one long-running process that reads the leases, applies the battery
rule and holds the Mac awake. This doc owns what the keeper does on every pass, how it holds exactly one
assertion with the union of the leases' flags, how a restart or a crash costs nothing, why there is no
fallback when it is down, how it is configured and how it is observed. The lease it reads is in
[`lease-lifecycle.md`](lease-lifecycle.md), the rule it applies in [`battery-rule.md`](battery-rule.md)
and the commands that talk to it in [`cli-contract.md`](cli-contract.md). It traces to design sections 4,
8, 11 and 12, to decisions O1, O4 and O7, and to ADR 0100 decisions 1, 5, 6 and 7.

## Stories

- **`STORY-KEEP-ONE-HOLDER`** <!-- uuid: 85e1c9a3-0d6f-4b72-a8e4-c3b07f2d9a16 --> — As the operator, I want exactly one process holding the Mac awake, with the
  flags every live lease asks for, and a change in what is asked to cost no gap, so there is one place to
  look and one rule to trust. _(→ `JOURNEY-KEEP-RESTART-AND-APPLY`; `GOAL-ONE-HOLDER`, `INV-KEEP-1`,
  `INV-KEEP-2`, `INV-KEEP-3`, `INV-KEEP-4`.)_
- **`STORY-KEEP-NEVER-DROPS`** <!-- uuid: 4f0d7b28-e9a5-4c13-b1d6-7a2e84c0f539 --> — As the operator, I want applying a new version, or a crash of the keeper, to
  leave the Mac held awake and to lose no lease, and I want a keeper that is down to be noticed and not
  silently replaced. _(→ `JOURNEY-KEEP-RESTART-AND-APPLY`, `JOURNEY-KEEP-DOWN`; `GOAL-SURVIVE-RESTART`,
  `INV-KEEP-5`, `INV-KEEP-6`, `INV-KEEP-7`, `INV-KEEP-8`, `INV-KEEP-11`, `INV-KEEP-12`.)_
- **`STORY-KEEP-CONFIGURE`** <!-- uuid: bd6a3f94-217c-4e05-8f39-0c1d5a7e2b48 --> — As the configurer, I want the durations, the battery rule and the telemetry
  endpoint set in one managed file that a bad edit cannot turn into a crash loop, and I want the keeper
  to report what it did, so I can see it working. _(→ `JOURNEY-KEEP-RESTART-AND-APPLY`; `INV-KEEP-9`,
  `INV-KEEP-10`.)_

## Journeys

### `JOURNEY-KEEP-RESTART-AND-APPLY` — applying a change while the Mac is held awake <!-- uuid: 2e9c5d71-a0b3-4f86-9d12-e7f4a8c06b35 -->

**Primary actor:** `ACTOR-CONFIGURER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`,
`ACTOR-CLIENT`. _Requires:_ `INV-KEEP-1`, `INV-KEEP-2`, `INV-KEEP-3`, `INV-KEEP-4`, `INV-KEEP-5`,
`INV-KEEP-6`, `INV-KEEP-7`, `INV-KEEP-9`, `INV-KEEP-10`. _Includes:_ none.

1. Two clients hold leases asking for the flags `i` and `s`, and the keeper holds one assertion with both
   (`INV-KEEP-1`, `INV-KEEP-2`).
2. The configurer changes the release level in the configuration and applies it. The configuration is
   read only when the keeper starts, so applying it restarts the keeper (`INV-KEEP-9`).
3. The service manager stops the keeper. It removes dead leases, withdraws its heartbeat and leaves the
   assertion and every valid lease (`INV-KEEP-6`).
4. The new keeper starts, creates its heartbeat first and finds the assertion the old keeper started,
   whose parameters now differ. It starts a replacement first and stops the old one only then, so there
   is no gap (`INV-KEEP-4`, `INV-KEEP-5`, `INV-KEEP-7`).
5. The keeper reports each step to telemetry (`INV-KEEP-10`).
6. Later one client releases its lease. The flag it asked for leaves the union, and the assertion is
   replaced the same way (`INV-KEEP-4`).
7. The last client releases. The assertion stays for the linger window and then drops (`INV-KEEP-3`).

Extensions:

- 2a. The new configuration has a mistake. The keeper starts anyway on the last configuration that
  parsed, or on the defaults, and shows the error in the status view (`INV-KEEP-9`).
- 4a. The gate is `no_spawn` at the time. The replacement waits for the gate to return to `ok`
  (`INV-KEEP-4`).
- 4b. A second keeper is started by mistake. It exits at once (`INV-KEEP-1`).
- 4c. An assertion left by a keeper of a different state directory is present. It is not touched
  (`INV-KEEP-5`).
- 5a. The telemetry collector refuses connections. The keeper's pass is not delayed (`INV-KEEP-10`).

### `JOURNEY-KEEP-DOWN` — the keeper stops and does not come back <!-- uuid: 91a4e0c6-5b2d-4837-a6f9-d03c1e8b7f24 -->

**Primary actor:** `ACTOR-WATCHER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`, `ACTOR-CLIENT`,
`ACTOR-CALLER`. _Requires:_ `INV-KEEP-7`, `INV-KEEP-8`, `INV-KEEP-11`, `INV-KEEP-12`. _Includes:_ none.

1. The keeper is killed and the service manager does not bring it back. Clients and shims go on
   recording leases; nothing starts a `caffeinate` of its own (`INV-KEEP-8`).
2. The assertion already held stays until the anchor's watchdog timeout passes, then drops, because
   nothing proves the keeper alive (`INV-KEEP-7`, `INV-KEEP-12`).
3. The alert that watches the telemetry's last-known state fires for a keeper that was down while leases
   were live, and pa-monitor's independent report of the keeper's health shows it down (`INV-KEEP-11`).
4. The operator restarts the keeper. It reads the leases that are still valid and holds again
   (`INV-KEEP-5`).

Extensions:

- 3a. The assertion is still held on a battery at or below the release level. A third alert fires
  (`INV-KEEP-11`).

## Invariants

- **`INV-KEEP-1`** <!-- uuid: 07d3a5f8-c1e4-4b92-8a60-b5e9d2c7f130 --> — Exactly one keeper MUST run per state directory, and a second MUST exit at
  once. The keeper MUST be the only process that starts `caffeinate`. At steady state one `caffeinate`
  holds the union of the live leases' flags, `d`, `i`, `m` and `s`, including `s` because the Mac may be on
  its power source. _(Design sections 2, 4 and 8; R9; ADR 0100 decision 1.)_
- **`INV-KEEP-2`** <!-- uuid: e5b82c14-9f0a-4d37-a1c3-6d84f0a9b2e7 --> — The keeper MUST reconcile on every tick, by default every 10 seconds: sweep the
  leases, read the battery once, work out the desired state from the live leases, the pause, the gate
  and the linger window, observe the actual state, converge, and publish its status and heartbeat. A
  `caffeinate` that was killed MUST be started again on the next tick. _(Design section 8; O1.)_
- **`INV-KEEP-3`** <!-- uuid: 38c6f1a7-d4b9-4e50-93e2-a0c7b81d5f64 --> — When no lease is live the keeper MUST keep the assertion for the linger window,
  30 seconds by default and configurable, before dropping it, so a client killed and respawned does not
  churn it. A release by the battery rule and a pause MUST override the linger. _(Design section 8; O4.)_
- **`INV-KEEP-4`** <!-- uuid: a0f94d63-71c2-4b58-8e1d-c3b6a2e07f95 --> — When the flags the leases ask for, or a parameter of the assertion, change, the
  keeper MUST start the new assertion first and stop the old one after, so no gap occurs. Under the
  gate `no_spawn` it MUST NOT start a replacement, so the change takes effect only when the gate returns
  to `ok`. _(Design section 8.)_
- **`INV-KEEP-5`** <!-- uuid: 6b1e8d02-f5a3-4c79-b0d4-97a5c3e1f860 --> — The assertion MUST survive the keeper. On start the keeper MUST adopt the
  assertion an earlier keeper of the same state directory started, if it is the one expected (same
  processes, same parameters), comparing what it runs and not where its program lives; failing that it
  MUST look for such an assertion by its state directory, adopt the newest valid one and stop the others;
  and it MUST NOT touch one that belongs to a different state directory. If the adopted assertion
  differs from the desired one it MUST replace it without a gap. _(Design section 8 (adoption); R6;
  ADR 0100 decision 5.)_
- **`INV-KEEP-6`** <!-- uuid: d97a2b30-8e6c-4f15-a4b1-e02f6c8d1a73 --> — On a start and on a graceful stop the keeper MUST remove expired and dead leases
  and stale partial files and MUST leave valid leases, the held assertion and the anchor. On a graceful
  stop only, it MUST also withdraw its heartbeat, so the anchor's watchdog countdown starts at the
  shutdown, and a start MUST create the heartbeat before anything else and MUST NOT withdraw it.
  _(Design section 8 (sweeps); R8.)_
- **`INV-KEEP-7`** <!-- uuid: 1c58e4f9-a2d0-4b36-8d97-f3b0a7c5e214 --> — The keeper's heartbeat MUST be given only after a completed pass, by the loop
  itself and not by a separate timer, so a wedged loop cannot look alive. A pass whose sweep was skipped
  because the lease lock was busy still counts as completed. _(Design section 8.)_
- **`INV-KEEP-8`** <!-- uuid: f4a0d6b1-3e87-4c29-95a8-0b2e1d7c9f46 --> — When the keeper is down, nothing MUST start a `caffeinate` on a client's or a
  caller's behalf: there is no fallback to the real tool. The service manager restarts the keeper and an
  alert covers a keeper that stays down. The accepted consequence is that the Mac can sleep during a
  task while the keeper is down and no assertion is held. _(Design section 3 (R5); ADR 0100 decision 6
  and Consequences.)_
- **`INV-KEEP-9`** <!-- uuid: 5e93b7c2-0a1f-4d68-b4e5-a8d2c06f3b19 --> — The configuration MUST be one file at the user's configuration location,
  read only by the keeper and never by a client, the shim or the anchor, with the heartbeat interval
  (default 10 seconds), the watchdog timeout (default 10 minutes), the linger (default 30 seconds), the
  battery levels and the telemetry endpoint. An absent file means the defaults and no telemetry. The
  configuration MUST be read only at the keeper's start, and a change MUST take effect by restarting the
  keeper. On a file that does not parse the keeper MUST fall back to the last configuration that parsed,
  then to the defaults, log the error and show it as a configuration error in the status view, and MUST
  NOT crash-loop. The managed configuration MUST be rejected at build time if the watchdog timeout is less than
  six heartbeat intervals or the release level is above the hold level; a keeper handed such a release
  level anyway caps it (`INV-BATT-6`). _(Design section 11; R7, O1,
  O4, O7.)_
- **`INV-KEEP-10`** <!-- uuid: 82f0c5d9-b6a4-4e31-8c7f-1e3a9d4b6c05 --> — Only the keeper MUST emit telemetry, through the catalog under `INTF-TELEMETRY`,
  and export MUST be non-blocking: a collector that refuses MUST NOT stall a pass. The metric labelled by
  owner MUST stay low-cardinality (after 16 distinct owners the rest are `other`), and a lease id or a
  process id MUST NOT be a label. The keeper MUST log lease creation and reaping, the assertion's start,
  adoption and release, gate changes, configuration errors and the reason the anchor ended. _(Design
  section 12; R11.)_
- **`INV-KEEP-11`** <!-- uuid: ca4e1b73-d085-4f92-a6c3-5b7d0e8f2a61 --> — Three alerts MUST exist. The first fires when the keeper was down while leases
  were live, from the telemetry's last-known state because a dead keeper emits nothing. The second is
  independent of the keeper: pa-monitor reports the keeper's health from the health check on each of its
  ticks and so keeps reporting while the keeper is dead. The third fires when the assertion is held on a
  battery at or below the release level. _(Design section 12.)_
- **`INV-KEEP-12`** <!-- uuid: 3a7d90e5-c2b8-4f14-9e06-d1b5f83a7c42 --> — With the keeper agent disabled, the held assertion MUST drain by itself,
  once the watchdog timeout has passed without a heartbeat. _(Design sections 9 and 11.)_

## The interface catalogs

- **`INTF-CONFIG`** <!-- uuid: d3f8a165-0b9c-47e2-a5d4-8e1b7c3f6029 --> — the configuration the keeper reads. What crosses in: the file of
  `INV-KEEP-9`. What crosses out: a configuration error, shown in the status view and in telemetry, and the
  non-zero result of `config check`. What MUST hold: `INV-KEEP-9`, `INV-KCLI-14`, `INV-BATT-6`. The
  counterparty is `ACTOR-CONFIGURER`.
- **`INTF-TELEMETRY`** <!-- uuid: a6d20f93-8c1e-4f57-86b0-3d9a5e7142cb --> — what the keeper emits. The closed catalog of metrics is the one in
  the table below; a change to it MUST edit that table in the same change:

| Metric                                             | Kind      | Labels                                                                        |
| -------------------------------------------------- | --------- | ----------------------------------------------------------------------------- |
| `pg_keep_awake.keeper.up`                          | gauge     | none                                                                          |
| `pg_keep_awake.keeper.last_tick_timestamp_seconds` | gauge     | none                                                                          |
| `pg_keep_awake.tick.duration`                      | histogram | none                                                                          |
| `pg_keep_awake.leases.live`                        | gauge     | `owner`                                                                       |
| `pg_keep_awake.leases.reaped_total`                | counter   | `reason`: `expired`, `client_dead`, `watched_dead`, `orphan`                  |
| `pg_keep_awake.leases.rejected_total`              | counter   | `reason`: `unreadable`, `too_large`, `over_cap`                               |
| `pg_keep_awake.assertion.held`                     | gauge     | none                                                                          |
| `pg_keep_awake.assertion.flags`                    | gauge     | `flag`                                                                        |
| `pg_keep_awake.gate.state`                         | gauge     | `state`: `ok`, `no_spawn`, `released`, `unknown`, `disabled`                  |
| `pg_keep_awake.battery.percent`                    | gauge     | none                                                                          |
| `pg_keep_awake.battery.on_battery`                 | gauge     | none                                                                          |
| `pg_keep_awake.battery.release_pct`                | gauge     | none (the configured release level)                                           |
| `pg_keep_awake.battery.releases_total`             | counter   | `reason`: `release_threshold`, `blind_threshold`, `spawn_blocked`, `watchdog` |
| `pg_keep_awake.anchor.exits_total`                 | counter   | `reason`: `release_threshold`, `blind_threshold`, `watchdog`, `unknown`       |

What MUST hold: `INV-KEEP-10`, `INV-KEEP-11`, `INV-SHIM-12`. A `spawn_blocked` release is a metric reason
only, because nothing was released. The counterparty is `ACTOR-WATCHER`.
