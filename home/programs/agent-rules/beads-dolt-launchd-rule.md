## Beads / Dolt: the shared local launchd server (Mac-local)

Exactly one local dolt server is legitimate on this machine: the shared
per-user launchd agent `org.nixos.beads-dolt-server` (`keepAlive=true`), which
owns `127.0.0.1:25252` and serves the real data under
`~/.local/share/beads-dolt`. The shared daemon is already running; an explicit
start spawns a competitor that races it for the port. Never start a dolt
server against the shared data or on port 25252.

**The smell** (a second, config-less `dolt sql-server` is stealing the port):

- a `dolt sql-server` restart-loop, or `org.nixos.beads-dolt-server` churning its
  PID with `LastExitStatus=256` / "Port 25252 already in use";
- `bd` errors like "database not found", or `bd` returning near-empty results
  (it is talking to a stray, near-empty workspace-local DB, not the shared one).

**When it breaks:** use the `beads-dolt-doctor` skill — it has the recognition
signature, the config use-case matrix, and the step-by-step debugging playbook
(find the rogue process, the port holder, the caller) plus how to confirm data
safety before killing anything.

**Not a rogue server:** a `bd` WRITE that refuses with "refusing to auto-apply N pending
schema migrations" (reads still work) means the shared database awaits an operator-run
`bd migrate schema`. Do not start or restart dolt and do not run the rogue-hunting
playbook; stop and tell the operator. During a migration window a second dolt server on
port 25253 is the operator's migrator, not a rogue (check `pg-router gate list` for
`BEAD_SERVER_DOWN` first). Runbook: `docs/beads-1-3-1-schema-migration.md` in the
`phillipg-nix-ziprecruiter` repo.
