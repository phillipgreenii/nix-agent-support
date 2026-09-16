# pg-desk — feedback list, feedback set

`pg-desk feedback list <pr>` prints the PR's comments and threads with their current
dispositions from the store. `pg-desk feedback set <pr> <comment-id> --disposition
open|will-fix|wont-fix|no-action [--actor A]` records a disposition override in the `annotation`
table, attributed to the caller (`--actor`, defaulting to the configured `actor`).

A disposition recorded through `feedback set` MUST be honored by interpret's disposition rule set
over its own verdict on every later run (see [`interpret.md`](interpret.md)) — it survives a
re-run and wins. This is the write path the rewritten process-feedback workflow uses in place of
the retired `pg-pr feedback disposition`.

## Exit codes, telemetry, and logs

`0` on success; `1` when `<pr>` or `<comment-id>` does not resolve, `--disposition` is not one of
the four recognized values, or the store cannot be written. No other exit code is used by these
commands in Phase 9.

These commands emit nothing over OpenTelemetry or Prometheus (D24) and carry no structured-JSON
logging contract of their own — only ordinary CLI error text on failure.

## Out of scope (Phase 9)

`feedback list`/`feedback set` operate against the single repository this phase supports. A
disposition arising from a Jira- or Slack-linked thread is out of scope until Phase 13's
cross-reference step exists.
