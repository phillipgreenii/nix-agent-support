---
name: beads-dolt-doctor
description: >-
  Use when the beads/dolt server is misbehaving on this machine — the dolt
  sql-server keeps restarting or crash-looping, `bd` reports "database not
  found" or is talking to a near-empty database, or you need to find which
  process keeps starting dolt. Fires on intents like "beads/dolt server keeps
  restarting", "which process keeps starting dolt", "debug the beads dolt
  server", "rogue dolt sql-server", "port 25252 already in use",
  "org.nixos.beads-dolt-server is crash-looping", or "bd is hitting an empty
  database". Provides the recognition signature, the config use-case matrix (how
  each context gets its `bd` and whether it can auto-start a server), and the
  step-by-step debugging playbook: find the rogue process, the port holder, and
  the caller, then confirm data safety before killing anything. Do NOT use for
  routine bead CRUD (create/list/close), for grooming the backlog, or for
  generic dolt SQL questions unrelated to a competing server.
---

# beads-dolt-doctor

Diagnose the classic failure on this machine: a **second, config-less
`dolt sql-server`** binds `127.0.0.1:25252`, starving the legitimate shared
server, so `bd` reads the wrong (near-empty) database while the real server
crash-loops trying to reclaim its port.

## The invariant (what "healthy" looks like)

- Exactly **one** dolt server is legitimate: the shared per-user launchd agent
  **`org.nixos.beads-dolt-server`** (`keepAlive=true`), serving the real data
  under `~/.local/share/beads-dolt` (large: hundreds of MB to tens of GB).
- It owns `127.0.0.1:25252`. Everything that runs `bd` talks to it.
- The machine-wide `bd` exports `BEADS_DOLT_AUTO_START=0`, so no context
  _transparently_ auto-starts a server. A rogue server therefore comes from an
  **explicit** `bd dolt start` (or a bare `dolt sql-server`) by some caller.

## Recognize the failure

Signature of the 2026-07-24 incident (and its class):

- **Two** `dolt sql-server` processes on port 25252 — one started with a
  `--config` (the shared server), one **config-less** rooted at a workspace-local
  `./.beads/dolt` (a tiny, near-empty DB).
- `org.nixos.beads-dolt-server` churns its PID; `launchctl` shows
  `LastExitStatus = 256` and the log says **"Port 25252 already in use"** — the
  daemon is crash-looping because the rogue holds the port.
- `bd` commands return "database not found" or near-empty results (they hit the
  rogue's empty DB, not the shared data).

## Config use-case matrix — how each context gets `bd`

See `references/use-case-matrix.md`. Summary: the **overlay `bd` wrapper** is the
only mechanism common to every context (shells, GUI apps, per-user launchd
agents, root daemons); `home.packages` / `home.sessionVariables` miss the
launchd/daemon rows. So a caller that resolves `bd` from the overlay inherits
`BEADS_DOLT_AUTO_START=0`; a caller running a **bare/unmanaged** `bd` or invoking
`dolt sql-server` directly does not — that is where rogues come from.

## Debugging playbook

Full step list with commands in `references/debugging-playbook.md`. The shape:

1. **Enumerate the dolt processes** (config vs config-less, start times).
2. **Find the port holder** on 25252.
3. **Check the shared agent's health** (PID churn, `LastExitStatus`).
4. **Read both dolt-server logs** to separate the real server from the rogue.
5. **Narrow the caller** — enumerate launchd jobs and `bd`-shelling daemons,
   Claude hooks, editor/IDE beads extensions; run a fast `ps` **spawn-catcher**
   to catch the ephemeral `bd`/`dolt` and record its PPID + parent command; shut
   down suspects one at a time.
6. **Confirm data safety BEFORE killing** — compare DB sizes (`du -sh`) and
   `bd stats` for the shared vs the local DB, so you never kill the real server
   or lose data.

## Fix

Remove/guard the caller (e.g. the VS Code extension), ensure the machine-wide
`bd` carries `BEADS_DOLT_AUTO_START=0`, kill the rogue, and let
`org.nixos.beads-dolt-server` rebind 25252. Holding the port with `keepAlive` is
a **mitigation** (it downgrades stray explicit starts to no-ops while it holds
the port), not a guarantee — restart/activation windows exist where the port is
briefly free.
