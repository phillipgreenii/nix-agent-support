# pg-desk — freshness

`pg-desk` reports how old the data behind its views is, per source, so a quiet menu bar or an
empty list is never mistaken for an all-clear when the origin has simply stopped answering. The
**freshness of a source** is the time of its last SUCCESSFUL origin fetch. It is distinct from
two things that are easy to confuse with it:

- **A row's `as_of`.** Most rows are old in a healthy, event-driven store; `as_of` says when the
  row last changed, not whether the origin is still being read.
- **Pipeline liveness.** `pg_desk_dashboard_stale` and `pg_desk_dashboard_age_seconds`, and the
  dashboard payload's `generated_at`, `age_seconds` and `stale`, are derived from
  `meta.last_heartbeat` and say whether the heartbeat is still running. A source can be stale
  while the pipeline is live, and the other way round.

A **source** is a `(backend, query)` pair known to the connector's ledger. The user-visible unit
is the backend: its **display label** defaults to the backend name without the `pg-connector-`
prefix (for example `pr-github`), and its age is the OLDEST age among its queries, which is the
conservative reading, judged over the queries something still polls (see
[Abandoned rows](#abandoned-rows)).

```mermaid
flowchart LR
    O["Origin API"] -->|"successful fetch only"| C["pg-connector"]
    C -->|"writes refreshed_at, last_error"| L[("connector ledger")]
    L -->|"pg-connector ledger show<br/>local, no network"| H["pg-desk heartbeat"]
    H -->|"meta source_fetch.*"| S[("pg-desk store")]
    S --> F["pg-desk freshness"]
    S --> D["dashboard sources[]"]
    S --> M["pg_desk_source_age_seconds"]
```

## Recording

`pg-desk heartbeat` reads `pg-connector ledger show` (a local read, zero origin cost) and records,
per ledger key, the last success and the last error under the meta key
`source_fetch.<backend>.<query>` (with `@<instance>` appended to the query part for a ledger that
carries an instance). Two ledgers that map to the same key, one per entity type, are folded
conservatively: the older success wins.

- **Monotonic.** A recorded success time never moves backwards, and the recorded last error is the
  later of the stored and the incoming one. A stale or reordered read, or a ledger that was
  cleared, cannot make a source look less fresh than it was already known to be fresh, or fresher.
- **Idempotent.** Recording the same ledger twice changes nothing.
- **A source with no success yet is still recorded**, so it is reported as unknown and stale
  rather than being absent.
- **The liveness stamp does not depend on the connector.** `heartbeat` stamps
  `meta.last_heartbeat` first. If `pg-connector ledger show` is missing, slow (30 seconds) or
  answers something undecodable, `heartbeat` names that on stderr, leaves the previously recorded
  source times to age, and still exits `0`. Only a store that cannot be written fails it.
- **The last poll is recorded too.** Alongside the success and the error, each record carries
  `last_seen_at`: the newest `last_seen` among the row's consumers in `ledger show` (the
  `pg-router` consumer stamps it on every poll, successful or not). It is monotonic like the
  success time, and absent when the ledger carries none.
- **Reporting lag** is at most one heartbeat period (60 seconds by default), far below the
  threshold.

## Abandoned rows

The ledger keeps a row for every `(backend, query[, instance])` ever polled, so a retired
instance or query leaves a **ghost**: a row that never succeeded (or last did long ago) and that no
consumer polls any more. Left alone it would hold its backend stale for ever, even though every
instance still polled is fresh.

A recorded row is **abandoned** when its `last_seen_at` is OLDER than 7 days (exactly at the bound
is not yet abandoned; `AbandonedAfter` in `internal/freshness`). A live consumer stamps `last_seen`
every poll, so the bound is orders of magnitude past any live cadence and past a weekend or a long
sleep, and it is deliberately far above the 15-minute stale threshold: that one judges a polled
source, this one judges whether anything still polls it.

- **A backend is judged by the rows that are not abandoned.** Abandoned rows are ignored, whether
  or not they ever succeeded. Nothing is deleted from the ledger or the store; the rule is applied
  on every read, so the indicator heals itself once a row has been unpolled for 7 days and heals
  again, with no operator action, if something resumes polling it.
- **A row polled recently that has never succeeded is NOT a ghost.** A connector that is live but
  failing (for example `thread-slack`, every fetch truncated) keeps reading unknown and stale.
- **A row with no known `last_seen_at` is not abandoned.** Absent evidence of a ghost, it counts.
- **If every row of a backend is abandoned, they all count.** Nothing polls the backend any more,
  and the indicator MUST NOT turn a dead poller into an all-clear or make the source vanish.

## Where the ledger's times come from

pg-desk's reader of the ledger does not change. What changes is who stamps the ledger for a backend
that owns its changes (the daemon-backed GitHub backend for `pr` and `ci`): that backend returns, on
every forwarded `changes` answer, a per-query `sources_freshness[]` carrying the time of its last
whole-query origin answer and its last error, and the connector's umbrella stamps the ledger's
`refreshed_at` and `last_error` from it, and the consumer's `last_seen` on every forwarded call.

- **The ledger keys stay `(backend, query)`.** No row becomes a ghost and the abandoned-row rule
  (INV-FRESH-6) keeps working from the stamped `last_seen`.
- **A cache answer does not advance freshness** (INV-FRESH-2). The backend counts only a whole-query
  answer from the origin as a success.
- **pg-desk still stamps nothing of its own.** It reads the ledger and records the success, the
  error and the last poll exactly as in "Recording".

## Surfaces

- **`pg-desk freshness --json`.** Read-only and offline: it reads the store and, for the threshold
  and labels, the configuration. Output is always one JSON document, `schemaVersion` 1, evolving
  additively only: `now`, `stale_after_seconds` (the default threshold), `any_stale` and
  `sources[]`. Each source has `source` (the backend name), `label`, `last_success_at`,
  `age_seconds` and `stale`; `last_success_at` and `age_seconds` are `null` for a source with no
  recorded success. Rows are sorted by `source`; `sources` is `[]` when nothing is recorded, and
  `any_stale` is then `false`. A configuration that cannot be loaded degrades to the defaults and
  is named on stderr, so the indicator never disappears into a false all-clear. Exit `0` whenever
  the store is readable; `1` otherwise.
- **`GET /api/v1/dashboard`.** An additive `sources[]` with the same row shape. Every existing
  field is unchanged, including the pipeline-liveness fields.
- **`pg_desk_source_age_seconds{source}`** on `/metrics` (a gauge, seconds; `source` is the backend
  name). A source with no recorded success exports NO series: it is never exported as `0`, which
  would read as freshly fetched. `pg-desk freshness` reports such a source as unknown and stale.

Consumers decide how to show it: the menu bar and the My Work dashboard show a row only for a stale
source (those are separate beads in the repos that own them). Staleness is never an attention item
(see [`attention.md`](attention.md), `ATTN-0`).

## Configuration

| Key                                    | Default                 | Meaning                                                                                             |
| -------------------------------------- | ----------------------- | --------------------------------------------------------------------------------------------------- |
| `freshness.source_stale_after`         | `15m`                   | A source is stale when its last success is OLDER than this (exactly at the bound is not yet stale). |
| `freshness.sources.<name>.stale_after` | global                  | Per-source threshold. `<name>` is the backend name or the name without the `pg-connector-` prefix.  |
| `freshness.sources.<name>.label`       | prefix-stripped backend | Display label.                                                                                      |

Durations accept the day form (`1d`, `1d12h`) and MUST be positive; an invalid value fails
configuration load naming the key. The key is deliberately not the top-level `stale_after`, which
nothing reads.

**Threshold.** 15 minutes is 15 poll periods at 60 seconds, or 7.5 at 120 seconds, so several
consecutive failed ticks, such as a short stretch of rate-limit refusals, do not raise the
indicator, while a sustained outage does within a quarter of an hour. It is below the 30-minute
reconcile cycle, so the indicator appears before the slow recovery path would have repaired
anything.

## Invariants

- **INV-FRESH-1.** Freshness MUST be the time of the last successful fetch from the origin system,
  never a per-row `as_of`.
- **INV-FRESH-2.** An answer served from a cache MUST NOT advance a source's freshness. This is
  upheld at the connector, which stamps the ledger only for a completed origin fetch (see the
  connector's `INV-LEDGER-FRESH-2`); `pg-desk` only copies what the ledger holds and MUST NOT
  stamp a success of its own.
- **INV-FRESH-3.** A fetch counts as successful only when the origin answered for the whole query
  (no `unavailable`, no truncation, no partial page). Also upheld at the connector (see
  `INV-LEDGER-FRESH-1` and `INV-LEDGER-FRESH-3`): a partial answer is recorded as a last error,
  never as a success.
- **INV-FRESH-4.** A source with no recorded success MUST be reported with `age_seconds: null` and
  `stale: true` (fail closed), on the verb and on the dashboard, and MUST export no
  `pg_desk_source_age_seconds` series. A backend one of whose queries has no recorded success is
  reported the same way. This holds for a row something still polls; an abandoned row is handled
  by INV-FRESH-6.
- **INV-FRESH-5.** `pg_desk_dashboard_stale` and `pg_desk_dashboard_age_seconds` keep their meaning
  (pipeline liveness, from `meta.last_heartbeat`) and their polarity (`0` fresh, `1` stale). Their
  descriptions and [`serve.md`](serve.md) MUST say that they are liveness and not data age.

- **INV-FRESH-6.** A ledger row that no consumer has polled for longer than 7 days (an abandoned
  row) MUST NOT make its backend stale or unknown while the backend has any row that is not
  abandoned, whether or not the abandoned row ever succeeded. A row polled within that window that
  has no recorded success MUST still read unknown and stale (INV-FRESH-4). When every row of a
  backend is abandoned they MUST all count. Applying the rule MUST NOT delete a ledger file or a
  store row.
- **INV-FRESH-7.** For a backend that owns its changes, the ledger times pg-desk reads MUST come from
  that backend's own record of its last whole-query origin answer and last error, and a cache
  answer MUST NOT advance them.

## Telemetry and logs

`pg-desk freshness` emits nothing over OpenTelemetry or Prometheus and has no structured-log
contract; on failure it prints ordinary CLI error text on stderr. `serve` exports
`pg_desk_source_age_seconds` (see [`serve.md`](serve.md)). No alert rule is shipped on it: the menu
bar and My Work indicators already surface it, and a Grafana rule is a one-line follow-up if one is
wanted.
