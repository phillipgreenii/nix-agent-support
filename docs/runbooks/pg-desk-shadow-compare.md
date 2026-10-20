# Runbook: pg-desk shadow compare (phase A)

Operator reference for running the fingerprint change detection beside the live change flow and
reading the comparison report. Intended behavior, definitions and metrics:
[`docs/behavior/pg-desk/shadow-compare.md`](../behavior/pg-desk/shadow-compare.md). Tool:
`packages/pg-desk-shadow` (`pg-desk-shadow`). Bead: `pg2-nu7h0`. Phase B (default tiers, no seeding)
is `pg2-dngh6` and is SUPERSEDED: the next multi-day run is the daemon run, described in
["Daemon run (Phase 4b)"](#daemon-run-phase-4b) below. The sections before it document the phase A
tooling that the daemon run reuses (sandbox, shims, budget guard, launch and watch commands). Two
shadow runs MUST NOT run concurrently: two shadow consumers double the spend against the shared
token.

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
  (the machine `bd`, not the router wrapper's bundled copy; history: the bundled copy once failed with
  "table not found: leases" against the then-unmigrated shared database, before the 2026-10-08 schema
  migration) on `PATH`; `bgrun`/`bgcheck`; `caffeinate`.
  `pg-desk-shadow` itself is on `PATH` only where the consuming flake sets
  `phillipgreenii.programs.pg-desk.shadow.enable = true` (default off; correction 10).
- The machine awake for the whole run (`caffeinate -is` below). Gaps from sleep, lid close, reboot and
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
   the scratch `pg-pr` config (no Jira backend; every `--beads-dir` in a registered instance command
   pinned to the same beads workspace, `pg2-ghmw0`);
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
bgrun shadow-a -- caffeinate -is pg-desk-shadow run --scratch "$HOME/pg-desk-shadow/phase-a-2026-10-08"
bgcheck shadow-a          # DONE exit=N plus the log tail; the only trustworthy exit status
```

The first 8-hour run (2026-10-09) recorded sleep gaps (7,269 seconds in total, per its report) while
launched under `caffeinate -i`; the cause was not established. `-i` only prevents idle sleep, so a
lid close would still sleep the machine. `-s` additionally prevents system sleep, but per
`caffeinate(8)` only while on AC power; keep the machine plugged in and the lid open (or on an
external display). Sleep gaps are recorded and excluded from misses either way, but they
still count against uptime.

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

| Flag                     | Default | Meaning                                                                                                                             |
| ------------------------ | ------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `--period`               | `60s`   | Slot length (the production cadence)                                                                                                |
| `--kill-points-per-hour` | `0`     | Shadow GraphQL spend above this in a rolling hour aborts the run; `0` uses the daemon cap recorded at prepare time (1500 when none) |
| `--max-failed-ticks`     | `10`    | Consecutive failed ticks before aborting                                                                                            |
| `--floor-margin`         | `200`   | Margin in the floor `max(2000, 1000 + max_per_poll x 12 + margin)`                                                                  |
| `--tick-timeout`         | `5m`    | One `pg-desk` call's deadline (an overrun skips the missed slots)                                                                   |

### The daemon child

`run` starts the scratch `pg-connector-github serve` as its own sandboxed child (same environment
checks as every other child, output in `<scratch>/logs/daemon.out.log`) and waits until
`status --json` reports it serving (exit 0 or 2) before the first slot; it refuses to start
(exit 2, `refusing to start: daemon: ...`) when the daemon never serves or the scratch directory was
prepared without one. It restarts a child whose process dies (counted) and stops it on exit or
signal. Every executed and every skipped tick row carries a `daemon` sample (`state` healthy,
degraded or down from the status exit 0, 2 or 3; `status_exit`; `alive`; cumulative `restarts`). A
slot with no tick row has no sample and counts as not up.

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

## Daemon run (Phase 4b)

The next multi-day run measures the daemon-backed GitHub backend (`pg-connector-github`) against the
live change flow (intended behavior: INV-SHADOW-11 to INV-SHADOW-14 in the behavior note). The
harness, its sandbox, shims, budget guard and the launch, watch and stop commands of sections 2 to 4
above are reused unchanged; only the prepare flags, one prerequisite and the report's cutover section
differ. `run` starts the daemon itself as a sandboxed child with its own socket, state directory and
configuration (the `PG_CONNECTOR_GITHUB_*` variables) under the scratch directory, so it never shares
the live daemon's socket, state or configuration (see "The daemon child" above). Starting the multi-day
run is still the operator's; an agent proves the chain with the smoke at the end of this section and
MUST NOT start it.

### Prerequisites (in addition to the list above)

- `pg-connector-github` on `PATH`, or built into a directory passed to `prepare` with `--tool-dir`
  (searched after `PATH`). `prepare` pins a copy into `<scratch>/bin`; the run uses that copy, so a
  later install does not change a running scratch directory.
- A queries file: one JSON object whose keys are the watched query names (default `mine` and `team`,
  the `--queries` flag) and whose values are that query's GitHub search expression as a string, or an
  array of strings, in the same form as the `pr.queries` entries of the backend configuration you
  already run. Keep it outside the repository and do not paste its contents anywhere; for example
  `$HOME/pg-desk-shadow/daemon-queries.json`. It is rendered only into the scratch daemon
  configuration.
- The same token headroom rule as above: read the NEWEST `graphql_remaining` first and wait when it
  is under about 3,000. The daemon spends from the same token as the live flow.

### Prepare

```bash
pg-desk-shadow prepare --scratch "$HOME/pg-desk-shadow/daemon-<date>" --no-seed \
  --daemon-queries-file "$HOME/pg-desk-shadow/daemon-queries.json"
# add --tool-dir DIR when pg-connector-github (or another tool) is not installed yet
```

`--no-seed` skips the phase A warm-up seeding (the daemon needs no seeded store). The daemon's GraphQL
cap is `--daemon-points-per-hour` and DEFAULTS to 1,500 points per hour, because the daemon runs
alongside the live flow on the same token. The operator MAY raise it (1 to 100,000 is accepted; the
cutover ceiling is 4,000), but only when preparing the scratch directory: the cap is rendered into the
scratch daemon configuration and recorded in `run.json`, so raising it later means a new `prepare` into
a new scratch directory. `--kill-points-per-hour` of `run` (default `0`, meaning the recorded cap)
is only the collector's abort ceiling on measured shadow spend; it does not change what the daemon may
spend. An agent MUST NOT raise the cap.

### Launch, watch and stop

Launch exactly as in section 2, with the daemon scratch directory:

```bash
bgrun shadow-d -- caffeinate -is pg-desk-shadow run --scratch "$HOME/pg-desk-shadow/daemon-<date>"
bgcheck shadow-d          # DONE exit=N plus the log tail; the only trustworthy exit status
```

Watch as in section 3 (`bgcheck shadow-d`, `<scratch>/collector/collector.log`, an idempotent
`pg-desk-shadow report --scratch <scratch>`), plus the daemon: every tick row of
`<scratch>/collector/ticks.jsonl` carries a `daemon` sample, so

```bash
jq -c 'select(.kind=="tick") | {slot, status, d: .daemon.state, exit: .daemon.status_exit, restarts: .daemon.restarts}' \
  "$HOME/pg-desk-shadow/daemon-<date>/collector/ticks.jsonl" | tail -5
```

shows the daemon state per tick (`healthy`, `degraded` or `down`, from `status --json` exit 0, 2 or 3)
and the restart count. The daemon's own logs are `<scratch>/state/pg-connector-github/events.jsonl`
(kind `origin` rows carry the GraphQL cost the report sums), `daemon.log.jsonl` and `client.log.jsonl`;
its stdout and stderr are in `<scratch>/logs/daemon.out.log`.

Stop with `SIGTERM` to the collector (it then stops the daemon child itself) and resume as in
"Resume":

```bash
pgrep -f 'pg-desk-shadow run --scratch .*daemon-<date>'      # the `pg-desk-shadow run` PID, not its caffeinate parent
kill -TERM <pid>                                             # then bgcheck shadow-d: exit 0
pgrep -f 'pg-desk-shadow/daemon-<date>'                       # exit 1 (nothing found) is the success case
```

If the second `pgrep` still finds a process of the scratch directory, that is a defect to file against the daemon
child lifecycle (a bare `pgrep -f 'pg-connector-github.*serve'` also matches every other daemon on the host, such as
test-runner daemons and the live one, so name the scratch path); kill it by the PID the collector log records (`daemon child started (pid N)`),
never by a broad pattern.

### Reading the cutover section

`pg-desk-shadow report --scratch <scratch>` writes `reports/report.md` and `reports/report.json`.
The `## Cutover criteria` heading of the report holds one line per default criterion, in this order,
each `- MET:` or `- NOT MET:` with the measured figure and, indented below it, the definition and the
evidence (INV-SHADOW-13):

1. zero unexplained misses over the LIVE-DETECTED events in the run window;
2. detection delay p90 no worse than live;
3. GraphQL points per hour inside the cap, for every clock hour of the run;
4. daemon uptime at least 95 percent (a slot counts as up when its tick row carries a healthy or
   degraded daemon sample).

A criterion that cannot be evaluated (no live events, no delay samples, no hourly cost samples, no
slots) reads `NOT MET` with `not evaluable`; a run of a few ticks therefore reads NOT MET for most
criteria, which is the correct output. Below the criteria, `### Budget recommendation` states one
measured verdict with its arithmetic: `keep-cap`, `raise-cap-to-<N>` (the smallest step of 100 above
the cap whose 90 percent background share covers the p90 hourly cost; at the 1,500 default only 1,600
is considered), or `relax-max-age-2h` (relax both `detail_max_age` and `conversation_max_age` to 2 h
when no cap step covers it). The JSON holds the same as `phases[].cutover` (keys `criteria`, `recommendation`, `follow_ups`).

### An unmet criterion (ruling 14)

The operator MAY cut over with criteria unmet when the run shows the daemon working (INV-SHADOW-14).
The cutover bead MUST then record the operator's decision and every unmet criterion, and each unmet
criterion MUST get its own follow-up bead. The report lists the proposed follow-up bead title and body
for every unmet criterion under `### Follow-ups` (it only lists them; filing is the operator's, or the
packet that operates the run). When the cutover raises the cap, the dashboard cap line and the alert
threshold must both be raised to 4,000 (`packages/pg-connector/grafana/pg-connector-github.json` and
`packages/pg-connector/grafana/alerting/pg-connector-github-alerts.yaml`).

### Smoke (agent-run, at most three ticks, scratch only)

An agent MAY prove the whole chain with at most three ticks, under `bgrun`, in a throwaway scratch
directory outside `~/.local/state`, with the cap at its 1,500 default (an agent MUST NOT raise it):

```bash
pg-desk-shadow prepare --scratch "$HOME/pg-desk-shadow/smoke-<date>" --no-seed --tool-dir "<bin>" \
  --daemon-queries-file "<queries.json>"
pg-desk-shadow probe --scratch "$HOME/pg-desk-shadow/smoke-<date>"            # probe ok, exit 0
pg-desk-shadow run --scratch "$HOME/pg-desk-shadow/smoke-<date>" --max-ticks 3
pg-desk-shadow report --scratch "$HOME/pg-desk-shadow/smoke-<date>"
pgrep -f 'pg-desk-shadow/smoke-<date>'                                        # exit 1: nothing of the scratch dir left
```

Expected: three tick rows, each with a `daemon` object and, unless `skipped_budget`, a `sources` entry
per watched query and instance (the ci instance's queries are prefixed `ci:`; a healthy entry has
status `succeeded`); each tick row's `graphql_cost_sum` equals the sum of `graphql_cost` over the
`kind=origin` rows of the scratch daemon's `events.jsonl` whose `time` is after the previous tick row's
`tick_end` (after the run start for the first) and not after this row's `tick_end`; a report with the
cutover section; no daemon process afterwards. Keep the scratch directory as evidence.

Result of the 2026-10-20 daemon smoke (default cap, `--no-seed`, queries `mine` and `team` with the generic
expressions `is:pr is:open author:@me` and `is:pr is:open review-requested:@me`): `probe` exited 0; three ticks, all
`ok`, each with `sources` entries `mine`, `ci:mine`, `team` and `ci:team` at `succeeded` and a `daemon` sample
`healthy` with `status_exit` 0, `alive` true and 0 restarts (the daemon reported exit 2, degraded, only while
starting); `graphql_cost_sum` 117, 19 and 3, each equal to the sum of `graphql_cost` over the `kind=origin` rows of
the scratch `events.jsonl` in its window (189 origin rows in total); the report wrote Markdown and JSON with
criteria 1 and 2 NOT MET (not evaluable: no live events in a three-minute run), criteria 3 and 4 MET and the
recommendation `keep-cap`; the collector log records the daemon child stopped, and no process of the scratch
directory remained. From a `bgrun`-launched session `gh auth status` exited 0 and `log show --last 1m --style
compact --predicate 'sender == "Sandbox"'` exited 0.

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
10. `pg-router-source-pg-desk` was not on this machine's `PATH` after the apply that was expected to
    install it (bead `pg2-wmv6p`): both it and `pg-desk-shadow` were overlay attrs only, with no module
    installing them. `home/programs/pg-desk` now installs the adapter with `pg-desk` by default
    (`routerSource.enable`, default true) and `pg-desk-shadow` only when
    `phillipgreenii.programs.pg-desk.shadow.enable = true` is set in the consuming flake. The adapter's
    installed wrapper (like `pg-desk`'s) prepends the real `pg-desk`; until that flag is applied,
    `prepare --tool-dir` finds a locally built unwrapped copy, which is what the smoke exercised.
11. Exit codes: the collector uses 2 (refused to start) and 4 (kill) and keeps 1 for a generic error,
    following the repo rule that exit 1 carries no branchable meaning.
12. Baselines drifted since the filing snapshot (916 sweep rows, 879 hash-changed): 923 and 884 at
    20:05Z the same day, 95.8 percent; the report recomputes them from the copied run record.
13. Correction 5 no longer holds for rows written after per-change event ids landed (observed
    2026-10-09): the dispatch row's `bead` field and the queue `eventId` are `<pr id>@<change hash>`,
    not the bare PR id. A report built before the fix joined on the raw field, so it matched nothing
    (0 of 28 live events in the first report, made 8 hours into the run) and classed matching events
    `copy-staleness` ("not in the seeded set"). Later reports of the same run cover more events (48
    in window when it was stopped, 11 of them matched after the fix).
    The report now joins on the bare id and keys de-duplication on the raw id. Regenerating a report
    over an earlier run's scratch directory recomputes it from the copied logs; nothing needs to be
    re-collected.
14. The report adds two informational figures after the first run: the late detection of missed
    events and the coverage ceilings (see the behavior note's (a)). Read the ceiling for the events
    while the collector was up alongside tick uptime, which overrun slots depress (see the
    "multi-day run's uptime" row of the Unverified items table). In the 2026-10-09 data, two groups
    of live events enqueued within one second (one around a tick overrun, one 46 seconds after a
    wake from sleep) make up a large share of the `collector-down` misses and mostly have no
    shadow item for the same PR; whether they are live re-emits or real changes the shadow missed
    is not yet determined.

## Unverified items

| Item                                                        | Result (2026-10-07 smoke)                                                                                                                                                                     |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gh` keychain read from a `bgrun` (non-interactive) session | VERIFIED: `prepare`, three ticks and the adapter exercise ran under `bgrun`, sandboxed, with the real `HOME`; again on 2026-10-20 (`gh auth status` exit 0, the daemon smoke ran three ticks) |
| `log show` access to sandbox denials from a background run  | VERIFIED: the per-tick scan ran under `bgrun` without an access error and found the denials of the first smoke; again on 2026-10-20 (`log show` exit 0 from a `bgrun` session)                |
| `bd` read verbs create no file under the live `.beads`      | REFUTED (see correction 8): the machine `bd` attempts writes; the hermetic default avoids them                                                                                                |
| First-diff shape compatibility of v1 facts versus new facts | OBSERVED, not judged: the first hydrations of seeded rows reported real changes (head, review, CI) plus a `new-entity` reconcile; the 4-tick smoke is too short to tell noise from change     |
| A multi-day run's uptime against the 95 percent criterion   | UNVERIFIED and at risk: ticks that hydrate take 90 to 150 seconds, so slots are skipped (overrun gaps count against uptime)                                                                   |
| `launchd`/sleep behavior of `caffeinate -i` with `bgrun`    | OBSERVED: sleep gaps under `-i` (2026-10-09, 7,269 seconds in an 8-hour run, cause not established); `-is` is the launch command now, UNVERIFIED over a multi-day run                         |

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
