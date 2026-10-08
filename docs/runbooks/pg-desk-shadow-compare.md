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
3. writes the scratch `pg-desk` config (`sync.mode: off`, no ticket patterns, no Jira, `beads_dir`
   pointing at a scratch stub workspace, `watch.pr.queries: [mine, team]`, `sweep.max_age: 8760h`) and
   the scratch `pg-pr` config (no Jira backend);
4. builds `<scratch>/bin` (unwrapped tool binaries and the `gh`/`bd` shims) and the run manifest
   (`run.json`: build ids, bd mode, parameters, `T0`);
5. WARM-UP (skip with `--no-seed`, phase B): per watched query one
   `pg-connector pr list --query <q> --fingerprints --output json`, then the seeding SQL on the
   scratch store, then the assertions (no active row without `hydrated_at`).

`--bd-mode hermetic` (the DEFAULT) has the `bd` shim answer every read from nothing (an empty
`{"data":[]}` envelope) against a scratch stub workspace. `--bd-mode passthrough` uses the machine
`bd` read-only against the live workspace, but that `bd` writes telemetry and a circuit-breaker file
outside the scratch tree even for `list`, so the sandbox denies it and the run stops: do not use it
unless that has changed. Other flags: `--tool-dir DIR[,DIR]` finds a tool that is not installed yet
(for example a freshly built `pg-router-source-pg-desk`), `--list-attempts N` (default 6) retries a
warm-up listing that exits 3, `--queries`, `--phase`, `--no-seed`.

Expect the `team` warm-up listing to need a retry or two: it takes 14 to 25 seconds against a 25
second connector backend deadline and fails on about half of its live runs too.

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

| Exit | Meaning                                                                                   |
| ---- | ----------------------------------------------------------------------------------------- |
| 0    | Stopped on request (`--max-ticks`, signal)                                                |
| 1    | Generic error (unexpected; read the message)                                              |
| 2    | Refused to start (safety check, missing tool, a second collector holding the lock)        |
| 4    | Kill criterion tripped (spend ceiling, failed ticks, sandbox denial, rejected write verb) |

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
7. The warm-up listing of the `team` query (ten search strings) regularly exits 3 with
   `deadline exceeded after 25s`: the live flow's own `team` listing does the same on about half its
   runs (visible in `~/.local/state/pg-connector-pr-github/events.jsonl`). The bead's design assumed a
   clean listing; `prepare` therefore retries (`--list-attempts`), and a shadow tick whose `team`
   source fails is a PARTIAL tick (exit 2), classed `hydration-failed` when it explains a miss.
8. The machine `bd` is not read-only in practice: even `bd list` writes `~/.beads/eventsData/*` and
   `/private/tmp/beads-circuit/*.json.tmp` (717 denials in the first smoke tick, from the `.bd-wrapped`
   process). The bead's `bd` passthrough against the live workspace therefore cannot reach zero sandbox
   denials; the bead's own alternative (a shim answering read verbs, `list` returning an empty result)
   is the default, with the empty result wrapped as `{"data":[]}` because the beads connector runs
   `bd` with `BD_JSON_ENVELOPE=1`. A scratch stub workspace stands in for `repos[0].beads_dir`.
9. The sandbox profile needs `(literal "/dev/dtracehelper")` in addition to the bead's `/dev` entries:
   every macOS Go binary opens it for write at start-up (29 denials from one `bd` run).
10. `pg-router-source-pg-desk` is not on this machine's `PATH` until the next apply, and its installed
    wrapper (like `pg-desk`'s) prepends the real `pg-desk`; `prepare --tool-dir` finds a locally built
    unwrapped copy, which is what the smoke exercised.
11. Exit codes: the collector uses 2 (refused to start) and 4 (kill) and keeps 1 for a generic error,
    following the repo rule that exit 1 carries no branchable meaning.
12. Baselines drifted since the filing snapshot (916 sweep rows, 879 hash-changed): 923 and 884 at
    20:05Z the same day, 95.8 percent; the report recomputes them from the copied run record.

## Unverified items

| Item                                                        | Result (2026-10-07 smoke)                                                                                                                                                                 |
| ----------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh` keychain read from a `bgrun` (non-interactive) session | VERIFIED: `prepare`, three ticks and the adapter exercise ran under `bgrun`, sandboxed, with the real `HOME`                                                                              |
| `log show` access to sandbox denials from a background run  | VERIFIED: the per-tick scan ran under `bgrun` without an access error and found the denials of the first smoke                                                                            |
| `bd` read verbs create no file under the live `.beads`      | REFUTED (see correction 8): the machine `bd` attempts writes; the hermetic default avoids them                                                                                            |
| First-diff shape compatibility of v1 facts versus new facts | OBSERVED, not judged: the first hydrations of seeded rows reported real changes (head, review, CI) plus a `new-entity` reconcile; the 4-tick smoke is too short to tell noise from change |
| A multi-day run's uptime against the 95 percent criterion   | UNVERIFIED and at risk: ticks that hydrate take 90 to 150 seconds, so slots are skipped (overrun gaps count against uptime)                                                               |
| `launchd`/sleep behavior of `caffeinate -i` with `bgrun`    | UNVERIFIED (not exercised: the smoke ran a few minutes)                                                                                                                                   |

## Smoke (agent-run, a few ticks, scratch only)

`pg-desk-shadow` ships a smoke recipe an agent MAY run: `prepare` into a scratch directory under
`$TMPDIR` prefixed `pg2-nu7h0-`, `adapter-exercise` once, `run --max-ticks 3`, then `report`. All of it
under `bgrun`. The smoke asserts every written file is under the scratch directory, ZERO sandbox
denials, ZERO write verbs in the shim logs (the three deliberate self-check probes precede the
`selfcheck-end` marker row in each shim log; count only rows after it), the budget read before and
after, and that the report generator runs. The live daemons write `~/.local/state` constantly, so "no
live file modified" cannot be asserted.

Result of the 2026-10-07 smoke (hermetic bd mode, phase A, seeded): prepare listed 81 PRs, seeded 79
active rows; 3 ticks (`partial`, `ok`, `partial`: the `team` listing hit the 25s deadline twice), 0 sandbox
denials by stderr and by `log show` (the only denial lines in the window came from two unrelated
processes), shim logs after the self-check marker 384 allowed `gh` rows, 52 allowed `bd` rows and no
rejected row, the adapter exercise exited 0 with 4 items, the report generated; the shared token read
4246 before the three ticks and 3793 after the adapter exercise (live flow included; the shadow's own
lower-bound spend was 109 points over the three ticks). An EARLIER smoke attempt
in passthrough bd mode stopped itself on the first tick with 717 denials: that evidence is why hermetic
is the default.
