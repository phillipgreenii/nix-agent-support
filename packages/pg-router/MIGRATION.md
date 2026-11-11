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
knowledge of **how a specific other tool is configured** (an issue tracker CLI's label-filter
flags, a code-host CLI's JSON shape, a ticket-search tool's query language and envelope) into
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
(a ticket-search CLI) is a package defined **only** in the downstream
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
change; `beads-ready`/`beads-list`'s backing command (the issue-tracker CLI) is pg-router's own
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

After — the deploying flake (ZR) already owns a ticket-search CLI (`ticket-search` below) whose
`search` command emits a `{items,truncated}` envelope close to the contract; wrap it
so `key`/`summary` become `id`/`title`:

```toml
[[query]]
name = "jira-source"
emits = ["work.ready"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "ticket-search search --jql 'project = \"PROJ\" AND labels = \"worker-ready\" AND resolution = Unresolved ORDER BY created ASC' --limit 100 | jq -c '[.items[] | {id: .key, type: \"jira-issue\", title: .summary, metadata: {project: \"PROJ\", key, issuetype, status, labels, url}}]'"
]
format = "json"
```

`ticket-search` (or whatever the deploying flake names its ticket-search CLI)
MUST be on the PATH the `pg-router` process actually runs with — the deploying
flake's OWN wrapper/service definition supplies it, exactly as it always had
to (agent-support's wrapper never carried it; see `default.nix`'s comment on
why it must not).

## Worked example: `beads-ready` / `beads-list` -> `command`

The issue-tracker CLI used here is pg-router's own first-class dependency (already on the
wrapper's PATH), so this is the simplest of the four, and pg-router's own built-in defaults now
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

After (the issue-tracker CLI's `list` instead of its `ready` subcommand for a `beads-list` migration — the flag
shape is otherwise identical):

(`issue-cli` below stands for the deployment's issue-tracker CLI; substitute its real name.)

```toml
[[query]]
name = "worker-source"
emits = ["work.ready"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "issue-cli ready --label worker-ready --exclude-label human --json --limit 0 | jq -c '[(.data // [])[] | select(.title | startswith(\"process-feedback:\")) | select(.issue_type == \"task\") | {id, type: .issue_type, title, metadata}]'"
]
format = "json"
```

The issue-tracker CLI's issue JSON uses `issue_type`, not `type`, and wraps results in a
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
own wrapper bundles the tools its worked examples shell out to, such as the issue-tracker CLI, the PR-management CLI and
`jq`, for exactly this reason — see `default.nix`).

## Gates: file-backed gates replaced by the Gate Registry, then by resource levels

This section records two successive replacements. The file-backed gates (`operator_paused`,
`cicd_down`, `disk_space_low` and their `<LogDir>/gates/*` files) were first replaced by a generic
gate registry in the event log; that registry has since been removed too, in favor of **resource
levels** (`INV-LVL-*`, `INV-REQ-1`; decision `DEC-LEVEL-1`). Nothing described as "removed" below
returns.

**Levels replace gates.** A gate was a global, TYPE-named flag that every participant blocked on
unless it opted out with `non_blocking_gates`. A **resource** is now a measured or declared
condition with an ordered scale, and a **level** is its current position on that scale. **Sensors**
(the generic `pg-router-sensors` binary, whose subcommands read hosts, remotes, trackers and
certificates from configuration) report levels at an interval the router sets. A registration
declares what it needs with `requires = [ "<resource> <op> <LEVEL>", ... ]` (an AND-only list); a
resource it does not name is one it does not care about, so the old per-registration opt-out list
is simply the absence of a clause. Levels are enforced from the first deploy, with no observe mode.

Term mapping for converting old wording:

| Old                                                  | Now                                                                                                                                                             |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pg-router gate set\|clear\|list`                    | `pg-router level set\|clear\|list` (plus `refresh`)                                                                                                             |
| `SYSTEM_PAUSE`                                       | the `router` resource at level `PAUSED` (`pause` / `resume` are sugar for it)                                                                                   |
| `LOW_DISK_USAGE`                                     | the `disk`, `cpu` and `ram` resources, ladder `PLENTY > OK > TIGHT > EXHAUSTED`                                                                                 |
| `BEAD_SERVER_DOWN`                                   | `beads_<tracker>` at level `DOWN`, one resource per tracker                                                                                                     |
| `non_blocking_gates` / `nonBlockingGates`            | no `requires` clause for the resources a registration does not name                                                                                             |
| the disk watchdog and its tick query                 | removed; the `disk`, `cpu` and `ram` levels replaced them                                                                                                       |
| `pg_router_gates_active`, `pg_router_gate_*` metrics | `pg_router_resource_level`, `pg_router_role_paused`, `pg_router_requirement_blocked`, `pg_router_requirement_drops` and the other `pg_router_resource_*` series |

**Operating it.**

```bash
pg-router pause [--for DURATION]                       # the router resource -> PAUSED (owner: operator)
pg-router resume [--all]                               # clears the operator pause (or every operator override)
pg-router level list [--json]
pg-router level set <resource> <LEVEL> --for 2h        # a time-boxed operator override (24h cap)
pg-router level set <resource> <LEVEL> --until-cleared # held until cleared
pg-router level clear <resource>                       # or: level clear --all
pg-router level refresh [<resource>...]                # ask the sensors for fresh readings now
pg-router explain <role>                               # the current decision for one role
```

A `--for` over 24 hours is refused with an error and is never clamped to the cap. External systems
push levels over the level-ingest socket verb. `pg-router status`/`--json` and the TUI list the
resource levels, each with its source (sensor, operator or grace fallback).

**What a level does.** It acts on **participants, never on events.** While a registration's
requirement is not met, each emitter is not polled; each listener is not dispatched to and its pull
is rejected naming the resource. Pushed events are still accepted, acks and confirmations still
processed, queued events stay queued, and a listener whose requirement is met still receives them.
The built-in `type = "timer"` query is the **timer emitter** and is never blocked ("you can't stop
time moving forward"). Event TTL is unchanged: when an event is about to expire, each listener that
has not received it gets one final attempt, and a listener still blocked then **loses the event**,
so a long hold drops events (counted in `pg_router_requirement_drops`), and the queue is bounded by
event TTL.

**Operator convention: a tracker's server going down.** The operator sets EACH tracker's level before
taking the shared issue-store server down (for example a schema migration), with no lease, and
clears it last, once the server is verified healthy. With placeholders:

```bash
pg-router level set beads_<tracker> DOWN --until-cleared   # repeat for each tracker, BEFORE the router stops
# ... take the server down, migrate, bring it back, verify it is healthy ...
pg-router level clear beads_<tracker>                      # repeat for each tracker, last
```

The command needs a running core, so set the level BEFORE the router is stopped (`level set` fails
with `no running core` otherwise). A registration that never touches beads names no
`beads_<tracker>` clause and keeps running; an emitter and its listener that must stay live
together MUST leave the resource unmentioned on BOTH sides. The full procedure lives in the
deployment repository that owns the tracker names.

**Legacy gates at upgrade (runbook precondition).** Gates are not translated into levels. Before the
apply that removes the gate registry, check that no gate is still set with the previous binary's
list command; if one is, record it and re-apply it as a `level set` after the apply. At startup the
new router logs one `legacy_gate_dropped` line for every gate record it finds in the event log and
drops, so a gate that was missed is visible.

**Breaking changes.**

- **`pause`/`resume`/`level` are socket clients.** They need a running core and fail with
  `no running core` (exit `1`) otherwise; a level cannot be set while the daemon is down. A
  positional gate name on `pause` / `resume` is a usage error.
- **Removed:** the `operator_paused`/`cicd_down`/`disk_space_low` gates and their `<LogDir>/gates/*`
  files (leftover files are inert; delete them); `PG_ROUTER_OPERATOR_PAUSED`, `PG_ROUTER_CICD_DOWN`,
  `PG_ROUTER_DISK_SPACE_LOW` and `[pool].operator_paused_path`/`cicd_down_path`/
  `disk_space_low_path` (the TOML keys still parse but are ignored with a warning); the per-gate
  **disable-override** mechanism (`PG_ROUTER_*_DISABLE`, `<LogDir>/gate-overrides/*`); `config
--show`'s gate-file rows; the nix `daemon.gates.{operatorPausedPath,cicdDownPath}` options; the
  generic gate registry and its `gate` subcommands; the `non_blocking_gates` registration key and
  the `nonBlockingGates` register field (both are rejected as unknown); and the disk watchdog
  listener with its timer query, replaced by the `disk`, `cpu` and `ram` levels.
- `operator_paused` is now the `router` resource at `PAUSED`. `cicd_down` is no special code any
  more; for CI health prefer the per-connector `command` source worked example below.
- Wire: `cli.status-reply` lists the resource levels instead of `gates[]`; `cli.pause`/`cli.resume`
  address the `router` resource only (`resume` gained `all`).
- `run-until-idle` while any registration's requirement is unmet still boots and stays reachable for
  one drain tick but produces and drains nothing (a drain could spin on events a blocked listener
  will not take).

**Not part of this change.** A maximum queue size / overflow behavior for held events is a separate
decision.

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

A new `daemon` submodule (`enable`, `repoRoot`, `beadsPrefix`, `configText`) drives a second, long-running systemd unit
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

(The `daemon` submodule's `gates.{operatorPausedPath,cicdDownPath}` options this paragraph used to
describe were removed with the file-backed gates — see "Gates: file-backed gates replaced by the Gate
Registry, then by resource levels" above.)

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
replace the fixed positional argument with a small loop over the PR-management CLI's `pr list --json` (the same seam
`internal/pgrouteracl.ReadPRList` already reads, and the same `<repo>#<number>` id shape
`internal/pgrouteracl.prKey` already builds) feeding `pg-connector ci list` once per open PR, then
folding every call's `runs`/`sources` together before the same staleness/degraded check above —
left as a deployment-specific extension (like the Jira example above, this only matters once a
deploying flake has real repos/PRs to name) rather than expanded inline here.

## Deployment: participant extraction — the handler participant is now required (Phase 5, `docs/adr/0065`)

Phase 5 (docket `pg2-oju6w`) moved every concrete participant implementation — the session-backed
and command-backed role executors, the issue-store-backed pull query, and the tool-naming/connectivity
pre-flight checks that back them — out of this package and into a new sibling module, the **handler
module** (its own binary). This
package (`packages/pg-router`) keeps only the generic dispatcher: events, bindings, participants,
handler sessions, and wiring — it no longer knows about any concrete tool
(`docs/adr/0065`'s "Positive consequences"). This is a **breaking deployment
change**, not an internal refactor:

- **The handler binary must be on `PATH` and running/registered.** Before this change, an
  unconfigured (`.pg-router/config.toml`-less) core fell back to a built-in feedback/worker role
  and query set, running entirely in-process. That built-in fallback is now **gone** (see "Breaking:
  the core ships with no built-in role/query set" below) — so a deployment that has not yet stood
  up the handler module alongside its `pg-router` core hits `INV-WORKFLOW-1` check 5's
  backing-command resolution and refuses to start: this is now a **hard startup failure**, not a
  degraded-but-working state. There is no soft landing for an unmigrated upgrade.
- **The handler module's home-manager capability** (new) and
  **its darwin module** (the LaunchAgent mirror) deploy the register step
  against a running `pg-router` core — see their own doc comments for the current, narrower scope
  (registration only; the core spawning `dispatch`/`query`/lifecycle-hook calls into this binary
  for real is docket `pg2-oju6w`'s Task 5.4, an accepted gap as of this change — `docs/adr/0065`'s
  Addendum).

### Breaking: `[[role]].type` is removed — role kind moves to the handler's own config

Before this change, a `[[role]]` block named its own participant kind (`<kind>` below stands for
that kind's name) and carried that kind's config inline:

```toml
[[role]]
name = "feedback"
type = "<kind>"
enabled = true
binds = ["feedback.requested"]
[role.<kind>]
actor = "<actor>"
completion = "close-only"
on_failure = "unclaim"
on_dispatch_fail = "unclaim"
authorship_guard = true
prompt = '''
...
'''
[role.<kind>.budget]
tokens = 0
cost = 0
time = "25m0s"
```

After this change, `roles.Role` (and its TOML decode, `internal/config/registry.go`'s `roleTOML`)
carries only `name`, `enabled`, `binds`, and an optional `retry` override — **`type` and the
per-kind blocks are gone entirely**, not narrowed:

```toml
[[role]]
name = "feedback"
enabled = true
binds = ["feedback.requested"]
```

The kind-specific configuration (the role's kind, and the `actor`/`completion`/
`on_failure`/`on_dispatch_fail`/`authorship_guard`/`prompt`/`budget`/`isolation` fields) moves to
the handler module's own per-role file (see that module's role source for the exact JSON shape).
There is no automatic TOML-to-JSON converter shipped for this move;
translate each `[[role]]` block's kind and per-kind fields into the
equivalent JSON file by hand (field names are unchanged, only the container format is). The
handler split below replaces this file with the role's own `[role.config]` table, and the
per-role file no longer exists.

### Multi-role dispatch differentiation (history, superseded by the handler split)

Bead `pg2-ymb3v` closed a gap left open by the move above: with one shared handler command for
every role, every enabled role resolved to the byte-for-byte identical handler argv, so a deployment
with more than one differently-configured role (e.g. `feedback`/`worker`/`review`, each with its own
actor/prompt/completion policy) could not tell them apart. A shared command and a directory of
per-role JSON files, rendered by the handler module's `roles` option, fixed this for a while. The
handler split below removed that mechanism: each role names its own handler and carries its own
`[role.config]`, so there is no shared command, no per-role file directory and no extra argument.

### The handler split: a role names its own handler (the compat window)

Bead `pg2-12gr4.5` records the decisions of the handler split (`docs/adr/0065`'s Addendum, handler
split; `DEC-ROLE-1`, `DEC-WIRE-3`). The split was **additive and per role** while it lasted: a role
moved to the per-role path by setting `handler`, and the core chose between the two dispatch paths
role by role. The window is over: every role names its own handler, and the earlier path (a shared
handler command from the environment, a per-role JSON file and its extra argument, and the JSON
`survivesShutdown` flag) is gone. A role without `handler` is now a configuration error naming the
role.

Today a role is one `[[role]]` entry:

- `handler`, an absolute path to the adapter command. The core sends the **role object**
  (`{ name, config }`) in the JSON on stdin of every request (`DEC-WIRE-3`).
- `[role.config]`, the role's own opaque settings (the adapter's behavior docs name its keys; it
  rejects an unknown one).
- `survives_shutdown = true` for a role whose dispatch must outlive a restart of the core
  (`docs/adr/0085`'s Decision).

The adapter's `validate` runs over the role's rendered table during the build, so a mistake fails
before apply. Exercise a new or changed role once with `pg-router run-role <role> '<json>'` against
a realistic event and read an actual outcome, not just an exit code (the repo's config-testing
rule). The keys decode strictly: a wrong-typed value, or a `handler` that is not an absolute path,
fails the load with exit `3` before anything runs.

### Breaking: the core ships with no built-in role/query set

Before this change, an operator relying on zero-config defaults (no `.pg-router/config.toml`) saw
`pg-router` log "config present but defines no `[[role]]`; using built-in roles" and fall back to
an in-process feedback/worker role set and an issue-store-backed built-in query. **That fallback is
deleted, not degraded** (`docs/adr/0065`'s "Source-side boundary" decision): a core with no
configured roles/queries today simply dispatches nothing — there is no equivalent "it still mostly
works" state to fall into. Any deployment that never wrote its own `config.toml` MUST author real
`[[role]]`/`[[query]]` configuration (see `pg-router config --print-defaults` for a
behaviorally-equivalent starting point, translated to the post-move shape above) and deploy
the handler module before upgrading.

## Additive: `status`/TUI now report dispatch concurrency (`dispatch: { busy, total }`, Task 6.5)

`status --json`'s reply (and the TUI header/banner) gained a new, optional `dispatch` object:
`busy` (how many delivery sessions are currently in a handler's custody,
`Queue.SessionsInFlight()`) and `total` (how many listeners are currently registered,
`Queue.ListenerCount()`). This is **purely additive** — `additionalProperties: false` is preserved,
`busy`/`total` are required only within `dispatch` once it is present, and nothing lands on the
reply's own top-level `required` array — so a client parsing only the pre-existing fields, or a
pre-Phase-6 core's reply lacking `dispatch` entirely, keeps working unchanged. **No migration step
is owed.**

Before Task 6.2's bounded fan-out, a dispatch pass offered one listener at a time, synchronously,
so `busy` would have legitimately saturated at `0` or `1` had this field existed then. Now that a
pass may hold more than one session in custody at once, `busy` is free to exceed `1` — the TUI's
pinned "N in flight" banner wording is unrelated and unchanged; `dispatch.busy`/`dispatch.total` are
the new, real fan-out signal, rendered alongside the existing gates line in every TUI tier
(Wide/Narrow/Tiny).

### Removed with no migration path: `sessions` / `reconcile`

The `sessions` and `reconcile` CLI subcommands are **deleted outright** in this change (per the
superseding operator ruling of 2026-09-02, recorded in `docs/adr/0065`'s "No deprecation shim"
decision). There is deliberately **no** deprecation shim, no dedicated diagnostic stderr line, and
no grace period: invoking either name now gets the binary's ordinary unknown-subcommand usage
error (exit `2`, `docs/adr/0042-coarse-exit-code-convention-busy-is-not-2.md`), exactly as any
other unrecognized subcommand would. No migration path is offered for either — this is a
deliberate omission, not an oversight.

## Scheduling: calendar trigger for timer queries

A `type = "timer"` query can now fire at a wall-clock time (`[query.trigger] kind = "calendar"`) and
carry a static `metadata` table onto its ticks. Existing `period` timers are unchanged, and nothing
needs migrating. See the README section "Scheduling with a calendar trigger".
