# pg-pr Dashboard Design

**Status**: Draft
**Date**: 2026-05-26
**Deciders**: phillipg

## Context

`pg-pr` is a daemon that tracks pull requests across configured GitHub repos
on behalf of a single user. It already syncs both the user's own PRs and PRs
authored by configured team members, resolves linked JIRA issues, manages
`merge-request` beads in `bd`, exposes Prometheus aggregate metrics on
`/metrics`, and emits OTel traces.

What `pg-pr` does not expose today is a per-PR snapshot suitable for a
human-facing dashboard. The current Prometheus surface is aggregate
counters/histograms; useful for operational health but not for "what is on my
plate right now". The user wants a Grafana dashboard with two tables — own
PRs and teammate PRs — each row carrying enough state to triage at a glance,
including derived signals like "waiting on me" computed from the bd
dependency graph.

The local observability stack (`phillipgreenii-nix-support-apps`) already
runs Prometheus, Loki, Tempo, and Grafana on 127.0.0.1. The dashboard must
slot into that stack with no remote dependencies.

## Decision

Extend the `pg-pr sync --daemon` HTTP server with a new JSON handler
`GET /api/v1/dashboard` that serves an in-memory snapshot maintained by the
sync loop. Render the dashboard in Grafana via the Infinity datasource
plugin, with the dashboard JSON and Infinity plugin install both provisioned
through the existing `otel-stack-tools` Nix module.

### Data substrate

A new JSON HTTP endpoint was chosen over the two alternatives:

- **Per-PR Prometheus gauges with rich labels** — rejected. Encoding titles,
  URLs, and nested JIRA/bead lists into Prometheus labels is ergonomically
  poor (PromQL joins are awkward, label churn on title edits creates series
  noise) and conceptually mismatched: Prometheus is for time-series, the
  dashboard wants a current snapshot.
- **SQLite state file + Grafana SQL datasource** — rejected as
  over-engineered for v1. Introduces a durable store the daemon does not
  have today and pays for capabilities (joins, history) that the current
  feature set does not need.

The JSON endpoint reuses the daemon HTTP server already serving `/metrics`,
costs one handler to add, and serializes nested lists naturally.

### Schema

```json
{
  "generated_at": "2026-05-26T14:03:11Z",
  "sync_interval_seconds": 60,
  "mine": [
    {
      "repo": "owner/repo",
      "number": 123,
      "title": "Add foo to bar",
      "url": "https://github.com/owner/repo/pull/123",
      "draft": false,
      "ci_status": "success",
      "human_approved": true,
      "agent_approved": false,
      "waiting_on_me": true,
      "jira": [
        {
          "id": "FOO-42",
          "title": "Refactor bar",
          "state": "In Progress",
          "url": "https://.../browse/FOO-42"
        }
      ],
      "beads": [
        {
          "id": "pgp-17",
          "title": "merge-request: owner/repo#123",
          "status": "open",
          "labels": ["merge-request"],
          "url": "bd://pgp-17"
        },
        {
          "id": "pgp-18",
          "title": "Decide rename strategy",
          "status": "open",
          "labels": ["human"],
          "url": "bd://pgp-18"
        }
      ]
    }
  ],
  "team": [
    {
      "repo": "owner/repo",
      "number": 130,
      "title": "Bump deps",
      "owner": "alice",
      "url": "https://github.com/owner/repo/pull/130",
      "ci_status": "pending",
      "human_approved": false,
      "agent_approved": true,
      "lines_changed": 412,
      "files_changed": 7,
      "jira": [
        {
          "id": "FOO-50",
          "title": "Quarterly bump",
          "state": "Open",
          "url": "https://.../browse/FOO-50"
        }
      ]
    }
  ]
}
```

Conventions:

- `mine` and `team` are disjoint. PR with `author == self` → `mine`. PR with
  `author ∈ TeamMembers ∧ !draft` → `team`. Draft teammate PRs are excluded.
- `ci_status` enum: `success | failure | pending | none`.
- `waiting_on_me` and `beads` appear only on `mine` rows.
- `lines_changed` and `files_changed` appear only on `team` rows.
- `owner` appears only on `team` rows (own PRs implicitly owned by self).
- Empty `jira` / `beads` arrays serialize as `[]`, never `null`.
- The handler serves the last good snapshot even when the most recent sync
  errored. `generated_at` reflects when the snapshot was actually produced;
  staleness is computed by the consumer. The handler returns `503` only
  when the daemon has not yet populated a first snapshot.

### Derived fields

| Field            | Definition                                                                                                                                                                                                                                                                                        |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ci_status`      | Rollup of required GH status checks: `success` / `failure` / `pending` / `none`.                                                                                                                                                                                                                  |
| `human_approved` | `∃ review` with `state=APPROVED` where `review.author ∉ agent_registry`.                                                                                                                                                                                                                          |
| `agent_approved` | `(∃ review APPROVED where author ∈ agent_registry)` ∨ `(∃ issue-comment or review-summary comment by an agent_registry author whose body matches that agent's approval_regex)`. Inline diff comments are excluded — agents post approval verdicts as top-level PR comments, not as line comments. |
| `waiting_on_me`  | Walk recursive dependencies of the merge-request bead. Among non-closed beads in that set, **all** carry the `human` label. Empty non-closed set → `false`.                                                                                                                                       |

The `bd human` flag is implemented as the `human` label in beads, confirmed
by inspecting `bd human --help` and `bd label list-all`.

### Agent registry

A new config block in pg-pr config (YAML) declares known agent accounts and
their approval-comment formats:

```yaml
agents:
  - login: claude[bot]
    approval_regex: '(?im)^verdict:\s*approve'
  - login: cursor-bot
    approval_regex: "(?im)approved by cursor"
```

`login` is used both to classify a GH approver as agent vs human and to
identify whose comment bodies are eligible for approval-mining.
`approval_regex` is applied only to comments authored by that agent's
`login`. The registry hot-reloads on next sync tick when the config file
changes.

### Architecture

```
┌─────────────────────────────────────────────────────┐
│ pg-pr sync --daemon                                 │
│                                                     │
│  sync loop (every --interval)                       │
│     │                                               │
│     ├─► enumerate self+team PRs       (existing)    │
│     ├─► fetch diff stats              (new)         │
│     ├─► fetch reviews + comments      (extend)      │
│     ├─► walk bd deps for each PR      (new)         │
│     ├─► resolve JIRA via jira-link beads (existing) │
│     └─► build Snapshot                              │
│                  │                                  │
│                  ▼                                  │
│         snapshot.Store (in-memory, RWMutex)         │
│                  │                                  │
│  HTTP server (existing scrape addr)                 │
│     ├─ /metrics            (existing)               │
│     └─ /api/v1/dashboard   (new: serves Snapshot)   │
└──────────────────────┬──────────────────────────────┘
                       │ JSON (Infinity plugin query)
                       ▼
              Grafana (localhost)
              ├─ Panel: My PRs   (Infinity Table)
              └─ Panel: Team PRs (Infinity Table)
```

New packages and files inside the pg-pr Go module:

- `internal/snapshot/` — `Snapshot` struct, `Store{Get(),Set()}`, JSON
  marshaling. RWMutex-guarded.
- `internal/snapshot/builder.go` — assembles a `Snapshot` from existing sync
  outputs plus the new fetches.
- `internal/agentregistry/` — loads agent registry config; exposes
  `IsAgent(login string) bool` and
  `MatchApproval(login, body string) bool`.
- `internal/httpapi/dashboard.go` — handler bound under the existing daemon
  HTTP server.
- `internal/sync/sync.go` — extended so each tick populates
  `snapshot.Store`.
- `cmd/pg-pr/sync.go` — mounts `/api/v1/dashboard` on the same listener as
  `/metrics`.

Binding and auth: reuses the existing `--scrape-addr` listener
(127.0.0.1-only by default). No auth — same trust model as `/metrics`.

Freshness signal: existing
`pg_pr_last_sync_success_timestamp_seconds` already covers staleness; the
dashboard derives age client-side. A new gauge
`pg_pr_snapshot_present` (0/1) is added so Grafana can distinguish
"never populated" from "stale".

### Grafana side

One dashboard, `pg-pr / My Work`, with three rows:

1. **Header stat panels** — snapshot age, count of open `mine` PRs, count of
   `mine` PRs where `waiting_on_me=true` (red threshold > 0).
2. **My PRs** (Infinity Table) — rows path `$.mine[*]`. Columns: `#`,
   `Title`, `Draft`, `CI`, `Human ✓`, `Agent ✓`, `Waiting`, `JIRA`,
   `Beads`. `#` is a markdown link to `url`. `JIRA` and `Beads` are
   comma-joined markdown links rendered with JSONata expressions like
   `$join(jira.id, ', ')`, with per-row data links templated from the
   nested array.
3. **Team PRs** (Infinity Table) — rows path `$.team[*]`. Columns: `#`,
   `Title`, `Owner`, `CI`, `Human ✓`, `Agent ✓`, `Files`, `Lines`,
   `JIRA`. Default sort: `ci_status=failure` first, then `lines_changed`
   descending.

Provisioning lives in the `otel-stack-tools` Nix module:

- New file
  `phillipgreenii-nix-support-apps/packages/otel-stack-tools/grafana/dashboards/pg-pr.json`.
- Infinity plugin added to Grafana's `GF_INSTALL_PLUGINS`.
- New provisioned datasource `pg-pr-infinity` pointing at
  `http://127.0.0.1:<scrape-port>`.

## Consequences

### Positive

- Single new HTTP handler — no new persistence layer, no new daemon
  process, no new external service.
- Nested JIRA and bead lists serialize naturally as JSON and render
  naturally in Infinity Table panels.
- Existing OTel and Prometheus surfaces are untouched; aggregate metrics
  remain the source of truth for operational health.
- Snapshot model means Grafana load on the daemon is constant, regardless
  of poll frequency.
- Agent registry is declarative config — adding a new bot is a config edit,
  not a code change (for the login case at least).

### Negative

- Snapshot only. No history, no time-series for "waiting on me over time".
  Acceptable for v1; revisit if needed.
- Recursive bd dependency walk is O(beads-per-PR × open-PR-count) per
  sync tick. Likely fine in practice; benchmarked in the implementation
  plan and cached on bead-version hash if needed.
- Comment-mining for agent approval adds a parser per agent format. First
  agent (claude) is concrete; future agents require a registry entry plus
  potentially regex tuning.
- Infinity plugin's JSONata for nested columns is non-trivial; the
  implementation plan must include a worked example panel.

### Neutral

- The endpoint is intentionally localhost-only and unauthenticated, matching
  the existing `/metrics` posture. Anyone with shell access to the host
  can read PR titles and reviewer logins; this is acceptable for a
  single-user local tool.
- JIRA freshness is bounded by the existing `jira-link` feedback bead cache
  cadence, not by sync-tick cadence.

## Alternatives Considered

### Per-PR Prometheus gauges with rich labels

Rejected. Prometheus optimizes for low-cardinality numeric time-series.
Encoding a PR title or URL as a label string forces every title edit to
churn the active series set, joins between a PR row and its JIRA list
require awkward `group_right` constructions, and the result is harder to
read in Grafana than a JSON-backed table panel.

### SQLite state file + Grafana SQL datasource

Rejected as premature. Adds a durable on-disk store that the daemon does
not have today, and pays for real joins and history that v1 does not need.
A future migration from the JSON snapshot to a SQL-backed model is
mechanical if requirements evolve.

### Compute snapshot on each Grafana request

Rejected. Would hammer GitHub, JIRA, and `bd` on every dashboard refresh,
defeat the existing sync loop, and make Grafana refresh interval a
correctness concern rather than a UX one.

## Related Decisions

- See also: `phillipgreenii-nix-agent-support` `docs/superpowers/specs/2026-05-19-pg-pr-design.md` — pg-pr's overall architecture and span boundary policy. This design extends that daemon with a new HTTP surface.

## Scope

### In scope for v1

- `/api/v1/dashboard` handler + in-memory snapshot store
- Diff-stat fetch for `lines_changed` / `files_changed`
- Reviews list with state and reviewer login
- Agent registry config (logins + per-agent approval regex)
- Comment-mining approval (with claude bot as the first registered agent)
- Recursive bd dependency walk per PR for `waiting_on_me` and `beads`
- Grafana dashboard JSON + Infinity plugin install + datasource provisioning
- Stale-snapshot freshness signal in the dashboard header

### Out of scope for v1

- Historical/time-series view of dashboard fields
- Multi-user support; single configured `self`
- Webhook / push refresh; polling only
- Remote Grafana access; localhost only
- Direct Grafana → `bd` queries
- CICD providers beyond what `pg-pr` already supports
