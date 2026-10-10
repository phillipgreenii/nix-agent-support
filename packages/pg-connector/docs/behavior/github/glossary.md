# Glossary — pg-connector-github

Vocabulary for the daemon-backed GitHub backend. A term the parent set already defines (backend,
daemon, daemon-backed backend, `owns_changes`, `cache_opt_out`, `sources_freshness[]`,
`last_seen`, `served_from`, `age_seconds`) is **inherited** with its parent meaning
(`INV-20`), cited from `phillipgreenii-nix-agent-support · packages/pg-connector/docs/behavior`,
and is not redefined here. Where this set needs a different meaning it takes a different name.

## Process and placement

- **Client mode** — the way the backend answers one umbrella call: it reads one request, forwards it
  to the daemon, and writes the daemon's answer. It never opens the store and never calls the
  origin (`INV-GH-PROC-1`). A **Proxy** in front of the daemon.
- **Rate-limit domain** — one credential on one host: the unit one daemon serves, because it is the
  unit the origin meters. A second credential or a second host is a second domain and a second
  daemon.
- **Store** — the persistent copy of everything the daemon holds: both caches, the change log and
  the consumer positions. The daemon is its only writer (`INV-GH-PROC-1`), and it survives a
  restart (`INV-GH-FRESH-6`). It never holds a credential (`INV-GH-FRESH-11`).
- **Supervisor** — whatever starts the daemon and starts it again when it exits. It does nothing
  else (`INTF-GH-SUPERVISION`). Its restart hint is part of the `unavailable` answer
  (`INV-GH-PROC-2`).
- **Instance** — which of the two capabilities a call is for, `pr` or `ci`. One daemon serves both
  instances; the registry keeps one name per instance.
- **Origin** — the upstream system the backend reads and writes: GitHub. This is the parent set's
  `served_from: origin` meaning, and is unrelated to a change row's `origin` field (see **Change
  cause**).

## Caches and freshness

- **Query cache** — the daemon's map from a query to its ordered list of entity ids and nothing
  else, with a complete flag saying whether the listing was whole (`INV-GH-FRESH-1`).
- **Entity cache** — the daemon's map from an entity id to the full object, held as field groups
  (`INV-GH-FRESH-1`).
- **Field group** — a set of an entity's fields fetched, refreshed and expired together on their own
  terms. Every field belongs to exactly one group. The pull-request groups are **summary** (what a
  listing shows, plus the branch names and base commit), **detail** (merge state, review requests,
  size counts, merged), **conversation** (reviews, review threads with comments, issue comments),
  **files**, **commits** and **pending** (the acting identity's pending review); the CI group is
  **runs** (workflow runs on the PR's branch, with job results for non-passing runs on the current
  head). A **Strategy** per group.
- **Freshness window** — the interval, per group, within which the stored content counts as current
  and is served with no origin call. A group outside its window is **stale**.
- **Hard age** — the longest a group may stay current by revalidation alone: after it the group is
  fetched again even if nothing appears to have changed (`INV-GH-FRESH-4`).
- **Expiry** — the age after which an entity none of whose groups has been fetched is evicted from
  the entity cache; change rows outlive it and are kept for their own retention
  (`INV-GH-FRESH-3`).
- **Keep-alive set** — the entities the daemon refreshes in the background: members of a configured
  query, and any entity accessed within the keep-alive window (`INV-GH-FRESH-3`).
- **Local hit** — a read answered from the store with no origin call, because every group it needs
  is inside its freshness window (`INV-GH-FRESH-2`).
- **Single-flight** — concurrent requests for the same (entity, group) share one fetch
  (`INV-GH-FRESH-2`).
- **Fingerprint** — a digest of an entity's summary content, compared with the previous summary to
  decide whether dependent groups need refreshing (`INV-GH-FRESH-4`).
- **Revalidation** — comparing a fresh summary with the stored one, and, when the fingerprint is
  unchanged, extending the freshness window of the dependent groups instead of fetching them
  (`INV-GH-FRESH-4`).
- **Last known value** — the most recent mergeability value that was not `UNKNOWN`, carried forward
  for the current head and base (`INV-GH-FRESH-7`).
- **Detection bound** — the longest a caller can wait before the backend notices a CI change
  (`INV-GH-FRESH-10`).
- **Out-of-band write** — a change a caller makes to a PR by a route that does not go through the
  daemon, such as pushing a commit; the caller reads it back with `--fresh` or `refresh`
  (`INV-GH-FRESH-8`).

## Change delivery

- **Change log** — the daemon's ordered, persistent record of content changes; each row has a
  position (`seq`) (`INV-GH-CHG-1`).
- **Change row** — one entry of the change log: the entity, its new version, the **kinds**, each
  changed **field** with its before and after value, the change's cause, the upstream's own update
  time when known, and the time the row was appended (`INV-GH-CHG-1`).
- **Entity version** — one counter per entity, bumped once for each appended change row, whatever
  groups the change touched (`INV-GH-CHG-1`).
- **Change kind** — one of the closed set of upstream kinds a change row carries, listed in
  `INTF-GH-WIRE` (`INV-GH-CHG-4`).
- **Change cause** — what led to a change row: a `query` run, a background `refresh`, a `write`
  through the daemon, or a `read` a caller waited on. Carried in the row's `origin` field.
- **Baseline** — the first fetch of an entity, of a query's membership, or of a group: it
  establishes what later changes are measured against and logs nothing (`INV-GH-CHG-2`).
- **Settle rule** — a detected change that queues dependent groups is held until those groups are
  refreshed or the settle timeout passes, so the row a consumer receives describes data already in
  the store (`INV-GH-CHG-3`). An **Aggregator** over one entity.
- **Pending row** — the durable, not yet visible form of a change held by the settle rule; it
  merges later changes to the same entity and survives a crash (`INV-GH-CHG-3`).
- **Cursor key** — the triple (consumer, kind, query) a consumer position is kept for
  (`INV-GH-CHG-6`). `kind` is `pr` or `ci`.
- **Consumer position** — the log position a cursor key has acknowledged: the rows after it are the
  ones the next poll returns (`INV-GH-CHG-5`).
- **Acknowledgement** — the explicit `changes_ack` that moves a consumer position to a `next_seq`,
  sent only after the consumer has been given the rows (`INV-GH-CHG-6`).
- **Tail** — the end of the change log: the position a new cursor key starts at (`INV-GH-CHG-7`).
- **Retention** — how long change rows are kept, and how long a cursor key may go unseen before it
  is dropped (`INV-GH-CHG-8`).
- **CI view** — the `ci changes` (PLANNED, not available) presentation of the PR rows whose kinds include a CI change, with
  the PR's current CI state as the entity (`INV-GH-CHG-10`).

## Budget

- **Bucket** — one origin allowance the backend draws on, metered separately by the origin: the
  point-metered query allowance, the request allowance, and the search-rate allowance
  (`INV-GH-BUDGET-1`).
- **Cap** — the most a bucket may be spent in an hour. It is hard: no class of work spends past it
  (`INV-GH-BUDGET-1`).
- **Reserve** — the floor of the point-metered bucket that nothing spends below
  (`INV-GH-BUDGET-1`).
- **Background share** — the fraction of each cap that background work may use, leaving the rest to
  interactive work and writes (`INV-GH-BUDGET-3`).
- **Priority class** — one of the five ranks a refresh task has, from interactive down to fill
  (`INV-GH-BUDGET-2`). **Interactive** work is a caller waiting; the others are background.
- **Fill** — the lowest class: entities nearly due, added to a batch that is already going out so
  its spare slots are not wasted (`INV-GH-BUDGET-2`).
- **Governor** — the part of the daemon that meters spend against each bucket and enforces the cap,
  the reserve and the pauses (`INV-GH-BUDGET-1`, `INV-GH-BUDGET-5`).
- **Metered pass-through** — an op the daemon performs against the origin itself, so the governor
  sees and limits its spend, but whose result it does not store (`INV-GH-BUDGET-4`).
