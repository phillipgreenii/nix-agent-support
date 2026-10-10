# pg-desk — configuration keys (focus)

This page documents the `config.yaml` keys that the daily-focus rank and its verbs read: the
`focus` block and the top-level `bead_id_pattern`. The entity-change-flow keys (`watch.*`,
`sweep.*`, `hydration.*`, `change_log_retention`, `consumer_stale_after`) are documented in
[`changes.md`](changes.md); the rest are named in [`README.md`](README.md). This change defines,
validates, renders and documents the keys; the rank, the verbs and `doctor` consume them in their
own behavior docs.

Every key is optional. An unset key takes the default below; an invalid value MUST be rejected when
the configuration loads, with an error naming the key. The `phillipgreenii.programs.pg-desk` home
module renders each key under the option named in the table and omits a null option, so a
configuration that sets none of them is byte-identical to one written before the keys existed.

| Key                          | Home-module option         | Type                   | Default                                          | Meaning                                                                                                                                                                                                                 |
| ---------------------------- | -------------------------- | ---------------------- | ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `focus.time_zone`            | `focus.timeZone`           | IANA zone name         | the process-local zone                           | The zone in which the focus day rolls over and a due date is read, so a scheduled job running under a different zone cannot roll the day at the wrong hour. An unknown name is a load error.                            |
| `focus.coverage_backlog_max` | `focus.coverageBacklogMax` | positive integer       | 10% of the active count of the type (rule below) | The backlog count above which coverage is incomplete (exit `2` of `focus show` and `focus replan`). A zero or negative value is a load error.                                                                           |
| `focus.pending_gate_age`     | `focus.pendingGateAge`     | duration such as `30m` | `30m`                                            | How old an unminted selection with its annotation set may be before `doctor` gates on it. A non-positive or unparseable duration is a load error.                                                                       |
| `focus.operator_identities`  | `focus.operatorIdentities` | list of strings        | none (empty)                                     | The assignee strings that make a Jira issue the operator's, for Jira candidacy only. Each entry is trimmed; an empty or whitespace-only entry is a load error.                                                          |
| `bead_id_pattern`            | `beadIdPattern`            | regular expression     | unset                                            | Tells a bead id from any other issue id: an issue whose id matches is a bead. The match is unanchored, so a deployment that needs an exact match supplies an anchored pattern. An uncompilable pattern is a load error. |

**Default coverage backlog bound.** When `focus.coverage_backlog_max` is unset, the bound for a type
whose active count is `N` is `N / 10` rounded up, and at least 1 when `N` is above 0 (so `N` of 0
gives 0, 1 to 10 give 1, 11 to 20 give 2). An explicit value is used as given, whatever `N` is.

**An empty identity list is valid.** An empty or absent `focus.operator_identities` MUST NOT fail
the load, and means no Jira issue can be a candidate by assignment. `focus show` and `focus replan`
report that as a notice and exit `0`; `doctor` reports it and does not gate on it. Matching is exact
and case-sensitive against the stored assignee trimmed of surrounding whitespace; the Jira owner
field is not read.

**A related key documented elsewhere.** `hydration.read_issue_deps` (home-module option
`hydration.readIssueDeps`, boolean, default `false`) is what makes the rank's unblocks key
computable for issues; it is specified in [`changes.md`](changes.md), "Issue-dependency
hydration". A non-boolean value is a load error naming the key.

## Invariants

- **INV-CONFIG-1.** Every focus key and `bead_id_pattern` MUST be optional: a configuration that sets
  none of them MUST load and MUST yield the defaults in the table.
- **INV-CONFIG-2.** An invalid focus value (unknown zone, non-positive backlog bound, non-positive or
  unparseable gate age, an empty identity entry, an uncompilable or blank `bead_id_pattern`) MUST
  fail the load with an error naming the key.
- **INV-CONFIG-3.** An empty or absent `focus.operator_identities` MUST NOT fail the load.
- **INV-CONFIG-4.** The home module MUST omit each of these keys from the rendered `config.yaml`
  when its option is null.

## Telemetry and logs

Loading these keys emits no OpenTelemetry or Prometheus output and logs nothing of its own. A
rejected value surfaces as the configuration load error of whichever command loaded it. The
consumers of the keys declare their own telemetry.
