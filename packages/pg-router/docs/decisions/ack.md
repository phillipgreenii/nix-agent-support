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

### `DEC-ACK-5` — retained accepted-event ids: persistence, release and abandonment <!-- uuid: 035343c4-6f20-4f9a-9efc-826baec48c9d -->

**Decided.** The landed behavior satisfies every constraint of `INV-EVT-3` (lane retention) and
`INV-CONC-1`, so it is kept and pinned by tests (`internal/settle/retention_pin_test.go`, the restart
test first). The retained id set is not stored on its own: an id is retained while the lane ledger
lists it with an unsettled entry at or before it, and the ledger is durable, so the set is rebuilt from
it on load. The answers:

| Question                                                                                         | Answer                                                                                                                                                                  | Reason                                                                                                                                                                                                                        |
| ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| (a) Is the retained id set persisted across a restart?                                           | Yes, derived from the durable ledger on load.                                                                                                                           | A restart before the hold limit must not re-run a handler that already accepted the event; the queue replay restores the accepted record and the ledger restores the retention.                                               |
| (b) When is a retained id released?                                                              | When its lane frontier has passed it (it and every earlier event settled), or when the hold limit abandons it.                                                          | Settling a later event alone must not release an id, or the source's re-offer would re-run a handler while the frontier is still held.                                                                                        |
| (c) Does abandonment release only ids at or before the abandoned event, or every id of the lane? | Neither: it settles each unsettled entry older than the hold limit, by that entry's own age, wherever it sits; an id is then released only when the frontier passes it. | The hold limit is per event (measured from first sight), so a younger unsettled event before an abandoned one still holds the lane.                                                                                           |
| (d) Does a retained id survive a newer poll that no longer lists its event?                      | No. The newest poll replaces the lane's list and the dropped id is released, settled or not.                                                                            | The source's own cursor has moved past the event, so it will never be re-offered and retention has nothing left to absorb.                                                                                                    |
| (e) Is abandonment evaluated only inside frontier evaluation, or also on a timer?                | Only inside frontier evaluation (after a poll, on an accept, at restart); there is no timer.                                                                            | Polls recur at the query's interval, so a held lane is evaluated about that often; a lane whose source stops polling is not abandoned until the next evaluation, which is acceptable because nothing is re-offered meanwhile. |
