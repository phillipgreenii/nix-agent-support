## Beads / Dolt: no rogue auto-start (machine invariant)

This machine forbids **transparent dolt auto-start**. Exactly one dolt server is
legitimate: the shared per-user launchd agent `org.nixos.beads-dolt-server`
(`keepAlive=true`), which owns `127.0.0.1:25252` and serves the real data under
`~/.local/share/beads-dolt`. The machine-wide `bd` exports
`BEADS_DOLT_AUTO_START=0`, so `bd` MUST NOT silently spawn its own server.

**The smell** (a second, config-less `dolt sql-server` is stealing the port):

- a `dolt sql-server` restart-loop, or `org.nixos.beads-dolt-server` churning its
  PID with `LastExitStatus=256` / "Port 25252 already in use";
- `bd` errors like "database not found", or `bd` returning near-empty results
  (it is talking to a stray, near-empty workspace-local DB, not the shared one).

**Guidance:**

- You MUST NOT run `bd dolt start` casually. The shared daemon is already
  running; an explicit start spawns a competitor that races for the port.
  Start a dolt server only for a deliberate, isolated test — never against the
  shared data or on port 25252.
- You MUST NOT install editor/IDE beads extensions that poll `bd dolt status`
  and auto-run `bd dolt start` (e.g. `planet57.vscode-beads`); they are the
  classic rogue-server cause on this machine.
- Any daemon or timer that shells out to `bd` MUST inherit the machine's `bd`
  (the overlay wrapper), so it gets `BEADS_DOLT_AUTO_START=0` — do not bypass it
  with a bare `bd` from an unmanaged environment.

**When it breaks:** use the `beads-dolt-doctor` skill — it has the recognition
signature, the config use-case matrix, and the step-by-step debugging playbook
(find the rogue process, the port holder, the caller) plus how to confirm data
safety before killing anything.
