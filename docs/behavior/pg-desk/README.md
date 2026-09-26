# pg-desk — behavior docs

`pg-desk` answers "what is on my desk": it gathers PR and issue facts through `pg-connector`,
derives meaning from them, and serves the operator a triage view and an opener, while signaling
agent work through beads. It is generic and config-driven, lives at `packages/pg-desk` in this
repo, and MUST contain no organization identifiers — the ZR repo supplies configuration only,
exactly as it does for `pg-pr` and `pr-pool` today.

**Composition rule (D10).** `pg-desk` MUST compose only `pg-connector` verbs and the configured
browser. It MUST NOT exec `gh`, `bd`, `pjira`, or any other system client, directly or
transitively. A chokepoint test enforces this (carried by this docket's second packet). The beads
backend it targets is always a config key, never a literal, so the same genericity extends to
which agent tracker `pg-desk` signals through.

This set describes intended behavior only — no implementation code, no data-structure or
call-site detail below that floor. It is this docket's (Phase 9 of the pg-desk and connector
retirement program) own **first packet**, landing ahead of any `pg-desk` code, per this repo's
rule that behavior docs are the source of truth for the pg-pr/pg-router system and per this
design's own packet-shaping instruction. Every later packet in this docket implements against
this set and MUST NOT contradict it without a docket design amendment.

## Scope (Phase 9, widened by Phase 10)

In scope: `store` and its schema, `gather`, `interpret`, `sync` (all three modes — added by Phase
10, docket pg2-2j5ac.34), the `run` pipeline, `sweep` (bead `pg2-gznpe`'s bulk backfill over every
stored entity), `serve` (behind the soak option only), `open`, `hide`/`unhide`/`wip`,
`feedback list`/`feedback set`, `show`, `status`, `doctor`, `heartbeat`/`heartbeat-item`, and
`import-pg-pr-annotations`.

**Out of scope for the whole set, named once here so no individual doc needs to repeat it as a
qualifier every time:**

- **`run thread`** is implemented as of Phase 13 (docket `pg2-2j5ac.40`, Slack half) — see
  [`run-thread.md`](run-thread.md). `run issue` is implemented for BOTH backends: the beads backend
  (Phase 10) and Jira (Phase 13) — see [`run-issue.md`](run-issue.md). The Slack incident signal is
  explicitly NOT carried (deferred with `pg2-jpfw.5`).
- **The cross-reference step.** Ticket keys found in a PR's own branch/title/body (the Jira half),
  and PR permalinks/ticket keys found in a Slack thread's own text (the Slack half), and the `xref`
  table rows they produce, are both implemented as of Phase 13 — see [`gather.md`](gather.md) and
  [`run-thread.md`](run-thread.md).
- **Layered urgency.** The Jira half (priority and incident signals, read from a PR's own
  cross-referenced Jira issue) is implemented as of Phase 13 — see [`interpret.md`](interpret.md).
  The project-health half stays deferred.
- **Multi-repository support.** `repos[]` supports exactly one repository; every command below
  resolves against that one configured (or cwd-resolved) repository.

Each doc below restates only the exclusions that apply to its own command family, as a pointer
back here — not the full list.

## The docs

| Doc                                                          | Covers                                                                                                 |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------ |
| [`store-schema.md`](store-schema.md)                         | The SQLite store, its version ladder, and its six tables                                               |
| [`gather.md`](gather.md)                                     | Pipeline stage 1 — facts, only through `pg-connector`                                                  |
| [`interpret.md`](interpret.md)                               | Pipeline stage 2 — pure, deterministic derivation                                                      |
| [`sync.md`](sync.md)                                         | Pipeline stage 3 — agent-visible bead writes (all modes)                                               |
| [`pipeline-run.md`](pipeline-run.md)                         | `pg-desk run` and the three-stage pipeline shape                                                       |
| [`run-issue.md`](run-issue.md)                               | `pg-desk run issue` — the beads-backend bead -> PR resolution and interpret-only re-run                |
| [`run-thread.md`](run-thread.md)                             | `pg-desk run thread` — the Slack half: permalink/ticket-key cross-references and interpret-only re-run |
| [`serve.md`](serve.md)                                       | `pg-desk serve`, soak-port only this phase                                                             |
| [`open.md`](open.md)                                         | `pg-desk open`                                                                                         |
| [`hide-unhide-wip.md`](hide-unhide-wip.md)                   | `pg-desk hide`/`unhide`/`wip`                                                                          |
| [`feedback.md`](feedback.md)                                 | `pg-desk feedback list`/`feedback set`                                                                 |
| [`operator-commands.md`](operator-commands.md)               | `show`, `status`, `sweep`, `doctor`, `heartbeat`, `heartbeat-item`                                     |
| [`import-pg-pr-annotations.md`](import-pg-pr-annotations.md) | The one-shot pg-pr cutover tool                                                                        |

## Telemetry declaration (D24)

Every component this docket introduces MUST state what it emits over OpenTelemetry/Prometheus and
what it logs, so the observability review (`pg2-7kizi`) can decide what the dashboard can show
before Phase 12 deletes the Ops board. The short version, detailed per doc above:

- **OpenTelemetry:** `serve`'s WARN/ERROR-level operational log lines export over OTLP as
  `{service_name="pg-desk-serve"}` (`internal/telemetry`, mirroring `pg-pr`'s own
  `Init`/`NewSlogHandler`/`Fanout` pattern) — resolved by the observability review (`pg2-7kizi`)
  and implemented against this doc. Every other command in this set still emits nothing over
  OpenTelemetry.
- **Prometheus:** only `serve` exposes anything, now a real catalog on the same `/metrics` route
  (`pg_desk_liveness`, `pg_desk_dashboard_age_seconds`, `pg_desk_dashboard_stale`,
  `pg_desk_dropped`, `pg_desk_sync_errors_total`), replacing the earlier minimal
  scrape-keeps-green stub — see [`serve.md`](serve.md)'s "Telemetry and logs".
- **Logs:** `run` logs structured JSON to stderr (pg-router captures it) and adds a three-stage
  timeline under `--verbose`, folding sync's own outcome (whether it ran, and any `sync_error`)
  into that same line rather than a separate stream (see [`sync.md`](sync.md)); `run issue`'s and
  `run thread`'s own interpret-only re-runs fold into that same structured line too, logged
  against the resolved PR's own entity id — once per linked PR, which can re-interpret more than
  one on either path (see [`run-issue.md`](run-issue.md) and [`run-thread.md`](run-thread.md)); a
  degraded Jira `issue show` during gather's own ticket-key scan is named `issue show`, and a
  degraded linked-threads read (gather's own eighth input) is named `linked threads`, in that same
  line (see [`gather.md`](gather.md)); `serve` logs to the path its launchd module configures.
  Every other command logs only ordinary CLI report/error text, with no structured-JSON contract
  of its own.

## Status

Phase 9's `store`/`gather`/`interpret`/`run` pipeline and CLI surface, Phase 10's `sync` (all
three modes) and beads-backend `run issue` bead -> PR resolution, and Phase 13's Jira half
(docket `pg2-2j5ac.40`: gather's ticket-key scan and cross-reference writes, `run issue`'s
Jira-id reverse resolution, and layered urgency's Jira signal) and Slack half (gather's
linked-threads eighth input, and `run thread`'s permalink/ticket-key cross-references and
interpret-only re-run) are implemented at `packages/pg-desk`. `daemon.enable`, the Slack incident
signal (deferred with `pg2-jpfw.5`), and the ZR-side query/role wiring are this docket's remaining
packets.
