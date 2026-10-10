# pg-desk — focus

`pg-desk focus` is the command group for the daily focus: the plan of the entities the operator chose
to work on in one period, kept in the pg-desk store. It has six verbs — `show`, `select`, `replan`,
`pull`, `close` and `explain` — and each verb's own behavior is described by the section for that
verb as it lands. This document is the part every verb shares: the group, the shared flags, the
exit codes, how a verb addresses a period, the one-line remedy on a failure, the `--json` envelope
convention and the run record a writing verb leaves behind. The daily-focus design is the design of
record for the verb family; the invariants below are the contract this document adds to it.

The group has no behavior of its own: run bare, `pg-desk focus` prints its help. It is generic and
config-driven like the rest of pg-desk and carries no organization identifiers; the time zone a day
is measured in is the one configuration key `focus.time_zone`.

## Shared flags

Every verb takes the same three flags:

- `--date YYYY-MM-DD` names the period the verb addresses. It is optional and defaults to today in
  the configured time zone. A date is accepted in its lenient spelling (`2026-9-3`) and is always
  handled in its canonical form (`2026-09-03`).
- `--period day` names the period type. `day` is the only implemented value; any other value is a
  usage error. The flag is accepted and hidden from help until a second value exists, so help does
  not advertise a choice that has one answer.
- `--json` prints one JSON object instead of the text form. Setting `PG_DESK_OUTPUT=json` selects
  the same output, as it does for the other pg-desk verbs.

## The addressed period

Every verb addresses exactly ONE period: `--date`, else today in `focus.time_zone`. The addressed
period is never taken from a stored draft, so a draft left over from yesterday cannot silently move
today's verb onto yesterday.

- An **applicable draft** is a stored draft whose period equals the addressed period. It is the one
  term used for "a draft this verb may use".
- A **stale draft** is a stored draft whose period is earlier than today or whose period is closed.
- A verb that writes and addresses a day other than today prints
  `NOTE: writing into period <date>, not today` on stderr, because a mistyped date would otherwise
  create a new period without a sign. A verb that only reads prints no such note.

## Exit codes

The focus verbs use pg-desk's established scheme with its established meanings, so a caller scripting
`pg-desk` and `pg-connector` together needs no special case.

| Exit | Meaning                                                                                       |
| ---- | --------------------------------------------------------------------------------------------- |
| `0`  | The verb did what was asked; routine skips are reported on stderr and are not a failure.      |
| `1`  | Usage error, malformed reply or stdin, a store that is not migrated for the focus tables.     |
| `2`  | Partial: part of the work applied or part of the data was missing; what was applied is valid. |
| `3`  | Total failure.                                                                                |
| `6`  | The addressed period is closed. Used only by `focus`.                                         |

`4`, `5` and `7` are never used by a focus verb: `4` belongs to `head-check`, `5` to a retired
multi-candidate code, and `7` once meant "the plan changed since the draft was made", a guard the
design dropped when the tool began storing the draft itself. None of the three is reused.

Checks run in one fixed order: usage and flag errors, then the period-closed check, then reading the
stored draft, then parsing of the reply, then the write. A run doomed by an earlier check never
reaches a later one. Every non-zero exit prints ONE line on stderr that names the problem and the way
out, for example `period 2026-10-09 is closed; use --date <tomorrow>`.

## Unmigrated store

A store that has not been cut over to the schema that carries the focus tables is refused: the verb
exits `1`, the message names the problem (the store is not migrated for the focus tables) and the
remedy (`pg-desk migrate --cutover`), and nothing is applied. This is the refusal every other typed
verb gives.

## JSON envelope

A verb's `--json` output is one object. It carries a `contract` member, `pg-desk.focus/v1` (the same
convention `show.md` follows; there is no separate schema-version member), a `verb` member, the
members the text form shows, and a `run_id` member for a verb that writes. The command prose that
consumes the output parses the contract, not a table.

## The run record

A non-dry-run `select`, `pull`, `close` and `select --repair` writes one run record: a row in the
store and the same record printed as ONE structured JSON line on stderr, contract
`pg-desk.focus-select/v1`. The row is the record; the stderr line is a copy for the interactive
session, whose stderr is gone by the next week. The record carries:

- the run's identity: `contract`, `verb` (`select`, `pull`, `close` or `repair`), `run_id`, `period`,
  `dry_run`, `actor` and `exit_code`;
- the cap in force, and how many plan rows were kept although their bead is claimed or deferred;
- the counts of ranked and selected rows, of rows removed from the plan by cause (`operator`, `cap`,
  `dropped` or `new_period`), and the lists of forced, hand-added, absorbed and hydrated entities by
  canonical key, so a bare count never stands in for an audit;
- the tier counts and the five rank-input degradation counters;
- one annotation outcome per entity, `{key, outcome, seq}`, where the outcome is `ok` or `failed` and
  `seq` is the change-log sequence of the annotation change the write produced (`0` when it failed),
  so a select, the decider's actions and a bead hold join through `run_id` and `seq`;
- the age of the draft the lock used and how many of its rows a fresh rank would have placed
  differently, whether the order was a fresh one, and the coverage state the run was computed under.

Absent counts are written as zero or empty, never omitted. The members are additive: a later
revision MAY add members and MUST NOT rename or remove one.

The row is written when the run starts, with no exit code, and finalized LAST, so a crash between the
two leaves a row without an exit code. Such a row is counted as a total failure. A usage error or a
closed period still writes its row when the store is writable, so those outcomes are counted too. A
dry run prints the same line with `dry_run` true and writes no row. The run identifier is a ULID, sortable
by time, generated without any new dependency.

The words the run statistics derive from the exit code are `ok`, `partial`, `total`, `period_closed`,
`usage` and `empty` (a select that selected nothing and removed nothing).

## Invariants

- **INV-FOCUS-1.** A focus verb MUST address exactly one period, `--date` or else today in
  `focus.time_zone`, and MUST NOT take it from a stored draft.
- **INV-FOCUS-2.** `--period` MUST accept `day` and reject every other value as a usage error, and
  MUST be hidden from help until a second value is implemented.
- **INV-FOCUS-3.** A focus verb MUST exit with `0`, `1`, `2`, `3` or `6` only, and MUST NOT use `4`,
  `5` or `7`.
- **INV-FOCUS-4.** Every non-zero exit MUST print one line on stderr naming the problem and the way
  out.
- **INV-FOCUS-5.** A focus verb MUST refuse a store that is not migrated for the focus tables with exit
  `1`, name the problem and apply nothing.
- **INV-FOCUS-6.** A verb that writes and addresses a day other than today MUST print
  `NOTE: writing into period <date>, not today`.
- **INV-FOCUS-7.** Every `--json` output MUST carry `contract` `pg-desk.focus/v1`, and the output of a
  verb that writes MUST carry `run_id`.
- **INV-FOCUS-8.** Every non-dry-run `select`, `pull`, `close` and `select --repair` MUST write one run
  record row and print the same record as one `pg-desk.focus-select/v1` stderr line, and a dry run
  MUST print the line with `dry_run` true and write no row.
- **INV-FOCUS-9.** The run record row MUST be finalized last; a row whose exit code is missing MUST be
  counted as a total failure.
- **INV-FOCUS-10.** The run identifier MUST be a ULID.
- **INV-FOCUS-11.** `show`, `replan` and `explain` MUST read only the store: no network call and no
  tracker call. The rank MUST be a deterministic computation with no language model.

## The rank

The rank orders the candidate set of the daily focus. `show`, `select`, `replan`, `pull` and `explain`
all call it; there is no `focus rank` verb and the computation itself writes nothing. It is a
deterministic function of the stored facts and an injected clock: no language model, no network call
and no tracker call.

**Tiers.** Three strict lexicographic tiers, with no weights and no arithmetic:

1. **Overdue**: the due date is before the addressed day. Inside it: started items first, then the
   most overdue, then unblocks (descending), then priority, then age.
2. **Started** (not overdue).
3. **Not started** (not overdue).

Inside tiers 2 and 3 the keys, in order, are the due date inside the 7-day horizon, unblocks
(descending), priority and age; the final key is the candidate order (kind `pr`, `jira`, `bead`, then
key), so the order is total and the same inputs always rank identically.

**Due date.** A date-only value is a calendar day; a timestamp is converted to `focus.time_zone` and
then truncated to its day. The addressed day is `--date`, else today in that zone. "Inside the
horizon" means due 0 to 7 days after the addressed day, inclusive; a nearer date sorts ahead of a
farther one, and any date inside the horizon sorts ahead of none. A due date beyond the horizon is
equivalent to no due date for this key. An empty value is no due date; an unparseable value also
counts as no due date and is never an error, but the rank counts it.

**Started** applies to SEEDS only (a candidate reached only through a link is never started): a bead
in state `in_progress`; a Jira issue whose status category is `indeterminate` (when the snapshot
carries no category, a state named in `jira.in_progress_statuses`, and the rank counts the absence);
any open seed pull request. Every open seed PR is therefore started.

**Unblocks** is the count of open items one blocks. For a PR it is the number of open dependents the
dependency resolver reports (stack and externally recorded `depends_on` edges); for a bead or Jira
issue it is the number of open DIRECT dependents in the reverse-edge index. It is 0 for an issue whose
stored facts carry no issue dependencies (issue-dependency hydration is off), and the rank counts
those issues.

**Priority.** `P0` to `P4`, smaller first; a tracker value is mapped by `focus.priority_map`, and a
value in neither the map nor the `P0`-`P4` form sorts after `P4` and is counted. A PR with no
priority of its own takes the highest priority among its correlated items, else `P2`.

**Age.** The tracker's creation time when the snapshot carries it, else the time pg-desk first
stored the entity; the OLDEST item first. An item with neither sorts after every dated one. Every PR
ranks on the first-stored time, because a PR snapshot has no creation time.

**Correlation groups and slots.** A correlation group is the set of candidates joined by derived
`work` links (a PR and the beads that name it); its members inherit the earliest due date and the
highest priority across the group. The group takes ONE row, its highest-ranked member; the others are
reported as covered by it. An epic with an open child gives its slot to the child and is reported as
covered by it (the epic slot rule). A key absorbed by a `--merge` is covered by the key that absorbed
it. Neither suppression removes anything from a stored plan.

**Cap line.** After those rules the first `cap` rows (default 6) that are not finished are marked in
the plan, for display only; a finished row keeps its place in the order and is not counted toward the
cap. The cap never changes the order.

**Counted degradations (`rank_inputs`).** Each silent degradation is counted per rank:
`unparseable_due`, `unmapped_priority`, `age_fallback`, `unblocks_unavailable` and
`status_category_absent`.

- **INV-RANK-1.** The rank MUST be a strict weak order and MUST be total: the same inputs MUST rank
  identically whatever order the candidates arrive in.
- **INV-RANK-2.** The rank MUST read time only from the injected clock and MUST write nothing, call no
  tracker and use no language model.
- **INV-RANK-3.** A candidate reached only through a link MUST NOT be in the started tier.
- **INV-RANK-4.** A due date beyond the 7-day horizon MUST rank as no due date, and an unparseable due
  date MUST rank as no due date without failing the verb.
- **INV-RANK-5.** Every silent degradation MUST be counted in `rank_inputs`.

## Telemetry and logs

The focus verbs emit nothing over OpenTelemetry and write no Prometheus series of their own.

- **OpenTelemetry:** none, including from the rank, which also logs nothing; the verbs that call it
  report its `rank_inputs` counts in the run record and the structured stderr line.
- **Prometheus:** none from the verbs. The `pg_desk_focus_*` families that `serve` exposes are
  computed from the store at scrape time and are specified with the metrics, not here; the run
  record's rows are what the `pg_desk_focus_runs{verb,outcome}` family counts.
- **Logs:** a writing verb prints exactly one structured JSON line on stderr per non-dry-run, contract
  `pg-desk.focus-select/v1`, and a dry run prints the same line with `dry_run` true. Every other
  line a verb prints on stderr is ordinary CLI text: the `NOTE` line, a notice, and the one-line
  remedy of a failure.
- **Store:** the run record row (see "The run record") is the durable copy of that line.
