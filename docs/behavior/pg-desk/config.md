# pg-desk — configuration keys (focus)

This page documents the `config.yaml` keys that the daily-focus rank and its verbs read: the
`focus` block and the top-level `bead_id_pattern`. The entity-change-flow keys (`watch.*`,
`sweep.*`, `hydration.*`, `change_log_retention`, `consumer_stale_after`) are documented in
[`changes.md`](changes.md); the rest are named in [`README.md`](README.md). The keys of the watched-only `issue` flow (the query
`watched` flag, `watch.issue.queries` and `hydration.orphan_max_per_poll`) are documented in "Issue
change-flow keys: watched queries and the orphan cap" below. This change defines,
validates, renders and documents the keys; the rank, the verbs and `doctor` consume them in their
own behavior docs.

Every key is optional. An unset key takes the default below; an invalid value MUST be rejected when
the configuration loads, with an error naming the key. The `phillipgreenii.programs.pg-desk` home
module renders each key under the option named in the table and omits a null option, so a
configuration that sets none of them is byte-identical to one written before the keys existed.

| Key                          | Home-module option         | Type                              | Default                                                          | Meaning                                                                                                                                                                                                                                                                                     |
| ---------------------------- | -------------------------- | --------------------------------- | ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `focus.time_zone`            | `focus.timeZone`           | IANA zone name                    | the process-local zone                                           | The zone in which the focus day rolls over and a due date is read, so a scheduled job running under a different zone cannot roll the day at the wrong hour. An unknown name is a load error.                                                                                                |
| `focus.coverage_backlog_max` | `focus.coverageBacklogMax` | positive integer                  | 10% of the active count of the type (rule below)                 | The backlog count above which coverage is incomplete (exit `2` of `focus show` and `focus replan`). A zero or negative value is a load error.                                                                                                                                               |
| `focus.pending_gate_age`     | `focus.pendingGateAge`     | duration such as `30m`            | `30m`                                                            | How old an unminted selection with its annotation set may be before `doctor` gates on it. A non-positive or unparseable duration is a load error.                                                                                                                                           |
| `focus.operator_identities`  | `focus.operatorIdentities` | list of strings                   | none (empty)                                                     | The assignee strings that make a Jira issue the operator's, for Jira candidacy only. Each entry is trimmed; an empty or whitespace-only entry is a load error.                                                                                                                              |
| `focus.priority_map`         | `focus.priorityMap`        | map of tracker value to `P0`-`P4` | `Highest: P0`, `High: P1`, `Medium: P2`, `Low: P3`, `Lowest: P4` | Maps a tracker priority value (a Jira priority name) to the rank's priority key. Matching is case-insensitive. A non-empty map REPLACES the default entirely; an empty map means the default. A value outside `P0`-`P4`, a blank key, or two keys that differ only by case is a load error. |
| `focus.suppressed_kind`      | `focus.suppressedKind`     | string                            | unset                                                            | The work-kind name the decider uses for focus beads, supplied by the deployment so pg-desk code carries no work-kind literal. The bead column reads a source whose kind is suppressed as `pending (suppressed)`; when unset, suppression is never reported. Each value is trimmed.          |
| `bead_id_pattern`            | `beadIdPattern`            | regular expression                | unset                                                            | Tells a bead id from any other issue id: an issue whose id matches is a bead. The match is unanchored, so a deployment that needs an exact match supplies an anchored pattern. An uncompilable pattern is a load error.                                                                     |

**Default coverage backlog bound.** When `focus.coverage_backlog_max` is unset, the bound for a type
whose active count is `N` is `N / 10` rounded up, and at least 1 when `N` is above 0 (so `N` of 0
gives 0, 1 to 10 give 1, 11 to 20 give 2). An explicit value is used as given, whatever `N` is.

**The priority map.** The rank reads a Jira issue's priority through `focus.priority_map`; a bead's
priority is already `P0` to `P4` and is read directly. A value that is in neither the map nor the
`P0`-`P4` form is unmapped and sorts after `P4`, and the rank counts it (see [`focus.md`](focus.md),
"The rank"). An issue with no priority sorts after `P4` without being counted. A pull request has no
priority of its own and takes its correlated items' (see the same section).

**An empty identity list is valid.** An empty or absent `focus.operator_identities` MUST NOT fail
the load, and means no Jira issue can be a candidate by assignment. `focus show` and `focus replan`
report that as a notice and exit `0`; `doctor` reports it and does not gate on it. Matching is exact
and case-sensitive against the stored assignee trimmed of surrounding whitespace; the Jira owner
field is not read.

**A related key documented elsewhere.** `hydration.read_issue_deps` (home-module option
`hydration.readIssueDeps`, boolean, default `false`) is what makes the rank's unblocks key
computable for issues; it is specified in [`changes.md`](changes.md), "Issue-dependency
hydration". A non-boolean value is a load error naming the key.

## Issue change-flow keys: watched queries and the orphan cap

The `issue` change flow is watched-only (see [`changes.md`](changes.md)): an issue produces change
events only when a query the deployment declared watched lists it, or an operator looked it up with the
one-off watch option (see [`operator-commands.md`](operator-commands.md), "issue refresh --watch").
These keys and rules are what the deployment configures for that.

- **The query `watched` flag.** A query of a backend's query configuration MAY carry `watched = true`;
  the default is false. The flag is declared once, by the deployment, and the home module renders it
  into the backend's query block AND, for the same queries, into pg-desk's `watch.issue.queries`, so the
  two cannot drift. A query written in the shorthand forms (a bare string, or a list) stays valid and
  means an unwatched query; only a query written with its own keys can carry the flag.
- **`watch.issue.queries`** MUST list exactly the queries marked watched and no others. pg-desk lists
  only watched queries. An unwatched query stays available to every other reader and produces no change
  event.
- **The backend of each watched query.** pg-desk's configuration names, for each watched issue query,
  the backend instance that serves it and whether that backend owns its changes. It is rendered from
  the same source as the backend registry, so the two cannot disagree. At start-up pg-desk compares this
  with what each backend reports about itself, including each query's `watched` value; a mismatch is a
  `doctor` gate (see [`operator-commands.md`](operator-commands.md), "doctor").
- **No `sweep.*` for `issue`.** The `issue` flow no longer reads the `sweep.*` keys. They remain, with
  their meaning in [`changes.md`](changes.md), for `pr` and `thread`.
- **`hydration.orphan_max_per_poll`** (home-module option `hydration.orphanMaxPerPoll`, positive
  integer) bounds how many orphans are read by id in one poll. An orphan is a watched, still-open issue
  of a backend with no change feed that no watched query lists any longer (a one-off watch, or an issue
  that left its query for an ordinary reason such as a deferral, a block or a relabel). Orphans are read
  FIRST, out of the same `hydration.max_per_poll` budget (default 50), so changed members cannot starve
  them and the orphans cannot take more than this cap from the members. The recommended default is
  `10`: that is `10 / 50 = 20%` of a poll's default budget, leaving changed members at least
  `50 - 10 = 40` slots. It is a recommendation, not a fixed rule: the shipped default is set from a
  measurement taken before the flow ships (how many `bd` processes stand behind one hydration), and a
  deployment MAY set another value. A zero or negative value is a load error naming the key.

Two other readers narrow with `watch.issue.queries`. An issue stored only because pull-request text
named its ticket key is HELD but not watched, so it stays silent and a pull-request change no longer
re-emits it. The due-date attention rules raise for exactly the issues the deployment's watch queries
select, so an issue selected only by an unwatched query no longer gets due-date attention: a deployment
that wants it MUST declare that query watched.

## Invariants

- **INV-CONFIG-1.** <!-- uuid: c4514292-856c-4d28-ad15-21a9a1229732 --> Every focus key and `bead_id_pattern` MUST be optional: a configuration that sets
  none of them MUST load and MUST yield the defaults in the table.
- **INV-CONFIG-2.** <!-- uuid: a5ba91fb-c06b-4307-8d13-ec0346fbb52a --> An invalid focus value (unknown zone, non-positive backlog bound, non-positive or
  unparseable gate age, an empty identity entry, a priority-map value outside `P0`-`P4` or a blank or
  case-duplicate priority-map key, an uncompilable or blank `bead_id_pattern`) MUST
  fail the load with an error naming the key.
- **INV-CONFIG-3.** <!-- uuid: b5b9f78b-e71c-4250-8232-598cf61c2e10 --> An empty or absent `focus.operator_identities` MUST NOT fail the load.
- **INV-CONFIG-4.** <!-- uuid: c7758e1f-8152-433a-a16e-a782257ee276 --> The home module MUST omit each of these keys from the rendered `config.yaml`
  when its option is null.
- **INV-CONFIG-5.** <!-- uuid: 3a42c2aa-e067-4cfb-a635-0749869da460 --> `focus.priority_map` MUST default to the standard Atlassian names, MUST replace
  that default entirely when non-empty, and MUST match a tracker value case-insensitively.
- **INV-CONFIG-6.** <!-- uuid: 37aef731-29da-449c-872b-a85285b46091 --> A query's `watched` flag MUST default to false, a query written as a bare string or a list MUST
  stay valid and mean an unwatched query, and nothing MUST be watched by default.
- **INV-CONFIG-7.** <!-- uuid: 41cab3ca-54a0-4d19-9a42-0ba9eacd3193 --> `watch.issue.queries` MUST list exactly the queries marked watched, rendered from the same source
  as the backend's query block, and a mismatch with what the backend reports MUST be a `doctor` gate.
- **INV-CONFIG-8.** <!-- uuid: 1e697425-fee0-4fc8-b372-00e0633d92c6 --> The `issue` flow MUST NOT read any `sweep.*` key; `pr` and `thread` still read them.
- **INV-CONFIG-9.** <!-- uuid: 193683c6-4588-4dac-9c6d-676cad254617 --> At most `hydration.orphan_max_per_poll` orphans MUST be read per poll, read first out of the
  `hydration.max_per_poll` budget; its default is a recommendation, and a non-positive value MUST fail the
  load with an error naming the key.

## Telemetry and logs

Loading these keys emits no OpenTelemetry or Prometheus output and logs nothing of its own. A
rejected value surfaces as the configuration load error of whichever command loaded it. The
consumers of the keys declare their own telemetry.
