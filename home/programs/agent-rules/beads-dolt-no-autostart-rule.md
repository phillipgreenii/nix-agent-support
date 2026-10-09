## Beads / Dolt: no rogue auto-start

The machine-wide `bd` exports `BEADS_DOLT_AUTO_START=0`; `bd` MUST NOT spawn its own `dolt sql-server`.

- MUST NOT start a dolt server on own initiative or run `bd dolt start` casually (only for a deliberate, isolated test, never against real beads data).
- MUST NOT install editor/IDE beads extensions that auto-run `bd dolt start` (e.g. `planet57.vscode-beads`).
- Any daemon/timer calling `bd` MUST use the machine's wrapped `bd` (so it inherits `BEADS_DOLT_AUTO_START=0`), never a bare unmanaged one.
- A `bd` write refusing with "pending schema migrations" is not a rogue server; see the
  `phillipg-nix-ziprecruiter` repo's `docs/beads-1-3-1-schema-migration.md` and stop for the operator.
