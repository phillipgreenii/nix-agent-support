# SQLite stores open through one shared helper with one standard pragma set

**Status**: Proposed (agent-proposed, operator review pending)
**Date**: 2026-11-13
**Deciders**: Phillip Green II

## Context

Five Go programs in this repository keep a SQLite store through the pure-Go `modernc.org/sqlite`
driver, and each one composed its own connection string:

| Program                   | Pragmas it set                                                                     |
| ------------------------- | ---------------------------------------------------------------------------------- |
| `pg-desk`                 | `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`; one connection only |
| `pa-monitor`              | adds `synchronous(NORMAL)`                                                         |
| `claude-tool-tracker`     | only `busy_timeout` and WAL                                                        |
| `work-report`             | adds `_txlock=immediate`                                                           |
| `pg-claude-code-recorder` | adds `cache_size`, `mmap_size`, `wal_autocheckpoint` and runs `PRAGMA optimize`    |

There was no written standard, so a new store copied whichever sibling the author saw first. The cost
showed in `pg-desk`: a 115 MB store was read through the driver's default page cache of about 8 MB, so
every pass over the table went to disk, and every read shared the one connection with the writers. A
profile of the running daemon on 2026-11-12 put about 76 percent of its CPU samples under the metrics
handler for that reason.

## Decision

1. **One helper opens every SQLite store.** The module `packages/pg-sqlite` (package `sqliteopen`) owns
   the DSN, the pool size and the read-only variant. A program MUST open its store through
   `sqliteopen.Open` and MUST NOT compose a `file:...?_pragma=` string of its own. A program that needs a
   different value for one field sets that field in `sqliteopen.Options` and MUST put the reason next to
   the override.
2. **The standard writer pragma set**, applied to every connection through the DSN:

   | Pragma               | Value                 | Reason                                                                               |
   | -------------------- | --------------------- | ------------------------------------------------------------------------------------ |
   | `journal_mode`       | `WAL`                 | readers do not block the writer                                                      |
   | `synchronous`        | `NORMAL`              | in WAL mode it cannot corrupt the store; a power loss may lose only the last commits |
   | `busy_timeout`       | `5000` ms             | a daemon, the CLI and a test can overlap without "database is locked"                |
   | `foreign_keys`       | `ON`                  | SQLite leaves it off by default                                                      |
   | `cache_size`         | `-65536` (64 MiB)     | a store of a hundred megabytes stays cached instead of being re-read on every pass   |
   | `mmap_size`          | `268435456` (256 MiB) | serves reads from the mapped file without copying pages through the cache            |
   | `temp_store`         | `MEMORY`              | sorts and temporary indexes stay off the disk                                        |
   | `journal_size_limit` | `67108864` (64 MiB)   | a checkpoint truncates a WAL that one large transaction grew                         |

3. **A reader that does not write opens a read-only pool.** `sqliteopen.ReadOnly(path, n)` opens an
   existing file with `mode=ro` and `query_only(ON)`, with a pool of `n` connections (default 3), the
   same busy timeout, cache, mmap and temp-store values, and no journal-mode change. A long read pass
   (a metrics build, a status read) therefore uses its own connections and no longer queues behind the
   writer's one connection.
4. **The single writer connection stays.** A writer pool is `1` connection unless a program records why
   it needs more. SQLite allows one writer at a time, and the driver serializes correctly on one
   connection.
5. **Per-connection state is the test's business.** `sqliteopen.Effective` reads the pragmas in force on
   a connection, and each program's tests MUST prove the standard set is applied to its writer and, when
   it has one, to every connection of its read-only pool, against real SQLite.
6. **Existing programs migrate one at a time.** `pg-desk` adopts the helper with this decision. The other
   four programs in the context table migrate in their own changes; until then each keeps its current
   connection string, and a migration MUST NOT change the schema or the data.

## Consequences

- The memory cost of the standard set is bounded: up to 64 MiB of page cache per connection plus the
  mapped file, which is reclaimable. A program with many connections MAY lower `CacheKiB` and MUST record why.
- `synchronous(NORMAL)` is weaker than the driver default (`FULL`) for the commits just before a power
  loss. Every store in this repository is derived from or reconstructible by a daemon that re-reads its
  sources, so a lost tail is acceptable; a store that is the only copy of its data MUST set
  `Synchronous: "FULL"` and say so.
- The module is a local `replace` target of each consumer, like `pg-connector`, so a consumer's nix
  fileset names `packages/pg-sqlite`.
- A migrated program reports the same behavior except for speed and the extra memory above.
