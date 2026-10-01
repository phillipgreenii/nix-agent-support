# Runbook: dispatched-session failure signatures (first seen, how often)

Bead: `pg2-8rgnl`. Contract: `INV-CCH-9`'s "Dispatch result" bullet in
`packages/pg-router-ccpool-handler/docs/behavior/invariants.md`.

## What is recorded

When a session dispatched by `pg-router-ccpool-handler` fails, exits without completing its bead,
or is hard-stopped by the budget watchdog, the handler appends one `dispatch_result` record to its
event log, `<stateDir>/events.jsonl` (the state directory is `originProbe.stateDir`, else the
handler's default state directory). Each record carries `time`, `failure_signature`,
`signature_evidence`, `role`, `pool`, `bead`, and `session`. A budget hard stop also carries
`limit`, `used`, and `cap`.

`failure_signature` is one of `git-auth`, `git-network`, `mount-or-path`, `budget`, `index-lock`,
or `unknown`. `signature_evidence` is redacted and at most 300 characters. There is no metric and
no separate store: the event log is the record.

```mermaid
flowchart LR
    S["session fails or is hard-stopped"] --> C["classify last 64 KB of transcript\nBEFORE the session is closed"]
    C --> E["dispatch_result event\n(failure_signature, evidence, role, pool, bead, session)"]
    E --> J["jq recipes below"]
```

## Recipes

Set `LOG` to the event log path. The recipes were verified against the fixture
`packages/pg-router-ccpool-handler/internal/executor/testdata/dispatch-results.jsonl`, which a Go
test (`TestRunbookRecipes`) runs when `jq` is on `PATH`.

First occurrence of signature `git-auth` (change the signature to look at another):

```bash
jq -s 'map(select(.kind=="dispatch_result" and .failure_signature=="git-auth"))
       | min_by(.time) | {time, role, pool, bead}' "$LOG"
```

Counts per day, per signature:

```bash
jq -r 'select(.kind=="dispatch_result") | "\(.time[0:10]) \(.failure_signature)"' "$LOG" \
  | sort | uniq -c
```

Counts per signature, role, and pool:

```bash
jq -r 'select(.kind=="dispatch_result")
       | "\(.failure_signature) \(.role) \(.pool)"' "$LOG" | sort | uniq -c | sort -rn
```

Fixture output for the first recipe is `2026-09-23T14:01:00.000000000Z` / `worker` / `default` /
`zr-1`; the second prints `2 2026-09-23 git-auth`, `1 2026-09-24 budget`, `1 2026-09-24 unknown`.

## Reading the result

- An `unknown` signature with empty evidence means the transcript path was empty or unreadable, or
  the transcript text matched no rule (including a transcript that shows success despite a
  non-zero exit). It is not evidence of success or of a specific cause.
- Records written before this change do not exist; "first seen" is the first record in the log,
  not the first failure ever.
- If a metric is wanted later, file a separate bead deciding the wire transport; the handler has
  no OTel emitter.
