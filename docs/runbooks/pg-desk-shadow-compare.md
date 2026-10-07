# Runbook: pg-desk shadow compare (phase A)

Operator reference for running the fingerprint change detection beside the live change flow and
reading the comparison report. Intended behavior, definitions and metrics:
[`docs/behavior/pg-desk/shadow-compare.md`](../behavior/pg-desk/shadow-compare.md). Tool:
`packages/pg-desk-shadow` (`pg-desk-shadow`). Bead: `pg2-nu7h0`. Phase B (default tiers, no seeding)
is `pg2-dngh6` and MUST NOT run concurrently with this run: two shadow consumers double the list cost
against the shared token.

**Starting the multi-day run is the operator's.** An agent builds the tooling and proves it with a
few-tick smoke (below); it MUST NOT start the multi-day run.

## Safety summary

| Guard                    | Enforced by                                                                     |
| ------------------------ | ------------------------------------------------------------------------------- |
| No file write outside it | `sandbox-exec` around every child; startup self-test; denial detection          |
| No GitHub write          | `gh` shim allowlist (reads only); a rejected write stops the run                |
| No beads write           | `bd` shim allowlist (reads only); `BEADS_DOLT_AUTO_START=0`; no `bd dolt start` |
| No stray environment     | `env -i` allowlist; per-tick re-verification of every scratch/read-only path    |
| Shared-token headroom    | budget guard (floor), shadow spend kill ceiling                                 |

The PARENT collector is not sandboxed (it tails the live logs and reads the live store, read-only);
only its child process tree is. `sandbox-exec` is deprecated; the collector refuses to start when it
is missing or a probe write outside the scratch directory succeeds. The sandbox does NOT block the
network or `bd`/dolt writes: those are covered by the shims and the environment.

## Prerequisites

- macOS with `/usr/bin/sandbox-exec`, `sqlite3` (3.35 or newer), `pg-desk`, `pg-connector`,
  `pg-router-source-pg-desk`, `gh` (logged in; the token is read from the login keychain) and `bd`
  (the machine `bd`, not the router wrapper's bundled copy) on `PATH`; `bgrun`/`bgcheck`; `caffeinate`.
- The machine awake for the whole run (`caffeinate -i` below). Gaps from sleep, lid close, reboot and
  router applies WILL occur; they are recorded and excluded from misses.
- A scratch directory on a volume with a few hundred MiB free, NOT under `~/.local/state`, for
  example `$HOME/pg-desk-shadow/phase-a-<date>`.
- Read the NEWEST `graphql_remaining` from `~/.local/state/pg-connector-pr-github/events.jsonl`
  before starting: if it is under about 3,000 wait for the window to reset.

## 1. Prepare

```bash
pg-desk-shadow prepare --scratch "$HOME/pg-desk-shadow/phase-a-2026-10-08"
```

`prepare` (once per scratch directory; it refuses to overwrite an existing one):

1. runs the startup safety self-test (sandbox probe, shim self-checks);
2. `sqlite3 -readonly <live store> ".backup"` into `<scratch>/state/pg-desk/store.db` and runs
   `pg-desk migrate --cutover` on the copy;
3. writes the scratch `pg-desk` config (`sync.mode: off`, no ticket patterns, no Jira, the live
   `beads_dir` kept for read-only use, `watch.pr.queries: [mine, team]`, `sweep.max_age: 8760h`) and
   the scratch `pg-pr` config (no Jira backend);
4. builds `<scratch>/bin` (unwrapped tool binaries and the `gh`/`bd` shims) and the run manifest
   (`run.json`: build ids, bd mode, parameters, `T0`);
5. WARM-UP (skip with `--no-seed`, phase B): per watched query one
   `pg-connector pr list --query <q> --fingerprints --output json`, then the seeding SQL on the
   scratch store, then the assertions (no active row without `hydrated_at`).

`--bd-mode hermetic` replaces the read-only `bd` with a shim answering `list` with `[]` (use it if
the machine `bd` writes under the live `.beads` directory, which the sandbox then denies).

The report states, and you MUST NOT contradict it: the REMOTE tier is effectively off because
`hydrated_at` is seeded and `sweep.max_age` is 8760h, the local reconcile tier (30m) stays on, and
the warm-up is a simplification, not the treatment of `pg2-5rb3t` (phase B measures that).

## 2. Launch (operator-run)

```bash
bgrun shadow-a -- caffeinate -i pg-desk-shadow run --scratch "$HOME/pg-desk-shadow/phase-a-2026-10-08"
bgcheck shadow-a          # DONE exit=N plus the log tail; the only trustworthy exit status
```

The collector outlives agent sessions (a launchd agent is out of scope). It writes
`<scratch>/collector/ticks.jsonl` and `collector.log` and exits when a stop or kill condition fires
or on `SIGTERM`/`SIGINT`.

| Exit | Meaning                                                                            |
| ---- | ---------------------------------------------------------------------------------- |
| 0    | Stopped on request (`--max-ticks`, signal) or the stop criteria were met           |
| 1    | Refused to start (safety check, missing tool, second collector holding the lock)   |
| 4    | Kill criterion tripped (spend ceiling, 10 consecutive failed ticks, denial, write) |

### Resume

Rerun the SAME command with the SAME `--scratch`. The collector keeps its consumer cursor in the
scratch store and its tail positions in `collector/state.json`: it truncates each scratch log copy to
the last recorded size before appending (so appends are idempotent), recovers a `tick_start` without
an end from the scratch `change_log` (marked `recovered`) and records a `gap` row for the downtime.
Never copy the scratch store or edit `ticks.jsonl` by hand.

### Parameters (adjustable here, defaults)

| Flag                     | Default | Meaning                                                            |
| ------------------------ | ------- | ------------------------------------------------------------------ |
| `--period`               | `60s`   | Slot length (the production cadence)                               |
| `--kill-points-per-hour` | `1500`  | Shadow GraphQL spend above this in a rolling hour aborts the run   |
| `--max-failed-ticks`     | `10`    | Consecutive failed ticks before aborting                           |
| `--floor-margin`         | `200`   | Margin in the floor `max(2000, 1000 + max_per_poll x 12 + margin)` |
| `--tick-timeout`         | `5m`    | One `pg-desk` call's deadline (an overrun skips the missed slots)  |

## 3. While it runs

- `bgcheck shadow-a` and `tail <scratch>/collector/collector.log`: one line per tick, scrubbed of
  identifiers.
- `pg-desk-shadow report --scratch <scratch>` at any time (idempotent): the DATA-QUALITY header and
  the stop-criteria table show how far along it is.
- Do not run `pg-desk pr changes` by hand against the live store or with the consumer name
  `shadow-compare`; do not edit anything under the scratch `state`.

## 4. Finish

The run is COMPLETE when ALL hold: at least 2 weekdays of ticks, at least 100 LIVE-DETECTED
`pr.changed` events, collector uptime at least 95 percent, AND the SWEEP-CAUGHT headline count
reaches 10 or 7 days have elapsed (then the observed rate IS the finding). The report's stop table
prints MET or NOT MET per criterion. Then:

```bash
pg-desk-shadow report --scratch "$HOME/pg-desk-shadow/phase-a-2026-10-08"
```

writes `reports/report.md` and `reports/report.json`: every PR appears only as an HMAC label (the key
is `hmac.key` in the scratch directory and MUST stay there). Phase B combines with this run through
`pg-desk-shadow report --combine <dirA> <dirB>`.

The verification child bead (labelled `human`) is where you confirm the run finished and the stop
criteria held; that child alone posts the report summary (no identifiers) on `pg2-nu7h0` and on
`pg2-2j5ac.52.22` with an explicit met / not met / needs-operator-ruling statement for metrics (a),
(b), (c) and (e).

## 5. Troubleshooting

| Symptom                                            | Meaning and action                                                           |
| -------------------------------------------------- | ---------------------------------------------------------------------------- |
| `refusing to start: ...`                           | A safety check named the offending variable or path; fix the cause           |
| every tick `failed` with `Operation not permitted` | A tool wrote outside the scratch tree; the run stops (exit 4); keep the log  |
| `skipped_budget` rows                              | Shared token below the floor; the guard resumes after the reset              |
| exit 2 ticks with `hydration_budget`               | More changed PRs than `hydration.max_per_poll`; expected right after seeding |
| a `bd` verb rejected in the shim log               | A new read verb: add it to the allowlist, never to a write list              |
| `no oauth token found`                             | The keychain read failed in this session type (see Unverified items)         |

## Corrections to the filing bead's code claims

Every claim was re-verified against current source and logs on 2026-10-07; these differ.

1. `run-record.log` rotates at 8 MiB (`runRecordLogMaxBytes = 8 << 20`), not 4 MiB. At about 250
   bytes and 130 rows per hour that is more than a week, so rotation is unlikely in one run; the
   reader still drains the rotated inode first.
2. A `PATH` shim for `gh` placed in front of the installed backends is BYPASSED:
   `pg-connector-pr-github` and `pg-connector-ci-github-actions` are wrapper scripts that PREPEND the
   nix-store `gh` to `PATH`, and `pg-desk` and `pg-router-source-pg-desk` prepend the real
   `pg-connector`. The scratch bin dir therefore links the UNWRAPPED `.<name>-wrapped` binaries.
3. `HOME` cannot be replaced for the children: with a scratch `HOME` (even with `GH_CONFIG_DIR` set to
   the real directory) `gh auth token` reports "no oauth token found", because the login keychain is
   resolved through `HOME`. The children keep the real `HOME`; the sandbox, not `HOME`, bounds writes.
4. The scratch copy of a WAL store cannot be opened with `sqlite3 -readonly` (the open needs the
   `-shm`/`-wal` files); only the LIVE store is opened read-only, the copy is read through a normal
   connection by the parent.
5. `items[]` carry `kinds` (an array, as in the envelope), not a single `kind`, and the dispatch
   row's `bead` field is the PR id itself (`<owner>/<repo>#<n>`), the same string as in `change`.
6. `gh api graphql` is a POST with `-f query=...`, so an allowlist keyed on `-f` alone would reject
   every read: the shim inspects the GraphQL document and rejects only a `mutation`.
7. Baselines drifted since the filing snapshot (916 sweep rows, 879 hash-changed): 923 and 884 at
   20:05Z the same day, 95.8 percent; the report recomputes them from the copied run record.

## Unverified items

Recorded as such until proven; the smoke fills in the third column.

| Item                                                        | Why unverified                              | Smoke result |
| ----------------------------------------------------------- | ------------------------------------------- | ------------ |
| `gh` keychain read from a `bgrun` (non-interactive) session | Verified interactively only                 | see below    |
| `log show` access to sandbox denials from a background run  | Needs the admin log group                   | see below    |
| `bd` read verbs create no file under the live `.beads`      | Decides passthrough versus hermetic bd mode | see below    |
| First-diff shape compatibility of v1 facts versus new facts | A spurious kind would be warm-up noise      | see below    |

## Smoke (agent-run, a few ticks, scratch only)

`pg-desk-shadow` ships a smoke recipe an agent MAY run: `prepare` into a scratch directory under
`$TMPDIR` prefixed `pg2-nu7h0-`, `adapter-exercise` once, `run --max-ticks N`, then `report`. The
smoke asserts every written file is under the scratch directory, ZERO sandbox denials, ZERO write
verbs in the shim logs, the budget read before and after, and that the report generator runs. The
live daemons write `~/.local/state` constantly, so "no live file modified" cannot be asserted. Results
of the 2026-10-07 smoke are recorded on bead `pg2-nu7h0`.
