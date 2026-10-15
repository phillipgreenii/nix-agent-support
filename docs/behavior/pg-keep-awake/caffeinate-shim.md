# pg-keep-awake — the `caffeinate` shim

Some callers spawn `caffeinate` by name and cannot be changed: the Claude Code CLI re-arms
`caffeinate -i -t 300` while a turn is busy and offers no switch to turn that off, and shells and wrapper
scripts do the same. The shim is a command that looks like `caffeinate` to those callers and turns each
call into a lease, so the call obeys the battery rule without the caller knowing. This doc owns how
faithfully the shim mirrors `caffeinate` (flags, times, watched processes, the utility form, signals and
exit statuses), what its lifetime is, what it does when it cannot record a lease and what it does not
cover. The lease is in [`lease-lifecycle.md`](lease-lifecycle.md) and the rule that then governs it in
[`battery-rule.md`](battery-rule.md). It traces to design section 7, to decisions O2 and O3 and to
ADR 0100 decision 3.

## Stories

- **`STORY-SHIM-NO-CHANGE`** <!-- uuid: 3b9e5d27-c8a0-4f14-b6e3-d17a0c94f582 --> — As a caller that spawns `caffeinate` by name, I want the same flags, the same
  messages and the same exit statuses as the real tool, so I keep working unchanged while my request now
  obeys the battery rule. _(→ `JOURNEY-SHIM-CLAUDE-CODE`; `GOAL-ONE-RULE`, `INV-SHIM-1`, `INV-SHIM-2`,
  `INV-SHIM-3`, `INV-SHIM-4`, `INV-SHIM-5`, `INV-SHIM-6`, `INV-SHIM-7`, `INV-SHIM-8`, `INV-SHIM-10`, `INV-SHIM-11`, `INV-SHIM-12`.)_
- **`STORY-SHIM-KILL-TO-RELEASE`** <!-- uuid: f2c06a81-5d93-4e47-a8b0-6e1d3f9c7a25 --> — As a script that starts `caffeinate` in the background and later kills its
  process, I want killing it to release the Mac, so I need no new protocol. _(→ `JOURNEY-SHIM-CLAUDE-CODE`;
  `INV-SHIM-9`.)_
- **`STORY-SHIM-HONEST-COVERAGE`** <!-- uuid: 8e47d1b3-a0f6-4c29-95d8-b3a7c2e01f64 --> — As the operator, I want to know exactly what the shim covers and what it
  cannot, and to see the holders it misses, so I never believe the rule applies to a holder that bypasses
  it. _(→ `JOURNEY-SHIM-CLAUDE-CODE`; `GOAL-ONE-HOLDER`, `INV-SHIM-13`, `INV-SHIM-14`.)_

## Journeys

### `JOURNEY-SHIM-CLAUDE-CODE` — a busy session keeps the Mac awake through the shim <!-- uuid: 1d7a9c40-e5b2-4f68-83a1-0c6e8d5b2f97 -->

**Primary actor:** `ACTOR-CALLER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`GOAL-ONE-HOLDER`, `INV-SHIM-1`, `INV-SHIM-2`, `INV-SHIM-3`, `INV-SHIM-4`, `INV-SHIM-5`,
`INV-SHIM-6`, `INV-SHIM-7`, `INV-SHIM-8`, `INV-SHIM-9`, `INV-SHIM-10`, `INV-SHIM-11`,
`INV-SHIM-12`, `INV-SHIM-13`, `INV-SHIM-14`. _Includes:_ none.

1. The caller starts `caffeinate -i -t 300` while its turn is busy. The command found first on the search
   path is the shim, so a lease with the idle flag and an expiry five minutes ahead is recorded, and the
   shim stays alive for as long as the lease (`INV-SHIM-1`, `INV-SHIM-5`, `INV-SHIM-9`).
2. The turn is still busy at the end of five minutes. The shim exits 0 as the real tool would, and the
   caller starts it again (`INV-SHIM-5`).
3. The turn ends and the caller kills the shim's process. The signal removes its lease and the shim ends by
   that signal, so the caller sees the status the real tool would give (`INV-SHIM-9`).
4. A wrapper script runs `caffeinate -w` on its own process in the background and exits. The shim exits when
   the watched process does (`INV-SHIM-6`).
5. A shell runs `caffeinate make test`. The shim runs the command as a child, holds a lease for as long as
   it runs and exits with its status (`INV-SHIM-7`).
6. The battery falls to the release level. The leases, shim ones included, stop holding the Mac awake;
   the shims keep running and their callers see nothing (`INV-SHIM-1`).
7. The operator reads the status and sees every shim lease and, separately, every other holder the host
   reports, including any that called the real tool by its full path (`INV-SHIM-13`).

Extensions:

- 1a. The caller passes a flag the real tool does not know. The shim prints the real tool's message and
  usage line and exits 1 (`INV-SHIM-4`).
- 1b. The caller passes `-u`. The shim rejects it with a message and exits 1 (`INV-SHIM-3`).
- 1c. The shim cannot record the lease because the state is unwritable. The pure form prints one line and
  exits 3; the utility form warns and runs the command anyway (`INV-SHIM-11`).
- 1d. The keeper is down. The shim records the lease and holds silently; the status view shows the keeper
  down (`INV-SHIM-11`).
- 3a. The shim is killed without warning. Its lease stays until the keeper reaps it (`INV-SHIM-9`).
- 3b. The caller started the shim with the interrupt signal ignored, as a background job does. The shim
  keeps ignoring it (`INV-SHIM-9`).
- 4a. The watched process does not exist or the id is negative. No lease is made and the shim exits 0
  at once (`INV-SHIM-6`).
- 5a. The command is not found. The shim prints the real tool's message and exits 127 (`INV-SHIM-7`).
- 7a. The shim is disabled by configuration. Existing shim leases stay valid until their callers end
  (`INV-SHIM-14`).

## Invariants

- **`INV-SHIM-1`** <!-- uuid: 6e0b2a95-d4c7-4e13-9f58-a1c83d7e0b46 --> — The shim MUST turn each call into a lease and MUST NOT hold the Mac awake
  itself, so every call passes the battery rule. It MUST NOT depend on the name it is run under; it is
  presented to callers as `caffeinate` by the configuration when enabled, and the keeper runs the real
  tool by its full path, so the shim is never recursive. _(Design section 7; R2, R3; ADR 0100 decision 3.)_
- **`INV-SHIM-2`** <!-- uuid: b83d6f1c-097e-4a52-b4a0-e5c2d9f7a381 --> — The shim MUST mirror the real tool's command line with the same option
  parsing: combined flags, values attached to their flag, `--` to end options, and option parsing that
  stops at the first argument that is not an option. It records the flags `d`, `i`, `m` and `s`, and when
  none is given records `i`. Each case in the catalog under `INTF-SHIM` MUST be checked against the real
  tool by an automated test. _(Design section 7.)_
- **`INV-SHIM-3`** <!-- uuid: 4a95e0d7-2f18-4c63-8e91-7b0a6c3d5f20 --> — The flag `-u` MUST be rejected with a message on standard error and exit
  status 1, because no known caller uses it and the real tool's behavior for it is not the one it
  documents. A caller rejected this way is discoverable only from its own standard error. _(Design
  section 7 and 17 (O2).)_
- **`INV-SHIM-4`** <!-- uuid: e7c1a38b-5d06-4f94-a2b8-0d9f4e6c1a73 --> — An unknown flag MUST print the real tool's illegal-option message and its
  usage line and exit 1, and any other usage error MUST print the usage line and exit 1. The shim keeps
  exit status 1 for a usage error to match the real tool, and does not use the product's own usage-error
  code. _(Design sections 6 and 7.)_
- **`INV-SHIM-5`** <!-- uuid: 90b4d2f6-a37e-4c18-8d65-c1e5a0b9f347 --> — With `-t N` for N above 0, the lease MUST expire N seconds later by the
  wall clock and the shim MUST exit 0 then. `-t 0` MUST give an indefinite lease; a negative or an
  overflowing value MUST make no lease and exit 0; a value that is not a number, or a missing value,
  MUST print the usage line and exit 1. A value with a numeric prefix MUST be read as the real tool reads
  it, and a repeated `-t` MUST use the last one. _(Design section 7.)_
- **`INV-SHIM-6`** <!-- uuid: 2c58f7a0-e1d9-4b34-96a7-f40b8e3d1c65 --> — With `-w PID`, the lease MUST end, and the shim MUST exit 0, when that
  process exits. `-w 0` MUST give an indefinite lease; `-w -1`, or a process that does not exist, MUST
  make no lease and exit 0 at once. A repeated `-w` MUST use the last one, and with both `-t` and `-w`
  the first to fire ends the lease. _(Design section 7.)_
- **`INV-SHIM-7`** <!-- uuid: d6a30e92-7b48-4f05-a1c9-3e8d5b0f7a14 --> — With a utility to run, the shim MUST run it as a child, hold a lease bound
  to the shim for as long as it runs, ignore `-t` and `-w` (a `-w` on a gone process still runs the
  utility), exit with the utility's status, forward the interrupt, terminate and hang-up signals to it,
  and re-raise the signal if the utility died of one. A utility that is not found MUST give the
  message `<command>: No such file or directory` and exit 127. _(Design section 7.)_
- **`INV-SHIM-8`** <!-- uuid: 07e9c5b4-f2a1-4d86-b3d0-9a6c1e8f4b52 --> — With no arguments, the shim MUST hold an indefinite lease bound to the shim
  itself. _(Design section 7.)_
- **`INV-SHIM-9`** <!-- uuid: 5f1a8d36-c0b7-4e29-9d84-2e7b4a0c6f98 --> — The lease MUST last exactly as long as the shim. On the terminate,
  hang-up or interrupt signal the shim MUST remove its lease and then end by that signal, so its status is
  143, 129 or 130, and MUST NOT take over a signal that was ignored when it started. A shim that is
  killed without warning leaves its lease for the keeper to reap. _(Design section 7 (lifetime and
  signals).)_
- **`INV-SHIM-10`** <!-- uuid: a39d4e07-6b52-4c81-8f1a-d0c7e5a3b269 --> — The time `-t` counts MUST be wall-clock time, so a Mac that sleeps through a
  long `-t` still lets it expire on schedule. _(Design section 7.)_
- **`INV-SHIM-11`** <!-- uuid: 68f2b0c5-a4d3-4e97-a5b1-81c9e7d03f4a --> — When the shim cannot record its lease, the pure form MUST print one line on
  standard error and exit 3, and the utility form MUST warn on standard error, still run the utility and
  exit with the utility's status. When the keeper is down the shim MUST still record its lease and hold
  silently, and MUST NOT fall back to starting a real `caffeinate`. _(Design section 7 (failure policy)
  and 3 (R5); O3.)_
- **`INV-SHIM-12`** <!-- uuid: c05a7e18-3d94-4b62-90f6-b2e8d1a4c735 --> — The shim MUST NOT read the configuration and MUST NOT emit telemetry, so a
  call costs one lease and nothing else, and a caller that is rejected leaves no record but its own
  standard error. _(Design sections 7, 12 and 17 (O2).)_
- **`INV-SHIM-13`** <!-- uuid: 1b86d3f9-e72a-4a05-8c43-5d0f9a6e2b81 --> — The shim's coverage MUST be bounded and stated: a caller that runs the real
  `caffeinate` by its full path, and any holder that is not `caffeinate`, is not intercepted, and a
  session or shell that started before the shim was installed keeps its earlier holder until it ends.
  The status view MUST list these as external holders, which is the proof of coverage. _(Design sections
  12, 15 and 17 (O6); ADR 0100 Consequences.)_
- **`INV-SHIM-14`** <!-- uuid: e4a9b106-5c38-4d72-b7e5-0a3f81c9d6a7 --> — Disabling the shim MUST remove the `caffeinate` name it presented, MUST leave
  shim leases valid until their callers end, and MUST NOT stop the keeper or the anchor. The assertion
  drains only when the keeper is also disabled, after the watchdog timeout. _(Design section 11.)_

## The interface catalog

- **`INTF-SHIM`** <!-- uuid: c19e0b57-4d2a-4836-8fb3-a97d1e6c0254 --> — the `caffeinate` command line as a caller sees it. What crosses in: the
  flags `d`, `i`, `m`, `s` and `u`, `-t` with a number of seconds, `-w` with a process id, `--`, and an
  optional utility with its arguments. What crosses out: exit statuses and standard-error messages. What
  MUST hold: `INV-SHIM-1` to `INV-SHIM-14`. The counterparty is `ACTOR-CALLER`. The cases that MUST match the
  real tool are these:

| Input                                     | The shim does                                                                |
| ----------------------------------------- | ---------------------------------------------------------------------------- |
| flags `d`, `i`, `m`, `s` (none means `i`) | Record them                                                                  |
| `-u`                                      | Reject with a message, exit 1                                                |
| an unknown flag                           | The real tool's illegal-option message and usage line, exit 1                |
| `-t N`, N above 0                         | Expire N seconds later, exit 0                                               |
| `-t 0`                                    | Indefinite lease                                                             |
| `-t` negative, or overflowing             | No lease, exit 0                                                             |
| `-t abc`, or `-t` or `-w` with no value   | The usage line, exit 1                                                       |
| a value with a numeric prefix             | Read as the real tool reads it                                               |
| a repeated `-t` or `-w`                   | The last one wins                                                            |
| `-w PID`                                  | End and exit 0 when the process exits                                        |
| `-w 0`                                    | Indefinite lease                                                             |
| `-w -1`, or a process that does not exist | No lease, exit 0 at once                                                     |
| `-t` and `-w` together                    | The first to fire ends the lease                                             |
| a utility to run                          | Run it as a child, ignore `-t` and `-w`, pass its status, 127 when not found |
| `--`                                      | Ends the options                                                             |
| no arguments                              | An indefinite lease bound to the shim                                        |
