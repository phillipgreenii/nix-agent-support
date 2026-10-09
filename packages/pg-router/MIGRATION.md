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

## Gates: file-backed gates replaced by the Gate Registry (bead `pg2-h63eu`, `INV-LIFE-2`)

This section **supersedes** three earlier ones that described the file-backed gates: "Hazard: gate
file paths are now configured by default" (Task 1.2b), "Operator: disabling a gate's effect
entirely, from outside pg-router" (beads `pg2-efbb0`, `pg2-hipf0`), and "`cicd-down` gate:
superseded, kept for backward compatibility" (bead `pg2-h410q`). All three described machinery
that no longer exists.

**What a gate is now.** A gate is what prevents pg-router from routing: a **TYPE** (an ALL-CAPS
string such as `SYSTEM_PAUSE` or `LOW_DISK_USAGE` — arbitrary, nothing hard-coded), an optional
description, an optional **owner** (the caller's identity — DEBUG ONLY, no behavioral effect,
overwritten on re-set) and an optional **TTL lease**. There is one active gate per TYPE, the last
writer wins, and **any caller may clear any gate**. Gates are records in the same write-ahead log
events use (`<LogDir>/queue.jsonl`): `GateSet` (each lease renewal is another one), `GateCleared`
and `GateExpired`. The active set is a projection of that log, so it survives a restart, and a gate
never depends on a queue drop.

**What a gate does.** It acts on **participants, never on events.** While any gate is active, each
participant that blocks on it is halted: a blocked **emitter** is not polled; a blocked
**listener** is not dispatched to and its pull is rejected naming the gate. Pushed events are still
accepted, acks and confirmations still processed, queued events stay queued, and an **unblocked**
listener still receives them. A participant blocks on every gate TYPE by default and declares at
registration which TYPEs it does **not** block on (`non_blocking_gates = ["LOW_DISK_USAGE"]` on a
`[[role]]`/`[[query]]`, or `nonBlockingGates` on the `register` verb). The new built-in
`type = "timer"` query is the **timer emitter** and is never blocked by any gate ("you can't stop
time moving forward"). Event TTL is unchanged: when an event is about to expire, each listener that
has not received it gets one final attempt, and a listener still blocked then **loses the event** —
so a long gate drops events (counted in `pg_router_gate_drops`), and the queue is bounded by event
TTL. Gate records themselves are routable as `gate.set` / `gate.cleared` / `gate.expired` events
(a role may bind them) and always bypass gating for delivery.

**Operating it.**

```bash
pg-router pause [--description TEXT]            # sets SYSTEM_PAUSE (owner: operator)
pg-router resume [--all]                        # clears SYSTEM_PAUSE (or every gate)
pg-router gate set LOW_DISK_USAGE --description "3GiB free" --owner disk-watchdog --ttl 6m
pg-router gate clear LOW_DISK_USAGE             # or: gate clear --all
pg-router gate list [--json]
```

External systems use the same socket verbs (`gate-set`, `gate-clear`; schemas `cli.gate-set`,
`cli.gate-clear`) — a TTL gate is kept alive by setting it again before the lease lapses, and
simply stops gating when its owner dies. `pg-router status`/`--json` and the TUI Gates modal (`g`)
list the active gates (TYPE, description, owner, set-at, TTL remaining).

**Operator convention: `BEAD_SERVER_DOWN`.** An operator sets this gate before taking the shared
beads/Dolt server down (for example a `bd` schema migration):
`pg-router gate set BEAD_SERVER_DOWN --owner beads-migration --description "..."`, with NO `--ttl`
(a lapsed lease counts as cleared). It needs a running core, so set it BEFORE the router is stopped
(`gate set` fails with `no running core` otherwise), and clear it last, once the server is verified
healthy. Participants that never touch beads declare `non_blocking_gates = ["BEAD_SERVER_DOWN"]`; an
emitter and its listener that must stay live together MUST opt out on BOTH sides. A long gate drops
events whose TTL lapses (`pg_router_gate_drops`). The full procedure is
`docs/beads-1-3-1-schema-migration.md` in the `phillipg-nix-ziprecruiter` repo.

**The disk-space watchdog (`LOW_DISK_USAGE`).** The replacement for `disk_space_low` is an ordinary
external listener, `pg-router-disk-watchdog` (bead `pg2-zwdwf`, `packages/pg-router-disk-watchdog`),
not pg-router code. Wire it as a timer-driven role that is registered **non-blocking on
`LOW_DISK_USAGE`**, so the gate it sets can never stop it from running again to clear it:

```toml
[[query]]
name = "disk-check-tick"
type = "timer"
emits = ["disk.check"]
trigger = { kind = "period", every = "5m" }

[[role]]
name = "disk-watchdog"
binds = ["disk.check"]
non_blocking_gates = ["LOW_DISK_USAGE"]
```

The role's backing command is `pg-router-disk-watchdog [--path P]... [--min-free 20GiB]
[--recover-free SIZE] [--ttl 7m] [--pg-router-path PATH]`. Each run measures the lowest free space
over the given paths (default `/`), then: below `--min-free` it runs `pg-router gate set
LOW_DISK_USAGE` (description with the free space, `--owner pg-router-disk-watchdog`, `--ttl` lease,
re-set on every low pass); at or above `--recover-free` (default `--min-free` x 1.25, hysteresis so a
borderline disk does not flap) it clears the gate; in between it renews a gate it already owns. It
only ever clears a gate whose owner is itself, so a hand-set `pg-router gate set LOW_DISK_USAGE`
is left alone. The default `--min-free` is `20GiB`; the default `--ttl` is `7m` (the 5 minute check
cadence plus margin). A failed measurement or an unreachable core exits `1` and changes nothing.

**Breaking changes.**

- **`pause`/`resume`/`gate` are socket clients now.** The old `pause [<gate>]`/`resume [<gate>]`
  wrote a file directly and succeeded with no core running. Gates live in the daemon's event log, so
  these commands need a running core and fail with `no running core` (exit `1`) otherwise; a gate
  cannot be set while the daemon is down. `pause <name>` / `resume <name>` (a positional gate name)
  is now a usage error — use `gate set TYPE` / `gate clear TYPE`.
- **Removed:** the `operator_paused`/`cicd_down`/`disk_space_low` gates and their `<LogDir>/gates/*`
  files (leftover files are inert — delete them); `PG_ROUTER_OPERATOR_PAUSED`, `PG_ROUTER_CICD_DOWN`,
  `PG_ROUTER_DISK_SPACE_LOW` and `[pool].operator_paused_path`/`cicd_down_path`/
  `disk_space_low_path` (the TOML keys still parse but are ignored with a warning); the whole
  per-gate **disable-override** mechanism (`PG_ROUTER_*_DISABLE`, `<LogDir>/gate-overrides/*`,
  `Config.OperatorPausedDisable`/`DiskSpaceLowDisable`) — clearing the gate **is** the veto now;
  `config --show`'s gate-file rows; and the nix `daemon.gates.{operatorPausedPath,cicdDownPath}`
  options (nothing in this workspace set them).
- `operator_paused` is now the `SYSTEM_PAUSE` gate. `cicd_down` is no special code any more — it is
  an arbitrary TYPE nothing sets (dead since `pg2-h410q`); for CI health prefer the per-connector
  `command` source worked example below. `disk_space_low` is replaced by `LOW_DISK_USAGE`, set by an
  ordinary external watchdog listener (bead `pg2-zwdwf`), not by pg-router code.
- Wire: `cli.pause`/`cli.resume` lost their `gate` enum (SYSTEM_PAUSE only; `resume` gained `all`);
  `cli.status-reply` `gates[]` now lists **active** gates only as `{type, description?, owner?,
setAt, expiresAt?, ttlRemainingMs?}` and `gatesObservedAt` is gone (the list is read live).
- `run-until-idle` while **any** gate is active still boots and stays reachable for one drain tick
  but produces and drains nothing (a drain could spin on events a blocked listener will not take).

**Not part of this change.** A maximum queue size / overflow behavior for held events is a separate
decision (bead `pg2-5d3ui`).

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
Registry" above.)

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

## Deployment: participant extraction — `pg-router-ccpool-handler` is now required (Phase 5, `docs/adr/0065`)

Phase 5 (docket `pg2-oju6w`) moved every concrete participant implementation — the ccpool-backed
and command-backed role executors, the beads-backed pull query, and the tool-naming/connectivity
pre-flight checks that back them — out of this package and into a new sibling module,
**`packages/pg-router-ccpool-handler`** (its own binary: `pg-router-ccpool-handler`). This
package (`packages/pg-router`) keeps only the generic dispatcher: events, bindings, participants,
handler sessions, and wiring — it no longer knows about `bd`, `ccpool`, `claude`, or any other
concrete tool (`docs/adr/0065`'s "Positive consequences"). This is a **breaking deployment
change**, not an internal refactor:

- **The handler binary must be on `PATH` and running/registered.** Before this change, an
  unconfigured (`.pg-router/config.toml`-less) core fell back to a built-in feedback/worker role
  and query set, running entirely in-process. That built-in fallback is now **gone** (see "Breaking:
  the core ships with no built-in role/query set" below) — so a deployment that has not yet stood
  up `pg-router-ccpool-handler` alongside its `pg-router` core hits `INV-WORKFLOW-1` check 5's
  backing-command resolution and refuses to start: this is now a **hard startup failure**, not a
  degraded-but-working state. There is no soft landing for an unmigrated upgrade.
- **`home/programs/pg-router-ccpool-handler`** (new home-manager capability) and
  **`darwin/modules/pg-router-ccpool-handler`** (its LaunchAgent mirror) deploy the register step
  against a running `pg-router` core — see their own doc comments for the current, narrower scope
  (registration only; the core spawning `dispatch`/`query`/lifecycle-hook calls into this binary
  for real is docket `pg2-oju6w`'s Task 5.4, an accepted gap as of this change — `docs/adr/0065`'s
  Addendum).

### Breaking: `[[role]].type` is removed — role kind moves to the handler's own config

Before this change, a `[[role]]` block named its own participant kind and carried that kind's
config inline:

```toml
[[role]]
name = "feedback"
type = "ccpool"
enabled = true
binds = ["feedback.requested"]
[role.ccpool]
actor = "claude"
completion = "close-only"
on_failure = "unclaim"
on_dispatch_fail = "unclaim"
authorship_guard = true
prompt = '''
...
'''
[role.ccpool.budget]
tokens = 0
cost = 0
time = "25m0s"
```

After this change, `roles.Role` (and its TOML decode, `internal/config/registry.go`'s `roleTOML`)
carries only `name`, `enabled`, `binds`, and an optional `retry` override — **`type` and
`ccpool`/`command` are gone entirely**, not narrowed:

```toml
[[role]]
name = "feedback"
enabled = true
binds = ["feedback.requested"]
```

The kind-specific configuration (`type = "ccpool" | "command"`, the `actor`/`completion`/
`on_failure`/`on_dispatch_fail`/`authorship_guard`/`prompt`/`budget`/`isolation` fields) moves to
`pg-router-ccpool-handler`'s own role-config JSON file (`--role-config`, or
`PG_ROUTER_CCPOOL_HANDLER_ROLE`) — see that module's `cmd/pg-router-ccpool-handler/roleconfig.go`
for the exact JSON shape. There is no automatic TOML-to-JSON converter shipped for this move;
translate each `[[role]]` block's `type`/`[role.ccpool]`/`[role.command]` fields into the
equivalent JSON `roleFile` by hand (field names are unchanged, only the container format is).

### Multi-role dispatch differentiation: `PG_ROUTER_HANDLER_COMMAND_DIR` + the nix `roles` option

Bead `pg2-ymb3v` closed a gap left open by the move above: with only `PG_ROUTER_HANDLER_COMMAND`
set, every enabled role resolved to the byte-for-byte identical handler argv — a deployment with
more than one differently-configured role (e.g. `feedback`/`worker`/`review`, each with its own
ccpool actor/prompt/completion policy) had no way to tell them apart, silently. Setting
`PG_ROUTER_HANDLER_COMMAND_DIR` to a directory of `<role.Name>.json` files (this package's own
`roleFile` shape — see the section above) fixes this: the resolved argv threads
`--role-config <dir>/<role.Name>.json` onto the handler command, so each role dispatches through
its own participant config. `PG_ROUTER_HANDLER_COMMAND` alone is unaffected — a single-role
deployment that never sets `PG_ROUTER_HANDLER_COMMAND_DIR` keeps its existing behavior unchanged.

For a nix-managed deployment (bead `pg2-pteab`), hand-authoring these JSON files is unnecessary:
`home/programs/pg-router-ccpool-handler`'s new `roles` option (attrset keyed by role name,
shaped like `roleFile`) renders one `pkgs.writeText` per role and joins them into one directory,
exposed as that module's own `handlerCommandDir` output. Point
`home/programs/pg-router`'s `daemon`/`periodicDrain.handlerCommandDir` option at it (interpolated
to a string, e.g. `"${config.phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir}"`)
to wire the two modules together — `handlerCommand`/`handlerCommandDir` are otherwise independent
plain-string options on `pg-router`'s own module, not auto-derived from the ccpool-handler module.
The darwin LaunchAgent mirrors (`darwin/modules/pg-router`, `darwin/modules/pg-router-ccpool-handler`)
follow the same shape.

### Breaking: the core ships with no built-in role/query set

Before this change, an operator relying on zero-config defaults (no `.pg-router/config.toml`) saw
`pg-router` log "config present but defines no `[[role]]`; using built-in roles" and fall back to
an in-process feedback/worker role set and a `bd`-backed built-in query. **That fallback is
deleted, not degraded** (`docs/adr/0065`'s "Source-side boundary" decision): a core with no
configured roles/queries today simply dispatches nothing — there is no equivalent "it still mostly
works" state to fall into. Any deployment that never wrote its own `config.toml` MUST author real
`[[role]]`/`[[query]]` configuration (see `pg-router config --print-defaults` for a
behaviorally-equivalent starting point, translated to the post-move shape above) and deploy
`pg-router-ccpool-handler` before upgrading.

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
