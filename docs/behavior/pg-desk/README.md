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

## Scope (Phase 9)

In scope: `store` and its schema, `gather`, `interpret`, the `run` pipeline, `serve` (behind the
soak option only), `open`, `hide`/`unhide`/`wip`, `feedback list`/`feedback set`, `show`,
`status`, `doctor`, `heartbeat`/`heartbeat-item`, and `import-pg-pr-annotations`.

**Out of scope for the whole set, named once here so no individual doc needs to repeat it as a
qualifier every time:**

- **Sync** (the stage that turns an interpretation into an agent-facing bead write) is out of
  scope beyond the `ledger` table's own schema in the store's version ladder. The full
  implementation, all three modes (`off`/`plan`/`apply`), is Phase 10.
- **The cross-reference step** (ticket keys and URLs found in PR/Jira/thread text, and the `xref`
  table it populates) is Phase 13. The table exists in this phase's schema ladder and stays
  unpopulated until then.
- **Layered urgency** (project health, Jira priority) is Phase 13; this phase runs base urgency
  only (labels, keywords, checks rollup, bugfix commits).
- **`run issue` for the beads backend** is Phase 10; **`run issue` for Jira and `run thread`** are
  Phase 13, together with the cross-reference step that would give either kind of event a linked
  PR to re-interpret.
- **Multi-repository support.** `repos[]` supports exactly one repository in this phase; every
  command below resolves against that one configured (or cwd-resolved) repository.

Each doc below restates only the exclusions that apply to its own command family, as a pointer
back here — not the full list.

## The docs

| Doc                                                          | Covers                                                    |
| ------------------------------------------------------------ | --------------------------------------------------------- |
| [`store-schema.md`](store-schema.md)                         | The SQLite store, its version ladder, and its six tables  |
| [`gather.md`](gather.md)                                     | Pipeline stage 1 — facts, only through `pg-connector`     |
| [`interpret.md`](interpret.md)                               | Pipeline stage 2 — pure, deterministic derivation         |
| [`pipeline-run.md`](pipeline-run.md)                         | `pg-desk run` and the three-stage pipeline shape          |
| [`serve.md`](serve.md)                                       | `pg-desk serve`, soak-port only this phase                |
| [`open.md`](open.md)                                         | `pg-desk open`                                            |
| [`hide-unhide-wip.md`](hide-unhide-wip.md)                   | `pg-desk hide`/`unhide`/`wip`                             |
| [`feedback.md`](feedback.md)                                 | `pg-desk feedback list`/`feedback set`                    |
| [`operator-commands.md`](operator-commands.md)               | `show`, `status`, `doctor`, `heartbeat`, `heartbeat-item` |
| [`import-pg-pr-annotations.md`](import-pg-pr-annotations.md) | The one-shot pg-pr cutover tool                           |

## Telemetry declaration (D24)

Every component this docket introduces MUST state what it emits over OpenTelemetry/Prometheus and
what it logs, so the observability review (`pg2-7kizi`) can decide what the dashboard can show
before Phase 12 deletes the Ops board. The short version, detailed per doc above:

- **OpenTelemetry:** nothing, anywhere in this set, in Phase 9. Export is a later observability
  item, alongside pr-pool's own metrics sink.
- **Prometheus:** only `serve` exposes anything — a minimal `/metrics` whose sole purpose is
  keeping the existing scrape target from going red, not a dashboard-grade surface.
- **Logs:** `run` logs structured JSON to stderr (pr-pool captures it) and adds a three-stage
  timeline under `--verbose`; `serve` logs to the path its launchd module configures. Every other
  command logs only ordinary CLI report/error text, with no structured-JSON contract of its own.

## Status

No code exists yet at `packages/pg-desk` as of this packet — that lands starting with this
docket's second packet. Every behavior this set describes is, by construction, ahead of its own
implementation until the packet that builds it lands; that is expected, not a defect.
