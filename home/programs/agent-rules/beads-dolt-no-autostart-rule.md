## Beads / Dolt: no rogue auto-start (machine invariant)

This machine forbids **transparent dolt auto-start**. The machine-wide `bd`
exports `BEADS_DOLT_AUTO_START=0`, so `bd` MUST NOT silently spawn its own
`dolt sql-server`.

**Guidance:**

- You MUST NOT start a dolt server on your own initiative. In particular, you
  MUST NOT run `bd dolt start` casually — start a dolt server only for a
  deliberate, isolated test, never against the machine's real beads data.
- You MUST NOT install editor/IDE beads extensions that poll `bd dolt status`
  and auto-run `bd dolt start` (e.g. `planet57.vscode-beads`); they are a
  classic rogue-server cause.
- Any daemon or timer that shells out to `bd` MUST inherit the machine's `bd`
  (the overlay wrapper), so it gets `BEADS_DOLT_AUTO_START=0` — do not bypass it
  with a bare `bd` from an unmanaged environment.
