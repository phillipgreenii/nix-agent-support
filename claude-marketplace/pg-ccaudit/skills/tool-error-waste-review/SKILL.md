---
name: tool-error-waste-review
description: Run a tool-error waste review over the Claude Code transcript corpus — which tool calls fail, how often, with what error signatures, at what measured cost, whether an agent retries blindly, and whether each class is a main-loop or a subagent problem. Use when asked to audit or census tool errors/failures across sessions, to find where agent time is being wasted, to check whether a documented rule or instruction fix actually stopped a class of error, to route a fix to the right place (always-on user rules vs. subagent brief vs. permission-approver tuning), or to re-run a previous transcript audit for comparison. Also use for questions about retry loops, error-then-narration adjacency, hook rejections, per-tool or per-Bash-command error rates, and how concentrated a failure class is in one runaway session. Do NOT use for debugging one specific failing command right now (that is ordinary debugging), for reading a single named transcript, or for token/cost usage accounting.
---

# Tool-error waste review

## THE FIRST INSTRUCTION: THE DATABASE ALREADY EXISTS. QUERY IT.

**You MUST answer every question in this review by querying the pre-built SQLite
index through `pg-ccaudit`. You MUST NOT read, `grep`, `jq`, `find`, or otherwise
scan the raw transcripts** under `~/.claude/projects`.

This is not a style preference. The corpus is ~1.7 GiB across ~2,400 transcripts.
The census that produced this tooling tried the raw-scan approach twice and
**stalled a supervising agent's 600-second progress watchdog both times**; it only
completed after the mechanical extraction was lifted out of the agent entirely.
Every number this review needs is already indexed, normalized, and reachable in
milliseconds. A raw scan re-earns the stall for no new information.

Concretely, in this skill:

- **NEVER** `Read`, `Grep`, or `Glob` a `.jsonl` file under `~/.claude/projects`.
- **NEVER** pipe transcripts through `jq`, `awk`, `sed`, `perl`, or `wc`.
- **NEVER** write your own SQL against the database before checking the canned
  query set — the canned queries are named and versioned precisely so two audits
  produce comparable numbers.

Ad-hoc SQL is permitted **only** for a question no canned query answers, and only
via `pg-ccaudit`'s read-only path. If you find yourself needing it more than once,
say so in your report: the missing query is worth adding.

### Step 0 — confirm the index and its staleness

```bash
pg-ccaudit status
```

Read the output before doing anything else:

- **`index current`** — proceed.
- **`index BEHIND — N file(s) on disk, M indexed …`** — proceed anyway, and
  **state the staleness in your report**. The results are still valid for the
  window they cover; they simply exclude the unindexed remainder.
- **`index not found`** — the sweep has never run on this machine. STOP and report
  that, naming the enable flags
  (`phillipgreenii.programs.pg-ccaudit.enable` and `.sweep.enable`). Do **not**
  work around it by scanning transcripts, and do **not** run `pg-ccaudit ingest`
  yourself unless the operator asks — a first full ingest is a long job and
  publishing/priming machine state is theirs to authorize.

`pg-ccaudit status` and `pg-ccaudit query` are **read-only**. They report
staleness and stop; they never trigger an ingest. That mirrors this machine's
standing posture against tools that silently start their own background work.

### Step 1 — list what you can ask

```bash
pg-ccaudit queries            # names, versions, one-line purpose
pg-ccaudit queries --verbose  # plus the interpretation notes and the SQL
```

Every query takes `--since` / `--until` (ISO-8601 date prefixes, `--until`
exclusive) and `--format table|tsv|json`. **Always pass an explicit window** for a
comparable census, and **always record the window and the query versions in your
report** — an unstamped table of numbers is what made the previous audit
impossible to repeat.

## The review

Work the four dimensions below in order. The first three shape the findings; the
fourth decides where each fix goes and is the one you must not skip.

### 1. What fails, and how often — with denominators

```bash
pg-ccaudit query error-rate-by-tool --since 2026-07-22 --until 2026-07-30
pg-ccaudit query top-signatures --since 2026-07-22 --until 2026-07-30
pg-ccaudit query bash-by-lead-cmd --since 2026-07-22 --until 2026-07-30
```

- `error-rate-by-tool` reports errors **with** call counts. A raw error count with
  no denominator is not a rate and MUST NOT be presented as one.
- `signature` is normalized at ingest (paths, hashes, tool ids, bead ids and
  numbers collapsed), so one recurring problem is one row rather than hundreds.
- `bash-by-lead-cmd` attributes each Bash call to its real leading command, with
  `sudo`, `nice`, `VAR=` assignments and subshell parens peeled off.

### 2. Is it concentrated, or is it a real pattern?

```bash
pg-ccaudit query session-concentration '<signature>' --since … --until …
```

**Apply the runaway discount before you propose anything.** A signature firing 40
times inside ONE session is one agent stuck in a loop; the same 40 spread over 40
sessions is a systemic problem worth a standing rule. Compare `total` against
`distinct_sessions` and `worst_session`. If `worst_session` dominates `total`, say
so and weight the finding down.

### 3. Retries, narration, and measured cost

```bash
pg-ccaudit query retry-chains --since … --until …
pg-ccaudit query error-then-narration --since … --until …
pg-ccaudit query cost-by-signature --since … --until …
pg-ccaudit query hook-rejections --since … --until …
```

- `retry-chains` pairs a failed call with a later call of the SAME tool within a
  window of line ordinals (default 6, override with `retry-chains <n>`), scoped to
  the same session and file. **`identical_input = 1` is the strongest single
  signal in this review**: the same input re-sent after a failure is an agent that
  learned nothing from the error. `retry_is_error = 1` means the retry failed too.
- `error-then-narration` shows the prose written on the line right after a
  failure — what an agent rediscovers the hard way, and therefore the best raw
  material for the wording of a fix.
- `cost-by-signature`: **read its notes before quoting a number.**
  `duration_ms_sum` is legitimately near zero — Claude Code records a top-level
  `durationMs` only on `system` events, never on the event carrying a tool result.
  **`elapsed_ms_sum` is the measured cost**: wall time between the `tool_use`
  line's timestamp and its result's timestamp. It is measured, not estimated, so
  report it as a measurement — and note that parallel sibling calls overlap, which
  makes the sum an upper bound on serial cost.
- `hook-rejections` counts from the recorded `hookErrors` payloads, not from
  grepping error text — a rejection whose wording changed would vanish from a
  grep while still being counted here.

### 4. Main loop or subagent? — this decides WHERE the fix goes

```bash
pg-ccaudit query sidechain-split --since … --until …
```

**You MUST run this, and you MUST report the split PER SIGNATURE CLASS, never as
one aggregate.** This is the query whose absence forced the routing of an entire
set of adopted instruction fixes to be redone by hand.

The reason is concrete. In the 8-day census that motivated this tool, 53% of all
errors were subagent — a figure that, on its own, would have told you nothing. The
per-class split is what routed each fix:

| Signature class            | main loop | subagent | Where the fix had to go         |
| -------------------------- | --------: | -------: | ------------------------------- |
| Fabricated absolute root   |       104 |        0 | always-on user rules            |
| Bash timeout               |        54 |       73 | must reach the subagent brief   |
| Foreground `sleep` blocked |         5 |       21 | subagent-dominated              |
| pre-commit probe           |         3 |       17 | subagent-dominated              |
| `.git` directory blocked   |        10 |       31 | permission-approver rule tuning |

Route each finding accordingly:

- **Main-loop dominated** → the always-on user rules (`~/.claude/CLAUDE.md`).
- **Subagent dominated** → the subagent BRIEF, and/or the skill that dispatches
  it. A rule that lives only in the always-on user rules does not reliably reach a
  subagent, which is exactly how the subagent-dominated classes above survived.
- **Split roughly evenly** → both, and say so.
- **Permission/approver rejections** → the `claude-extended-tool-approver` rule
  set rather than an instruction anywhere.

### 5. Did a previous fix actually work?

```bash
pg-ccaudit query first-seen '<signature>' --since … --until …
pg-ccaudit query last-seen  '<signature>' --since … --until …
```

This closes the loop: it turns "we added a rule" into a **testable claim**. Use it
on every rule you are about to propose, and on every rule you are about to call
redundant. Doing this by hand previously produced two wrong conclusions — one
class was dated "last seen" three weeks before its actual final occurrence, and a
rule was called probably-redundant while its signature was firing 26 times in 8
days. Both were one query away.

## Reporting

Your report MUST contain:

1. **Provenance** — the window, the `pg-ccaudit status` coverage line (files
   indexed, `lines_bad`), and the query names with their versions. Without this
   the numbers cannot be compared with the next audit, which is the whole reason
   the queries are versioned.
2. **Findings ranked by measured cost and breadth**, each with its denominator,
   its session concentration, and its main-loop/subagent split.
3. **A routing decision per finding**, justified by that split.
4. **A verification claim per proposed fix** — the `first-seen` / `last-seen`
   evidence that the class is live and not already dead.
5. **Anything you could not answer**, and the query you wish existed.

Rules you propose MUST use RFC 2119 language (MUST / SHOULD / MAY), and MUST NOT
contain time estimates. Where you give a number, give the query that produced it.

## What NOT to do

- Do NOT scan raw transcripts. (Repeated because it is the failure this exists to
  prevent.)
- Do NOT report a raw error count as a rate.
- Do NOT propose a standing rule for a class that is one runaway session.
- Do NOT report an aggregate sidechain ratio in place of the per-class split.
- Do NOT quote `duration_ms_sum` as the cost of failures.
- Do NOT run `pg-ccaudit ingest` on your own initiative to "fix" a stale index —
  report the staleness and let the operator decide.
