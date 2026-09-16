# pg-desk — import-pg-pr-annotations

`pg-desk import-pg-pr-annotations --store <pg-pr store.db>` is a one-shot cutover tool that
copies the `pull_request` table's `user_hidden`, `user_hidden_reason`, and `wip` columns from a
`pg-pr` `store.db` into `pg-desk`'s own `annotation` table. It MUST be idempotent — running it a
second time against the same source MUST NOT change the result of the first run.

It MUST run before the first sweep, so hidden PRs do not flash onto the board before their hidden
state is known. It is the first step of the Phase 11 cutover flip, and because it is idempotent
it MAY also run earlier — at this phase's own checkpoint, or during the Phase 10 soak —
specifically so the soak board hides what `pg-pr` already hides.

## Exit codes, telemetry, and logs

`0` on success, including a no-op second run; `1` when the given `--store` path cannot be read as
a `pg-pr` store, or `pg-desk`'s own store cannot be written.

It emits nothing over OpenTelemetry or Prometheus (D24) — a one-shot CLI tool. It logs only
ordinary CLI report text (rows copied, rows already matching); it carries no structured-JSON
logging contract of its own, since it is not part of the `run` pipeline (see
[`pipeline-run.md`](pipeline-run.md)).

## Out of scope (Phase 9)

Only the three named columns migrate. Every other `pg-pr` table slated for retirement is dropped
without migration — revision history, approvals, and per-comment feedback state are accepted
losses, re-derived by gather and interpret instead of carried over.
