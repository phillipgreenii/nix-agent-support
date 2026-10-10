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
`feedback list`/`feedback set`, `show`, `links` (a read-only batch lookup of cross-reference
links, see [`links.md`](links.md), which also holds the `<type> link add|remove` external-link
verbs), `<type> consumer list|forget` (see [`consumer.md`](consumer.md)), `<type> changes` (see [`changes.md`](changes.md)), the focus configuration keys (`focus.*`, `bead_id_pattern`, see [`config.md`](config.md)), `<type> refresh` (see [`refresh.md`](refresh.md)), `pg-desk-shadow` (the one-off shadow-compare measurement tool, see [`shadow-compare.md`](shadow-compare.md)), `attention list`/`attention explain` and the `pg-desk-attention` plugin (see [`attention.md`](attention.md)), `freshness` (the per-source data age, see [`freshness.md`](freshness.md)), the typed `<type> show` composite view (see [`show.md`](show.md)), `status`, `doctor`, `heartbeat`/`heartbeat-item`, and
`import-pg-pr-annotations`, and the annotation verbs `<type> annotate|suppress|unsuppress`,
`pr force-review`, `pr head-check` (see [`operator-commands.md`](operator-commands.md)) and the typed `hide`/`unhide`/`wip`/`pr feedback` forms (see
[`annotate.md`](annotate.md)).

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

| Doc                                                          | Covers                                                                                                                             |
| ------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| [`store-schema.md`](store-schema.md)                         | The SQLite store, its version ladder, schema cutover and tables                                                                    |
| [`gather.md`](gather.md)                                     | Pipeline stage 1 — facts, only through `pg-connector`                                                                              |
| [`interpret.md`](interpret.md)                               | Pipeline stage 2 — pure, deterministic derivation                                                                                  |
| [`sync.md`](sync.md)                                         | Pipeline stage 3 — agent-visible bead writes (all modes)                                                                           |
| [`pipeline-run.md`](pipeline-run.md)                         | `pg-desk run` and the three-stage pipeline shape                                                                                   |
| [`run-issue.md`](run-issue.md)                               | `pg-desk run issue` — the beads-backend bead -> PR resolution and interpret-only re-run                                            |
| [`run-thread.md`](run-thread.md)                             | `pg-desk run thread` — the Slack half: permalink/ticket-key cross-references and interpret-only re-run                             |
| [`serve.md`](serve.md)                                       | `pg-desk serve`, soak-port only this phase                                                                                         |
| [`open.md`](open.md)                                         | `pg-desk open` and the typed `pg-desk <type> open`                                                                                 |
| [`show.md`](show.md)                                         | `pg-desk <type> show` — typed composite view (`pg-desk.view/v1`), links with origins, `--refresh`                                  |
| [`hide-unhide-wip.md`](hide-unhide-wip.md)                   | `pg-desk hide`/`unhide`/`wip` and their typed `<type>` forms                                                                       |
| [`feedback.md`](feedback.md)                                 | `pg-desk feedback list`/`feedback set` and `pr feedback list`/`set`                                                                |
| [`annotate.md`](annotate.md)                                 | `pg-desk <type> annotate`/`suppress`/`unsuppress` and `pr force-review` — the key/value annotation verbs                           |
| [`history.md`](history.md)                                   | `pg-desk pr`/`issue`/`thread` groups and `<type> history`                                                                          |
| [`links.md`](links.md)                                       | `pg-desk links` — read-only batch lookup of PR/issue/thread/build cross-reference links; `<type> link add`/`remove` external links |
| [`consumer.md`](consumer.md)                                 | `pg-desk <type> consumer list`/`forget` — change-log consumer lifecycle                                                            |
| [`changes.md`](changes.md)                                   | `pg-desk <type> changes` — list-and-diff change feed (the daemon-backed backend owns `pr` and `ci`), envelope, cursor, exit codes  |
| [`refresh.md`](refresh.md)                                   | `pg-desk <type> refresh` — targeted single-entity hydration, change kind, exit codes                                               |
| [`shadow-compare.md`](shadow-compare.md)                     | `pg-desk-shadow` — the side-by-side comparison of the fingerprint change detection against the live change flow (phase A)          |
| [`attention.md`](attention.md)                               | `pg-desk attention list`/`explain` and the `pg-desk-attention` plugin — the read-time attention evaluator, its rules, grouping     |
| [`freshness.md`](freshness.md)                               | `pg-desk freshness`, the dashboard `sources[]` and `pg_desk_source_age_seconds` — per-source data age, the staleness threshold     |
| [`operator-commands.md`](operator-commands.md)               | `show`, `status`, `sweep`, `doctor`, `heartbeat`, `heartbeat-item`                                                                 |
| [`import-pg-pr-annotations.md`](import-pg-pr-annotations.md) | The one-shot pg-pr cutover tool                                                                                                    |

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
  `pg_desk_dropped`, `pg_desk_sync_errors_total`, and `pg_desk_source_age_seconds{source}`, the
  per-source data age, see [`freshness.md`](freshness.md), plus the `pg_desk_reconcile_*` gauges for
  the last `reconcile` run), replacing the earlier minimal
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

## Realization gaps

This set's **realization-gap register**: intended behavior this set's
implementation has not yet built, one row per gap. The daemon-backed GitHub backend that the
changes, consumer, freshness and refresh docs describe does not exist yet, so the rows below record
where pg-desk's own flow still stands.

| Element          | Intended                                                                                                                                                                                                                                                                                                                  | Where the implementation stands                                                                                                                                                                                                                                                                                                                                                   | Tracked by                 |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| `INV-CHANGES-13` | for `pr` and `ci`, the daemon-backed backend owns change detection, the baseline, the age sweep and the per-PR hydration, and pg-desk's own PR change flow is retired; `changes.md` points at the behavior-docs set for the GitHub connector backend, which describes what the backend does instead                       | pg-desk still ships its own PR change flow (baseline, two-tier sweep, hydration budget, change-log tables) and serves `pr` through it; no daemon-backed backend exists; the behavior-docs set for the GitHub connector backend is a sibling deliverable that does not exist yet, so `changes.md` names it by its role and carries no link; retiring the flow is the cleanup phase | `pg2-z5fax` (later phases) |
| `INV-CHANGES-14` | a CI change behind an unchanged rollup re-triggers categorization of the PR, once per change; the connector's CI-only `ci changes` feed is planned and is not used by pg-desk                                                                                                                                             | a PR's categorization re-runs today only when the live listing diff sees a rollup or head change, so a job-level change behind an unchanged rollup is not delivered; the PR feed's `ci_changed` kind and the `ci changes` verb do not exist yet                                                                                                                                   | `pg2-z5fax` (later phases) |
| `INV-CHANGES-15` | a desk run that did not complete is recovered by the backend's redelivery of unacknowledged feed rows and by the periodic reconcile lanes, with no local reconcile tier in pg-desk for `pr`                                                                                                                               | recovery today is the local reconcile tier of pg-desk's own flow; the backend's acknowledged feed does not exist yet                                                                                                                                                                                                                                                              | `pg2-z5fax` (later phases) |
| `INV-CONSUMER-4` | for `pr` and `ci`, the backend that owns the changes holds the consumers' cursors and pg-desk holds none                                                                                                                                                                                                                  | pg-desk's consumer table and the `consumer list` and `consumer forget` verbs still serve `pr`; the backend's cursors do not exist yet                                                                                                                                                                                                                                             | `pg2-z5fax` (later phases) |
| `INV-REFRESH-4`  | `refresh` is the only verb that classifies a change, and it does not depend on the backend's change feed                                                                                                                                                                                                                  | the list-and-diff `changes` verb is still a second caller of the classifier for `pr`; whether the classifier retires together with `refresh` is left to the cleanup phase                                                                                                                                                                                                         | `pg2-z5fax` (later phases) |
| `INV-FRESH-7`    | for a backend that owns its changes, the ledger times pg-desk reads come from the backend's own record of its last whole-query origin answer and last error                                                                                                                                                               | the umbrella stamps the ledger from its own listing outcomes; the backend's per-query freshness record does not exist yet                                                                                                                                                                                                                                                         | `pg2-z5fax` (later phases) |
| `INV-GATHER-1`   | a gather's `pr` reads do not pass `--fresh`, because the daemon-backed backend keeps them fresh; the `issue` reads and the head check keep it                                                                                                                                                                             | gather's `pr` reads still pass `--fresh` today, because they are served through the umbrella's own cache; the daemon-backed backend does not exist yet and the flag is removed at the cutover                                                                                                                                                                                     | `pg2-z5fax` (later phases) |
| `INV-SHADOW-11`  | the shadow run for `pr` and `ci` starts the daemon as `pg-desk-shadow`'s own child, with its own socket, state directory and configuration and a default cap of 1,500 GraphQL points per hour the operator MAY raise; the collector reads the daemon's change feed and compares it with the live events (`INV-SHADOW-12`) | `pg-desk-shadow` is still the phase A harness, which runs the fingerprint change detection against the live flow; the daemon does not exist yet and the harness does not start one                                                                                                                                                                                                | `pg2-z5fax` (later phases) |
| `INV-SHADOW-13`  | the shadow report states each of the four default cutover criteria as met or not met, and an operator override with unmet criteria is recorded on the cutover bead with one follow-up bead per unmet criterion (`INV-SHADOW-14`)                                                                                          | the phase A report states no cutover criteria; the daemon shadow run's criteria, report and cutover bead are not built                                                                                                                                                                                                                                                            | `pg2-z5fax` (later phases) |
