# Published views

The tracker's SQLite database is read through these views only; the tables behind them are private
and may change in a migration. Open the database read-only (`file:<path>?mode=ro`). Default path:
`$XDG_STATE_HOME/claude-tool-tracker/tracker.db` (default `~/.local/state`). The views are
recreated each time `claude-tool-tracker ingest` opens the database. Later packets take view and
column names only from this file; a test keeps it equal to the database.

Conventions: every `*_ts` and `ts` is milliseconds since the Unix epoch (the producer's clock).
`NULL` means no event carried the value. Booleans are `1`/`0`. When a call has several events of
one kind, the newest (by `ts`, then arrival) supplies the value. The event kinds and their fields:
[event-kinds.md](event-kinds.md).

## `tool_calls`

One row per call, identified by `session_id` plus `tool_use_id`. `call_id` is a stable integer
assigned at the call's first ingest; it never changes and is never reused.

| Column              | Meaning                                                                                                                        |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `call_id`           | stable integer id of the call                                                                                                  |
| `session_id`        | the Claude Code session                                                                                                        |
| `tool_use_id`       | the call's id                                                                                                                  |
| `agent_id`          | the subagent id when the call is a subagent's, else `NULL`                                                                     |
| `tool_name`         | the tool (`tracker.call_start` `tool`, else the hook events' `tool_name`)                                                      |
| `tool_input`        | the tool's arguments as JSON text (`tracker.call_start` `input`)                                                               |
| `cwd`               | the call's working directory (`tracker.call_start`, else `tracker.pre_tool`)                                                   |
| `verdict`           | the mod's permission decision: `allow`, `ask` or `deny` (`tracker.check` `decision`)                                           |
| `deciding_hook`     | the classic hook event that decided or whose ask the verdict was reached under (`tracker.check` `hook`)                        |
| `reason`            | the reason text of the decision (`tracker.check` `reason`)                                                                     |
| `permission_mode`   | the live permission mode (`tracker.pre_tool`, else `tracker.post_tool`)                                                        |
| `agent_type`        | the agent type name, in a subagent or an `--agent` session                                                                     |
| `result`            | how the call ended: `ok`, `error`, `refused` or `interrupted` (`tracker.call_end` `outcome`)                                   |
| `error_text`        | the error text (`tracker.call_end` `error`, else `tracker.post_tool` `error`)                                                  |
| `duration_ms`       | Claude Code's tool execution time (`tracker.post_tool`); excludes permission-prompt and hook time                              |
| `call_start_ts`     | time of `tracker.call_start`                                                                                                   |
| `running_ts`        | time of `tracker.running`: when the row first showed running (interactive sessions only)                                       |
| `call_end_ts`       | time of `tracker.call_end`                                                                                                     |
| `first_event_ts`    | time of the call's earliest event of any source                                                                                |
| `last_event_ts`     | time of the call's latest event of any source                                                                                  |
| `is_error`          | `tracker.post_tool` `is_error`; else from `result`: `1` for `error`, `0` for `ok`, `refused`, `interrupted`                    |
| `denial_reason`     | the denial reason of `tracker.denied`, including the auto-mode classifier's reason                                             |
| `sandbox_enabled`   | whether the sandbox was enabled for the call (`ceta.verdict`)                                                                  |
| `engine_verdict`    | CETA's engine verdict: `approve`, `ask`, `reject` or `abstain` (`ceta.verdict` `verdict`)                                      |
| `delivered_verdict` | the verdict CETA actually delivered (`ceta.verdict` `delivered`)                                                               |
| `repo`              | the repository name for `cwd`; a git worktree maps to its main checkout's name; `NULL` when `cwd` is no repo                   |
| `settled_by`        | who settled the call: `ceta`, `person`, `mode`, `rule` or `classifier`; `NULL` when no verdict event exists                    |
| `settlement`        | `approved`, `refused`, `pending` (no `tracker.call_end`, session may still run) or `unresolved`                                |
| `converted_ask`     | `1` when CETA said ask and an unattended session delivered a reject (settled `ceta`, `refused`), else `0`                      |
| `failed_timing`     | `1` when the call ran and errored (`result` `error`; still `approved`), `0` when it ended otherwise, `NULL` with no `call_end` |

### Settlement

Ingest derives `settled_by`, `settlement`, `converted_ask` and `failed_timing` once per call and stores
them; every reader uses the stored values. They are recomputed whenever a later event for the call
arrives (a resumed session), with the same `call_id`. "CETA decided" means a `ceta.verdict` event
whose `delivered` is `approve`, `reject` or `ask` (an `abstain` falls through to the `tracker.check` rows).

| Verdict                               | Permission mode     | `settled_by` | `settlement`                               |
| ------------------------------------- | ------------------- | ------------ | ------------------------------------------ |
| CETA delivered approve or reject      | any                 | `ceta`       | `approved` or `refused`                    |
| CETA delivered ask                    | any                 | `person`     | `approved` if the call ran, else `refused` |
| `allow`, no CETA                      | `bypassPermissions` | `mode`       | `approved`                                 |
| `allow`, no CETA                      | other               | `rule`       | `approved`                                 |
| `deny`, no CETA                       | any                 | `rule`       | `refused`                                  |
| `ask`, no CETA                        | `auto`              | `classifier` | `approved` if the call ran, else `refused` |
| `ask`, no CETA                        | `dontAsk`           | `mode`       | `refused`                                  |
| `ask`, no CETA                        | other               | `person`     | `approved` if the call ran, else `refused` |
| CETA engine `ask`, delivered `reject` | any                 | `ceta`       | `refused`, and `converted_ask` is `1`      |

"Ran" means `tracker.call_end` `outcome` is `ok` or `error`; `refused` and `interrupted` did not
complete. A call with no `tracker.call_end` is `pending` while its session may still be running and
`unresolved` once the session has a `tracker.session_end` or has written no event for the quiet
period of 2 hours (measured against the clock of the ingest run, since a crashed session never sends
`session_end`). The `classifier`, `dontAsk` and `bypassPermissions` rows are implemented as specified
but not yet observed live.

## `annotations`

Every event whose kind the tracker does not consume into `tool_calls` itself: all other sources'
events (`ceta.*`, ...) and every kind this version does not know. Join to `tool_calls` by
`call_id`, or by `session_id` plus `tool_use_id`. An event without `tool_use_id` has `call_id`
`NULL`.

| Column        | Meaning                                           |
| ------------- | ------------------------------------------------- |
| `event_id`    | the event's unique id                             |
| `session_id`  | the session                                       |
| `tool_use_id` | the call's id, or `NULL`                          |
| `agent_id`    | the subagent id, or `NULL`                        |
| `call_id`     | the call the event belongs to, or `NULL`          |
| `source`      | the producer (`tracker`, `ceta`, ...)             |
| `kind`        | the event kind                                    |
| `ts`          | the event time                                    |
| `data`        | the producer's `data` object as compact JSON text |

## `ingest_skips`

Per event file and ingest run that read lines: what the run did with them, with the run's time, so
a reader can report the skipped lines for a time window (`WHERE ingest_ms BETWEEN ...`). A run
that read no new line of a file adds no row for it. A skipped line is never dropped silently:
`skipped_invalid_json` counts a line that is not one JSON object (an empty line included);
`skipped_missing_field` counts an object that lacks a required field or has one of the wrong type,
an unsupported `schema`, or a `source` or `session_id` that differs from the file name's.

| Column                  | Meaning                                                            |
| ----------------------- | ------------------------------------------------------------------ |
| `run_id`                | the ingest run                                                     |
| `ingest_ms`             | the time the run started                                           |
| `file`                  | the event file name                                                |
| `session_id`            | the file's session                                                 |
| `source`                | the file's source                                                  |
| `lines_read`            | complete lines read in this run                                    |
| `events_new`            | lines stored as new events                                         |
| `events_duplicate`      | lines whose `event_id` was already stored                          |
| `skipped_invalid_json`  | lines skipped as not valid JSON objects                            |
| `skipped_missing_field` | lines skipped for a missing or invalid envelope field              |
| `skipped_total`         | `skipped_invalid_json` plus `skipped_missing_field`                |
| `events_without_call`   | new events stored without a `tool_use_id`, so belonging to no call |

## `session_sources`

One row per session that has at least one event file: which sources have files for it. A session
with `has_ceta = 1` and `has_tracker = 0` had CETA activity and no tracker recording.

| Column           | Meaning                                            |
| ---------------- | -------------------------------------------------- |
| `session_id`     | the session                                        |
| `sources`        | comma-separated sorted list of sources with a file |
| `has_tracker`    | `1` when the session has a `tracker` file          |
| `has_ceta`       | `1` when the session has a `ceta` file             |
| `tracker_events` | stored events from the `tracker` source            |
| `ceta_events`    | stored events from the `ceta` source               |
| `first_ts`       | the earliest stored event time of the session      |
| `last_ts`        | the latest stored event time of the session        |

## `event_span`

The earliest and latest stored event time with the numbers of sessions, calls and events: one row
with `scope` `all`, and one per source with `scope` equal to the source. For a source, `calls`
counts the distinct calls that have an event from it. With no events, the `all` row holds `NULL`
times and zero counts.

| Column        | Meaning                          |
| ------------- | -------------------------------- |
| `scope`       | `all` or a source name           |
| `earliest_ts` | the earliest event `ts` in scope |
| `latest_ts`   | the latest event `ts` in scope   |
| `sessions`    | distinct sessions in scope       |
| `calls`       | calls in scope                   |
| `events`      | stored events in scope           |
