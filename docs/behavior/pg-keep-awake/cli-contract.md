# pg-keep-awake — the command line

The command line is the only supported interface to `pg-keep-awake`. A client asks the Mac to stay
awake, withdraws the ask, holds it for the life of a command, reads what is going on, pauses the hold
and checks a configuration, and a watcher checks that the keeper is healthy. This doc owns what each
command does, what it prints, how it names things, when it fails and with which exit code. The lease
itself is in [`lease-lifecycle.md`](lease-lifecycle.md), the battery rule in
[`battery-rule.md`](battery-rule.md) and the single holder in [`keeper.md`](keeper.md). It traces to
design sections 2, 3, 6 and 13 and to ADR 0100 decision 2.

## Stories

- **`STORY-KCLI-ASK`** <!-- uuid: 1b7e4d92-5c08-4a36-9f81-d2a0c6e37b54 --> — As a client (a script, a session, pa-monitor), I want to ask the Mac to stay
  awake while my process lives, for a set time, or for the life of one command, and to withdraw the ask,
  so I never run `caffeinate` myself and every ask obeys the battery rule. _(→ `JOURNEY-KCLI-HOLD`;
  `GOAL-ONE-RULE`, `GOAL-CLI-ONLY`, `INV-KCLI-1`, `INV-KCLI-2`, `INV-KCLI-3`, `INV-KCLI-4`,
  `INV-KCLI-5`, `INV-KCLI-6`, `INV-KCLI-7`.)_
- **`STORY-KCLI-WHAT-HOLDS`** <!-- uuid: e48a07c3-91d6-4b2f-8e5a-6c3f1d9b0a72 --> — As the operator, I want one status view that lists every reason the Mac is
  being held, whether it is held now, the battery and the gate, why the hold last ended, and every other
  holder the host reports, so "what is holding my Mac awake?" has an answer. _(→ `JOURNEY-KCLI-DIAGNOSE`;
  `INV-KCLI-8`, `INV-KCLI-9`.)_
- **`STORY-KCLI-PAUSE`** <!-- uuid: 7a2c95e1-d3f4-4087-b6a9-0e8d4c1f5a36 --> — As the operator, I want to drop the hold for a while without losing any
  request, and have it return by itself, so I can let the Mac sleep once and not re-ask everyone.
  _(→ `JOURNEY-KCLI-DIAGNOSE`; `INV-KCLI-13`.)_
- **`STORY-KCLI-SCRIPT`** <!-- uuid: c05f8b27-4e91-4d6a-a3b7-92d1e7a64f08 --> — As a script or a watcher, I want machine output that is one document,
  exit codes that say what kind of failure happened, and a health check that tells a dead keeper from a
  healthy one, so I never parse prose. _(→ `JOURNEY-KCLI-HOLD`, `JOURNEY-KCLI-FAILURES`; `INV-KCLI-8`,
  `INV-KCLI-10`, `INV-KCLI-11`, `INV-KCLI-12`, `INV-KCLI-14`.)_
- **`STORY-KCLI-PA-MONITOR`** <!-- uuid: 39d6a1f0-8b52-4c7e-91d3-a5e08f2c7b64 --> — As pa-monitor, I want to turn the hold on and off by command, read whether
  the Mac is held and the gate, and have a wiped state heal on my next ask, so I need no battery rule
  and no `caffeinate` process of my own. _(→ `JOURNEY-KCLI-HOLD`; `INV-KCLI-1`, `INV-KCLI-3`,
  `INV-KCLI-15`.)_

## Journeys

### `JOURNEY-KCLI-HOLD` — a client holds the Mac awake <!-- uuid: 5e0b3c78-a1d9-4f24-8b67-c4f2d9a0e153 -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-KCLI-2`, `INV-KCLI-3`, `INV-KCLI-4`, `INV-KCLI-5`, `INV-KCLI-6`, `INV-KCLI-7`, `INV-KCLI-8`,
`INV-KCLI-15`. _Includes:_ `JOURNEY-LEASE-CLIENT-DIES`.

1. pa-monitor wants the Mac awake while its daemon runs. It acquires a lease as owner `pa-monitor`,
   bound to the daemon's own process. The command prints the lease id and exits 0 (`INV-KCLI-2`,
   `INV-KCLI-15`).
2. A minute later it repeats the same request. Nothing is duplicated: the lease is refreshed
   (`INV-KCLI-2`).
3. A wrapper script runs a long command under `run` as its own owner. The command's exit status is the
   wrapper's exit status, and the lease ends when the command does (`INV-KCLI-5`).
4. The operator reads the status as JSON; it is one document that lists both leases (`INV-KCLI-8`,
   `INV-KCLI-9`).
5. pa-monitor no longer wants the Mac awake and releases its lease. Releasing again changes nothing and
   still exits 0 (`INV-KCLI-4`).

Extensions:

- 1a. The process the client names is already gone. No lease is created, the command exits 0 and warns
  on standard error (`INV-KCLI-3`).
- 1b. The request names neither a process nor a time. The lease is bound to the caller's parent process,
  so a script that forgets to release still cannot hold the Mac forever (`INV-KCLI-2`).
- 1c. The keeper is down. The request still succeeds, because it only records the lease (`INV-KCLI-6`).
- 3a. The wrapper's command is not found or is killed by a signal. The wrapper sees exit status 127 or
  128 plus the signal number (`INV-KCLI-5`).
- 3b. A name with a space or a dot in it is refused as a usage error (`INV-KCLI-7`).

### `JOURNEY-KCLI-DIAGNOSE` — what is holding my Mac awake <!-- uuid: b3f7160d-2e84-4a95-9c0b-17d5e8a4f6c2 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`. _Requires:_ `INV-KCLI-8`,
`INV-KCLI-9`, `INV-KCLI-13`. _Includes:_ none.

1. The Mac is awake and the operator asks. The status view names the keeper and how long ago it proved it
   was alive, the held assertion with its flags, the battery and power source, each lease with its owner,
   key, process, expiry and state, and each external holder with its process and age (`INV-KCLI-9`).
2. The operator wants to sleep the Mac now. They pause for an hour. The hold drops; every lease stays;
   the status view shows the pause (`INV-KCLI-13`).
3. The hour passes. The hold returns on its own because leases are still live (`INV-KCLI-13`).

Extensions:

- 1a. Nothing is holding anything. The view says so in one line (`INV-KCLI-9`).
- 1b. The keeper has never started. The view says so and exits 0 (`INV-KCLI-9`).
- 2a. The operator resumes before the hour ends. The hold returns at once (`INV-KCLI-13`).

### `JOURNEY-KCLI-FAILURES` — when it does not work <!-- uuid: 94c8e2b5-0f17-4d3a-b6e9-3a1c75d08f24 -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-WATCHER`, `ACTOR-CONFIGURER`.
_Requires:_ `INV-KCLI-10`, `INV-KCLI-11`, `INV-KCLI-12`, `INV-KCLI-14`. _Includes:_ none.

1. A client runs a command with a mistake in it. It exits 2 with a usage message on standard error and
   writes nothing (`INV-KCLI-11`).
2. The state directory belongs to someone else, or cannot be written. The command exits 3 and says why;
   it does not change the directory's permissions (`INV-KCLI-12`).
3. A watcher runs the health check. The keeper has not proven it is alive for twice its interval, so
   the check exits 5 (`INV-KCLI-10`).
4. Later the keeper is up but leases are live and no assertion is held, with nothing to explain it. The
   check exits 6 (`INV-KCLI-10`).
5. The configurer checks a configuration file before shipping it. A bad value exits 4 and names the
   problem; nothing is changed (`INV-KCLI-14`).

Extensions:

- 3a. The state directory does not exist. The status view says the keeper never started and exits 0;
  the health check exits 5 (`INV-KCLI-10`).
- 4a. A pause, the gate, or the linger window explains the missing assertion. The check does not report
  degraded (`INV-KCLI-10`).

## Invariants

- **`INV-KCLI-1`** <!-- uuid: 6d29f3a8-b1c4-4e07-8a53-e90b7d2c1f46 --> — The command line MUST be the only supported interface: a client MUST
  NOT read or write the product's stored state, and the stored state MUST NOT be a contract. pa-monitor
  enables and disables itself through the command line and not through a shared library. _(Design
  sections 2, 3 (R10) and 4; ADR 0100 decision 2.)_
- **`INV-KCLI-2`** <!-- uuid: f1a5084e-73d9-4b62-9c1d-4e6a0b8d5237 --> — `acquire` MUST create or refresh a lease for an owner and a key (the key
  is `default` when not given). The lease MAY be bound to a process (`--pid`), to a time (`--for`, a
  duration) or to both; with neither, it MUST be bound to the caller's parent process; with only a
  time, it is a pure timer with no process. The flags `d`, `i`, `m` and `s` MAY be requested and `i` is
  the default. The command MUST print the lease id and MUST be idempotent: repeating it changes nothing
  but the refresh. A lease MUST be bound to at least one of a process, an expiry or a watched process,
  and a request that would produce none MUST be refused. _(Design sections 5 and 6.)_
- **`INV-KCLI-3`** <!-- uuid: 0c83d7b6-e5a2-4f19-b4d0-71a9c3e2f850 --> — When the process a request names is already gone, `acquire` MUST create
  no lease, exit 0 and warn on standard error. _(Design section 6.)_
- **`INV-KCLI-4`** <!-- uuid: a97e1c40-26b3-4d58-8f02-d5c6b1e9a374 --> — `release` MUST remove one lease of an owner (key `default` when not given)
  or, with `--all`, every lease of the owner, and MUST exit 0 whether or not a lease existed. _(Design
  section 6; O5.)_
- **`INV-KCLI-5`** <!-- uuid: 48d0b7f3-9a15-4c6e-a2e8-3b7f6c0d1e95 --> — `run` MUST hold a lease for exactly the life of one command, name it by
  default uniquely to that run, pass the command's exit status through, give 128 plus the signal number
  for a command killed by a signal and 127 for a command not found, and forward interrupt, terminate and
  hang-up signals to the command. The lease MUST end when the command ends, however it ends. _(Design
  section 6.)_
- **`INV-KCLI-6`** <!-- uuid: d2e69a15-c8f0-47b3-8d41-0a5e7c93b6f8 --> — The state-changing commands (`acquire`, `release`, `run`, `pause`,
  `resume`) MUST succeed while the keeper is down; they only record. _(Design section 6; R4, R5.)_
- **`INV-KCLI-7`** <!-- uuid: 8f3b5c0a-17e6-4d92-b1a7-c64d0e8f2a53 --> — An owner and a key MUST each be 1 to 64 letters, digits, underscores or
  hyphens, so a lease's id (owner and key joined by a dot) is unambiguous. Anything else MUST be refused
  as a usage error. _(Design section 5.)_
- **`INV-KCLI-8`** <!-- uuid: 3e7a92d4-0b6f-4c18-9d35-f8a1b4c07e62 --> — Standard output MUST carry data and standard error MUST carry
  diagnostics. With `--json` a command MUST print exactly one JSON object. `status --json` MUST give the
  held state, the gate, the keeper's health, the battery, every lease and the last release, so a client
  such as pa-monitor needs nothing else. _(Design sections 6 and 13.)_
- **`INV-KCLI-9`** <!-- uuid: b6014f8d-d29a-4e73-a5c8-7c3e9f1b0d24 --> — `status` MUST show every live lease (owner, key, process, expiry and
  state), the keeper's health and how long since its last heartbeat, the assertion with its flags, the
  battery with its percentage and power source, the gate, why the hold last ended, and every external
  holder with its process, kind of assertion and age, marking a holder the keeper manages (`INV-BATT-13`)
  with the program that manages it. It MUST read what the keeper wrote plus a live
  process check, and so MUST work with the keeper down. A lease of a newer version than the reader understands MUST be shown
  as unreadable and MUST NOT be deleted. An empty system MUST print one line saying there are no leases,
  no assertion and the keeper's state, and a state directory that does not exist MUST print that the
  keeper never started and exit 0. _(Design sections 5, 6 and 12; O6.)_
- **`INV-KCLI-10`** <!-- uuid: 5a91e6c2-4f08-4b3d-86a7-12d0c8b5e9f7 --> — `status --check` MUST exit 0 when the keeper is healthy, 5 when the
  keeper is down (no heartbeat for twice its interval, or never started) and 6 when it is degraded:
  leases are live and no assertion is held, and none of the gate, a pause or the linger window explains
  it, for more than one tick. It MUST NOT report degraded for a state those three explain. _(Design
  section 6; R10.)_
- **`INV-KCLI-11`** <!-- uuid: c47d3b80-e1a6-4952-9f0c-8d6a2e5b7134 --> — Exit codes MUST follow the repository's exit-code convention: 0 success, 1
  an unexpected error, 2 a usage error, 3 the state directory is unwritable, 4 the configuration is
  invalid (`config check` only), 5 the keeper is down (`status --check` only) and 6 degraded
  (`status --check` only). This catalog is the interface's and no command MAY use a code for another
  meaning. _(Design section 6; ADR 0042.)_
- **`INV-KCLI-12`** <!-- uuid: 1f8c6a49-b3d7-4e05-a92e-6b0d4c7e8a31 --> — The state directory MUST be private to the current user. A writer MUST
  create it private and MUST, on finding one that is owned by someone else or open to the group or
  others, exit 3 and say why, and MUST NOT change that directory's permissions. A command that cannot
  take the state's lock within a short bound MUST treat the state as unwritable and exit 3. _(Design
  sections 5 and 5.4.)_
- **`INV-KCLI-13`** <!-- uuid: e0b29d57-6c14-4a8f-b3e1-95a7d28c0f6b --> — `pause` MUST make the keeper drop the assertion while leaving every lease
  untouched, with an optional `--for` duration after which the pause ends by itself; a pause with no
  duration lasts until `resume`. `resume` MUST end a pause at once. While paused, `status` MUST show the
  pause. _(Design sections 5.3 and 6; O5.)_
- **`INV-KCLI-14`** <!-- uuid: 72a4f5c1-d80b-4396-8e9a-b1c3e60d7f48 --> — `config check` MUST validate a configuration file (the default
  location, or one named by `--config`), exit 4 and name each problem when it is invalid, and change
  nothing. The two long-running modes, `keeper` and `anchor`, are not part of the client contract. _(Design
  sections 6 and 11.)_
- **`INV-KCLI-15`** <!-- uuid: 0d65b8e3-a2f9-4c71-95b4-e8c0a37d1f26 --> — pa-monitor MUST enable and disable its hold through `acquire` and
  `release` bound to its own daemon, SHOULD repeat `acquire` about once a minute while it wants the Mac
  awake so a wiped state heals, and MUST read the held state and gate from `status --json`. The degraded
  result of the health check is what pa-monitor's own error states correspond to. _(Design section 13.)_

## The interface catalogs

- **`INTF-CLI`** <!-- uuid: 8a3d6f21-b4c7-4e95-91a8-6f2e0d5c7b43 --> — the command line a client uses. What crosses it in: `acquire`,
  `release`, `run`, `status`, `pause`, `resume` and `config check`, with an owner, a key, a process, a
  duration and flags. What crosses out: the lease id, one JSON document with `--json`, the status text, and
  an exit code from the catalog in `INV-KCLI-11`. What MUST hold: `INV-KCLI-1` to `INV-KCLI-15`. The
  counterparty is `ACTOR-CLIENT`.
- **`INTF-HEALTH`** <!-- uuid: 5e7b4c08-91f6-4a2d-b3e5-1c8a6f0d9e72 --> — the status view and the health check, as a watcher reads them. What
  crosses out: the keeper's up or down, its heartbeat age, the gate (one of `ok`, `no_spawn`, `released`,
  `unknown`, `disabled`), whether an assertion is held, the battery, a configuration error if the keeper
  fell back, and why the hold last ended (one of `release_threshold`, `blind_threshold`, `watchdog`,
  `no_leases`, `paused`, `keeper_stop`, `unknown`). What MUST hold: `INV-KCLI-9`, `INV-KCLI-10`,
  `INV-KEEP-9`, `INV-BATT-11`. The counterparty is `ACTOR-WATCHER`.
