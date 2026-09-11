# Actors — prpool-ccpool-handler

Who this module interacts with. A behavior docs set MUST define all of its actors.

## System actors (participants behind interfaces)

- **`ACTOR-CCH-CORE` — pg-router core** <!-- uuid: d2e45343-0bc7-4625-9172-e55518cd38cf --> — the
  generic dispatcher this module registers with (`phillipgreenii-nix-agent-support` ADR 0036:
  nothing here auto-starts a core). Drives this module over `INTF-HANDLER` (dispatch) and is
  driven by this module over `INTF-SOURCE` (query/ingest). Defined and owned by
  `packages/pg-router/docs/behavior/actors.md`; named here only as this module's counterparty.

## Boundary counterparties (named, internals undescribed)

Per this set's floor, each backing tool is named as a **boundary** this module hands work across —
not as a system actor with its own catalog, because its internals are out of this set's extent.

- **`ccpool`** — the CLI a ccpool-backed handler session invokes to run and observe an agent
  session; its own behavior docs (`packages/ccpool/docs/behavior`) own that internal behavior.
- **A configured command** — an operator-named argv a command-backed handler session execs; this
  module never interprets what it does, only that it ran and how it finished (the "boundary
  principle" pg-router's own README states, which binds this module the same way it binds the core).
- **`bd` (beads)** — the issue-tracker CLI/store a beads-backed source queries and a handler
  session's completion policy may write to.
- **`pg-pr`** — the CLI a handler session's ACL check consults.
