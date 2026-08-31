# Migrating off `beads-ready` / `beads-list` / `github-issues` / `jira-issues`

`pg2-n75tk` removed four typed `[[query]]` source types from pr-pool's Core:
`beads-ready`, `beads-list`, `github-issues`, and `jira-issues`. A `config.toml`
declaring `type = "beads-ready"` (or any of the other three) now fails
`Config.Load()` with `unknown query type "beads-ready"` — a hard pre-flight
error, not a silent fallback. This is a **breaking config change**: existing
`[[query]]` blocks using one of the four removed types MUST be rewritten to
`type = "command"` before upgrading.

## Why

pr-pool's Core is a flat edge-router (`INV-WORKFLOW-1`, `docs/behavior/invariants.md`):
it validates the declared wiring and nothing beyond it — "source semantics are
opaque to the core." Typing `beads-ready` / `beads-list` / `github-issues` /
`jira-issues` into `internal/config/registry.go`'s `queryTOML` struct and
`internal/query/factory.go`'s dispatch map broke that boundary: each one baked
knowledge of **how a specific other tool is configured** (bd's label-filter
flags, `gh issue list`'s JSON shape, a Jira search tool's JQL/envelope) into
Core, so adding or changing a source type meant changing Core — a live
violation of the "adding a source type must not require changing Core"
invariant (`GOAL-MIN-1` in `docs/behavior/invariants.md`; the deploying ZR
flake's own behavior docs state an analogous config-minimality requirement
from the deployment side).

`command` is the one source type that keeps the boundary: it is an opaque
token — pr-pool invokes `argv` and parses its JSON/JSONL stdout, and never
interprets what the command does or how it is configured. Every one of the
four removed types is expressible as a `command` block; this doc shows how.

There is also a **correctness reason**, not just an architectural one. A
recent change (`pg2-0xa2n`) made "a source or handler whose backing command is
absent" a **blocking pre-flight failure** in `Config.Load()` (`INV-WORKFLOW-1`
check 5) instead of a runtime warning. `jira-issues`' backing command
(`pg-pr-issues-jira-zr`) is a package defined **only** in the downstream
`your-private-flake` (e.g. the operator's private ZR-deployment flake) — this (upstream, public) flake can never
legitimately supply it without inverting the dependency direction. So
`jira-issues` as a typed-in-Core source was **structurally unsatisfiable**:
any config declaring it would refuse to load in any context where this
flake's own wrapper is the one resolving backing commands. Collapsing it to
`command` — so the deploying flake (ZR) declares the invocation and supplies
the backing command from its own wrapper/PATH — is the only fix that neither
weakens check 5 nor inverts the dependency.

`github-issues`' backing command (`gh`) is a plain nixpkgs package and was
already fixed upstream (the pr-pool wrapper added it, `f427a830`) before this
change; `beads-ready`/`beads-list`'s backing command (`bd`) is pr-pool's own
first-class dependency and was never at risk. Those two are removed from the
TOML surface for the **architectural** reason only (boundary/`GOAL-MIN-1`),
not the correctness one — but the fix is the same shape.

`query.BeadsReady` (the Go type) is **not** deleted: it still backs the
in-Go built-in default query set (`roles.BuiltinQuerySet`), which is
constructed directly as Go values and never goes through TOML decode. Only
its **TOML-configurability** — the `beads-ready` type name in `queryTOML` and
the query factory — was removed. `query.BeadsList`, `query.GitHubIssues`, and
`query.JiraIssues` had no other caller, so those Go types were deleted
outright along with their tests.

## The `command` contract

A `command`-type `[[query]]` block:

```toml
[[query]]
name = "my-source"
emits = ["my.event.type"]
type = "command"
[query.command]
argv = ["my-lister", "--some-flag"]
format = "jsonl"   # or "json"
```

pr-pool runs `argv` (no shell — argv[0] is the exact executable, so a
pipeline needs `argv = ["sh", "-c", "<pipeline>"]`) and parses its stdout as
either a JSON array (`format = "json"`) or newline-delimited JSON objects
(`format = "jsonl"`), each shaped:

```json
{ "id": "...", "type": "...", "title": "...", "metadata": { "...": "..." } }
```

`id` is required; `type`, `title`, `metadata` are optional. The backing
command is `argv[0]` — `INV-WORKFLOW-1` check 5 resolves it, so it MUST be on
the PATH pr-pool actually runs with (a launchd/service PATH is minimal — see
"Testing the minimal-PATH case" below). Check 5 only resolves `argv[0]`
itself; if `argv[0]` is `sh` running a pipeline, anything the pipeline shells
out to internally (e.g. `jq`) is NOT separately checked — put it on the same
wrapper's PATH deliberately, the same way this flake's own `pr-pool` wrapper
bundles `jq` for its own generated example (see `default.nix`).

## Worked example: `github-issues` -> `command`

Before:

```toml
[[query]]
name = "gh-source"
emits = ["work.ready"]
type = "github-issues"
[query.github-issues]
repo = "my-org/my-repo"
labels = ["worker-ready"]
```

After — `gh issue list` already emits close to the right shape, but its field
names differ (`number`/`title`/`url`/`labels` vs. `id`/`type`/`title`/
`metadata`), so it still needs a `jq` translation:

```toml
[[query]]
name = "gh-source"
emits = ["work.ready"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "gh issue list --repo my-org/my-repo --state open --limit 200 --json number,title,url,labels --label worker-ready | jq -c '[.[] | {id: (\"my-org/my-repo#\" + (.number|tostring)), type: \"github-issue\", title, metadata: {repo: \"my-org/my-repo\", number, url, labels: [.labels[].name]}}]'"
]
format = "json"
```

`gh` supplies its own authentication (`GH_TOKEN` / `gh auth`) — pr-pool never
handled credentials for this source and still doesn't.

## Worked example: `jira-issues` -> `command`

Before:

```toml
[[query]]
name = "jira-source"
emits = ["work.ready"]
type = "jira-issues"
[query.jira-issues]
project = "PROJ"
labels = ["worker-ready"]
```

After — the deploying flake (ZR) already owns a `pg-pr-issues-jira-zr search`
command whose `{items,truncated}` envelope is close to the contract; wrap it
so `key`/`summary` become `id`/`title`:

```toml
[[query]]
name = "jira-source"
emits = ["work.ready"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "pg-pr-issues-jira-zr search --jql 'project = \"PROJ\" AND labels = \"worker-ready\" AND resolution = Unresolved ORDER BY created ASC' --limit 100 | jq -c '[.items[] | {id: .key, type: \"jira-issue\", title: .summary, metadata: {project: \"PROJ\", key, issuetype, status, labels, url}}]'"
]
format = "json"
```

`pg-pr-issues-jira-zr` (or whatever the deploying flake names its Jira CLI)
MUST be on the PATH the `pr-pool` process actually runs with — the deploying
flake's OWN wrapper/service definition supplies it, exactly as it always had
to (agent-support's wrapper never carried it; see `default.nix`'s comment on
why it must not).

## Worked example: `beads-ready` / `beads-list` -> `command`

`bd` is pr-pool's own first-class dependency (already on the wrapper's PATH),
so this is the simplest of the four, and pr-pool's own built-in defaults now
use exactly this shape — run `pr-pool config --print-defaults` to see it
live, or read `internal/config/example.go`'s `beadsReadyCommand`.

Before:

```toml
[[query]]
name = "worker-source"
emits = ["work.ready"]
type = "beads-ready"
[query.beads-ready]
labels = ["worker-ready"]
exclude_labels = ["human"]
title_prefix = "process-feedback:"
item_type = "task"
```

After (`bd list` instead of `bd ready` for a `beads-list` migration — the flag
shape is otherwise identical):

```toml
[[query]]
name = "worker-source"
emits = ["work.ready"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "bd ready --label worker-ready --exclude-label human --json --limit 0 | jq -c '[(.data // [])[] | select(.title | startswith(\"process-feedback:\")) | select(.issue_type == \"task\") | {id, type: .issue_type, title, metadata}]'"
]
format = "json"
```

`bd`'s issue JSON uses `issue_type`, not `type`, and wraps results in a
`{"data": [...]}` envelope — the `jq` filter does that translation and
reproduces the `title_prefix`/`item_type` post-filters `BeadsReady.Run` used
to apply in Go. Drop the `select(...)` clauses you don't need (e.g. the
built-in `worker-source` above uses neither).

## Testing the minimal-PATH case

Check 5 resolves `argv[0]` against the PATH the `pr-pool` process runs with,
which for a launchd/systemd service is minimal — an interactive shell's PATH
hides gaps a real deployment would hit. Verify with:

```bash
env -i PATH=/usr/bin:/bin HOME=/tmp PR_POOL_CONFIG=<your-config.toml> \
  <pr-pool-binary> config --show
```

This MUST fail with `backing command "<argv[0]>" cannot be invoked` if
`argv[0]` (or, for a `command` role, its own backing executable) is not on
that minimal PATH, and MUST succeed once it is. For a `sh -c` pipeline,
remember this only proves `sh` resolves — anything the pipeline shells out to
internally needs to be on the real runtime PATH by construction (this flake's
own wrapper bundles `bd`, `ccpool`, `pg-pr`, and `jq` for exactly this
reason — see `default.nix`).

## Hazard: gate file paths are now configured by default (Task 1.2b, `INV-LIFE-2`)

Before Task 1.2b, `Config.QuotaPaused`/`Config.CICDDown` defaulted to `""` — no gate could ever
be set unless an operator explicitly pointed `PR_POOL_QUOTA_PAUSED`/`PR_POOL_CICD_DOWN` (or,
now, `[pool].quota_paused_path`/`cicd_down_path`) at a real path themselves. As of this change,
`Config.Load()` fills either still-empty field with `<LogDir>/gates/{quota-paused,cicd-down}`
(after the repo-TOML layer, so an existing `[pool]`/env override still wins).

**What this means for an existing deployment:** `<LogDir>` (the standard XDG state path, or
`PR_POOL_LOG_DIR`) is now a live gate location even for a pool that never configured one. A
**stray file** already sitting at `<LogDir>/gates/quota-paused` or `<LogDir>/gates/cicd-down` —
left over from an unrelated process, a manual experiment, a copy/paste of another pool's state
directory — now **gates a pool that previously could not be gated at all**. Check for one before
upgrading if `<LogDir>` is shared or was ever used for something else:

```bash
pr-pool config --show   # prints each gate's path and whether it is set
```

**Gate files are never swept.** `pause`/`resume` (and `Config.Load()`'s defaulting above) create
directories and files under `<LogDir>/gates/`, but nothing in this codebase ever cleans
`<LogDir>` — that has always been true (no process here purges old state there) and this change
does not alter it. A gate file, once created, persists until an explicit `resume` removes it;
there is no time-based or startup expiry. Keep it that way: `<LogDir>` is also where
`events.jsonl` and the discovery record live, and neither of those is swept either — introducing
sweeping for gate files alone would make `<LogDir>`'s cleanup story inconsistent across the
three, for no invariant that requires it.
