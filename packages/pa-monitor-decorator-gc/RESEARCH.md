# pa-monitor-decorator-gc Research

Research phase for bead `beads_pg2-3jbl`. Captures live Gas City agent
session env + identifies fields a decorator can map onto pa-monitor's
label taxonomy. The decorator binary itself is scaffolded empty in this
package; the label mapping below is a **proposal for Phil + Claude
review** before any logic is implemented.

ADR-0011 says Gas City–specific label enrichment must live in a
decorator binary, not in pa-monitor's built-in detectors. The existing
`packages/pa-monitor/internal/labels/detectors/gascity.go` already maps
`GC_RIG` -> `workspace.project` and `GC_AGENT` -> `agent.role` from env
only. This decorator adds the richer labels that aren't worth a built-in
detector or that require shelling into `gc`.

## Methodology

All commands run from a workstation that hosts a running `pgii-gastown`
city. Per the brief, `tmux -L gc list-panes ...` was disabled by the
sandbox, so role attribution was done by parsing the GC envs out of
`ps eww` plus the `[gc] <role>` token in the claude argv.

```bash
# Find live claude PIDs and identify GC roles
ps -A -ww -o pid,args | grep -E "(claude|tmux -L gc|gc-)" | grep -v grep

# Dump env for each candidate PID
for pid in <pids>; do
  ps eww -p $pid -o command= \
    | tr ' ' '\n' \
    | grep -E '^(GC_|CMUX_|PA_MONITOR|GASCITY|OTEL_|WORKSPACE|BEADS_)' \
    | sort -u
done

# Confirm cwd
lsof -p <pid> | awk '$4=="cwd" {print $NF}'

# Attempted but not available (Dolt server down for this city run):
gc session list --json
gc bd show $GC_SESSION_ID --json
```

`gc session show` does not exist — the closest commands are
`gc session list [--json] [--state] [--template]` and
`gc bd show <id> [--json]`. Both require Dolt; for this city the
provider-state shows `running:false` and `bd dolt start` was not
invoked (research must not perturb live agents). These outputs are
left as a TODO for re-running once Dolt is up. The env captured below
is what the decorator actually sees on stdin — `gc bd show` would only
add work-item metadata that the decorator could fetch _itself_ if
needed.

## Live sessions sampled

City root: `/Users/phillipg/gc` (only city running on this host).
OTEL_SERVICE_NAME=gascity, OTEL_RESOURCE_ATTRIBUTES=`gc.city=/Users/phillipg/gc`
on every session.

| Role   | PID   | GC_AGENT            | GC_SESSION_ID | GC_SESSION_NAME        | GC_TEMPLATE         | GC_SESSION_ORIGIN | GC_DIR                               | cwd                                  |
| ------ | ----- | ------------------- | ------------- | ---------------------- | ------------------- | ----------------- | ------------------------------------ | ------------------------------------ |
| mayor  | 56575 | pgii-gastown.mayor  | gc-b9nm       | pgii-gastown\_\_mayor  | pgii-gastown.mayor  | named             | /Users/phillipg/gc                   | /Users/phillipg/gc                   |
| deacon | 25155 | pgii-gastown.deacon | gc-5z36       | pgii-gastown\_\_deacon | pgii-gastown.deacon | named             | /Users/phillipg/gc/.gc/agents/deacon | /Users/phillipg/gc/.gc/agents/deacon |
| dog-1  | 45744 | dog-gc-9xsla        | gc-9xsla      | dog-gc-9xsla           | dog                 | ephemeral         | /Users/phillipg/gc                   | /Users/phillipg/gc                   |
| dog-2  | 45746 | dog-gc-yxrta        | gc-yxrta      | dog-gc-yxrta           | dog                 | ephemeral         | /Users/phillipg/gc                   | /Users/phillipg/gc                   |
| dog-3  | 40755 | dog-gc-69m41        | gc-69m41      | dog-gc-69m41           | dog                 | ephemeral         | /Users/phillipg/gc                   | /Users/phillipg/gc                   |

Note: argv-suffix `[gc] dog-N` (e.g. `[gc] dog-1 • 2026-05-29T...`)
is the controller's friendly pool-slot label and is **not** in any env
var. It only appears in `claude --session-id ... [gc] dog-N` argv. The
decorator can't see argv (only stdin JSON), so the pool-slot identity
must come from `GC_SESSION_NAME` / `GC_AGENT` (`dog-gc-9xsla`) — the
controller maps gc-id -> dog-N elsewhere.

### Roles I could NOT find live

| Role     | Status                                                |
| -------- | ----------------------------------------------------- |
| witness  | not running (no `pgii-gastown.witness` GC_AGENT seen) |
| polecat  | not running                                           |
| operator | not running                                           |

These are documented in the pack at
`packages/pgii-pack-gastown/` (deacon, operator, mayor agents +
prompts; foreman retired 2026-05-29 per `flake.nix` layout check). The
decorator should still handle these GC_AGENT values when they appear —
mapping rules below cover them by structure, not by enumeration.

## Per-PID full GC env (filtered)

Common to all sessions (drop these from the per-role tables for brevity):

```
BEADS_CREDENTIALS_FILE=
BEADS_DOLT_AUTO_START=0
BEADS_DOLT_PASSWORD=
BEADS_DOLT_SERVER_HOST=
BEADS_DOLT_SERVER_PORT=24158
BEADS_DOLT_SERVER_USER=
GC_BEADS_PREFIX=gc
GC_BEADS_SCOPE_ROOT=/Users/phillipg/gc
GC_BEADS=bd
GC_BIN=/nix/store/34mrgykkbn65q11ap7380ps6zr0nyh8n-gascity-1.1.0/bin/.gc-wrapped
GC_CITY_PATH=/Users/phillipg/gc
GC_CITY_RUNTIME_DIR=/Users/phillipg/gc/.gc/runtime
GC_CITY=/Users/phillipg/gc
GC_CONTROL_DISPATCHER_TRACE_DEFAULT=/Users/phillipg/gc/.gc/runtime/control-dispatcher-trace.log
GC_DOLT_CONFIG_FILE=/Users/phillipg/gc/.gc/runtime/packs/dolt/dolt-config.yaml
GC_DOLT_DATA_DIR=/Users/phillipg/gc/.beads/dolt
GC_DOLT_LOCK_FILE=/Users/phillipg/gc/.gc/runtime/packs/dolt/dolt.lock
GC_DOLT_LOG_FILE=/Users/phillipg/gc/.gc/runtime/packs/dolt/dolt.log
GC_DOLT_MANAGED_LOCAL=1
GC_DOLT_PID_FILE=/Users/phillipg/gc/.gc/runtime/packs/dolt/dolt.pid
GC_DOLT_PORT=24158
GC_DOLT_STATE_FILE=/Users/phillipg/gc/.gc/runtime/packs/dolt/dolt-state.json
GC_HOME=/Users/phillipg/.gc
GC_OTEL_LOGS_URL=http://127.0.0.1:4318/v1/logs
GC_OTEL_METRICS_URL=http://127.0.0.1:4318/v1/metrics
GC_PACK_DIR=/Users/phillipg/gc/.gc/system/packs/maintenance
GC_PACK_NAME=maintenance
GC_PACK_STATE_DIR=/Users/phillipg/gc/.gc/runtime/packs/maintenance
GC_PROVIDER=claude
GC_READY_PROMPT_PREFIX=❯
GC_STORE_ROOT=/Users/phillipg/gc
GC_STORE_SCOPE=city
GC_SUPERVISOR_ENV=OTEL_EXPORTER_OTLP_ENDPOINT,OTEL_EXPORTER_OTLP_PROTOCOL,OTEL_SERVICE_NAME,GC_OTEL_METRICS_URL,GC_OTEL_LOGS_URL
GC_SUPERVISOR_PRESERVE_SESSIONS_ON_SIGNAL=1
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_RESOURCE_ATTRIBUTES=gc.city=/Users/phillipg/gc
OTEL_SERVICE_NAME=gascity
```

Per-role differentiating env:

```
# mayor (PID 56575)
BEADS_ACTOR=pgii-gastown__mayor
GC_AGENT=pgii-gastown.mayor
GC_ALIAS=pgii-gastown.mayor
GC_CONTINUATION_EPOCH=2
GC_DIR=/Users/phillipg/gc
GC_INSTANCE_TOKEN=6aac02f4454f57eb5c6b2b38c6da5345
GC_RUNTIME_EPOCH=4
GC_SESSION_ID=gc-b9nm
GC_SESSION_NAME=pgii-gastown__mayor
GC_SESSION_ORIGIN=named
GC_TEMPLATE=pgii-gastown.mayor

# deacon (PID 25155)
# (no BEADS_ACTOR — older session before that was added?)
BEADS_DIR=/Users/phillipg/gc/.beads
GC_AGENT=pgii-gastown.deacon
GC_ALIAS=pgii-gastown.deacon
GC_CONTINUATION_EPOCH=2
GC_DIR=/Users/phillipg/gc/.gc/agents/deacon
GC_INSTANCE_TOKEN=9f6f33cfacf56309b5dc7f798bfbece8
GC_RIG_ROOT=                   # explicitly empty
GC_RIG=                        # explicitly empty
GC_RUNTIME_EPOCH=3
GC_SESSION_ID=gc-5z36
GC_SESSION_NAME=pgii-gastown__deacon
GC_SESSION_ORIGIN=named
GC_TEMPLATE=pgii-gastown.deacon

# dog-1 (PID 45744)
BEADS_ACTOR=dog-gc-9xsla
GC_AGENT=dog-gc-9xsla
# no GC_ALIAS for dogs
GC_CONTINUATION_EPOCH=2
GC_DIR=/Users/phillipg/gc
GC_INSTANCE_TOKEN=7b367640bb384bb4adeb6bc2d3359571
GC_RUNTIME_EPOCH=2
GC_SESSION_ID=gc-9xsla
GC_SESSION_NAME=dog-gc-9xsla
GC_SESSION_ORIGIN=ephemeral
GC_STARTUP_PROMPT_DELIVERED=1
GC_TEMPLATE=dog

# dog-2 (PID 45746) — same shape as dog-1
GC_AGENT=dog-gc-yxrta GC_SESSION_ID=gc-yxrta GC_TEMPLATE=dog
GC_SESSION_NAME=dog-gc-yxrta GC_SESSION_ORIGIN=ephemeral

# dog-3 (PID 40755) — same shape as dog-1
GC_AGENT=dog-gc-69m41 GC_SESSION_ID=gc-69m41 GC_TEMPLATE=dog
GC_SESSION_NAME=dog-gc-69m41 GC_SESSION_ORIGIN=ephemeral
```

## Observations relevant to label design

1. **`GC_RIG` was empty on every sampled session.** This city is in
   city-mode only (no active rig). The existing `gascity.go` detector
   keys off `GC_RIG` for `workspace.scope=gascity`, which means **none
   of these sessions are currently tagged as gascity by the built-in
   detector**. That's a pre-existing issue and is out of scope here,
   but the decorator should be the workaround: emit
   `workspace.scope=gascity` whenever `GC_CITY` is set (or
   `GC_PROVIDER=claude` + `GC_BEADS_PREFIX=gc`). Discuss with Phil
   before relying on this.

2. **`GC_AGENT` shape varies by template.** Named agents are
   `<city>.<role>` (e.g. `pgii-gastown.mayor`, `pgii-gastown.deacon`).
   Pool agents are `<template>-<sessionid>` (e.g. `dog-gc-9xsla`).
   The decorator should split `GC_AGENT`:
   - If it contains a `.`: role = part after `.` (`mayor`, `deacon`)
   - Else, if it matches `<template>-gc-<id>`: role = part before
     `-gc-` (`dog`), and the pool-slot can come from
     `GC_INSTANCE_TOKEN` (high cardinality, not labelled) or the
     gc-id (`9xsla`, also high cardinality — don't label).

3. **`GC_TEMPLATE` is the cleanest "template" axis** — values seen so
   far: `pgii-gastown.mayor`, `pgii-gastown.deacon`, `dog`. Stable per
   release of the pack.

4. **`GC_SESSION_ORIGIN`** is `named` for permanent agents, `ephemeral`
   for pool slots. Good low-cardinality label.

5. **`GC_INSTANCE_TOKEN`, `GC_SESSION_ID`, `BEADS_ACTOR` (when agent ==
   pool-slot)** are high-cardinality. Do NOT promote to labels — they
   only belong on event attrs/traces.

6. **No `CMUX_WORKSPACE_ID` on any GC session.** The brief mentioned
   this as a possible source, but GC agents are not wrapped by cmux —
   they're spawned directly by the gc supervisor under tmux on socket
   `-L gc`. cmux-wrapped claudes (e.g. interactive user sessions) have
   it, but the decorator running for those won't get GC env, so the
   key never co-occurs with `GC_*`.

7. **OTEL_RESOURCE_ATTRIBUTES carries `gc.city=<path>`.** Already on
   the metric/log stream out-of-band; no need to duplicate as a
   pa-monitor label unless we want a single-host label that says which
   city directory the agent came from. Cardinality = number of cities
   on the host (~1-3, low).

8. **`GC_DIR` differs for deacon** (`.gc/agents/deacon` — the agent's
   per-role working dir) **vs mayor and dogs** (`/Users/phillipg/gc`
   — the city root). Not directly useful as a label but may help
   `workspace.project` reasoning later.

9. **`mol_state` / formula-state was not discoverable from env or
   cwd.** It lives in beads (`gc bd show $GC_SESSION_ID --json`,
   which requires Dolt). If Phil wants `gascity.mol_state` as a
   label, the decorator must shell out to `gc bd show` per tick —
   that's a hard "discuss before implementing" since each pa-monitor
   tick already spawns a subprocess per decorator per session.

## Proposed label mapping (DRAFT — review required)

Convention: dotted keys, lowercase, namespaced. Add labels on top of
what the built-in `gascity` detector already emits — don't restate
`workspace.scope=gascity` unless GC_RIG is empty (see observation #1).

| Label key                    | Source field(s)                                                      | Example value                | Stability | Cardinality risk                | Notes                                                                                                                                                                                                                        |
| ---------------------------- | -------------------------------------------------------------------- | ---------------------------- | --------- | ------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `workspace.scope`            | `GC_CITY` set OR `GC_PROVIDER=claude && GC_BEADS_PREFIX=gc`          | `gascity`                    | high      | 1 distinct                      | Fallback when `GC_RIG` is empty (today's reality). Only emit if built-in detector wouldn't already set it.                                                                                                                   |
| `workspace.project`          | basename of `GC_CITY` (e.g. `gc`)                                    | `gc`                         | high      | 1-3 per host                    | Built-in detector emits `GC_RIG` if non-empty; this is the fallback when GC_RIG is empty. Skip if already set.                                                                                                               |
| `agent.role`                 | `GC_AGENT` split on `.` (named) OR `GC_TEMPLATE` (pool, e.g. `dog`)  | `mayor`, `deacon`, `dog`     | high      | ~6-10 per city                  | Roles: mayor, deacon, dog, witness, polecat, operator, foreman (retired), worker. Built-in detector already sets this from `GC_AGENT` raw — the decorator should **normalise** (split `.`, collapse pool ids) and overwrite. |
| `agent.template`             | `GC_TEMPLATE`                                                        | `pgii-gastown.mayor`, `dog`  | high      | ~5-15 per host                  | Template the supervisor launched from. Useful to distinguish `pgii-gastown.mayor` from a future `other-city.mayor`.                                                                                                          |
| `agent.session_origin`       | `GC_SESSION_ORIGIN`                                                  | `named`, `ephemeral`         | high      | 2                               | Distinguishes pool slots (recycled) from permanent agents. Very useful for Grafana panels.                                                                                                                                   |
| `gascity.city`               | basename of `GC_CITY` (or last segment of path)                      | `gc`                         | high      | 1-3 per host                    | If host runs multiple cities. Skip if duplicate of `workspace.project`.                                                                                                                                                      |
| `gascity.pack`               | `GC_PACK_NAME`                                                       | `maintenance`                | med       | 1-5                             | Which pack scheduled this agent. Today only `maintenance` seen; rig-mode agents would carry a different pack.                                                                                                                |
| `gascity.runtime_epoch`      | `GC_RUNTIME_EPOCH`                                                   | `2`, `3`, `4`                | low       | grows monotonically per session | **DO NOT LABEL** — monotonic counter, would blow cardinality. Listed here so we explicitly reject it.                                                                                                                        |
| `gascity.continuation_epoch` | `GC_CONTINUATION_EPOCH`                                              | `1`, `2`                     | low       | grows                           | **DO NOT LABEL** — same reason as runtime_epoch.                                                                                                                                                                             |
| `gascity.rig.owner`          | future: from `gc rig show $GC_RIG --json`                            | `phillipg`                   | high      | 1-5                             | Only when `GC_RIG` is non-empty (rig-mode). Needs Dolt at decorator runtime — costly. Defer.                                                                                                                                 |
| `gascity.mol_state`          | future: from `gc bd show $GC_SESSION_ID --json` → metadata.mol_state | `running`, `nudging`, `done` | med       | ~5-10                           | Per-tick shell out to Dolt. **Discuss before implementing** — every tick spawns a `gc` subprocess.                                                                                                                           |
| `workspace.project_owner`    | not derivable from env alone (would need a per-rig owner map)        | `phillipg`                   | high      | 1-5                             | Defer until rig-owner mapping exists; could be a static lookup in decorator config.                                                                                                                                          |

### Hard "no" list (explicitly rejected)

| Field                   | Why rejected                                              |
| ----------------------- | --------------------------------------------------------- |
| `GC_SESSION_ID`         | Unbounded — one per session ever                          |
| `GC_INSTANCE_TOKEN`     | Unbounded — random per spawn                              |
| `BEADS_ACTOR`           | Same as `GC_SESSION_NAME` for dogs; redundant + unbounded |
| `GC_RUNTIME_EPOCH`      | Monotonic counter                                         |
| `GC_CONTINUATION_EPOCH` | Monotonic counter                                         |
| `GC_DIR` / cwd          | Already exposed via session.CWD; no useful label value    |

### Open questions for Phil + Claude

1. **Fallback `workspace.scope=gascity` when GC_RIG is empty:** safe to
   add? Today no GC sessions get `workspace.scope=gascity` because
   `gascity.go` keys off `GC_RIG`. Either fix the detector or have the
   decorator fill the gap. Decorator side is reversible if we change
   our mind.
2. **`agent.role` normalisation:** is it OK for the decorator to
   _overwrite_ the built-in detector's `agent.role` (which is the raw
   `GC_AGENT`) with a normalised value (`mayor` vs
   `pgii-gastown.mayor`)? The `Merge` semantics say the argument wins,
   so order in the daemon's detector list matters. Need confirmation
   that decorator order puts this after the built-in detector.
3. **`gascity.mol_state`:** is the Dolt round-trip per tick acceptable?
   Alternatives: (a) read mol_state from a file the deacon writes
   periodically; (b) skip mol_state until we move to a push model.
4. **`agent.template` vs `agent.role`:** are both worth emitting, or
   does the template carry no extra signal beyond `<city>.<role>`?

## Sample decorator-stdin Session JSON

Reconstructed from observed env + `labels.Session` struct shape:

```json
{
  "ID": "gc-5z36",
  "PID": 25155,
  "CWD": "/Users/phillipg/gc/.gc/agents/deacon",
  "Env": {
    "GC_AGENT": "pgii-gastown.deacon",
    "GC_ALIAS": "pgii-gastown.deacon",
    "GC_CITY": "/Users/phillipg/gc",
    "GC_PROVIDER": "claude",
    "GC_TEMPLATE": "pgii-gastown.deacon",
    "GC_SESSION_ID": "gc-5z36",
    "GC_SESSION_NAME": "pgii-gastown__deacon",
    "GC_SESSION_ORIGIN": "named",
    "GC_PACK_NAME": "maintenance",
    "GC_RIG": "",
    "GC_DIR": "/Users/phillipg/gc/.gc/agents/deacon"
  },
  "Model": "claude-opus-4-1"
}
```

The decorator must check `PA_MONITOR_DECORATE=1` env (per
`packages/pa-monitor/internal/labels/decorator.go`), then read this
struct from stdin, and write `{"labels":{...}}` to stdout.

## Next steps after review

1. Resolve the open questions above.
2. Implement the label mapping in `main.go` (currently scaffold).
3. Add a config file for the static rig-owner map (if we keep
   `workspace.project_owner`).
4. Wire the decorator into the pa-monitor `config.toml`
   `[decorators]` table in the consumer flake (this repo only ships
   the binary; the consumer points pa-monitor at it via /nix/store path).
