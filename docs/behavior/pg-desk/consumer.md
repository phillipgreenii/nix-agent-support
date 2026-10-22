# pg-desk — consumer lifecycle

A consumer is a named reader of the change log for one entity type (for example a menu bar plugin
that reads `pg-desk issue changes --consumer <name>`). Its registration is one `(name, type)` row holding a
cursor (the highest change record it has been handed and confirmed) and `seen_at` (when it last
read). Two verbs under each type group manage those rows (entity-change-flow design 6.9):
`pg-desk <type> consumer list` and `pg-desk <type> consumer forget <name>`, for `<type>` one of
`issue`, `thread`.

For `pr` pg-desk keeps no change log and no consumer rows: the daemon-backed backend owns the
change feed for `pr` and `ci`, so it owns the consumers' cursors and their retention, and pg-desk's
own PR change flow is retired (see [`changes.md`](changes.md), "Who owns change detection"). The
consumer verbs and everything below describe the change log pg-desk itself keeps. For `issue` that
is the log of EVERY backend: pg-desk is the single emitter of the issue change event, so it keeps
the log and its consumers' cursors whether the backend detects changes by list-and-diff (the beads
backend) or owns its changes and is consumed through its feed (the Jira backend). The change log
also serves `thread`.

The feed cursor pg-desk holds with an owning backend's feed is a separate thing from the consumers
listed here: it is the connector's cursor for the consumer named `pg-desk`, acknowledged only after
a commit (see [`changes.md`](changes.md), "Feed consumption for a backend that owns its changes").
It is not a row of this table.

```mermaid
flowchart LR
    R["consumer reads changes"] -->|"registers, stamps seen_at"| C[("consumer row")]
    C -->|"holds back pruning while fresh"| P["change_log pruning"]
    L["consumer list"] -->|"reads"| C
    F["consumer forget name"] -->|"deletes"| C
```

## Behavior

- `consumer list` prints the registered consumers of the group's type, ordered by name, one line
  each, two spaces between fields: `<name>  cursor=<n>  seen_at=<at>  <fresh|stale>`. `<at>` is
  the stored RFC3339 UTC time, or `never` for a consumer registered but never read. With no
  consumer it prints nothing and exits `0`. `--json` (or `PG_DESK_OUTPUT=json`) prints
  `{"consumers": [...]}` with `name`, `type`, `cursor`, `seen_at` (empty when never read) and
  `stale` per consumer.
- A consumer is **stale** when it was never read, or not seen within `consumer_stale_after`
  (default 7 days, see the configuration docs). This is the same rule change-log pruning applies:
  a stale consumer is excluded from the pruning horizon, so it no longer holds rows back.
- `consumer forget <name>` deletes that consumer's row for the group's type, and so stops it from
  holding back change-log pruning. It prints `forgot <type> consumer <name>`. Forgetting a
  consumer that is not registered is not an error: it prints that there was nothing to forget and
  exits `0`. A forgotten consumer that reads again is registered afresh at cursor `0`, so it
  replays the retained log.
- Neither verb moves a cursor, reads a record, or contacts the network. `list` is read-only;
  `forget` only deletes the one row.
- The consumer named `pg-desk`, under which pg-desk reads an owning backend's feed, is serialised
  across the feed fetch AND the commit of the rows it fetched, not only across the delivery step the
  per-`(type, name)` lock covers for every other consumer. Two concurrent polls therefore cannot
  fetch the same rows, and one cannot acknowledge a position past rows the other has not committed.

## Invariants

- **INV-CONSUMER-1.** <!-- uuid: 3c6de8dc-dd2f-4239-a83f-3125bfabecd3 --> `forget` MUST delete only the `(name, type)` row it names: a consumer of the
  same name under another type, and every other consumer, MUST be untouched.
- **INV-CONSUMER-2.** <!-- uuid: cf3da6a0-904b-4071-ad06-db5039c8bf6e --> After `forget`, change-log pruning MUST NOT wait for that consumer.
- **INV-CONSUMER-3.** <!-- uuid: 3a3afd38-0838-4d8d-96a1-66c6d6b01f8d --> `list` and `forget` MUST NOT register a consumer or stamp `seen_at`.
- **INV-CONSUMER-4.** <!-- uuid: ddb69252-819c-4dfe-93d0-1ee5bcacf22c --> For `pr` and `ci`, pg-desk MUST NOT hold a consumer's cursor: the backend that
  owns those changes holds it.
- **INV-CONSUMER-5.** <!-- uuid: cc386b66-fdc6-4aa0-9112-972655beaae0 --> For `issue`, pg-desk MUST keep the change log and its consumers' cursors for every
  backend, and the consumer named `pg-desk` MUST be serialised across the feed fetch AND the commit.

## Old-schema refusal

The consumer table exists only on the migrated store, so both verbs refuse an old-schema store with
the store's error, which says to run `pg-desk migrate --cutover`, and exit `1`.

## Exit codes

`0` on success (including an empty list and forgetting an absent consumer); `1` on any error.

## Out of scope

Flagging a stalled consumer (reported by `doctor`) and the consumer-lag metric.
