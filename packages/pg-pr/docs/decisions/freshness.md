# Freshness — decisions

### `DEC-FRESH-1` — the staleness bound is a multiplier of pg-pr's own sync interval <!-- uuid: 938c8f17-157e-41fa-899c-8452cfc367fb -->

**Decided.** The bound past which a read seam's as-of time is judged stale (behavior docs:
`../behavior/invariants.md` — `INV-ASOF-1`, `INV-ASOF-2`) is derived from pg-pr's own sync
cadence rather than a fixed wall-clock number: currently twice the configured sync interval
(`freshness.BoundIntervals = 2`; default sync interval 60s, so a default bound of 120s). Because
pg-pr owns the sync cadence, retuning either value is a config/code change here, never a
behavior-doc edit — the multiplier and the interval are implementation detail, not a contract
term. Code: `packages/pg-pr/internal/freshness/freshness.go` (`BoundIntervals`); consumed
identically by the machine read seam (`pg-pr pr list --json`) and the dashboard payload.

**Not decided here.** Whether the bound should ever become a per-repo or per-deployment tunable,
rather than a single process-wide constant, is the implementation's own to revisit; it does not
change the behavior-side obligation (`INV-ASOF-1`, `INV-ASOF-2`), only the number.

See also `docs/pr-review-flow.md` (JR3) for how the current implementation wires this bound into
`pr-pool`'s ACL.
