# The `cmdparse` lowering seam — coverage record

Authority: `docs/adr/0039-ceta-shell-parser-front-end.md` (this repo), its Decision, Invariants and
Enforcement sections. This file is the **per-construct coverage record** ADR 0039's migration step 1
owes, plus the **corpus population** every later step MUST cite instead of re-deriving one.

The seam is `shellparse.go`. It is the only file in this Go module that may import `mvdan.cc/sh/v3`
(I6), enforced by `TestSeamIsTheOnlyParserImporter`. The shadow comparison is `shadow.go`; the
latency gate and the census are `frontend_ab_test.go`.

## Status of this step

The candidate front end runs in **SHADOW**. The **outgoing** front end —
`StripCommentsPreservingHeredocs` then `Parse`, in that order — remains authoritative for every
verdict. `LogShadowDisagreement` returns nothing, so nothing in the engine can read the candidate's
result. **No behaviour change ships in this step**, and no existing test expectation was edited.

## Lowering completeness

Every construct ADR 0039 and bead `pg2-jxmk9` name, and whether it is covered or deferred.

| Construct                                | Status      | How                                                                                                                                                                                                                                             |
| ---------------------------------------- | ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `unwrapCommand`                          | **Covered** | REUSED verbatim. It is already `ParsedCommand -> ParsedCommand`, so the lowering builds a leaf and calls it. Reuse is what makes parity provable rather than re-argued.                                                                         |
| `unwrapExecPrefix`                       | **Covered** | Reached through `unwrapCommand`. `env`/`command` land in `CallExpr.Args`, which is where it already looks.                                                                                                                                      |
| `unwrapCommandRunner`                    | **Covered** | Reached through `unwrapCommand` (`nice`, `timeout`, `nohup`, `stdbuf`). Pinned by `TestShellParse_UnwrapReuse`, which also asserts the outgoing front end agrees.                                                                               |
| `liftAssignmentArgs`                     | **Covered** | Reached through `unwrapCommand` for the `export` branch. `export A=1 B` arrives as a `*syntax.DeclClause`, lowered to `Executable="export"` with each `Assign`'s verbatim source slice as an argument, so the existing lift applies unchanged.  |
| `NormalizeExecutable`                    | **Covered** | Pure string work on an executable; unaffected by the front end. No change.                                                                                                                                                                      |
| `NormalizeCommand`                       | **Covered** | `NormalizeCommandShell` is the same function over the seam. Both spellings are kept so the persisted hook-miss bucketing can be re-keyed deliberately rather than discovered after the fact.                                                    |
| `resolveLoops` (hole A, terminator)      | **Covered** | Structurally, and MORE generally than the text version: a redirection on a compound sits on the compound's own `*syntax.Stmt`, so `emitCompoundRedirs` covers `done > f`, `(cmd) > f`, `{ …; } > f`, `if … fi > f` and `case … esac > f` alike. |
| `resolveLoops` (hole B, word list)       | **Covered** | `WordIter.Items` lower to ONE command-less data leaf carrying only `Raw`, `PipelineID` `-1`. Replicates the POST-`pg2-qkecz` behaviour, not the pre-fix behaviour.                                                                              |
| Herestring `<<<`                         | **Covered** | `syntax.WordHdoc` sets `HasHeredoc` from the OPERATOR and records no extent and no redirection. Keying off a non-empty body would drop the I2 floor for every herestring.                                                                       |
| `unquote` parity                         | **Covered** | The token is the outgoing `unquote` applied to each word's EXACT SOURCE SLICE. A true literal expansion is deliberately NOT used: it would turn `a'b'c` into `abc` and newly clear the predicate I4 exists to fence.                            |
| Heredoc extents                          | **Covered** | Delimiter, `Quoted`, `StripTabs` and `Body` from `Redirect.Word`/`Redirect.Hdoc`. `Body` matches the outgoing `readHeredocBody` byte for byte, `<<-` included. `Terminated` is always true — an unterminated heredoc is a parse failure.        |
| Process substitution                     | **Covered** | The fabricated `/dev/fd/63` operand plus the lifted body, matching `tokenize`. Both halves are load-bearing: the source text instead causes mass new abstains, nothing at all loses the operand.                                                |
| Redirection grammar                      | **Covered** | `redirCore` maps every parser operator onto the outgoing operator TEXT, so `Operator` and the derived `Kind` are unchanged. `2>&1`, `>&-` and `<&3` are dropped as fd duplication/close.                                                        |
| Position independence (`pg2-gkd5e`)      | **Covered** | Leading assignments come from `CallExpr.Assigns`, the `env` form from `Args`. `TestShellParse_PositionIndependentAssignments` pins both reaching the same `EnvVars`, and pins that a trailing `cmd FOO=1` operand is NOT lifted.                |
| Indexed / associative assignment         | **Covered** | `BEAD_IDS[1]="x"`, `m[$k]=$(…)`. The name is not a valid identifier so it cannot become an `EnvAssignment`; it reaches a DATA leaf instead. Dropping it was a real defect the census caught — see "Defects this step found".                    |
| Pipeline relation                        | **Covered** | A `BinaryCmd` pipe chain is flattened in source order and shares one `PipelineID`. The under-approximation for a compound's LATER stages is carried over from the outgoing front end unchanged, not newly introduced.                           |
| Untaken branches (I14)                   | **Covered** | `if`/`elif`/`else`, every `case` item, and a `FuncDecl` body are all lowered. Executedness is a runtime property; the binding form is I14's static surrogate.                                                                                   |
| `ArithmCmd` / `TestClause` / `LetClause` | **Covered** | Each reaches a DATA leaf carrying its source span. None executes a command, but each can embed a live `$( )`. The outgoing front end judged them as commands (`[[`, `((`), which this stops.                                                    |
| Comment handling                         | **Retired** | `KeepComments(true)` makes a comment a parser fact, so no comment pass runs in the candidate at all. `ParsedCommand.Comment` is left empty; on the engine's path the outgoing value is ALWAYS empty too, because the engine pre-strips.         |

### Deferred, with reasons

| Item                                                  | Why deferred                                                                                                                                                                                                                                                                           |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Flipping the engine to the candidate                  | This step is shadow-only by construction. The flip is `pg2-zeqa5`/`pg2-fez3d`.                                                                                                                                                                                                         |
| The substitution-scan family (`ScanSubstitutions`)    | ADR 0039's Consequences calls this a third front end whose local "unparseable" notion has no AST analogue. Migrating it changes verdicts and owes its own enumeration, so it is not done under a no-behaviour-change step.                                                             |
| Enforcement guard 3 (parse-count, I7)                 | ADR 0039's Enforcement states it MUST land AFTER the per-rule `gitdir` migration, because `gitdir` re-parses the root expression and recurses to depth 8, so the guard cannot go green before that.                                                                                    |
| Enforcement guard 2 (raw-text-structure, I9)          | ADR 0039 carries the mechanism as an OPEN question and assigns the decision to the per-rule migration step, which must record which mechanism it chose and why.                                                                                                                        |
| Enforcement guard 4 (coverage check, I14)             | Partially discharged: the leaf-delta accounting in `frontend_ab_test.go` runs over the whole snapshot and did catch a real dropped-leaf defect. The full span-union assertion against every `*syntax.CallExpr` is a separate check and belongs with the flip, which owns the leaf set. |
| `ParsedCommand.Raw` becoming the exact slice for real | The candidate already produces it (I12). It cannot be adopted while the outgoing front end is authoritative, because rules re-parse `Raw` and the two spellings differ on heredoc-bearing leaves.                                                                                      |

### Defects this step found

- **Indexed and associative array-element assignments were DROPPED by the first draft of the
  lowering** — `BEAD_IDS[85591]="zr-8pl"` produced no leaf at all, because `isEnvAssign` rejects the
  bracketed name and nothing else claimed it. That is root cause 4 (a pass DELETING a segment) in the
  new code, and it would have looked like a tidier leaf count in a differential replay. Found by the
  corpus census's dangerous-direction dump, fixed, and pinned by
  `TestShellParse_IndexedAssignmentReachesALeaf`.

### Defects in the OUTGOING front end that the census surfaced

These are recorded because they are the largest components of the leaf delta and each will move
verdicts at the flip. None is fixed here — this step ships no behaviour change.

- **Line-continuation token debris.** `splitCompound` consumes `\`+newline as an escaped pair but
  copies BOTH bytes into the segment, so `tokenize` yields the bogus executable `"\<newline>curl"`
  and arguments like `"\<newline>-H"`. No argv[0]-keyed rule matches such a leaf.
- **Process-substitution bodies split at an inner operator.** `splitCompound` tracks no `<(` extent,
  so `diff -u <(cat x | jq .) <(cat y | jq .)` splits at the inner `|`, producing the argument
  `"<(cat"` and the bogus executable `"jq -S .)"`, with neither body lifted.
- **Multi-line array literals shredded.** `t=(\n "a"\n "b"\n)` is split on the newlines inside the
  paren group, so each element becomes a bogus executable and the assignment's value is emptied.
- **Unbalanced outer quote retained.** `git commit -m "$(cat <<'EOF' … EOF)"` keeps the leading `"`
  in the token, because `unquote` only strips a fully wrapped token and the heredoc extent pass left
  it unwrapped.

## Corpus population — the definition later steps MUST cite

Recorded once, here, per bead `pg2-jxmk9`'s acceptance criteria. Later steps cite THIS, not ADR
0039's figure. Measured 2026-08-12 against
`file:$HOME/.local/share/claude-extended-tool-approver/asks.db?immutable=1` (read-only; note
`sqlite3 -readonly` FAILS on that file with SQLite error 14, and reading a LIVE database as
`immutable=1` can transiently report "database disk image is malformed (11)" — re-run the query).

| Population                                                     | Count       | Definition                                                                            |
| -------------------------------------------------------------- | ----------- | ------------------------------------------------------------------------------------- |
| all rows                                                       | 337,781     | `tool_decisions`                                                                      |
| non-excluded rows, all tools                                   | 337,236     | `excluded=0`                                                                          |
| **non-excluded `Bash` rows**                                   | **218,089** | `excluded=0 AND tool_name='Bash'`                                                     |
| **distinct `.command` VALUES** — the parse/lowering population | **185,185** | `COUNT(DISTINCT json_extract(tool_input_json,'$.command'))`                           |
| distinct input BLOBS                                           | 198,691     | `COUNT(DISTINCT tool_input_json)` — the unit ADR 0039 reported as "distinct commands" |
| snapshot rows actually measured                                | 185,188     | the extracted JSONL, deduplicated by the harness                                      |

ADR 0039's **"189,678 distinct `command` strings" is MISLABELLED** (bead `pg2-bc8ol`): that figure
counts distinct input BLOBS, not distinct `.command` values. Its percentages are self-consistent —
only the unit is wrong. The equivalent figures above are the blob count (198,691) and the true
distinct-command count (185,185), and the corpus has grown since the ADR was written, which is why
step 1 re-measured rather than comparing against the recorded numbers.

**Replayability.** The parse, lowering and coverage checks need no working directory and run on ALL
185,188 distinct commands. The VERDICT replay cannot: measured 2026-08-12 over the 218,178
non-excluded `Bash` rows then present, **73,776 (33.81%) name a `cwd` that no longer exists** — 909
of 1,129 distinct `cwd` values — and cannot be replayed. The replayable subset is 144,402 rows
across 220 `cwd` values and MUST NOT be presented as the whole.

**Offline discipline.** A replay MUST use `setup.NewEngineForCWD` plus `EvaluateHook`, or redirect
`XDG_DATA_HOME`. `cmd_evaluate` MUST NOT be used: it opens the shared production asklog
READ-WRITE.

## Latency gate result

Same-snapshot A/B, 185,188 distinct commands, interleaved per command, 3 reps per side. The
historical figures were NOT used as a baseline — they were taken on an incomplete lowering and the
corpus has grown since.

Measured on Apple M3 Pro, darwin/arm64, PARSEABLE rows only:

| Run | outgoing mean | candidate mean | outgoing p50 | candidate p50 | outgoing p99 | candidate p99 | outgoing max | candidate max |
| --- | ------------- | -------------- | ------------ | ------------- | ------------ | ------------- | ------------ | ------------- |
| 1   | 20.707 µs     | 8.418 µs       | 10.347 µs    | 4.139 µs      | 123.139 µs   | 48.542 µs     | 28.621 ms    | 22.559 ms     |
| 2   | 13.813 µs     | 5.314 µs       | 8.097 µs     | 3.097 µs      | 87.903 µs    | 33.278 µs     | 2.654 ms     | 2.604 ms      |
| 3   | 11.722 µs     | 4.271 µs       | 7.056 µs     | 2.611 µs      | 68.125 µs    | 24.389 µs     | 1.173 ms     | 0.324 ms      |
| 4   | 14.014 µs     | 5.423 µs       | 8.028 µs     | 3.069 µs      | 88.695 µs    | 35.445 µs     | 1.603 ms     | 0.680 ms      |

Absolute figures move with machine load between runs; the RATIO does not — across four independent
runs the candidate/outgoing ratio is **mean 0.364–0.407x, p50 0.370–0.400x, p99 0.358–0.400x**, and
`max` improved in every run too.

**GATE: PASS.** ADR 0039's pass criterion is mean and p99 both NO WORSE; both are roughly 2.5x
BETTER, so the "not slower than what it replaces" conclusion the decision rests on survives
re-measurement against the COMPLETE lowering. No max-only waiver is needed.

The gate is judged on the PARSEABLE-ONLY series deliberately: the candidate returns early on a parse
failure without lowering anything, so including those rows would flatter it. The two series differ by
under 0.1% because only 63 of 185,188 rows fail to parse.

Reproduce with:

```bash
CETA_AB_SNAPSHOT=/path/to/corpus-snapshot.jsonl \
CETA_AB_REPORT=/path/to/report.txt \
  go test ./internal/cmdparse/ -run TestFrontEndAB -timeout 60m -v
```

## Disagreement census

Over the whole 185,188-command snapshot, candidate against outgoing:

| Measure                                 | Rows    | Share    |
| --------------------------------------- | ------- | -------- |
| content-identical leaf sets             | 173,096 | 93.4704% |
| content disagreements                   | 12,029  | 6.4956%  |
| candidate unparseable (I1b forfeiture)  | 63      | 0.0340%  |
| — of which the parser attributed to zsh | 6       | 0.0032%  |
| `Raw` differs on a content-matched leaf | 3,444   | 1.8597%  |
| pipeline grouping differs               | 4,883   | 2.6367%  |

`Raw` and the pipeline grouping are counted SEPARATELY from leaf content, because each changes by
design (I12 redefines `Raw`; `PipelineID` is a per-call sequence that necessarily renumbers wherever
the leaf set differs). Folding them into one "differs" bit would bury the changes that are NOT by
design.

The 63 unparseable rows are I1b forfeitures: no leaf is examined, so any `Reject` a leaf would have
earned is given up. That is a movement in the more permissive direction on
`Approve < Abstain < Ask < Reject` even though it never reaches approve, and it is why the replay
gate is worded as "no transition in the less-restrictive direction" rather than "toward approve".

## The leaf-count delta, accounted for row by row

ADR 0039's Enforcement FORBIDS blanket annotation — three beads in this chain shipped on a blanket
plan and were wrong each time. So every disagreeing row is assigned a cause by a MECHANICAL
predicate over its leaf sets, the per-cause deltas are asserted to SUM EXACTLY to the snapshot
delta (the harness fails the test otherwise), and whatever no predicate claims is DUMPED VERBATIM
with both leaf sets rather than absorbed into an "other" bucket.

Snapshot leaf totals: outgoing **863,721**, candidate **854,694**, delta **−9,027 (−1.05%)**. ADR
0039 recorded −1.22% (818,475 against 828,620) on the smaller corpus; the direction and magnitude
reproduce.

| Cause                                                                         | Rows       | Leaf delta |
| ----------------------------------------------------------------------------- | ---------- | ---------- |
| outgoing line-continuation token debris removed (backslash-newline)           | 3,784      | −694       |
| same executables, argument tokenisation differs (quoting/expansion)           | 3,691      | −1         |
| keyword pseudo-leaves removed; real commands in branches/bodies judged (I14)  | 3,588      | −7,234     |
| outgoing token retained an unbalanced outer quote (heredoc in a substitution) | 374        | **+308**   |
| outgoing kept bash's `!` negation as an operand of a keyword pseudo-leaf      | 149        | −1         |
| outgoing process-substitution body split at an operator inside it             | 123        | −344       |
| candidate unparseable (I1b forfeiture)                                        | 63         | −468       |
| keyword pseudo-leaves removed; surrounding leaf set re-derived                | 48         | −143       |
| keyword pseudo-leaves removed; data leaf added (word list/case/test)          | 46         | −63        |
| outgoing shell-keyword pseudo-leaves removed                                  | 45         | −74        |
| outgoing multi-line array literal shredded into bogus executables             | 30         | −296       |
| UNCLASSIFIED — all 151 dumped verbatim in the report                          | 151        | −17        |
| **TOTAL**                                                                     | **12,092** | **−9,027** |

Reading of the table:

- **The delta is dominated by ONE cause.** −7,234 of −9,027 (80.1%) is the outgoing front end's
  shell-keyword pseudo-leaves disappearing while the real commands inside those compounds start being
  judged. A leaf whose executable is `if`, `then`, `fi`, `do` or `done` is not a command; removing it
  and judging the branch contents instead is the point of the migration, not a loss.
- **One cause moves the count UP.** +308 leaves on 374 rows, where the outgoing front end swallowed a
  following command into a token that kept an unbalanced outer quote. Those are commands that were
  never judged at all.
- **The DANGEROUS direction is EMPTY.** The harness has a dedicated cause for "an outgoing leaf the
  candidate does not produce, with no keyword, continuation, process-substitution or quote
  explanation", and dumps every such row in full because it is the only class that could LOSE a
  judgement. It found 5 rows on the first run — all of them the indexed-assignment defect recorded
  above — and **zero** after that defect was fixed.
- **151 rows (0.08% of the snapshot, 0.19% of the delta) remain unattributed** and are dumped
  verbatim with both leaf sets. They are not annotated, blanket or otherwise; the flip step must
  read them.
