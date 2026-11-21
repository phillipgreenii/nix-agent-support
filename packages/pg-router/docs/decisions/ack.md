# Acknowledgement — pg-router decision docs

Realization decisions about how a cursor-feed source learns what the core has finished with: the
settlement report, the opaque lane and token on an event, the frontier, and the bounded hold. The
behavior side is `INTF-SOURCE` ("Acknowledgement of a cursor-feed source"), `INV-EVT-3` (lane
retention) and `INV-EVT-10`. The metric names are recorded in `DEC-OBS-15`.

### `DEC-ACK-1` — the settlement report is a Command invocation <!-- uuid: fd613180-7e70-48b9-b985-fbaf9e52e0e9 -->

**Decided.** The core reports settlement by running a second opaque command declared on the query
(`settle`), in the way it already runs `command`. This keeps "adding a source is configuration" and the
one-opaque-invocation rule of `INTF-SOURCE`: the core does not learn what the source does with the
report. A source with no `settle` command is not a cursor-feed source and behaves exactly as today: on a
query that declares no `settle`, the core ignores an `ack` object on an event (no ledger, nothing held,
nothing run).

### `DEC-ACK-2` — lane and token are opaque strings on the event <!-- uuid: ca966f10-3875-44b0-b8bc-b0453efe2dfd -->

**Decided.** The core needs to know which events of one query belong to the same ordered feed, and how
far it may report. Both are supplied by the source as opaque strings on the event (`ack.lane`,
`ack.token`), so the core never interprets a source's cursor. The core does not learn that a lane is a
backend or that a token is a log position, and it does not order tokens against one another: order is
the order of emission within a lane.

### `DEC-ACK-3` — the frontier, not each event, is reported <!-- uuid: f141324f-f6fa-40fb-ad5b-2010e5cc65b4 -->

**Decided.** A cursor is a position, not a set. Reporting only the frontier is also what keeps the
report cheap and idempotent, and the report carries no list of settled ids, so a source never has to
hold a set.

### `DEC-ACK-4` — a lane's frontier is held for a bounded time <!-- uuid: b637511d-3407-4cfb-bbe0-72ba63da2982 -->

**Decided.** A lane's frontier MUST NOT be held back longer than the query's `ack_hold_limit`. Past it,
the oldest unsettled event is abandoned, counted and logged, never silently. The default is **24
hours**, which stays below the 14 day log retention. Resetting a consumer's cursor is an operator step;
the adapter does not signal it to the core.
