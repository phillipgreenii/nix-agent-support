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

The row is written with no exit code, by the lock transaction for a lock (together with the plan
rows) and when the run starts for any other run, and finalized LAST, so a crash between the two
leaves a row without an exit code. Such a row is counted as a total failure. A usage error or a
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

## The draft, the plan view and the cap line

A **plan** is the period's `focus_selection` rows: what has been locked, in its recorded order. A
**draft** is the one stored row of `focus_draft`, which pg-desk keeps and manages; the caller never
holds a file. The draft is a Memento: the order, tiers and cap line computed ONCE, so the order does
not shift while the operator reads it, until a lock deletes it or a `replan` replaces it. The model
lives in `internal/focus` as pure code; the verbs that store, render and lock it are specified with
each verb.

**The stored document.** Contract `pg-desk.focus.draft/v1`, mode `draft`. It holds the ordered rows,
the trailing epics block, the coverage state and the `rank_inputs` it was computed under, and nothing
else. Each row holds its entity key, kind, tier, the key that put it ahead of the row below, the `+`
proposal flag, the `finished` and `new` marks, and `via` for a linked candidate. It holds NO plan rows
and NO facts (title, due date, priority, state): the `PLAN` block and every fact are read live from the
store at render, so a plan that changes between two `show` calls (a `pull`, a strike) is never shown
stale. The `--json` output of a verb carries the same document and is read by the command prose; it is
never passed back in. A stored body that is undecodable, or whose contract is missing or is not exactly
`pg-desk.focus.draft/v1`, is unreadable and is treated as ABSENT, never as an error.

**Two ways to compute a draft.** An initial draft (a period with no plan) lists every ranked candidate in
rank order and proposes (`+`) the top `cap` UNFINISHED rows. A finished row keeps its place, is never
proposed and does not count toward `cap`. A replan draft (a period with a plan) keeps the plan rows out
of the document, orders every other candidate afresh and proposes NOTHING, so a bare `ok` over a replan
changes nothing; a candidate that arrived after the plan was locked (first seen later than the earliest
plan row's `selected_at`) carries `new`. A replan over a period with no plan is the initial draft.

**Re-rendering a stored draft.** The rows are shown in the DRAFT's order with live facts. A fresh rank is
computed only to report drift and never orders anything: `rows placed differently` (rows both hold that
sit at a different position, so a removal is not a displacement of its neighbours), the rows that have
finished since, and the rows that are no longer candidates. A row whose source finished stays visible,
marked `finished`. A candidate the draft does not hold shows as `new since draft` and is never added to
the table.

**The plan view.** The plan block lists the plan's rows in recorded `rank_position` order with their
recorded tier and live facts, each marked `finished`, `claimed by <actor>`, `deferred`,
`no longer a candidate: hidden|inactive` or `covered by <key>`. It never re-orders, re-tiers or removes
a row because the rank now differs; drift is a notice.

**The cap line.** `cap N: A in plan, B open slots; C finished, not counted`. `A` counts the UNFINISHED
rows in the plan, `B` is `N - A` and is never negative, and the finished clause is omitted when no row
is finished. A finished row is shown once, inside the plan, and its slot is free. For an initial draft
the cap line counts the proposed (`+`) rows as the rows in plan.

- **INV-FOCUS-12.** A draft MUST be stored singly, as one row that holds the order, tiers, proposals and
  marks and neither a plan row nor a fact, and it MUST end at lock: a lock deletes it, a `replan`
  replaces it, and a draft is never kept per period or per caller.
- **INV-FOCUS-13.** Rank drift MUST NEVER move or remove a row: a stored draft renders in its own order,
  a locked plan renders in its recorded order and tier, and a fresh rank is used only to count and name
  drift.
- **INV-FOCUS-14.** Only the current plan MUST exist: the plan is the `focus_selection` rows of one
  period, it is always read live (never copied into the draft), and a first lock of a later period
  replaces it.

## `show` and `replan`

`pg-desk focus show [--date YYYY-MM-DD] [--cap N] [--all] [--no-store] [--json]` prints the period's
plan or draft; `pg-desk focus replan [--date YYYY-MM-DD] [--cap N] [--all] [--json]` opens a draft
again over the locked plan. Both read only the store, never prompt and write no run record.

**The three modes of `show`.**

- **An applicable draft is stored (a stored draft whose period is the addressed one).** `show`
  re-renders it. The order, tiers, deciding keys and cap line are the draft's; every fact (title, due
  date, priority, status, bead column, stale mark) is read live and the PLAN block is read live from
  the plan rows. A drift notice names how many draft rows would sit differently under a fresh rank
  and which rows are now finished or no longer candidates, and a `new since draft` block lists
  candidates the draft does not hold without ever adding them to the table. `--cap` with an
  applicable draft is a usage error. An unreadable draft is treated as absent.
- **No applicable draft and no plan.** `show` computes ONE draft (candidate set and rank) and prints
  a PLAN block of the proposed rows (`+`), the cap line and the other candidates sorted by the rank.
  It stores that draft only when ALL of these hold: no `--no-store`; the addressed period is today in
  `focus.time_zone` (any other `--date` implies `--no-store` and prints
  `NOTICE period <date> is not today; no draft stored`); the period is not closed; the period has no
  plan; and the single draft row is free (empty, stale, or unreadable). The store is one transaction
  that re-checks "no plan rows for the period", so a lock that committed after the compute makes
  `show` print the plan and store nothing, and a draft for the same period is never overwritten (it
  is re-rendered). A stale draft is replaced without a message; an unreadable one prints
  `NOTICE stored draft unreadable; replaced`. A stored draft of a LATER period is left alone with
  `NOTICE stored draft is for <date>; pass --date <date> or run replan`. `--no-store` computes and
  renders but never stores and never re-renders a stored draft (it prints
  `NOTICE stored draft ignored (--no-store)` when one applies). When it stored a draft the draft line
  reads `DRAFT (stored; reply to lock it in)`; a second bare `show` re-renders it, so the order the
  operator saw is the order the lock uses.
- **A plan exists and no applicable draft.** `show` prints the PLAN in the recorded order and tier
  with its cap line, `PLAN (locked <selected_at>)`, and stores nothing. The cap line counts only
  UNFINISHED rows against the cap, so finishing work frees its slot. The candidates below it are in
  the CURRENT rank, labelled `current rank, not frozen`, with a notice counting those that now rank
  above the plan's last row and suggesting `replan`.

`replan` prints the same layout with the existing plan as the PLAN block and every other candidate,
new ones included (marked `new`), ranked afresh below it. It proposes nothing. It replaces the stored
draft (whatever its period) only when the addressed period is today and not closed; for any other
`--date` it prints and stores nothing with the same `period <date> is not today` notice. The store is
a transaction that re-checks the plan state: when a lock changed it between the compute and the
store, `replan` recomputes for the state it now finds (a plan-mode replan proposes nothing). On a
closed period `replan` prints the plan (mode `plan`, `period closed`), stores nothing and exits `0`.
On a period with no plan it prints what `show` prints (the first plan is the degenerate replan).

**Layout (fixed section order).** Scope; notices; coverage; a `candidates:` count line per source;
the draft line; the PLAN table (a finished row is marked `finished` INSIDE it); the cap line; the
candidates table; the epics block; `new since draft` when a stored draft is re-rendered; the
footer. The initial draft, `replan` and plan mode share this skeleton and differ only in what fills
PLAN. The tables have a leading MARK column (`*` in the plan, `+` proposed and would be selected on
`ok`, blank otherwise), then rank, tier, the key that put the row ahead of the row below it, due
date, priority, the bead column, the key and the title, and for a linked candidate a
`via <seed key> (<relation>)` fact; a row carries `(stale)` when its snapshot is flagged stale. The
per-source line reads like `candidates: pr 5, jira 3, bead 0 (label pg-focus-planable matches none of 233 open beads; label one or use +key), 2 of them via links`,
so an empty source says why. By default the candidates table shows the next ten; `--all` shows every
candidate and a footer `<n> shown of <m> candidates (--all)` says when it truncates. Every failed,
degraded or unknown candidate source prints one `NOTICE` line above the table, and a
`NOTICE period <date> was never closed` line names each older period that still holds rows and has
no `closed_at`.

**`--json`.** Beside `contract` and `verb`, the document carries `mode` (`draft` or `plan`),
`stored`, `has_plan` (mode independent), `plan_rows` (the locked rows, live facts),
`unclosed_periods`, `period`, `made_at`, `cap`, `cap_line`, the ordered `rows` (every candidate, with
its mark, tier, deciding key, bead column and `via`), the `epics` block, `new_since_draft`, the
`coverage` state, `rank_inputs` and the `notices`. A stored replan draft over a locked plan reads
`mode: draft` with `has_plan: true`, so a caller asking whether a period has a plan MUST test
`has_plan` on `show --json --no-store` and never `mode`.

**Coverage and the exit codes of `show` and `replan`.** The coverage header gives, per watched type,
the active count, the count still due or never hydrated (the shared `changes.ActiveCount` and
`changes.DueBacklog`), the age of the oldest `hydrated_at`, the persisted listing status of each
watch query, each epic's count of children the store does not hold yet, and the `rank_inputs`
counts. A configured watch query with no persisted status is `unknown` and counts as incomplete, except a
`pr` query while no `pr` change flow is wired (no `pr` consumer is registered in the store): it is
`not-watched`, listed with a NOTICE line and not incomplete, until a flow is wired. A
source outside every recorded watch set reads `unwatched` in the bead column.

| Exit | Meaning                                                                                                                                                        |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `0`  | Coverage complete (a small routine hydration backlog is a notice), including a legitimately empty candidate set and an empty `focus.operator_identities` list. |
| `2`  | Coverage incomplete: a listing degraded, failed or unknown, or a due backlog above `focus.coverage_backlog_max`. The table is shown, never silently omitted.   |
| `3`  | Total failure: no candidates are computable (an empty set and every known listing failed). Nothing is stored.                                                  |
| `1`  | Usage error (`--cap` not positive, `--cap` with an applicable draft, a bad `--date`) or a store that is not migrated.                                          |

An empty `focus.operator_identities` list is not a failure: `show` and `replan` print
`NOTICE focus.operator_identities is empty: no Jira issue can be a candidate by assignment` and
exit `0` with the table the other sources produce. Exit `6` is not used: reading a closed period is
allowed.

**The bead column.** A closed vocabulary, so `pending` never hides a reason. Each value is the
"Shown as" word of the state-by-outcome table below, which is also the table of the `select` echo, so
the two cannot drift; it is defined once, as the `BeadStates` table in `internal/focus/beadstate.go`,
and a test keeps this document and that table equal. A bead is linked to its source by relation
`source`, so its status, assignee and hold marker are read from the store with no tracker call. A
selected row whose bead is not minted yet shows `pending` with its age (`pending 12m`) and never a
bare "pending". `focus.suppressed_kind` names the work kind a source's `suppress.<kind>` annotation
is read for: the deployment, not pg-desk code, names it, and an empty value means suppression is
never reported.

| Bead state                                                        | Shown as                          | On strike                        | On select                           | Skip reason     |
| ----------------------------------------------------------------- | --------------------------------- | -------------------------------- | ----------------------------------- | --------------- |
| No bead yet, decider not run                                      | pending                           | nothing to do                    | will mint                           | not matched     |
| No bead yet, source hidden                                        | pending (hidden)                  | nothing to do                    | no change (source hidden)           | hidden          |
| No bead yet, kind suppressed                                      | pending (suppressed)              | nothing to do                    | no change (suppressed)              | suppressed      |
| No bead yet, source terminal (finished)                           | source done (no bead)             | nothing to do                    | no change (source finished)         | not matched     |
| Source is an epic or a bd task (its id is a bead id)              | n/a (epic or bead)                | no bead                          | no bead                             | n/a             |
| A bead exists (any bead state), source hidden                     | the bead word (source hidden)     | no change (source hidden)        | no change (source hidden)           | hidden          |
| A bead exists (any bead state), kind suppressed                   | the bead word (source suppressed) | no change (source suppressed)    | no change (source suppressed)       | suppressed      |
| open, unclaimed, no marker or marker released                     | open                              | will hold                        | bead open, no change                | already handled |
| open, unclaimed, no marker or marker released, with open children | open (has children)               | no change (has open children)    | bead open, no change                | already handled |
| open, unclaimed, marker struck (undeferred by hand)               | open (marker stale)               | will hold                        | will rewrite the marker             | already handled |
| in_progress or assigned                                           | claimed                           | claimed by <actor>, left running | claimed by <actor>, no change       | already handled |
| deferred, marker struck, unassigned (held)                        | held                              | no change (already held)         | held, will release                  | already handled |
| deferred, no marker (parked by someone else)                      | deferred by someone else          | no change                        | deferred by someone else, no change | already handled |
| blocked, pinned or hooked                                         | the status word                   | no change                        | no change                           | already handled |
| closed                                                            | closed                            | no change                        | worker-closed, no change            | already handled |
| Source terminal (merged or closed PR, done issue), bead unclaimed | source done                       | will hold                        | will hold (source done)             | already handled |
| A hold failed or was abandoned after the live re-read             | hold stale                        | no change (changed since read)   | no change (changed since read)      | skipped-stale   |
| An escalated human sibling exists (INV-DECIDER-18)                | escalated                         | unchanged                        | unchanged                           | n/a             |
| Source outside every watch query                                  | unwatched (added to above)        | unchanged                        | unchanged                           | n/a             |

A hidden or kind-suppressed source whose bead already exists keeps the bead's own word (`open`,
`claimed`, `held`, `deferred by someone else`, `closed` or the status word of a blocked, pinned or hooked bead) and
appends the marker: `open (source hidden)`, `claimed (source hidden)`, `held (source hidden)` and,
for a suppressed kind, `open (source suppressed)`. Both echoes print `no change (source hidden)` or
`no change (source suppressed)`, because the decider skips every rule for such a source and leaves the
bead as it was; this holds for every bead state, and `pending` stays "no bead yet" only. `doctor`
therefore reports no pending reason for such a source.

A bead closed as stale or moot by a sweep reads `closed` too, because the decider does not read who
closed it, and the word does not claim the work was done. A source that is terminal holds an
unclaimed bead exactly like a strike. `show` reads the decider's plan versus apply mode from
nowhere, so a bead that stays `pending` past `focus.pending_gate_age` is the signal and `doctor`
reports it. The skip-reason column is the decider's closed vocabulary plus the apply outcome
`skipped-stale`; `already handled` covers several states on purpose, and the "Shown as" column is
what separates them for a human.

- **INV-FOCUS-15.** A lock MUST write the draft's order: the order the operator saw in a stored draft
  is the order `select` locks, so a second bare `show` MUST re-render the stored draft instead of
  recomputing, and only `replan` recomputes.
- **INV-FOCUS-16.** `show` MUST store a draft only when no `--no-store` is given, the addressed
  period is today and not closed and has no plan, and the draft row is empty, stale or unreadable,
  inside a transaction that re-checks that the period has no plan rows; `replan` MUST write only the
  single draft row.
- **INV-FOCUS-17.** `show` and `replan` MUST exit `0` for complete coverage (an empty candidate set
  and an empty identity list included), `2` for incomplete coverage, `3` for no computable
  candidates and `1` for a usage error or an unmigrated store, and MUST NOT exit `6`.
- **INV-FOCUS-18.** The bead column MUST use only the "Shown as" words of the state-by-outcome table,
  defined once in code.

## The reply grammar and the final set

`select` reads one reply, parses it completely, and only then decides what would change. Both steps are
pure code in `internal/focus`: they read the draft, the plan and the live facts and write nothing; the
lock that applies the result is specified with `select`.

**The grammar.** A reply is a list of tokens separated by whitespace or commas (a CRLF, a tab and a mix
of separators all work):

| Token    | Meaning                                                                                                                                                                                                                 |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ok`     | Accept. It may be combined with any other token and changes nothing by itself: the draft's `+` rows are locked whether or not it is written, so it exists to make an accepting reply distinguishable from an empty one. |
| `-<key>` | Strike the entity: its row is deleted in every period.                                                                                                                                                                  |
| `+<key>` | Force-pull a candidate, or hand-add a key that is no candidate (any bead, labelled or not).                                                                                                                             |
| `cap=N`  | Re-cap. `N` is a positive integer (ASCII digits, at least 1); `cap=` may appear once.                                                                                                                                   |

Keywords are case-insensitive. An exact repeat of `ok`, `+<key>` or `-<key>` is harmless and counts once.
These are usage errors (exit 1, one line naming the problem and the way out, no selection state
changed, the caller re-prompts): an EMPTY reply (`reply ok to accept`, so a closed stdin in a
non-interactive run can never apply the whole plan), an unrecognized token, a sign with no key, `cap=`
with a value that is empty, zero, negative, not a number or too large, `cap=` twice, a conflicting pair
for one key (`-X +X`, also when the two are written in different forms of the same key), a key that
resolves to nothing, and a hand-add that is closed, finished or hidden. Parsing is atomic: it returns a
whole reply or an error and nothing else.

**Keys.** A key is written in any form the typed verbs resolve: a bare positive integer (a PR number in
the configured repository), a PR URL ending `/pull/N`, `OWNER/REPO#N` or `#N`, a bead id matching
`bead_id_pattern`, or a tracker key shaped like a configured ticket pattern (matched as a whole; a key
that matches only in upper case is upper-cased). A PR reference that names another repository than the
configured one is a usage error, so a number is never resolved against the wrong repository. The echo
prints the canonical key and the title, so a key that resolved to something unintended is visible.

**The final set** is computed from the draft the operator saw, the plan, the live candidate set and the
live facts. The rank is never recomputed (the order is the draft's). `cap` has two roles.

- _Initial-plan size_: the reply's `cap=N`, else the draft's own cap (its `--cap`, else the period's
  persisted cap, else the default 6). A first `ok` selects exactly the rows the operator saw marked
  `+`; `cap=N` re-takes the first `N` UNFINISHED rows of the SAME draft order instead. A finished row
  keeps its place in that walk and does not count.
- _Removal threshold_: over an existing plan only a reply `cap=N` counts, and it deletes only when `N`
  is LESS THAN the persisted period cap (the default 6 when none is persisted). The rows it can delete
  are those already in the plan before the reply: the unfinished ones with the highest
  `rank_position` beyond `N`, cause `cap`. Rows added by the same reply's `+key` and the draft's own
  `+` proposals are exempt and raise the effective cap. `cap=N` at or above the persisted cap deletes
  nothing and only moves the cap line; `replan --cap` and `select --cap` persist nothing, so
  `replan --cap 3` followed by a bare `ok` removes nothing.

The persisted period cap is the reply's `cap=N` when given; at a first lock with no reply cap it is the
draft's cap; otherwise it is unchanged.

- **First lock** (the period has no row): the final set is the draft's `+` rows in draft order plus
  force-pulled and hand-added keys. Each force-pull or hand-add raises the effective cap by one, since
  it falls outside the top `cap`. A finished draft row is skipped and does not count. A `-key` on a
  proposal never selected writes no row and backfills nothing; the key is left out of the set.
- **A plan exists**: the final set is every selected row, in its recorded order, plus the draft's `+`
  rows not yet in the plan (only an initial draft has any; a replan draft marks none, so a bare `ok`
  over it never adds and never removes), plus any `+key`, minus any `-key`. A row whose source is
  terminal stays, shown finished. A row that lost candidacy for a NON-terminal reason is deleted with
  cause `dropped` (the closed list: an entity hidden; an entity inactive without a terminal state). A
  worker claiming a bead, a defer, a release, a reassignment, a slot, a correlation group and a merge
  never remove a row: candidacy is for entry only and only `-key` removes it.
- **Finished is read before active**: a closed entity is also deactivated, so a row that is finished
  stays whatever else is true of it.

**Keys of both signs.** A key known to the draft or the plan is valid with both signs. For a finished
or no-longer-candidate row, `+key` is a routine skip (exit 0) and `-key` is a no-op, both reported on
stderr; `+key` of a row already selected is the routine skip `already-selected`. A `-key` naming no
candidate and no row in ANY period's plan is a usage error; one that exists only as a row of another
period is accepted and deletes it. A `+key` of a candidate that arrived after the draft is judged against
the LIVE candidate set and is a force-pull, not a hand-add. `+<epicKey>` of an epic with an open child is a
usage error naming the child.

**Merges.** `--merge keep=absorbed` drops the absorbed key from consideration (it never gets a row,
and nothing backfills its proposal slot) and is reported for the lock to record as an external link.
`+<absorbedKey>` reverses a merge: for a recorded merge the lock removes the link and the key is an
ordinary `+key`; for a merge of the same run it cancels the merge.

**What it decides.** Inserted rows with their source (`draft`, `force`, `handadd`) and tier, deleted
rows with their cause (`operator`, `cap`, `dropped`; `new_period` belongs to the lock that replaces an
earlier period's plan), the routine skips (`finished`, `no-longer-candidate`, `already-selected`,
`not-a-candidate`), the effective cap, the cap to persist and the merges to record or reverse. It
assigns no `rank_position`, performs no store write and takes no transaction.

- **INV-FOCUS-19.** A reply MUST be parsed completely before any selection state is applied: a
  malformed, empty or conflicting reply MUST be a usage error (exit 1) that changes no state.
- **INV-FOCUS-20.** The final set MUST be computed from the draft's order and MUST NOT recompute the
  rank; a bare `ok` over a plan MUST NOT remove or add by rank.
- **INV-FOCUS-21.** A reply `cap=N` MUST delete rows only when `N` is below the persisted period cap and
  only among the unfinished rows already in the plan, highest `rank_position` first; a finished row,
  and a row the same reply adds, MUST NOT be removed by it.
- **INV-FOCUS-22.** A row MUST be deleted only with cause `operator`, `cap`, `dropped` or `new_period`,
  and a claim, a defer, a reassignment, a slot, a group or a merge MUST NOT remove one.

## `select`: the lock, repair and merge

`pg-desk focus select [--date] [--cap N] [--apply | --dry-run] [--merge KEEP=ABSORBED,...] [--repair]
[--json]` reads a reply on stdin and, with `--apply`, LOCKS the draft into the plan. Without
`--apply`, or with `--dry-run` (alone or together with `--apply`), it previews: it computes everything
and writes nothing, in any table, and leaves the stored draft. `--cap N` with an applicable stored
draft is a usage error (the draft's cap stands; `cap=N` in the reply is where it changes); with no
stored draft it sets the cap of the draft computed in process. Nothing is minted here: `select`
writes selection state only, and the annotation it writes is what the decider reacts to.

**The draft.** An applicable stored draft is locked as stored: its order is the order written. With
no applicable draft, or an unreadable one, `--apply` computes one in process, exactly as `replan`
would, prints `WARNING: no stored draft; the order was computed now and may differ from what you
saw` on stderr and records `fresh_order` in the run record. A second lock that finds the draft
already consumed by a concurrent lock, pull or replan reads the plan as it now stands and computes
afresh the same way.

**The order of checks.** The first failing check decides the exit code: usage and flag errors (`1`),
the period is closed (`6`), a malformed reply (`1`), a first lock for an earlier or a future period
(`1`), hand-add hydration (`1`), then the writes (`3`, or `2` partial, or `0`). A malformed reply over
a closed period is therefore `6`, and a run that is doomed by an earlier check never hydrates.

**Hand-add hydration** is a separate, reported pre-step, not part of the atomic parse: a key the store
has never gathered is hydrated first (a read of the tracker that writes the store's own entity row),
the run is recomputed over the result and the hydrated keys are listed. A failure is a usage error
that says hydration may already have written store rows and that no selection state changed. Under a
preview nothing is hydrated and the key is reported as would-hydrate.

**The lock** is one store transaction that holds, together: the plan rows (inserts and deletes, the
rows of other periods on a first lock included), the deletion of the stored draft (when its period
is at or below the locked one), the period row (get or create, with the persisted cap) and the run
record row. Whether the lock is a FIRST lock is tested inside that transaction, by counting the
period's rows (a period row that exists does not matter), so of two concurrent first locks the second
takes the plan-exists branch. After the commit each affected entity's `focus_selected` annotation is
written under pg-desk's per-entity lock, its value computed from the committed table: the maximum
period key over the entity's rows, else `none`, and a value already held writes nothing. Then the run
record's per-entity `{outcome, seq}` is completed and its exit code is set last. A crash between the
steps leaves rows without annotations, which `--repair` mends.

**A first lock may replace the plan.** When the period has no row, the final set is non-empty, the
period key is greater than every other period's key that holds rows and is not later than today in
`focus.time_zone`, the same transaction deletes every row of every other period (cause `new_period`;
a closed period's own row, `closed_at` and note stay), and a period that was never closed is named in
the preview. A first lock for an earlier period is exit `1` (`period <date> is earlier than the
current plan (<date>); history is not rewritten`) and one for a future date is exit `1` (`period
<date> is in the future; plan it on the day`). An EMPTY first lock replaces nothing but still applies
`-key` deletions, writes its run row (outcome `empty`) and deletes the draft, so with no `-key` it
writes nothing but that row and the draft deletion.

**Rows.** `rank_position` is a per-period lock sequence, not a rank: the first lock numbers its rows
1..n in draft order (force-pulled and hand-added rows after them, in reply order) and every later add
takes the maximum over the period's rows plus one, in reply order. A row already in the plan is never
rewritten and a deletion leaves a gap. A hand-add has no tier. `-key` deletes the entity's rows in
EVERY period (so the annotation reads `none` and the decider always holds the bead), and a row whose
source is terminal stays and is shown finished.

**Exit codes.** `0` applied (or previewed); `1` a usage error, including a first lock for an earlier
or a future period, with no selection state changed; `2` the rows committed but some annotation write
failed (the run record says which; run `select --repair`); `3` a write failed outright and the
transaction rolled back; `6` the period is closed.

**Merge.** `--merge KEEP=ABSORBED` (repeatable, comma-separated) records an EXTERNAL `references` link
from the kept entity to the absorbed one with reason `focus:merge`; it writes no row, no annotation
and no change record, whether or not the kept key is in the plan. The absorbed key is dropped from
consideration and never gets a row. The link persists, so a later `select`, run without the flag or by
another caller, does not return the absorbed item as a candidate. A self-merge is a usage error.
`+<absorbedKey>` reverses the merge: the link is removed and the key is an ordinary candidate again.

**Repair.** `select --repair` re-writes every missing or mismatched `focus_selected` annotation from
the table. It scans every entity whose annotation is not `none` and every entity with a plan row in any
period, reads no reply, takes no `--merge` or `--cap`, is exempt from the period-closed check, and writes a run record
row of verb `repair`. An annotation whose entity has no row is rewritten to `none`. With `--dry-run`
it lists what it would write and writes nothing.

**Output: the CHANGES block and the echo table.** After the draft, a preview prints the **CHANGES
block** and nothing else; `--dry-run` and `--apply` add the **echo table**. The CHANGES block lists
every row the run will newly SELECT (`will mint`, `will release`) and every row it will REMOVE
(`will hold`, `left running`), selections first, each with its CAUSE, so a removal is visible before it
takes a bead out of play. A cause is shown as `you` (stored `operator`), `cap`, `no longer a candidate:
hidden` or `no longer a candidate: inactive` (stored `dropped`, by the row's loss reason) or `new plan`
(stored `new_period`); a selection has no cause. When the first lock would delete the rows of an older
period that has no `closed_at`, the block also prints `period <date> was never closed; its rows are
deleted by this lock`. A bare `ok` over an existing plan removes nothing for rank or for a finished
source, so its block is empty unless the plan holds a row that lost candidacy or the lock is the first of
a new period.

The echo table names the resolved entity AND title of every applied token (`ok` for each draft proposal
it selects, `+key`, `-key`, `cap=N`, `--merge KEEP=ABSORBED`), so a token that resolved to something other
than intended is visible at once, and gives each row its downstream consequence as an EFFECT (`mint`,
`hold`, `release`, `none`) plus a BECAUSE clause, both read from the state-by-outcome table above (a
strike reads its strike columns, a selection its select columns; the "Shown as" word is the `show` word
only). It prints the bead id wherever it names a bead; for an item that IS a bead it says `is bead <id>;
strike does not defer it`, and for a claimed bead `claimed by <actor>, left running (stop it with bd;
pg-desk cannot)`. The fixed messages are `+<key> (hydrated, new to store)` for a hand-add new to the
store, `merged PROJ-5 into OWNER/REPO#400 (link recorded; no bead for PROJ-5)` for a merge, `no change;
name items with +key or use pull` for a bare `ok` over an existing plan, `no change` for a first lock that
selects nothing, and `will mint (the router creates it on its next run; show reads pending until then)`
under any `will mint` row. A routine skip (`finished`, `no-longer-candidate`, `already-selected`) is not an
applied token: it is one `skipped <key>: <reason>` line on stderr. A dry run's CHANGES block and echo table
are the apply's, byte for byte over keys the store already holds; a hand-add new to the store has no title
under `--dry-run` because nothing is hydrated, and the run reports `would hydrate <key>` (`hydrated <key>`
after an apply) on its own line before the block.

`--json` carries `changes[]` (`direction`, `key`, `title`, `effect`, `because`, `cause`, `cause_text`,
`consequence`, `state`, `bead`, `source`) and `echo[]` (`kind`, `token`, `key`, `other`, `title`,
`result`, `effect`, `because`, `cause`, `consequence`, `bead`), with `effect`, `because` and `cause` as
separate fields over the closed vocabularies (`effect` is one of `mint`, `hold`, `release`, `none`;
`cause` the stored word), plus `hydrated`, `would_hydrate`, `annotations`, `first_lock`, `replaces_plan`,
`never_closed`, `fresh_order` and, for a preview, the `draft`. `run_id` is present when the run wrote a
record. Both forms read one table, so `show` and the echo cannot drift.

- **INV-FOCUS-23.** A lock MUST reconcile the plan rows, delete the stored draft, write the period row
  and write the run record row in ONE store transaction, MUST choose its first-lock or plan-exists
  branch by re-reading the period's rows inside that transaction, and MUST write the annotations and
  finalize the run record's exit code only after it commits.
- **INV-FOCUS-24.** A first lock MUST replace the plan (delete every row of every other period) only
  when the final set is non-empty, the period key is greater than every other period's key that holds
  rows and the period is not later than today in `focus.time_zone`; otherwise an earlier or a future
  first lock MUST be exit `1` and an empty one MUST replace nothing.
- **INV-FOCUS-25.** A strike (`-key`) MUST delete the entity's rows in every period, and the
  `focus_selected` annotation of every affected entity MUST be the maximum period key over its rows,
  else `none`.
- **INV-FOCUS-26.** A preview, a dry run and `--repair --dry-run` MUST write nothing in any table and
  MUST leave the stored draft.
- **INV-FOCUS-27.** `--repair` MUST read no reply, MUST ignore the period-closed check and MUST rewrite
  an annotation that has no row to `none`.
- **INV-FOCUS-28.** `--merge` MUST record only an external `focus:merge` link and MUST NOT write a row,
  an annotation or a change record; `+<absorbedKey>` MUST remove the link.
- **INV-FOCUS-29.** Hand-add hydration MUST run only after every earlier check has passed and MUST
  NOT run under a preview.
- **INV-FOCUS-30.** A select MUST print every row it newly selects or removes in the CHANGES block with
  its cause before anything takes a bead out of play, MUST name the resolved entity and title of every
  applied token in the echo table with an effect, a because clause and a cause as separate fields read
  from the one state-by-outcome table, and a dry run's echo MUST equal the apply's.

## `pull`: add to the plan without replacing it

`pg-desk focus pull [--date] [--top K | key...] [--dry-run] [--json]` adds candidates to the plan of the
addressed period. It is strictly additive and outside the draft and lock cycle: it computes the rank in
its own run, ignores the stored draft and leaves it alone (the draft's marks survive; a pulled entity
now in the plan shows in the `PLAN` block instead of the candidates, and at the lock a draft row that
is already in the plan is simply already there), and it removes nothing, re-caps nothing and re-sorts
nothing. "Pull more" is deliberately uncapped. It IS a lock of what it adds: each row takes the next
`rank_position` by the lock sequence rule (the maximum plus one, in rank order) and records the tier of
the run, the period row is created when missing, and each pulled entity receives the same
`focus_selected` annotation `select` writes, under the same per-entity lock, so the decider reacts
to a pulled row exactly as to a selected one. Nothing is minted here. A pulled row is an ordinary plan
row and persists across later `select` runs unless struck.

**What is asked.** A positional argument is ALWAYS a key (a PR number, a PR URL, `OWNER/REPO#N` or a
tracker key), so `pull 3` can never mean "three more" or "PR number 3" by guesswork: it is PR 3. A count
is only ever `--top K`, which takes the first K candidates that are not yet selected and not finished,
by the CURRENT rank (which can differ from the stored draft). `--top` together with a key, or a `K`
that is not positive, is a usage error. A bare invocation previews: it recomputes and prints the plan
and what is left to pull, and changes nothing (the same answer to "what is left today?").

**Named keys.** A named key must already have an entity row: `pull` hydrates nothing. A key that is
already in the plan, that is finished, hidden or inactive (merged or closed since), or that is no
candidate at all is a routine skip, not a failure; each skip is one stderr line,
`skip <canonical key>: <reason>`, with the reason `already-selected`, `no-longer-candidate` or
`not-a-candidate`. A request whose every key is such a skip exits `0`.

**The period rules.** A pull never replaces a plan, so two live plans can never exist. A pull into a
period that has no rows while ANOTHER period holds rows is exit `1` with the remedy
`lock today's plan first: run show, then select --apply`; a pull into a future period is exit `1`; a
pull into a closed period is exit `6`, terminal. The first two are checked again INSIDE the pull's
transaction, so a lock that commits after the verb read the store still refuses the pull and leaves
no period row behind.

**Exit codes.** `0` done, including nothing left to pull and routine skips; `1` usage, the no-plan and
the future-period cases; `2` the rows committed but some `focus_selected` annotation writes failed
(`select --repair` mends them); `3` a store write failure, or no named key has an entity row at all;
`6` the period is closed. A candidate source that failed or degraded does not change the exit code: a
`NOTICE` line on stderr names it, so a pull that looks complete cannot hide a missing source.

**Output.** Text is the CHANGES block of `select` over the rows selected (one line per key with its
consequence), followed by the skips on stderr. `--json` prints `{contract, verb, run_id, period,
selected[], skipped[], annotations[]}`: `selected` members carry `key`, `title`, `effect` and `because`
(the vocabulary of the `select` echo), `skipped` members `key` and `reason`, `annotations` members
`key`, `outcome` and `seq`; `run_id` is the empty string under `--dry-run`.

**Dry run.** `--dry-run` runs the same resolution and reports exactly what would be selected and
annotated, writing nothing to any table, and does not create the period row, so a no-op preview never
leaves even an empty period behind. A non-dry-run `pull` writes one run record row, verb `pull`.

- **INV-FOCUS-31.** A `pull` MUST be additive: it MUST NOT delete, re-sort or re-cap a plan row, MUST
  NOT delete or change the stored draft, and MUST give each row it adds the `rank_position` of the
  maximum plus one in rank order and the tier of its run.
- **INV-FOCUS-32.** A `pull` MUST write each added row's `focus_selected` annotation exactly as
  `select` does, and a re-run MUST add no row and append no change record.
- **INV-FOCUS-33.** A positional argument of `pull` MUST be a key; a count MUST be `--top K` only, and
  `--top` together with a key MUST be a usage error; a bare invocation MUST write nothing.
- **INV-FOCUS-34.** A `pull` into a period with no rows while another period holds rows, and a `pull`
  into a future period, MUST be exit `1`, checked inside the pull's transaction; a `pull` into a closed
  period MUST be exit `6`.
- **INV-FOCUS-35.** A named key that is already selected or is no longer a candidate MUST be a routine
  skip (exit `0`); only a request in which no named key has an entity row MUST be exit `3`.
- **INV-FOCUS-36.** A `pull --dry-run` MUST write nothing in any table, including the period row.

## `close`: end the day

`pg-desk focus close [--date] [--dry-run] [--json]` closes the addressed period. It reads ONE JSON
object on stdin, `{"summary": "<day summary text>"}`: the text is free-form (quotes and newlines
included, which is why it is not an argument), unknown members are ignored, and `summary` MUST be a
string. In ONE transaction `close` creates the period row when the day has none, writes `closed_at`
(the run's clock, RFC 3339 UTC) and `close_note` (the summary, verbatim) onto it, deletes the stored
draft when it is the draft of exactly this period (so a closed day leaves no draft behind, while
another period's draft is untouched), and writes the run's `focus_run` row. The day's plan rows stay (close does not
touch them); the next period's first lock replaces them.

**Per-bead progress notes are not written here.** pg-desk executes no tracker write verb, so the
note a day leaves on each bead is the command prose's job: it appends each note with the connector's
issue comment, refreshes the bead with `pg-desk issue refresh`, and only then calls `close`, stopping
at the first failed append (no rollback of notes already appended) and not calling `close` at all
when any note failed. The reason it lives there and not in `close` is that the comment is the only
durable, dated, per-bead record of what happened, and the store holds no narrative text; the plan
rows of a past day are not kept (a lock replaces them), so those comments are also the only trace of
which items a past day's plan held.

**Idempotence.** Closing an already-closed period with an IDENTICAL summary is exit `0`, reported as
`already closed <date> (same summary)`, so a retried command does not look like a failure after the
summary was recorded; nothing changes (the recorded `closed_at` stays) and the run still writes its
row. With a DIFFERENT summary it is exit `6`: the recorded summary is kept and the message names the
recorded close time. A closed day cannot be amended or reopened this phase; `select` and `pull` on it
exit `6` with `period <date> is closed; use --date <tomorrow>`. Two concurrent closes serialize on
the transaction, and the loser decides again against what the winner recorded.

**Exit codes.** `0` closed, or already closed with the same summary; `1` usage: a malformed stdin
(empty, not one JSON object, no `summary`, a `summary` that is not a string, more than one JSON
value), a `--date` later than today, a store not yet migrated; `3` the store write failed (nothing
is written: the close, the draft deletion and the period row commit together or not at all); `6`
already closed with a different summary. Checks run in that order: usage and flag errors, the store,
the closed-period comparison, then the write.

**Output.** Text is one line: `closed <date>`, or `already closed <date> (same summary)`. `--json`
prints `{contract, verb, run_id, period, closed_at, close_note, already_closed, dry_run}`; `closed_at`
and `close_note` are the recorded values.

**Dry run.** `--dry-run` reports the resolved `close_note` and writes nothing to any table: not the
period row, not a run row. Its text line is `would close <date> with close_note "<summary>"` (the
summary quoted, so it stays one line), its `--json` has `run_id` empty, `dry_run` true and
`already_closed` false (the would-be `closed_at` is the run's clock). A dry run of a day that is
already closed reports what the real run would: exit `0` for the same summary, `6` for another.

- **INV-FOCUS-37.** A `close` MUST, in one transaction, write `closed_at` and `close_note` onto the
  period row (creating it when absent), delete the stored draft only when it is the draft of the
  period being closed, and write one run record row; a failure MUST leave every one of them unwritten.
- **INV-FOCUS-38.** Closing an already-closed period MUST change nothing: with an identical summary
  it MUST be exit `0` (`already closed`), with a different summary exit `6` naming the recorded close
  time and keeping the recorded summary. A closed period MUST NOT be reopened or amended.
- **INV-FOCUS-39.** `close` MUST read the summary as a string member of ONE JSON object on stdin;
  a malformed stdin or a `--date` later than today MUST be exit `1`.
- **INV-FOCUS-40.** `close` MUST NOT execute a tracker write verb; the per-bead progress notes MUST
  be written by the caller before it calls `close`.
- **INV-FOCUS-41.** A `close --dry-run` MUST write nothing in any table, including the period row.

## `explain`: why is this entity here, or not

`pg-desk focus explain <key> [--date] [--json]` answers "why is X among my candidates, or not in my
plan" and "why did a bead appear" for ONE entity. It is a pure read: it writes nothing, creates no
period row, stores no draft, writes no run record and appends no change record, so it is safe to run
at any time, on a closed or a not-yet-started day alike. It is computed live only: there is no
history to read, so the output carries no flip-history member in either form. It accepts the key
forms `select` accepts (a PR number, a PR URL, `OWNER/REPO#N` or a tracker key). `--date` addresses
the period as for every verb: it selects the plan rows to read and the day the rank is computed for.

**The text form** is a fixed list of labelled lines, in this order:

1. `key:` the canonical key.
2. `candidate:` `yes`, or `no (<reasons>)` where the reasons come from the closed list the candidate
   set answers with: `inactive`, `hidden`, `minted-bead`, `work-item-bead`, `deferred`, `terminal`,
   `absorbed`, `not-seed-or-linked` (`not-in-store` is the unknown key, below).
3. `via:` only for a linked candidate: the seed it is joined to, the relation, and how many further
   seeds are joined to it.
4. `tier and deciding keys:` the tier, and the key that decided its position against the row above it
   and the row below it (`above <key> / below <key>`, from the rank's closed list: `tier`, `overdue`,
   `started`, `due`, `unblocks`, `priority`, `age`, `tiebreak`; `none` at either end). A candidate with
   no row of its own (suppressed by an epic, a correlation group or a merge) is `not ranked`.
5. `slot:` `ranked`, `suppressed by <key>` (the epic slot rule, a correlation group or a merge) or
   `epics in play` (listed in the trailing block), else `none`.
6. `cap line:` `position <n> of cap <c>` (n counts the unfinished rows inside the cap line up to and
   including this one) or `outside the cap line`. The cap is the addressed period's, else the
   applicable stored draft's, else the default.
7. `plan row:` `selected_at`, `rank_position` and `tier` of its plan row, or `not in the plan`. When
   the addressed period holds no row of it but another period does, that row is shown and the line
   says which period it is in, because only one period holds plan rows.
8. `annotation:` `focus_selected=<value>`: the stored period key or `none`, or `unset`.
9. `bead:` the bead column's shown word, then the bead's hold marker (`none` when absent) and its
   latest audit comment, both read from the bead's stored snapshot. The bead is the issue linked to
   the source by relation `source`; when the snapshot carries no comment list the line says
   `audit: not stored` and prints nothing it does not hold.
10. `skip reasons (recomputed now):` the decider's closed skip reason for the row the bead falls in.
    The decider's skip reasons are NOT recalled (no durable record of them exists and a plan writes
    nothing): they are recomputed from the same view and labelled so.
11. `run_id: null`: explain writes no run row.

**An unknown key** (no stored entity) prints `<key>: not held by pg-desk` and `run_id: null` and
exits `0`, the house convention of `attention explain`; its `--json` has `candidate` false and the
reason `not-in-store`. **An unmigrated store** exits `1` with the usual refusal. A malformed key or a
repository other than the configured one is a usage error, exit `1`.

**`--json`** prints `contract` `pg-desk.focus/v1`, `verb` `explain`, and the members the text shows:
`key`, `candidate`, `reasons`, `via` (`{seed_key, relation, more}` or null), `tier`, `deciding`
(`{above, below}`), `slot` (`{state, covered_by}`; state is `ranked`, `suppressed`, `epics_in_play`
or `none`), `cap_position` (integer or null), `plan_row` (`{period, selected_at, rank_position,
tier}` or null), `annotation`, `bead` (`{state, marker, audit, recomputed_now}`) and `run_id`, which
is always null.

**Telemetry.** The verb emits no OpenTelemetry and no Prometheus signal, writes no run record and logs
nothing; its output is the whole of what it leaves behind.

- **INV-FOCUS-42.** `explain` MUST be a pure read: it MUST NOT write any table (no period row, no
  draft, no run record, no change record), call the tracker or the network, or use a language model.
- **INV-FOCUS-43.** `explain` MUST compute from the live rows only, MUST carry no flip-history member
  in text or `--json`, and MUST label the decider's skip reasons as recomputed now.
- **INV-FOCUS-44.** `explain` MUST accept the key forms `select` accepts; an unknown key MUST print
  that it is not held and exit `0`, and an unmigrated store MUST exit `1`.

## Help, the tldr page and shell completions

The focus group is documented to the operator at the command line, not only here.

**`pg-desk focus --help`** lists the six verbs (`show`, `replan`, `select`, `pull`, `close`,
`explain`) with one line each and says where each verb's own help is. It names the label that marks a
bead for planning, `pg-focus-planable`, with the command that sets it,
`bd update <id> --add-label pg-focus-planable`, and says the label is the only bead marker: no
assignee and no owner is set. It also says the label needs `bead_id_pattern` in the configuration,
because without one no issue is read as a bead. Where it mentions `focus.operator_identities` it says
that the key is Jira-only: it lists the assignee strings that make a Jira issue the operator's, has no
effect on pull requests or beads, and an empty list yields no Jira candidates, not a failure. The
group's help carries the morning flow as its example.

**`pg-desk focus select --help`** documents the reply grammar (`ok`, `-<key>`, `+<key>`, `cap=N`, the
separators and what an empty reply does), the exit codes `0`, `1`, `2`, `3` and `6` one per line, and
`--dry-run` and `--apply`, with examples that preview, lock, and strike or add or re-cap. It names no
exit code outside that set.

**The tldr page** `pg-desk-focus` is installed with the package under
`share/tldr/pages.common/pg-desk-focus.md` and registered by the pg-desk home module through
`programs.tldr.customPages`, only when `programs.tldr` is enabled. It shows the morning flow in the
order an operator runs it: `show`, `select --dry-run`, `select --apply`, `replan`, `pull --top 2`
and `close`, then `explain`, the label command, and the `--date` and `--json` flags.

**Shell completions** for bash, zsh and fish are generated into the package at build time
(`pg-desk completion <shell>`), and cover the verbs and their flags. The keys that `explain <key>` and
`pull <key>...` take complete from the store's candidates: a PR as its number, any other candidate as
its key, each with a short description. `pull` offers only candidates not already in the plan and not
already typed, and nothing with `--top`; `explain` takes one key. A completion is a read: it opens the
store read-only, calls no tracker and no network, writes nothing, is cut off after two seconds, and on
ANY failure (an unreadable config, a store that cannot be opened or is not migrated) it offers
nothing and prints nothing, because a shell shows everything a completion writes. Ranking order is not
promised: the candidates come in the candidate order (PRs, Jira issues, beads, then key).

**Telemetry.** Help text and completions emit no OpenTelemetry and no Prometheus signal and log
nothing; a completion writes no run record.

- **INV-FOCUS-45.** `focus --help` MUST link the verbs `show`, `replan`, `select`, `pull`, `close` and
  `explain`, and MUST name the label `pg-focus-planable` with
  `bd update <id> --add-label pg-focus-planable` as the only bead marker.
- **INV-FOCUS-46.** Wherever the help mentions `focus.operator_identities` it MUST describe it as a
  Jira-only key.
- **INV-FOCUS-47.** `focus select --help` MUST document the reply grammar with examples, the exit codes
  `0`, `1`, `2`, `3` and `6`, and `--dry-run`.
- **INV-FOCUS-48.** The key completion of `explain` and `pull` MUST read the store read-only, MUST NOT
  write, call the tracker or the network, MUST give up after a short time limit, and MUST print
  nothing on any error.

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

- **The draft model:** the draft document, its re-render, the plan view and the cap line are pure
  computations over their arguments and the injected clock; they emit no OpenTelemetry or Prometheus
  signal and log nothing.
- **The reply parser, key resolution and the final set:** pure computations over their arguments; they
  emit no OpenTelemetry or Prometheus signal and log nothing. A usage error they return is printed by
  the verb as its one-line stderr remedy and counted by the run record's `usage` outcome.
- **OpenTelemetry:** none, including from the rank, which also logs nothing; the verbs that call it
  report its `rank_inputs` counts in the run record and the structured stderr line.
- **Prometheus:** none from the verbs. The `pg_desk_focus_*` families that `serve` exposes are
  computed from the store at scrape time and are specified with the metrics, not here; the run
  record's rows are what the `pg_desk_focus_runs{verb,outcome}` family counts.
- **Logs:** a writing verb prints exactly one structured JSON line on stderr per non-dry-run, contract
  `pg-desk.focus-select/v1`, and a dry run prints the same line with `dry_run` true. Every other
  line a verb prints on stderr is ordinary CLI text: the `NOTE` line, a notice, and the one-line
  remedy of a failure.
- **`show` and `replan`:** they emit no OpenTelemetry and no Prometheus series, log nothing and write
  no run record; `show` writes the single draft row in the one case above and `replan` replaces it.
  Their output (the table, the NOTICE lines and `--json`) is the whole of what they leave behind.
- **`select`, the lock, repair and merge:** `internal/focus` (the lock, the preview, repair, merge
  and hydration helpers) emits no OpenTelemetry or Prometheus signal and logs nothing; the verb writes
  one run record row and prints the one structured stderr line, plus the `WARNING` and `NOTE` lines.
  The `focus_selected` annotation's change-log record is the only other trace of a write, and its
  sequence is carried in the run record.
- **`pull`:** the verb and `internal/focus`'s pull emit no OpenTelemetry or Prometheus signal and log
  nothing; a non-dry-run writes one run record row (verb `pull`) and prints the one structured stderr
  line, plus the `NOTE`, `NOTICE` and `skip` lines. The annotation's change-log record is the only
  other trace of a write.
- **`close`:** the verb and the store's close write emit no OpenTelemetry or Prometheus signal and
  log nothing; a non-dry-run writes one run record row (verb `close`) and prints the one structured
  stderr line, plus the `NOTE` line when the day is not today.
- **`explain`:** the verb and `internal/focus`'s bead-note reader emit no OpenTelemetry or Prometheus
  signal, write no run record and log nothing; its output (text or `--json`) is the whole of what it
  leaves behind.
- **Help and completions:** the help text, the tldr page and the key completion emit no OpenTelemetry or
  Prometheus signal, log nothing and write no run record; a completion prints only its candidates.
- **Store:** the run record row (see "The run record") is the durable copy of that line.
