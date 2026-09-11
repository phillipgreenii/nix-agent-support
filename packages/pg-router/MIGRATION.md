# Migrating off `beads-ready` / `beads-list` / `github-issues` / `jira-issues`

`pg2-n75tk` removed four typed `[[query]]` source types from pg-router's Core:
`beads-ready`, `beads-list`, `github-issues`, and `jira-issues`. A `config.toml`
declaring `type = "beads-ready"` (or any of the other three) now fails
`Config.Load()` with `unknown query type "beads-ready"` — a hard pre-flight
error, not a silent fallback. This is a **breaking config change**: existing
`[[query]]` blocks using one of the four removed types MUST be rewritten to
`type = "command"` before upgrading.

## Why

pg-router's Core is a flat edge-router (`INV-WORKFLOW-1`, `docs/behavior/invariants.md`):
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
token — pg-router invokes `argv` and parses its JSON/JSONL stdout, and never
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
already fixed upstream (the pg-router wrapper added it, `f427a830`) before this
change; `beads-ready`/`beads-list`'s backing command (`bd`) is pg-router's own
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

pg-router runs `argv` (no shell — argv[0] is the exact executable, so a
pipeline needs `argv = ["sh", "-c", "<pipeline>"]`) and parses its stdout as
either a JSON array (`format = "json"`) or newline-delimited JSON objects
(`format = "jsonl"`), each shaped:

```json
{ "id": "...", "type": "...", "title": "...", "metadata": { "...": "..." } }
```

`id` is required; `type`, `title`, `metadata` are optional. The backing
command is `argv[0]` — `INV-WORKFLOW-1` check 5 resolves it, so it MUST be on
the PATH pg-router actually runs with (a launchd/service PATH is minimal — see
"Testing the minimal-PATH case" below). Check 5 only resolves `argv[0]`
itself; if `argv[0]` is `sh` running a pipeline, anything the pipeline shells
out to internally (e.g. `jq`) is NOT separately checked — put it on the same
wrapper's PATH deliberately, the same way this flake's own `pg-router` wrapper
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

`gh` supplies its own authentication (`GH_TOKEN` / `gh auth`) — pg-router never
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
MUST be on the PATH the `pg-router` process actually runs with — the deploying
flake's OWN wrapper/service definition supplies it, exactly as it always had
to (agent-support's wrapper never carried it; see `default.nix`'s comment on
why it must not).

## Worked example: `beads-ready` / `beads-list` -> `command`

`bd` is pg-router's own first-class dependency (already on the wrapper's PATH),
so this is the simplest of the four, and pg-router's own built-in defaults now
use exactly this shape — run `pg-router config --print-defaults` to see it
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

Check 5 resolves `argv[0]` against the PATH the `pg-router` process runs with,
which for a launchd/systemd service is minimal — an interactive shell's PATH
hides gaps a real deployment would hit. Verify with:

```bash
env -i PATH=/usr/bin:/bin HOME=/tmp PG_ROUTER_CONFIG=<your-config.toml> \
  <pg-router-binary> config --show
```

This MUST fail with `backing command "<argv[0]>" cannot be invoked` if
`argv[0]` (or, for a `command` role, its own backing executable) is not on
that minimal PATH, and MUST succeed once it is. For a `sh -c` pipeline,
remember this only proves `sh` resolves — anything the pipeline shells out to
internally needs to be on the real runtime PATH by construction (this flake's
own wrapper bundles `bd`, `ccpool`, `pg-pr`, and `jq` for exactly this
reason — see `default.nix`).

## Hazard: gate file paths are now configured by default (Task 1.2b, `INV-LIFE-2`)

Before Task 1.2b, `Config.OperatorPaused`/`Config.CICDDown` defaulted to `""` — no gate could ever
be set unless an operator explicitly pointed `PG_ROUTER_OPERATOR_PAUSED`/`PG_ROUTER_CICD_DOWN` (or,
now, `[pool].operator_paused_path`/`cicd_down_path`) at a real path themselves. As of this change,
`Config.Load()` fills either still-empty field with `<LogDir>/gates/{operator-paused,cicd-down}`
(after the repo-TOML layer, so an existing `[pool]`/env override still wins).

**What this means for an existing deployment:** `<LogDir>` (the standard XDG state path, or
`PG_ROUTER_LOG_DIR`) is now a live gate location even for a pool that never configured one. A
**stray file** already sitting at `<LogDir>/gates/operator-paused` or `<LogDir>/gates/cicd-down` —
left over from an unrelated process, a manual experiment, a copy/paste of another pool's state
directory — now **gates a pool that previously could not be gated at all**. Check for one before
upgrading if `<LogDir>` is shared or was ever used for something else:

```bash
pg-router config --show   # prints each gate's path and whether it is set
```

**Gate files are never swept.** `pause`/`resume` (and `Config.Load()`'s defaulting above) create
directories and files under `<LogDir>/gates/`, but nothing in this codebase ever cleans
`<LogDir>` — that has always been true (no process here purges old state there) and this change
does not alter it. A gate file, once created, persists until an explicit `resume` removes it;
there is no time-based or startup expiry. Keep it that way: `<LogDir>` is also where
`events.jsonl` and the discovery record live, and neither of those is swept either — introducing
sweeping for gate files alone would make `<LogDir>`'s cleanup story inconsistent across the
three, for no invariant that requires it.

## Behavior: a partial produce during `run-until-idle` is a generic failure (Task 1.1)

`run-until-idle` always **completes the drain** first — every
enqueued event, including one pushed in over the socket unrelated to any failing pull source, is
dispatched or expired regardless of a partial produce (`INV-FAIL-3`/`INV-EVT-1`; source isolation
never drops work, per the Task 0.6 ADR resolving `INV-PREC-1`). Only **after** that drain
completes, if one or more sources failed during discovery, does the run exit `1` (generic
failure, `exitGeneric`) instead of `0`. This is **deliberately not a branchable, specific exit
code** (the repo's coarse exit-code convention, `docs/adr/0042-coarse-exit-code-convention-busy-is-not-2.md`)
— a caller MUST NOT infer anything from it beyond "something did not fully succeed"; check the
run's own logs (`source failed; other sources still drained`) for which source and why.

**What this means for a deployment's own exit-code handling:** a `pg-router-drain`/`pg-router-daemon`
systemd unit (or a hand-rolled cron wrapper) that treated a non-zero `run-until-idle` exit as
"nothing ran" is wrong — the drain already ran to completion; a `1` here means "ran, but at least
one source had an error," never "did not run."

## Deployment: HM drain unit moved to `run-until-idle`; new `daemon` submodule + darwin LaunchAgent (Task 1.7)

`home/programs/pg-router/default.nix`'s `periodicDrain`-driven systemd unit (`pg-router-drain`) runs
`pg-router run-until-idle` — no behavior change, just the unit's own `ExecStart` naming the current
subcommand directly.

A new `daemon` submodule (`enable`, `repoRoot`, `beadsPrefix`, `configText`,
`gates.{operatorPausedPath,cicdDownPath}`) drives a second, long-running systemd unit
(`pg-router-daemon`) running `pg-router run` — the daemon core, producing and dispatching on a fixed
poll interval until SIGINT/SIGTERM, as opposed to `periodicDrain`'s timer-triggered one-shot pass.
**`periodicDrain.enable` and `daemon.enable` are mutually exclusive** and asserted so: both are
independent pg-router cores, and running both against the same `PG_ROUTER_LOG_DIR` would race on
`events.jsonl`, the discovery record, and the push-ingest socket. Pick one per deployment.

On darwin, the HM module's `systemd.user.services` is a no-op (darwin has no systemd), so
`darwin/modules/pg-router/default.nix` mirrors any HM user's `daemon.enable` into a LaunchAgent via
`phillipgreenii.system.launchdServices.userAgents.pg-router-daemon` (the same helper/pattern as
`pa-monitor`'s daemon LaunchAgent). `periodicDrain` has no darwin-side equivalent yet — a
darwin deployment that wants the timer-driven form still needs its own launchd timer wiring, or
should use `daemon` instead.

The `daemon` submodule's `gates.operatorPausedPath`/`gates.cicdDownPath` set
`PG_ROUTER_OPERATOR_PAUSED`/`PG_ROUTER_CICD_DOWN` for that unit only; leaving them `null` (the default)
falls back to `Config.Load()`'s own default gate paths under `<PG_ROUTER_LOG_DIR>/gates/` — see
"Hazard: gate file paths are now configured by default (Task 1.2b, `INV-LIFE-2`)" above, which
applies equally to a daemon deployment: a stray file already sitting at that default path now
gates a daemon that previously could not be gated, and gate files are never swept.

## Worked example: per-connector CI health (`pg-connector`) as a `command` source (bead `pg2-h410q`)

This is not a migration off a removed type — it is a NEW worked example, added the same way the
four migrations above were: as an equivalent `command` block, because `command` is still the one
source type that keeps `GOAL-MIN-1`'s boundary (`docs/behavior/invariants.md`; "adding a source
type must not require changing Core" — see "Why" above). pg-connector's own `ci` capability
(`packages/pg-connector/pkg/schema/ci.go`) carries a per-run `as_of`/`stale` pair — bead
`pg2-4aoeg`'s AsOf/Stale contract, mirroring `pkg/provider/pr.Provider.Show`'s own — which is
true PER RUN, independent of `pg-connector ci list`'s own CLI exit code: a backend that served a
last-known-good cached run list because the real CI system was degraded still answers with
`sources[].status = "succeeded"` and exit `0` (`ci.go`'s own doc comment: "a list_runs call that
returns stale runs is still exit 0, never folded into a sixth error/exit code"). So a `command`
source that only looked at the exit code would miss exactly the case this contract exists for —
the recipe below inspects the JSON body instead, via `jq`, matching this doc's own translation
convention.

The result is a pure **health probe**, not a work source: it emits no items on a healthy tick
(`Run()` returns zero events, same as `BeadsReady` finding nothing ready) and instead surfaces
degradation the way `internal/discover.runAndEnqueue` already surfaces any other query failure —
by making `Run()` return an error, which `internal/tui/panes.go`'s existing
`sourceHealthText` (`disabled > excluded > failing > stale > idle > ok`) renders as `failing ×N`
in the Sources pane, no TUI or wire-protocol change required. A connector whose command source
stops ticking altogether (e.g. the process pausing production) still falls through to that same
function's tick-cadence `stale <duration>` rendering — the pre-existing, unrelated meaning of
"stale" in this codebase (a source whose polling has gone quiet), which this recipe does not
touch.

```toml
[[query]]
name = "connector-ci-github-actions"   # one query per CONNECTOR you track separately
emits = ["ci.health"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "set -o pipefail; pg-connector ci list 'my-org/my-repo#123' | jq -c --arg provider github-actions '(.runs // [] | map(select(.provider == $provider))) as $runs | (.sources // [] | map(select(.source == $provider))) as $srcs | if ($runs | any(.stale)) or ($srcs | any(.status == \"degraded\")) then error(\"pg-connector ci: \" + $provider + \" is stale or degraded\") else [] end'"
]
format = "json"

# "ci.health" is declared here purely to satisfy the config's own orphan-producer
# check (Config.Validate's "query %q emits event type %q that no role binds") — the
# probe above never actually emits an item of this type (it emits [] when healthy and
# a Run() error when not), so this role in practice never dispatches. Its own backing
# command still must resolve (INV-WORKFLOW-1 check 5 does not exempt disabled roles),
# so `true` (present on every PATH this flake's wrapper builds, including the minimal
# one "Testing the minimal-PATH case" above describes) is used rather than inventing
# a real handler for an event that is not meant to carry work.
[[role]]
name = "ci-health-sink"
type = "command"
enabled = false
binds = ["ci.health"]
[role.command]
argv = ["true"]
```

Replace `my-org/my-repo#123` with the PR id (`<owner>/<repo>#<number>`, the same convention
`pg-connector-ci-github-actions`'s own resolver parses — see that backend's `resolver.go`) whose
CI you want this connector's health judged by, and `github-actions` with the connector/provider
name you registered under `connector.ci` (`pg-connector config --show` lists what is
registered). Declare one `[[query]]`/jq-filter pair **per connector** you want a separate Sources
pane row for — this is the "per-Source/connector" granularity `pg2-h410q` asked for: `pg-connector
ci list` already fans out across every registered backend and returns one `sources[]` row and a
`provider`-tagged run per backend in a single call, so filtering that one call's JSON by
`$provider` is cheaper than shelling out once per connector.

A real deployment tracking a live, changing set of open PRs (rather than one fixed `pr-id`) can
replace the fixed positional argument with a small loop over `pg-pr pr list --json` (the same seam
`internal/pgrouteracl.ReadPRList` already reads, and the same `<repo>#<number>` id shape
`internal/pgrouteracl.prKey` already builds) feeding `pg-connector ci list` once per open PR, then
folding every call's `runs`/`sources` together before the same staleness/degraded check above —
left as a deployment-specific extension (like the Jira example above, this only matters once a
deploying flake has real repos/PRs to name) rather than expanded inline here.

## `cicd-down` gate: superseded, kept for backward compatibility (bead `pg2-h410q`)

The `cicd-down` gate (`INV-LIFE-2`'s "Gate identity"; `cmd/pg-router/gates_cmd.go`) was added
2026-08-31 (commit `325edc35`) as a stopgap for "an automation actor" to signal CI trouble, but no
producer was ever built — nothing in this codebase, nor (as far as this repo can see) any
deploying flake, has ever written or cleared that file. The worked example immediately above gives
pg-router a real, per-connector, non-binary CI health signal sourced from `pg-connector`'s own
AsOf/Stale contract, which is what `cicd-down` was always meant to eventually consume — so
`cicd-down` is **superseded**: prefer the per-connector `command` source above for any new CI
health integration.

`cicd-down` is **not removed**. It stays wired exactly as it always was (`pause`/`resume
cicd-down`, `PG_ROUTER_CICD_DOWN`, `[pool].cicd_down_path`, the daemon submodule's
`gates.cicdDownPath`, the TUI's gate modal/banner, `INV-LIFE-2`'s two-gate identity) — an operator
or automation actor that already scripted against it keeps working unchanged. Retiring the gate
mechanism itself (its CLI verbs, config surface, wire fields, and `docs/behavior/invariants.md`'s
formal "exactly two named gates" text) is a larger, separately-scoped change than this bead's
acceptance criteria required, and was deliberately left undone here rather than half-removed
across the ~15 Go files (and the `home/programs/pg-router`/`darwin/modules/pg-router` Nix options) that
reference it.
