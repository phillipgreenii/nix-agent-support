# pg-connector

`pg-connector` is the unified, pluggable connector umbrella for PR/issue/CI/SCM state: one
user-facing CLI, backed by a config-driven registry of small backend binaries, so no other tool
in this repo talks to GitHub/Jira/beads/git directly. It replaces the overlapping GitHub/Jira/beads
sync and correlation logic that `pg-pr`, `pg-router`, and `work-activity-tracker` each used to
reimplement independently.

> **Behavior:** what pg-connector guarantees about the wire protocol, the registry, the CLI's own
> exit codes, and the boundary between the umbrella and its backends lives in the
> [behavior docs](docs/behavior/README.md) — read that for the full, RFC 2119 contract. This file
> is a practical how-to-use-it reference. The architecture decision this package implements is
> recorded in
> [`docs/adr/0062-pg-connector-tier1-tier2-connector-architecture.md`](../../docs/adr/0062-pg-connector-tier1-tier2-connector-architecture.md).

## Architecture: one umbrella, N pluggable backends

pg-connector is a **Tier 1 umbrella**: it owns the four entity-type schemas (`pr`, `issue`, `ci`,
`scm`), the wire protocol, and the only user-facing CLI surface. It knows nothing about GitHub,
Jira, beads, or git — that knowledge lives entirely in a **Tier 2 backend**, one thin binary per
(capability, external system) pair, registered under a config-driven registry and reached only
through the wire protocol. In design-pattern terms: the umbrella is a **Facade** over N
interchangeable backends; each capability's own Go interface (`pr.Provider`, `issue.Provider`,
`ci.Provider`, `scm.Provider`) is a **Strategy** the registry selects among; each backend is a
process-boundary **Adapter** translating one external system into that capability's generic wire
contract.

```mermaid
flowchart LR
    OP["you (human or automation)"] -->|"pg-connector pr/issue/ci/scm/auth/config ..."| UMB
    subgraph UMB["pg-connector (Tier 1 umbrella)"]
      REG["registry: connector.&lt;type&gt; -> backend binary name(s)"]
      DISP["dispatch + outcome reporting"]
      REG --> DISP
    end
    DISP -->|"one JSON request/response per call"| BE1["pg-connector-pr-github"]
    DISP -->|"..."| BE2["pg-connector-issue-beads"]
    DISP -->|"..."| BE3["pg-connector-ci-github-actions"]
    DISP -->|"..."| BE4["pg-connector-scm-git"]
    BE1 -.-> GH["GitHub"]
    BE2 -.-> BD["bd (beads)"]
    BE3 -.-> GHA["GitHub Actions"]
    BE4 -.-> GIT["local git"]
```

Adding a fifth backend, or a second backend for an existing capability (e.g. a Forgejo PR
backend), is a **registry-config change** — one more `connector.<type>` entry and one more
binary on `PATH` — never a change to pg-connector's own code.

## Registry (`connector.<type>`)

Backends are named in the same YAML config `pg-pr` already reads (resolution order:
`$PG_PR_CONFIG`, else `$XDG_CONFIG_HOME/pg-pr/config.yaml`, else `~/.config/pg-pr/config.yaml` —
the env var and directory name deliberately stay `pg-pr`, so one config file can serve both
binaries on a host running each):

```yaml
connector:
  pr: [pg-connector-pr-github] # list-valued
  issue: [pg-connector-issue-beads] # list-valued
  ci: [pg-connector-ci-github-actions] # list-valued
  scm: pg-connector-scm-git # single-valued (scm has no multi-backend future)
```

Every value is a bare binary name on `PATH` — there is no `exec:`-prefix or other built-in/
external distinction, since nothing is compiled into the umbrella itself.

## Subcommands

| Command                                                           | Description                                                                                                                    |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `pr show <id>`                                                    | show a PR's current full state, including comments/review-thread entries (targeted)                                            |
| `issue show <id>`                                                 | show an issue's current state (targeted)                                                                                       |
| `issue create --title <t> [--priority] [--labels] [--issue-type]` | create a new issue (targeted)                                                                                                  |
| `issue comment <id> --body <b>`                                   | add a comment to an issue (targeted)                                                                                           |
| `issue transition <id> --state <s>`                               | transition an issue to a backend-declared target state (targeted)                                                              |
| `ci list <pr-id>`                                                 | list CI runs for a PR, fanned out across every registered `ci` backend (fan-out)                                               |
| `ci logs <run-id>`                                                | get the raw logs for a CI run (targeted)                                                                                       |
| `ci rerun-failed <pr-id>`                                         | rerun a PR's failed CI runs (targeted)                                                                                         |
| `scm worktree add <branch-or-ref>`                                | add a local git worktree for a branch or ref — **never** a PR number (targeted)                                                |
| `scm worktree remove <path>`                                      | remove a local git worktree by path (targeted)                                                                                 |
| `scm worktree list`                                               | list local git worktrees (targeted; `scm` is single-valued, so there is no fan-out here)                                       |
| `scm branch detect [cwd]`                                         | resolve a working directory to its repo and current branch (defaults to the process's own cwd) (targeted)                      |
| `auth status`                                                     | fan `auth_status` out across every registered backend, regardless of capability (fan-out)                                      |
| `config validate`                                                 | fan `auth_status` **and** `capabilities`/schema-version checks out across every registered backend (fan-out)                   |
| `--output json\|human`                                            | persistent flag on every command above: `json` (default; the stable envelope scripts already parse) or `human` (readable text) |
| `version`                                                         | print the version and exit                                                                                                     |

`pg-connector pr open <PR#>` from `pg-pr` has no equivalent here yet — `pg-pr` itself has not been
retired (see the ADR's "Negative" consequences and the design's own Appendix A/B).

Wanting a PR checked out for review composes two calls rather than one command doing both:
`pg-connector pr show <id>` to resolve the branch, then `pg-connector scm worktree add <branch>` —
`scm` is deliberately generic, never PR-aware.

## The wire protocol, in brief

Every Tier-2 backend speaks one small protocol: JSON on stdin (`{"op": "...", "args": {...}}`),
JSON on stdout (`{"protocolVersion", "schemaVersion", "result": ...}` on success, or
`{"protocolVersion", "schemaVersion", "error": {"code", "message"}}` on failure), plain `0`/`1`
process exit. `error.code` is drawn from a closed six-value taxonomy — `not_found`,
`unauthenticated`, `unavailable`, `unknown_op`, `version_mismatch`, `invalid_argument` — each
mapped to a Go sentinel error (`pkg/scriptout`) so a caller uses `errors.Is` rather than
substring-matching a message. Two independent version numbers travel on every response
(`protocolVersion` for the envelope shape, `schemaVersion` per schema-bearing capability), so an
unrelated capability's schema break never forces every backend to redeploy together. Full contract:
[`docs/behavior/interfaces.md`](docs/behavior/interfaces.md)'s `INTF-WIRE`.

**A Tier-2 backend MUST NOT exec `pg-connector` itself, or a sibling backend binary, to satisfy
its own op** — a cross-capability data need is resolved through that backend's own direct system
access instead. See [`docs/behavior/invariants.md`](docs/behavior/invariants.md)'s `INV-COMP-1`.

## pg-connector's own CLI exit codes

A different, higher layer than the wire protocol's plain `0`/`1` above — pg-connector's own
process exit code is never built from it. It splits by whether the invoked op is a **fan-out**
(queries every registered backend of a type) or **targeted** (resolves to one backend):

| Op shape | `0`                  | Other codes                                                                                                |
| -------- | -------------------- | ---------------------------------------------------------------------------------------------------------- |
| Fan-out  | every source healthy | `2` degraded/partial (some healthy, some not); `3` total failure (none healthy, including zero registered) |
| Targeted | success              | `4` `not_found` (a well-formed negative, not a failure); `1` any other error                               |

Every fan-out response also carries a `sources[]` row per backend actually queried
(`{source, status, count, reason}`) — never collapsed into one pass/fail signal, and never
reported only as a stderr line. Full contract:
[`docs/behavior/invariants.md`](docs/behavior/invariants.md)'s `INV-EXIT-1`/`INV-EXIT-2`/`INV-OUT-1`.

## Package layout

```
packages/pg-connector/
  pkg/
    schema/       shared JSON wire shapes (pr.go, issue.go, ci.go, scm.go)
    provider/     per-capability Go interfaces (pr.Provider, issue.Provider, ci.Provider, scm.Provider)
                  + the optional AuthChecker sub-interface
    scriptout/    the wire protocol itself (envelope, versioning, error taxonomy, serve loop)
    eventlog/     shared writer mechanics behind each backend's OWN event log (see below)
  cmd/
    pg-connector/                        umbrella; imports pkg/schema + pkg/provider + pkg/scriptout only
    pg-connector-pr-github/internal/     backend-private
    pg-connector-issue-beads/internal/
    pg-connector-ci-github-actions/internal/
    pg-connector-scm-git/internal/
```

Only `pkg/schema`, `pkg/provider` (and its per-capability subpackages), `pkg/scriptout`, and
`pkg/eventlog` are importable across backend boundaries — a backend's own code lives in `main` or under its own
`internal/`. `cmd/pg-connector/layout_convention_test.go` backstops this mechanically.

## Backend observability (each backend owns its own)

A backend that fronts an external service owns its own rotating JSONL event log, and the Loki log
source plus Grafana alert rules for it are registered in that backend's own nix module under
`darwin/modules/`, never through pg-connector's config (the umbrella is not bound to OTel).
`pg-connector-pr-github` is the first: it appends one event per call to
`${XDG_STATE_HOME}/pg-connector-pr-github/events.jsonl` (override with
`PG_CONNECTOR_PR_GITHUB_EVENTS_FILE`; `off` disables it), rotated at 5 MiB to `events.jsonl.1`. Each
event carries `time`, `level`, `msg`, `op`, `error_code`, `duration_ms` and, on calls that read the
GraphQL budget, `graphql_remaining`, `graphql_reset_at`, `graphql_reserve` and `graphql_headroom`.
Every `op=list` event also carries a numeric `graphql_cost`: the points that one list call spent on
its search requests, summed over every page of every search string (0 for an `--ids-only` call; a call
refused below the reserve logs the cost of the one search whose response carried the reading). The row names no query, so attribute cost to one search string by
running a single list call at a time.
`packages/pg-connector/grafana/alerting/pr-github-alerts.yaml` alerts on auth failure, sustained
`unavailable`, and the budget sitting under `rate_reserve_points`. Writing the log is best effort and
never changes an op's result.

The `pg-connector-pr-github` PR list is deliberately cheap. Its batched GraphQL search selects the
fields in `schema.PRListFields` (identity, state, body, labels with their `label_count`, the
comment, review and `review_thread_count` totals, head SHA and a `statusCheckRollup { state }`
rollup) and every search request also selects `rateLimit { cost remaining resetAt }`. The list MUST
NOT select `reviewRequests` (a nested connection that adds 3 points per page), `mergeStateStatus`
(it made the search answer HTTP 502 or 504 on strings matching 24 or more PRs; it stays on `show`)
or any other CI detail. Cost per 100-PR page is 2 points however many PRs match, and a string
matching nothing still costs 2 points.

Points budget rule: the non-list GitHub spend per hour, plus the number of PR search strings times
the cost per page times the list calls per hour (the 60 PR-cadence polls plus the 12 other list
calls, so `60 + 12`), MUST stay at or under the 4,000 points per hour usable ceiling. Recompute it
whenever the string count or the list field set changes, using the logged `graphql_cost` rather than
an estimate.

An event's `error` field is capped at 1024 bytes. A longer message keeps its first 256 bytes (the
failing command) and its last bytes (the `gh` stderr, which names the cause), with an
`...[N bytes elided]...` marker between them, so a connect failure, an HTTP 502/503/504, a timeout
and a truncated response stay distinguishable in the log.

`pg-connector-pr-github` retries transient `gh` failures on its read-only calls (show, files,
commits, list and search reads): a connection error, an HTTP 502/503/504, or a response that is
empty or cut off mid-JSON. It makes at most 3 attempts, backing off 500ms and then 1s, and never
starts a retry that would not leave at least 5s of the call's own deadline. Auth failures, 4xx
answers, rate-limit errors and not-found answers are final on the first attempt, and writes are
never retried. Before every retry the same GraphQL reserve check that guards `list` runs again, and
a refusal ends the retrying. When every attempt fails the call still answers `unavailable`, with
the last attempt's stderr and a note of the earlier attempts in `error`.

`pg-connector-thread-slack` follows the same pattern: it appends to
`${XDG_STATE_HOME}/pg-connector-thread-slack/events.jsonl` (override with
`PG_CONNECTOR_THREAD_SLACK_EVENTS_FILE`; `off` disables it), same rotation, with the common fields
plus `claude_calls`, `failure_stage` (where the `claude -p` round trip broke: `exec`, `envelope`,
`claude_error`, `reply_decode`, `reply_incomplete`) and `failure_class` (`auth` when claude's own
failure text looks like an authentication problem; the wire `error_code` stays `unavailable`).
`packages/pg-connector/grafana/alerting/thread-slack-alerts.yaml` alerts on auth failure and sustained
`unavailable`. There is deliberately NO quota alert: the backend reads no Slack rate-limit header or
`Retry-After` (claude's envelope is decoded for `result`/`is_error` only), so it has no quota reading
to log.

`pg-connector-issue-jira` follows the same pattern: it appends to
`${XDG_STATE_HOME}/pg-connector-issue-jira/events.jsonl` (override with
`PG_CONNECTOR_ISSUE_JIRA_EVENTS_FILE`; `off` disables it), same rotation, with the common fields plus
`pjira_calls` (how many `pjira` runs the call made), `auth_state` (on `auth_status` calls: `pjira
auth-status`'s own state, since that op answers with a well-formed result even for a bad credential)
and `failure_class` (`auth` for a 401/403 or an `auth_status` of `MISSING`/`UNAUTHENTICATED`/
`FORBIDDEN`; `rate_limited` when pjira's error text carries an HTTP 429; the wire `error_code` stays
`unavailable` for a 429). `packages/pg-connector/grafana/alerting/issue-jira-alerts.yaml` alerts on
auth failure, sustained `unavailable` (excluding throttling) and repeated 429s. There is deliberately
NO remaining-budget alert: the backend reads no Jira quota figure, header or `Retry-After`, so the 429
itself is the only quota signal it can see.

The writer mechanics all three backends share (the common event fields, size-based rotation, the path
rule, call timing) live in `pkg/eventlog`; each backend still owns its log path, its event shape and
its alert rules. Other external-service connectors are expected to follow the same pattern.

## Versioning

Per-source content digest (this repo's own "Versioning of Custom Packages" convention):
`mkGoApp` stamps `main.Version`. Refresh third-party deps with
`go mod tidy && nix run github:nix-community/gomod2nix -- generate`.

## Tests

```bash
cd packages/pg-connector
go test ./...
```

`nix build .#checks.<system>.pg-connector-go-tests` (or the full `nix flake check`) is the
whole-module test gate — `nix build .#pg-connector` alone only compiles `cmd/pg-connector` and
does not exercise every backend's own tests.
