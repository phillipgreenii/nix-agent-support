# pg-keep-awake — the battery rule

The battery rule is the reason the product exists. On 2026-10-09 and 2026-10-10 the Mac lost its power
source with the lid open, programs that no battery rule governed kept it awake, the charge fell to 1% to
3%, the CPU throttled and the host stalled for most of an hour until it hibernated. This doc owns the
rule that now governs every keep-awake request: the levels, the gate, the way a battery that cannot be
read is treated, the autonomous anchor that enforces the rule even when the keeper is gone, and the
guards that stop a low battery from being fought. The single holder that applies it is in
[`keeper.md`](keeper.md); the requests it governs are in [`cli-contract.md`](cli-contract.md) and
[`caffeinate-shim.md`](caffeinate-shim.md). It traces to design sections 8, 9 and 10 and to ADR 0100
decisions 4 and 5, and takes over, for the keep-awake path, the rule of ADR 0095.

## Stories

- **`STORY-BATT-NEVER-DRAIN`** <!-- uuid: 9d3f7a62-1b0e-4c85-a4d9-e2c6f8b05a17 --> — As the operator, I want every request to keep the Mac awake to stop
  holding it awake before the battery is spent, whoever made the request, so a stalled agent never leaves
  the host throttled until it hibernates. _(→ `JOURNEY-BATT-DISCHARGE`; `GOAL-ONE-RULE`, `INV-BATT-1`,
  `INV-BATT-2`, `INV-BATT-3`, `INV-BATT-6`, `INV-BATT-11`.)_
- **`STORY-BATT-WEDGED-KEEPER`** <!-- uuid: 0b58e4c3-a7d1-4f92-8e36-71d5c9a0b2f8 --> — As the operator, I want the rule to hold when the keeper is hung or gone and
  when the battery cannot be read, so the one failure I cannot see does not leave the Mac awake on an empty
  battery. _(→ `JOURNEY-BATT-DISCHARGE`; `INV-BATT-7`, `INV-BATT-8`, `INV-BATT-9`.)_
- **`STORY-BATT-NO-FORCED-SLEEP`** <!-- uuid: e2a19d70-5f84-4b3c-96a1-d08b7e3c4f65 --> — As the operator, I do not want the product to force the Mac to sleep or to
  shut down, only to stop holding it awake, so the Mac sleeps when it would have anyway and anything I
  chose to keep it awake in another way is respected. _(→ `JOURNEY-BATT-DISCHARGE`; `INV-BATT-4`,
  `INV-BATT-5`, `INV-BATT-10`, `INV-BATT-12`.)_
- **`STORY-BATT-AGENT-HOLDERS`** <!-- uuid: 3c8e51a7-90d2-4f64-b1a8-d7e0425f6b93 --> — As the operator, I want the keep-awake that Claude Code starts for itself, which no
  `caffeinate` command on my path can intercept, to stop at the release level as well, so the many
  agent sessions I run cannot hold the Mac awake on an empty battery. _(→ `JOURNEY-BATT-DISCHARGE`;
  `INV-BATT-13`, `INV-BATT-10`.)_

## Journeys

### `JOURNEY-BATT-DISCHARGE` — the power source is lost and the battery falls <!-- uuid: 6a07c1e9-d3b4-4f58-b2a6-0e94c8d7f1a3 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`. _Requires:_
`INV-BATT-1`, `INV-BATT-2`, `INV-BATT-3`, `INV-BATT-4`, `INV-BATT-5`, `INV-BATT-6`, `INV-BATT-7`,
`INV-BATT-8`, `INV-BATT-9`, `INV-BATT-10`, `INV-BATT-11`, `INV-BATT-12`, `INV-BATT-13`. _Includes:_ none.

1. Agents are working, so leases are live and the Mac is held awake on its power source (`INV-BATT-2`).
2. The power source is lost with the battery at 64%. The hold continues: the battery is above the hold
   level (`INV-BATT-2`).
3. The battery falls to 30%. The gate becomes `no_spawn`: the held assertion is kept, but no new or
   replacement one is started, so a change of flags waits (`INV-BATT-3`).
4. The battery falls to 20%. The gate becomes `released`: the assertion is dropped, every lease stays,
   and the reason is recorded as `release_threshold` (`INV-BATT-3`, `INV-BATT-4`, `INV-BATT-11`).
5. The Mac, no longer held, sleeps within about a minute of the last activity. Nothing forced it to
   (`INV-BATT-4`).
6. The operator plugs in the charger. The gate returns to `ok`, and because leases are still live the
   Mac is held again (`INV-BATT-5`).

Extensions:

- 3a. The battery climbs back to 33% on the battery. It is above the hold level but inside the hysteresis
  margin, so the gate stays `no_spawn` until the battery passes 35% or the power source returns
  (`INV-BATT-3`).
- 4a. The keeper is wedged and never drops the assertion. The anchor, which the assertion follows, ends
  at the release level on its own and the assertion ends with it (`INV-BATT-7`).
- 4b. The host is so starved that the battery cannot be read. One failed read fails open; after
  three failed reads in a row, if the last good reading was on the battery at or below the hold level,
  the assertion is released with the reason `blind_threshold` (`INV-BATT-8`).
- 4c. The keeper is gone for good. After the watchdog timeout without a heartbeat, the anchor ends the
  assertion with the reason `watchdog` (`INV-BATT-9`).
- 4c2. Agent sessions are running and each holds the Mac awake through the keep-awake Claude Code starts for
  itself. At the release level the keeper stops each of those too and the status view no longer lists
  them; a session that starts another while the gate is still `released` has it stopped on a later pass
  (`INV-BATT-13`). Any other `caffeinate` on the Mac keeps running (`INV-BATT-10`).
- 4d. A client asks again right after the release. The keeper does not start an assertion on a battery
  already at or below the release level, and does not respawn one that ended within moments of starting
  (`INV-BATT-12`).
- 6a. The rule is disabled by configuration (hold is 0). The gate is `disabled` and behaves as `ok`
  (`INV-BATT-6`).
- 6b. The battery cannot be read at all and the last good reading was above the hold level. The gate is
  `unknown`, which behaves as `ok` (`INV-BATT-8`).
- 6c. The operator paused. The pause is reported as the reason the assertion is not held (`INV-BATT-10`).

## Invariants

- **`INV-BATT-1`** <!-- uuid: 14e6b0d8-9a3c-4f72-85d1-a7c2e9f03b46 --> — Every keep-awake request that reaches the Mac through the command line or
  through the shim MUST pass the same battery rule, applied only by the keeper and the anchor; no client,
  caller or shim MUST apply or bypass it. _(Design section 2; ADR 0100 decision 4.)_
- **`INV-BATT-2`** <!-- uuid: c93a5f17-e08d-4b24-a6c9-1d4f7b82e0a5 --> — The rule's defaults MUST be a hold level of 30, a release level of 20 and a
  hysteresis of 5. On the power source the gate MUST be `ok`; on the battery above the hold level, and not
  inside the hysteresis latch, the gate MUST be `ok`. _(Design section 10; ADR 0095.)_
- **`INV-BATT-3`** <!-- uuid: 5f2d8a41-b6c9-4e03-9d77-38a0e1c5f2b9 --> — On the battery at or below the hold level, or above it but still inside the
  hysteresis latch and above the release level, the gate MUST be `no_spawn`: a running assertion is kept
  and none is started or replaced. The latch ends when the power source returns or the battery passes the
  hold level plus the hysteresis. On the battery at or below the release level the gate MUST be
  `released`. The latch is not kept across a keeper restart, which is accepted. _(Design sections 8 and
  10.)_
- **`INV-BATT-4`** <!-- uuid: a80c3e96-4d17-4b5a-8f21-e6d9b0c7a354 --> — Reaching the release level MUST drop the assertion and MUST NOT remove any
  lease, and the product MUST NOT force the Mac to sleep or to shut down. _(Design section 3 (R1);
  ADR 0100 decision 4.)_
- **`INV-BATT-5`** <!-- uuid: 7d19f2a5-c0e8-4a63-b394-5b1e8d6f07c2 --> — The hold MUST return when the gate returns to `ok` and a lease is live; a
  release by the battery MUST NOT outlast the condition that caused it. _(Design section 8 (desired
  state).)_
- **`INV-BATT-6`** <!-- uuid: 2e65b8d0-f4a1-4c97-8d3e-a9c05f1b7e48 --> — The rule MUST be configurable and MUST be off when the hold level is 0, in
  which case the gate is `disabled` and behaves as `ok`. A release level of 0 MUST mean a running
  assertion is never stopped for the battery. A release level above the hold level MUST be capped at the
  hold level. _(Design section 10.)_
- **`INV-BATT-7`** <!-- uuid: d48e0a3c-71b5-4f29-96d4-0c8a3e5b9f17 --> — The held assertion MUST end at the release level even when the keeper is hung
  or gone: the small anchor it follows MUST read the battery on its own, take its levels from its start-up
  parameters, MUST NOT depend on the configuration file or on a connection to the keeper, and MUST end
  when the battery is on and at or below the release level, recording why. _(Design section 9; ADR 0100
  decision 4.)_
- **`INV-BATT-8`** <!-- uuid: 83c7e1f4-a2d6-4b08-b5e9-4f1d0a6c3e72 --> — A single failed battery read MUST fail open: the gate is `unknown`, which behaves as `ok`. After
  three failed reads in a row, if the last good reading was on the battery at or below the hold level, the
  keeper MUST treat the gate as `released` and the anchor MUST end with the reason `blind_threshold`. A
  last good reading above the hold level, or on the power source, keeps failing open. The hold level, not
  the release level, is the line, because a rule keyed on the release level could never fire once
  readings stop. _(Design section 10 (reading) and 9.)_
- **`INV-BATT-9`** <!-- uuid: f6b2d570-3c8e-4a14-9a07-e5d1c8b4a392 --> — The anchor MUST NOT end merely because the keeper is gone. It MAY end when it
  sees no heartbeat from the keeper for the watchdog timeout (10 minutes by default), measured in time the
  Mac is awake and unaffected by a step of the wall clock, and MUST record the reason `watchdog`. A
  keeper that is restarted within the timeout MUST find the assertion still held. _(Design sections 3
  (R6), 9 and 17 (O1).)_
- **`INV-BATT-10`** <!-- uuid: 0a47c9e2-b81d-4f35-a6c8-d3e7f2051b96 --> — Only the battery rule, the watchdog, an empty set of live leases, a pause
  and the keeper's own reconciliation MAY end the hold. The product MUST NOT stop any assertion that it did not start, and
  MUST NOT stop another `caffeinate` holder or a non-`caffeinate` holder, though the status view lists
  them, with one exception: the keep-awake that Claude Code starts for itself, as `INV-BATT-13` defines
  it. _(Design sections 2 (non-goals) and 12; ADR 0100 decision 8.)_
- **`INV-BATT-11`** <!-- uuid: 59d3a8b1-e670-4c42-8f9d-b2a0c64e1d37 --> — Why the hold last ended MUST be recorded and shown. The reason is one of
  `release_threshold`, `blind_threshold`, `watchdog`, `no_leases`, `paused`, `keeper_stop` or `unknown`
  (an anchor that was killed without a record). An anchor MUST record its reason before ending, and the
  keeper MUST report it on its next pass and then clear it. _(Design sections 5.3 and 5.3a.)_
- **`INV-BATT-12`** <!-- uuid: bc10f6e5-4a93-4d78-81c2-7e9a5d3b0f64 --> — The keeper MUST NOT start an assertion when its last reading is on the
  battery at or below the release level. If an anchor ends within moments of being started, the keeper
  MUST NOT start another for a longer interval, so a failing start is not retried in a tight loop. _(Design section 8 (anchor churn guard).)_

- **`INV-BATT-13`** <!-- uuid: 8f2b64d9-1ea7-4c05-a93b-5d70c8e1f4a2 --> — The keep-awake that Claude Code starts for itself is a managed holder: an external
  `caffeinate` that runs the system's own `caffeinate`, whose command line is exactly `caffeinate -i -t`
  and a number of seconds, and whose parent is a running `claude` process. It is identified from the live
  process table of the product's own user at the moment of use, and anything that fails any part of that
  test MUST be left alone. The status view MUST mark a managed holder as managed by `claude`. While the gate is
  `released`, including by the blind rule, the keeper MUST stop each managed holder, and MUST only signal
  a process whose start time still matches the one it identified. In every other gate it MUST leave them
  running. A managed holder started while the gate is `released` MUST be stopped on a later pass. The product
  MUST NOT stop the `claude` process itself. _(Design section 8 (Claude Code's own inhibitor); ADR 0100
  decision 8; operator ruling 2026-10-22, bead pg2-0n6og.)_

## The interface catalog

- **`INTF-HOST`** <!-- uuid: 17c94e8a-6d05-4b31-9a7f-e2b5d08c4163 --> — what the product reads from and asks of the host. What crosses in: the
  battery's percentage and power source (a read that can fail), the state of a process (running, a zombie,
  gone), its start time, its parent, its executable and its command line, and the assertions the host reports for other holders. What crosses out: the one
  held assertion, a stop of each managed holder, and the status view's list of other holders. What MUST hold: `INV-BATT-7`, `INV-BATT-8`,
  `INV-BATT-10`, `INV-BATT-13`, `INV-LEASE-3`. The counterparty is `ACTOR-HOST`.
