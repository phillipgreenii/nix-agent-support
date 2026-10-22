# Glossary — pg-connector-issue-jira

Vocabulary for the daemon-backed Jira backend. A term the parent set already defines (backend,
daemon, daemon-backed backend, `owns_changes`, `cache_opt_out`, `sources_freshness[]`, `last_seen`,
`served_from`, `age_seconds`) is **inherited** with its parent meaning, cited from
`phillipgreenii-nix-agent-support · packages/pg-connector/docs/behavior`, and is not redefined here.
A term the GitHub set defines (store, supervisor, query cache, entity cache, field group, freshness
window, local hit, single-flight, settle rule, pending row, acknowledgement, tail, retention,
governor, priority class) keeps its GitHub meaning here, cited from
`phillipgreenii-nix-agent-support · packages/pg-connector/docs/behavior/github`, except where an
entry below says otherwise.

Inheritance follows `INV-20`: where this set needs a different meaning it takes a different name.
This set's **consumer cursor** is not the GitHub set's **cursor key**; its **instance** is not the
GitHub set's **instance**.

## Process and placement

- **Client mode** — the way the backend answers one umbrella call: it reads one request and, for a
  read, forwards it to the daemon and writes the daemon's answer; for a write it performs the write
  itself (`INV-JIRA-PROC-1`, `INV-JIRA-WRITE-1`). A **Proxy** in front of the daemon for reads.
- **Instance** — one configured Jira deployment, named in the registry. Each instance has its own
  daemon and its own store, so nothing of an instance appears in a key (`INV-JIRA-PROC-1`). This is
  not the GitHub set's meaning (there, which of two capabilities a call is for).
- **Origin** — the upstream system the backend reads and writes: Jira. This is the parent set's
  `served_from: origin` meaning and is unrelated to a change row's `origin` field (see **Change
  cause**).
- **Origin tool** — the command-line tool through which every origin call is made, which holds the
  credential and runs under the calling process's identity (`pjira`). The daemon never holds a
  credential.

## Groups, tracking and watching

- **Summary group** — what a listing returns for an issue; fetched with one full query run per
  configured query (`INV-JIRA-FRESH-1`).
- **Detail group** — what a single-issue read adds to the summary: description, comments and the
  like; fetched one issue at a time (`INV-JIRA-FRESH-1`).
- **Children group** — an issue's children. Never held: always a live read (`INV-JIRA-FRESH-1`).
- **Revalidation token** — the issue's update time as the daemon uses it: a change to it re-pulls
  the detail group and is not itself content (`INV-JIRA-FRESH-1`, `INV-JIRA-CHG-3`).
- **Comment summary** — the count of an issue's comments and the time of the latest one, carried on
  the entity so a comment is a visible difference (`INV-JIRA-FRESH-3`).
- **Pass-through read** — `children`, `search` or `list_activity`: an op the daemon performs
  against the origin itself, so the governor sees its spend, but whose result it does not store
  (`INV-JIRA-FRESH-2`).
- **Tracked** — the daemon keeps the issue's content and keeps it fresh: every issue it retrieves,
  from a query, a read or a watch, until it has been closed for 24 hours (`INV-JIRA-TRACK-1`).
  Tracking alone never produces a change row.
- **Closed** — in the origin's done category of status.
- **Grace period** — the time from 24 hours to 7 days after closing, in which a closed issue is
  served from the store and not refreshed (`INV-JIRA-TRACK-1`).
- **Eviction** — removal of an issue and its groups from the store 7 days after closing. Change rows
  outlive it for their own retention; eviction also clears the watched flag (`INV-JIRA-TRACK-1`,
  `INV-JIRA-WATCH-2`).
- **Watched flag** — the stored per-issue property that makes the daemon append and deliver change
  rows for the issue. Set by membership of a watched query or by a one-off watch; cleared only by
  eviction (`INV-JIRA-WATCH-1`, `INV-JIRA-WATCH-2`).
- **Watched query** — a configured query whose definition carries `watched = true` (default false)
  (`INV-JIRA-WATCH-1`).
- **One-off watch** — the watch option on a read or lookup of one issue (`INV-JIRA-WATCH-1`).
- **Baseline marker** — the time a configured query's first complete run set; a run with no marker
  is a baseline run (`INV-JIRA-WATCH-5`).
- **Baseline run** — a first complete run of a query: it tracks every member, marks every member of
  a watched query watched, and appends no row (`INV-JIRA-WATCH-5`).
- **First observation** — an issue first seen in a watched query that already has its marker; it
  appends one `added` row (`INV-JIRA-WATCH-6`).
- **Late watch** — a known issue becoming watched after its query's baseline; it appends one
  `added` row (`INV-JIRA-WATCH-6`).

## Change delivery

- **Change row** — one entry of the change log: the issue, its new version, the kinds, each changed
  field with its before and after value, the change cause, the origin's own update time when known
  and the time the row was appended (`INV-JIRA-CHG-1`).
- **Change kind** — one of the closed catalog of kinds, listed in `INTF-JIRA-WIRE`
  (`INV-JIRA-CHG-2`). `fields_changed` is the **catch-all**; `added` is the one **first-observation
  kind**.
- **Change cause** — what led to a row: a `query` run, a background `refresh`, a `write` through the
  caller, or a `read` a caller waited on, carried in the row's `origin` field.
- **Membership information** — an `entered_query` or `left_query` report: told to the consumer
  beside the rows and never stored as a row (`INV-JIRA-CHG-5`).
- **Consumer cursor** — the one acknowledged position a consumer holds in the change log of a store,
  with no component for a query or an entity kind (`INV-JIRA-CHG-6`). Renamed from the GitHub set's
  cursor key, which has three components.
- **Store identity** — the persisted identity of the daemon's store, carried by every feed answer
  (`INV-JIRA-CHG-9`).
- **Normalised content** — an issue's content as compared for change: every field except `as_of`,
  `stale` and the update time, plus the comment summary (`INV-JIRA-CHG-3`).

## Operations and writes

- **Write notice** — `write_begin` before a write and `invalidate` after it, sent by the client to
  the daemon, each with a short delivery timeout (`INV-JIRA-WRITE-2`).
- **Hold** — the daemon's refusal to start a new refresh of an issue between its `write_begin` and
  its `invalidate`, or until the lease ends (`INV-JIRA-WRITE-2`).
- **Lease** — how long a hold lasts when `invalidate` never arrives (`INV-JIRA-WRITE-2`).

## Budget

- **Request-rate cap** — the most requests per second the daemon sends to the origin
  (`INV-JIRA-BUDGET-1`).
- **Concurrency cap** — the most requests the daemon has in flight at once (`INV-JIRA-BUDGET-1`).
- **Pause** — the interval during which the daemon sends no background request after the origin
  refused one for rate; it doubles, with jitter, on each consecutive refusal (`INV-JIRA-BUDGET-2`).
