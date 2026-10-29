# Runbook: the PR lane backfill driver (`pg-pr-backfill`)

The driver brings every followed PR through `pg-pr-change` once in `plan` mode and once in `apply`
mode (design: the PR lane refactor, "Adoption, backfill and the first apply"). What each mode and
exit code MEANS is in `docs/behavior/pg-decider/backfill.md`; this page is how to operate it. The
numbered first-apply procedure (plan period, review, flip, apply pass, verify, rollback) is built on
these commands in its own runbook.

## Prerequisites

- `pg-pr-backfill`, `pg-pr-change` and `pg-connector` on `PATH`, and `bd` for `snapshot` and `rollback`
  (the package puts the last two, and `pg-desk` for `mark-watched`, on the driver's own `PATH`).
  `PG_DECIDER_CONFIG` set as for the `pr-change` role: the driver takes the
  tracker backend and directory (`agent_tracker_backend`, `beads_dir`) from it unless its own
  configuration names them.
  Without it the decider has no `work_items_query` and `run` fails every PR with "work_items_query is
  not configured" (the driver prints a warning at the start of `run` when it is unset). The file is
  the one the deployment renders for the `pr-change` role: read the path from that role's environment
  (the `PG_DECIDER_CONFIG` entry of its deployed spec, the same value the role's own runs use) and
  export it in the shell that runs the driver. The path is deployment-specific and is not recorded
  in this repository.
- The deployment's query-name file and the driver configuration file (below).
- The newest GraphQL reading is readable (see `rate_source`), and above the floor.
- The machine awake for the whole pass. A run that is interrupted resumes.

## Configuration

The driver reads a JSON file: `--config FILE`, else `PG_PR_BACKFILL_CONFIG`, else
`$XDG_CONFIG_HOME/pg-pr-backfill/config.json` (it MAY be absent: the defaults and the flags then
decide). A flag of the same name overrides a key.

| Key                            | Meaning                                                                 | Default                                                                             |
| ------------------------------ | ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `queries_file`                 | File of PR query names, one per line, rendered by the deployment        | none (`enumerate` fails closed)                                                     |
| `anchors_query`                | The tracker's named query that lists the anchors (children are ignored) | `work-beads`                                                                        |
| `backend`, `beads_dir`         | The tracker backend instance and directory                              | from `PG_DECIDER_CONFIG`                                                            |
| `rate_source`                  | `old-backend-log` before the daemon cutover, `daemon-metrics` after it  | `old-backend-log`                                                                   |
| `rate_log`                     | The old backend's event log (`old-backend-log`)                         | `$XDG_STATE_HOME/pg-connector-pr-github/events.jsonl`                               |
| `metrics_url`, `metrics_query` | The local metrics API and the gauge query (`daemon-metrics`)            | `http://127.0.0.1:9090`, the daemon's budget gauge, bucket `graphql`                |
| `rate_max_age`                 | How old the newest reading may be (a Go duration)                       | `1h` for the log, `15m` for the metrics                                             |
| `floor`                        | GraphQL points under which a run ends `FLOOR`                           | `3000`                                                                              |
| `pace`                         | Wait between two PRs (a Go duration)                                    | `2s`                                                                                |
| `max_plan_age`                 | Bound on the age of the oldest plan record (a Go duration)              | `48h`                                                                               |
| `run_record`                   | The run record the composed command appends to                          | `PG_PR_CHANGE_RUN_RECORD`, else `$XDG_STATE_HOME/pg-pr-change/pr-change-runs.jsonl` |
| `set_file`                     | Where `enumerate` persists the set                                      | `$XDG_STATE_HOME/pg-pr-backfill/set.json`                                           |
| `pr_backend`                   | The PR backend instance `mark-watched` reads and inspects through       | none (the umbrella resolves it)                                                     |
| `desk_cmd`                     | The pg-desk command `mark-watched` refreshes with                       | `pg-desk`                                                                           |
| `bd`                           | The tracker's command line, run in `beads_dir` by `snapshot`/`rollback` | `bd`                                                                                |
| `tracker_snapshot`             | Where `snapshot` writes the tracker's before-image                      | `$XDG_STATE_HOME/pg-pr-backfill/tracker-snapshot.json`                              |

The daemon cutover changes ONE key: `rate_source` from `old-backend-log` to `daemon-metrics`. Until it
is changed, the driver reads a log that stops being written once the old backend is gone, and a log
whose newest reading is older than `rate_max_age` makes a run stop (`rate-signal-unavailable`), so the mistake cannot go unseen.
The metrics source queries `last_over_time((<metrics_query>)[<rate_max_age>:])`, so a quiet daemon (it emits
the gauge only when it spends budget) does not hide its last reading: a run stops with
`rate-signal-unavailable` only when no sample exists within `rate_max_age`.

## Commands

```bash
# 1. The set (read-only). Prints one line per PR, the unparseable items and the counts.
pg-pr-backfill enumerate

# 2. The plan pass. Resumes by itself; --new-pass starts a new pass (for example a re-plan).
#    A PR whose plan record carries a plan_error (the tracker lookup timed out) is retried by
#    the resume. A --new-pass over a copy of the set (--set FILE) does not move the resume
#    pointer of the persisted set.
pg-pr-backfill run --mode plan
pg-pr-backfill summary                      # the plan summary; exit 20 when a check does not hold

# 3. The flip marker, written just BEFORE the deployment apply that changes the role's mode.
pg-pr-backfill flip-marker --time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --epic-bead <epic-id>

# 4. The apply pass, with the creates per kind the operator recorded from the plan summary.
pg-pr-backfill run --mode apply --max-creates anchor=12,review-pr=9

# 5. Verify: the residual of every PR, then a second apply pass that writes nothing.
pg-pr-backfill summary --mode apply
pg-pr-backfill run --mode apply --new-pass --max-creates anchor=0
pg-pr-backfill writes                       # every write since the flip, by timestamp

# The rollback (the first-apply runbook, step 8): the before-image is taken before the flip marker.
pg-pr-backfill snapshot
pg-pr-backfill rollback                     # dry run: the by-timestamp listing and the reverts
pg-pr-backfill rollback --apply
```

## Marking the open-anchor PRs watched (the cutover's verify step)

After the daemon serves PR reads and the watch read exists, `mark-watched` marks every PR that has an
open anchor as watched, refreshes those PRs (hydrate only: no decider, no apply, no tracker write) and
checks that the daemon's watched count equals the tracker's open-anchor count. Run it in the
cutover window, before the consumer's cursor is reset (the reset is the cutover's own step, not this
tool's). It enumerates afresh and leaves the persisted set alone; `--from-set` marks from the persisted
set instead. It needs the same `rate_source` as `run`.

```bash
pg-pr-backfill mark-watched                 # exit 0 and a last line DONE watched=N expected=N, N non-zero
pg-pr-backfill mark-watched                 # idempotent: run again after a FLOOR or a FAILED 11
```

Read the lines before the last one: `UNMAPPED anchor <id> "<title>"` is an open anchor whose title names
no PR (it cannot be counted or marked: decide what it is); `skip <pr>` is an anchor whose PR is merged
or closed (not marked: the apply pass closes it). Exit `20` with `check=` in the last line means every
PR was marked and the count still differs: run `pg-connector pr inspect <id>...` on the PRs listed in
the output to see which are not watched. Exit `20` with "no PR with an open anchor to mark" means the
tracker listed no open anchor with a PR: the step refuses to pass at zero equals zero.

## Reading the last line

| Last line, exit | Do                                                                                                                                                                                                                                                                    |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DONE`, 0       | Next step.                                                                                                                                                                                                                                                            |
| `FLOOR`, 10     | Wait for the GraphQL window to reset (read the newest `graphql_remaining` first), run again.                                                                                                                                                                          |
| `FAILED`, 11    | The PRs it names (`transient=`, `degraded=`, others) failed and the run went on. Run again; it resumes and skips the finished ones. If the same PRs fail again, read their failure line with `pg-pr-change plan <id>` by hand.                                        |
| `FAILED`, 12    | `stop=` says why. A create limit exceeded or an `unauthenticated` or `decide_failed` run in an apply pass is a stop of the first apply: do not run again; follow the rollback of the first-apply runbook. An unreadable rate signal: fix `rate_source` and run again. |
| (none), 64      | A usage or configuration error; nothing ran. The message names it.                                                                                                                                                                                                    |

## Failure modes

- Each failed PR of a pass prints a progress line `fail <PR>: exit <n> (<class>): <reason>` before the
  last line. For `decide_partial` the reason is the composed command's failure line, which carries the
  decider's own lines naming each failing action (op, work item, rule) and its error text. The reason is
  one line, credential-shaped text redacted, at most 400 bytes.
- `enumerate` exits `30` with "fails closed": the query-name file is absent or lists no query, a
  listing was incomplete (a source degraded, truncated), or the set is empty. Nothing was written.
- A PR the forge reports gone (`not_found`) is a final result: it is counted apart and not run again, and
  the plan summary names it, because the operator MUST decide what its anchor is.
- A run record that is not rotated grows; do not rotate it between the plan pass and the end of the
  verification, because the summary and the listing read it.
