---
name: pg-router-wiring-testing
description: Isolated pre-land smoke-test procedure for a new/changed pg-router `[[query]]` (emitter) or `[[role]]`/ccpool-handler role (listener) — use `run-query`/`run-role` against a scratch config+state to catch wiring bugs before they ever reach the live daemon.
paths:
  [
    "packages/pg-router/**",
    "packages/*-probe/**",
    "home/programs/pg-router-ccpool-handler/**",
    "home/programs/*-probe/**",
    "home/programs/pg-router/**",
  ]
---

# pg-router wiring smoke test (pre-land, isolated)

Moved out of the always-on `CLAUDE.md` (this detail only matters while wiring a new pg-router
emitter/listener). Motivating incident: `pg2-93e5s`/`pg2-cjwfu`/`pg2-61i00`/`pg2-84i8o`
(2026-09-23) — a docket landed, `nix flake check`-clean, independently reviewed, and STILL
shipped two silently-broken wirings (a role invoked with zero of its required flags; a
ccpool-type role that could never launch a session) plus a cross-tracker dispatch collision —
none visible from source, all found only by actually running the wiring live, after activation.
This procedure runs the equivalent checks BEFORE that point, in complete isolation from the live
system, so the same class of bug is caught while it's still cheap to fix.

## Why `nix build`/`nix flake check` are not enough

They prove the config **evaluates** and the binary **compiles**. They do not invoke the wired
command with real arguments, do not launch a real ccpool session, and do not check that a role's
_actual_ invocation matches what you intended — see this repo's own `CLAUDE.md` "pg-router config
testing trap" and the two bullets after it (the config.toml-vs-`handlerCommandDir` split, and why
build-clean does not mean live-working). A broken wiring of this kind fails **silently** — no
crash, no alert, nothing to grep for — so the only way to know it works is to actually run it.

## Procedure

### 0. Build the real artifact

`nix build .#<pkg>` for the command-backing binary. Note the store path — the scratch config
below MUST reference this real path, not a placeholder.

### 1. Point everything at scratch, never the live daemon's own state

```bash
export PG_ROUTER_CONFIG=/tmp/pgr-smoketest/config.toml
export PG_ROUTER_LOG_DIR=/tmp/pgr-smoketest/state
```

`PG_ROUTER_LOG_DIR` is not optional here: its default is `~/.local/state/pg-router` — the SAME
gates/discovery-record/`events.jsonl` the live daemon uses. Omitting this override means your
"isolated" test is reading and writing live production state.

Write `$PG_ROUTER_CONFIG` with ONLY the new `[[query]]`/`[[role]]` pair, plus (per the existing
config-testing-trap) **at least one `[[role]]`** — a query-only config silently falls back to the
built-in role set. Point the query's `command.argv` at the real store path from step 0 with the
REAL arguments you intend to ship — render them for real (copy the exact nix let-binding text, or
`nix eval` the relevant attribute) rather than hand-typing an approximation that could itself
drift from what ships.

### 2. Test the emitter (the query) in isolation

```bash
pg-router run-query --json query:<name>
```

Expect a match count you can explain. A `0` is only a pass if you know _why_ it's zero (no current
input to detect) — if it's zero because a required flag/env var was never wired, that is exactly
today's `pg2-cjwfu` bug, and this step is where it should have been caught.

**Negative control, mandatory** (mirrors the existing binary-hash negative control in the
config-testing-trap paragraph): deliberately break ONE required input the real argv depends on
(omit a flag it actually passes, or point it at an empty scratch target) and confirm the output
changes. If breaking the input produces the same result as the working case, this test isn't
exercising anything.

### 3. Test the listener (the role) in isolation

Take one real match's JSON from step 2 and:

```bash
pg-router run-role --json <role> '<event-json>'
```

**Command-type role**: confirm the backing command's real side effect actually happened, against
a SCRATCH target (never the live/real tracker at this pre-land stage).

**ccpool-type role**: `run-role`'s own report is NOT sufficient — a "delivered" outcome with no
wireclient error can still mean the ccpool session never reached `ready`. Separately confirm:

- `claude.plugin_dir` is configured for whatever ccpool pool this role's handler actually
  dispatches into (`phillipgreenii.programs.ccpool` enabled for that consumer) — the single most
  common way a brand-new ccpool-type role fails at launch, silently (`pg2-61i00`).
- The session actually reached a real state, not a launch-preflight error: `ccpool list`, and grep
  your scratch `$PG_ROUTER_LOG_DIR/events.jsonl` for `"kind":"dispatch"` entries naming the role —
  a `"level":"warn"` result carrying an `"error"` field is a FAIL even though the listener's own
  `delivered` counter still incremented.

### 4. Check for cross-wiring collisions

If more than one role/query could match the SAME identifier (bead id, event id, session id) —
e.g. two trackers' otherwise-identical named queries, both keyed only by that id with no
tracker/source qualifier — verify by construction that they cannot both match the same real item.
Don't rely on reading the nix source: `pg2-84i8o` (a bead in one tracker dispatched to the OTHER
tracker's role too) was found only by watching a live run, never by inspecting config. If
isolation state (a worktree, a lock, a session name) is keyed only by an identifier shared across
roles that could both fire on it, that's a race waiting to happen.

### 5. Land it, then do exactly ONE bounded live check post-activation

Steps 1-4 make the design's own "manual smoke test" requirement fast and low-risk instead of the
first live exercise. After activation, force one real finding through the LIVE system the same
way, watch it to a real outcome, and clean up immediately:

- Close/un-label the forced test item the moment you've observed the outcome — don't leave it in
  a state that keeps getting redispatched against production. Note: an unresolved "triage" outcome
  legitimately gets redispatched on the query's own period until the item's state changes — that's
  by design, not a bug, which is exactly why a synthetic test item must not be left in it.
- Remove any isolation artifact your test created (worktree, branch, ccpool session).
- Confirm the query's real target (tracker/beads-dir) and the daemon's own state dir show no
  residue before considering the packet done.

## Quick reference: finding what's actually deployed

- The live daemon's resolved config: `pg-router status` (shows `configPath=`) or
  `pg-router config --show` from the repo root it operates on.
- A role's REAL command/ccpool invocation (never visible in `config.toml`): find the running
  `pg-router-ccpool-handler dispatch --role-config <dir>/<role>.json` process
  (`ps aux | grep pg-router-ccpool-handler`) and `cat` that JSON file directly.
