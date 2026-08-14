---
name: tool-error-waste-review
description: Run an agent-waste review over the Claude Code transcript corpus — both halves of it. Which tool calls fail, how often, with what error signatures, at what measured cost, whether an agent retries blindly; AND the mistakes that were never failed commands at all: work that succeeded and had to be undone, corrections a person had to type, interruptions, rejected tool calls, files rewritten five times, commands re-issued with only the quoting changed. Use when asked to audit or census agent errors, mistakes, corrections or wasted time across sessions, to find where agent time is going, to check whether a documented rule or instruction fix actually stopped a class of error, to route a fix to the right artifact (always-on user rules vs. workspace rules vs. a skill vs. a slash command vs. a subagent brief vs. a hook vs. permission-approver tuning), to measure how often the user corrects the agent, or to re-run a previous transcript audit for comparison. Also use for questions about retry loops, error-then-narration adjacency, hook rejections, per-tool or per-Bash-command error rates, how concentrated a failure class is in one runaway session, and how many human turns the corpus actually contains. Do NOT use for debugging one specific failing command right now (that is ordinary debugging), for reading a single named transcript, or for token/cost usage accounting.
---

# Agent-waste review

## THE SHAPE OF THIS REVIEW: TWO HALVES, ONE RANKED LIST

Failed commands are the **cheap** half of the waste. A failed command announces
itself, is usually self-correcting inside one round trip, and the agent notices
unaided. The expensive half is invisible to an `is_error` census: work that
**succeeded** technically and was wrong, and corrections a **person** had to type —
which mean nothing in the harness caught the mistake at all.

`pg-ccaudit report` emits **both halves in ONE ranked list**. You MUST NOT present
them as two lists: two lists let the cheap half dominate attention purely by being
easier to find, which is exactly what the census that motivated this tool did.

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
pg-ccaudit query hook-refusals-in-body --since … --until …
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
  grep while still being counted here. **It reads ZERO today, and that is not good
  news.** Measured over 408,651 indexed events, 406,373 carry no `hookErrors` key and
  the remaining 2,278 carry a literal `[]`; Claude Code puts the refusal in the
  `tool_result` body instead. Run it anyway — the day it stops returning zero is the
  day the structured field started arriving.
- `hook-refusals-in-body` is where the hook refusals actually are: 160 rows across 78
  sessions, 100 of them in a subagent. Group them by `kind` and by `signature`, not by
  `opening` (which is the raw evidence and differs per occurrence). **Two cautions.**
  Its recall over the whole class is a measured 160/203 (78.8%) — a guard whose refusal
  contains no refusal verb, such as the Jira greenlist guard's 43 rows, is invisible to
  it — so do not present its count as the total. And these rows are `is_error = 1`
  results, so they ALSO appear in `top-signatures` and `sidechain-split`; report the
  class once, not twice.

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

### 5. The mistakes that were never failed commands

```bash
pg-ccaudit candidates --since … --until …
```

Nine structural detectors, no model calls, tuned for **recall** — so most of what
this returns is NOT a mistake. Read the per-signal header it prints and the
`EMPTY SIGNALS` warning on stderr.

- **`human-turns` is the denominator, and the trap.** `type == "user"` is NOT a human
  turn. Measured corpus-wide: 90,579 user records, 1,183 `typed` and 26 `queued`; the
  rest are harness injections, slash-command and skill expansions, and 84,556 tool
  results. Reading every user record as a turn inflates the count **74.9x**. When you
  quote a correction rate, quote `inflation_factor` beside it.

  ```bash
  pg-ccaudit query human-turns --since … --until …
  ```

- **An empty detector is NOT evidence that the thing did not happen.** `hook-rejection`
  is a correct detector that returns **zero rows** corpus-wide, because Claude Code
  writes `hookErrors: []` and puts the rejection in the `tool_result` body instead.
  `hook-refusal-body` reads them there, and the empty one was deliberately kept and is
  still named on every report — so expect to see `hook-rejection` under
  `EMPTY SIGNALS` and do NOT report it as "no hook rejected anything". If a signal
  reports 0, read its notes before reporting good news.

- **A hook refusal is NOT fixed by proposing a hook.** The hook already exists and
  already fired. Split the class before routing it: a refusal that fired on the WRONG
  command is approver-rule tuning (the `.git` guard has blocked a
  `find … -not -path`), while a refusal that fired correctly on a reflex is an
  instruction problem (the `sleep` guard fired 80 times across 80 distinct sessions —
  once per session, re-learned from scratch every time).

- **The file channel means your correction count is a LOWER BOUND.** The operator
  sometimes writes a correction into a FILE rather than into a session (the
  `feedback_*.md` memories, a workspace `FEEDBACK.md`). Those are structurally
  invisible here. `pg-ccaudit gold seed` counts them; the report states the undercount.
  You MUST state it too, and MUST NOT present a transcript-only figure as "the
  correction rate".

- **Acknowledgment text is SUPPLEMENTARY, never a rate.** `ack-markers` fires only when
  the agent NOTICED and said so, so it measures an ACKNOWLEDGED mistake rate; reported
  as a mistake rate it makes agents getting quieter look like agents getting better.
  The `Correction:` stem is forward-only from 2026-07-30 and rule M-2 forbids it
  changing acknowledgment FREQUENCY, so a rise across that boundary is a **marking
  artifact** and MUST NOT be read as a rise in mistakes.

Then classify. The default classifier is the **naive baseline**, which exists to be
beaten, not used for findings:

```bash
pg-ccaudit classify --classifier cli --since … --until … --max 100
```

`--classifier cli` makes model calls and **reports what the run cost**. Bound it with
`--since`/`--until` or `--max`, and if the run was truncated say so — every rate
downstream is over the truncated set.

### 6. The one ranked, routed report

```bash
pg-ccaudit report --classifier cli --since … --until … --max 100
```

Every finding carries **exactly one** route: `global-rule`, `workspace-rule`, `skill`,
`slash-command`, `subagent-prompt-template`, `hook`, `permission-config`, or an
explicit `not-actionable` close. Nothing is unrouted. Where a finding needs a second
artifact the report says so in its `also:` line — quote that rather than inventing a
second route.

Ranking is

```text
score = occurrences x (1 + cost_ms/1000) x preventability(route)
```

Two things you MUST NOT do with those numbers:

- Do **not** treat `cost_ms = 0` as "free". It means no span was measurable. It is
  deliberately zero for anything whose interval ends at a HUMAN action — a typed turn,
  an interruption, a rejection — because that interval is the person's reading and
  deciding time, not agent waste. Measured, including it summed to 2,330 hours across
  719 typed turns and put the noisiest signal at the top of the report.
- Do **not** propose a standing rule for a finding carrying `RUNAWAY DISCOUNT`. That is
  one agent stuck in a loop, not a systemic problem.

If you propose a fix for a class the semantic pass called `guidance-defect`, route it
**back** to the instruction that induced it, not forward to a new rule. The worked
example is real: a stored memory recommended `--no-ext-diff` without stating flag
position, agents wrote the invalid `git --no-ext-diff diff`, and that class still
carries 25 occurrences — 0 main-loop, 25 subagent.

### 7. Is the classifier trustworthy on this corpus?

```bash
pg-ccaudit evaluate --classifier cli --since … --until …
```

It scores the semantic classifier AND the naive baseline over the same gold set and
**exits non-zero** if the semantic one does not win on correction F1. Report:

- the per-class precision and recall, and the `scored` count — under 50 the figures are
  arithmetic, not evidence, and the tool says so;
- **who labelled the gold set**. An agent-labelled set measures agreement between two
  models; only an operator-labelled one measures agreement with the person whose
  attention the corrections cost;
- the **marker recall** figure, which is MARKER COMPLIANCE and not a mistake rate.

If the gold set is missing or under-sized, say that instead of quoting per-class
numbers. `pg-ccaudit gold status` reports it; `gold seed` and `gold sample` grow it.

### 8. Did a previous fix actually work?

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
2. **ONE ranked list holding both halves** — mistakes and command failures together —
   each finding with its denominator, its session concentration, and its
   main-loop/subagent split. Never two lists.
3. **A routing decision per finding**, justified by that split, and exactly one route
   per finding.
4. **A verification claim per proposed fix** — the `first-seen` / `last-seen`
   evidence that the class is live and not already dead.
5. **The classifier and its evaluation** — which classifier ran, its reported run cost,
   whether it beat the baseline, and who labelled the gold set.
6. **The file-channel undercount**, stated explicitly wherever you quote a correction
   count.
7. **Anything you could not answer**, and the query you wish existed.

Rules you propose MUST use RFC 2119 language (MUST / SHOULD / MAY), and MUST NOT
contain time estimates. Where you give a number, give the query that produced it.

## What NOT to do

- Do NOT scan raw transcripts. (Repeated because it is the failure this exists to
  prevent.)
- Do NOT report a raw error count as a rate.
- Do NOT read `type == "user"` as a human turn — it inflates the count 74.9x. Use
  `human-turns`.
- Do NOT present mistakes and command failures as two separate lists.
- Do NOT propose a standing rule for a class that is one runaway session.
- Do NOT report an aggregate sidechain ratio in place of the per-class split.
- Do NOT quote `duration_ms_sum` as the cost of failures.
- Do NOT read a detector's zero as evidence that the thing it detects did not happen.
- Do NOT report an ACKNOWLEDGED mistake rate as a mistake rate, and do NOT read the
  `Correction:` marker's 2026-07-30 boundary as a behavioural change.
- Do NOT present a transcript-only correction count as "the correction rate".
- Do NOT run the `cli` classifier unbounded over the whole corpus without saying what
  it cost.
- Do NOT run `pg-ccaudit ingest` on your own initiative to "fix" a stale index —
  report the staleness and let the operator decide.
