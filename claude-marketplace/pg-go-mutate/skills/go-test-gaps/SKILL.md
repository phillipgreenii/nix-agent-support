---
name: go-test-gaps
description: Use when writing, reviewing, or strengthening tests for a Go package in this workspace — including when asked to "improve test coverage", "add missing tests", "make these tests better", or after implementing a Go feature and deciding what to test. Runs pg-go-mutate to find which assertions the tests do not make, then closes those gaps. Do NOT use for non-Go code, for running the normal test suite, or to measure or track a coverage or mutation score over time.
---

# Finding Go test gaps

Line coverage cannot tell you whether a test ASSERTS anything. A test can execute
an error branch and assert nothing, and coverage still reports the line green.
`pg-go-mutate` finds exactly that: it mutates the code and reports which mutations
the tests fail to catch.

**Every surviving mutant is an assertion the tests do not make.** It is not a bug
in production code, and you MUST NOT "fix" production code to make a mutant die.

## The loop

1. Run it scoped to one package, with `--workers 1` (the default is 2, which makes
   per-mutant verdicts non-reproducible — see the MUST below). Cost is roughly
   `mutants x the package's test-suite runtime`, so never point it at a whole large
   module:

   ```bash
   pg-go-mutate ./internal/collect --workers 1
   ```

2. Read the worklist. Each entry is `file`, line, the mutation, and the operator.
3. Write an assertion that would fail under that mutation.
4. Re-run and confirm **that specific mutant**, matched on `file:line:type`, is
   now killed — the worklist reports survivors only, so establish that by the
   procedure in "Establishing that a named mutant was killed" below.

## Establishing that a named mutant was killed

The worklist — human output and `--json` alike — reports **survivors only**, and the
wrapper deletes the engine report when it exits, so there is no positive `KILLED` row
to read for a named mutant. **Killed is established by ABSENCE from the survivor
worklist**, and absence is the same observable as a mutant that

- was never **generated** — gomu type-checks candidate mutations, but a mutation that
  would not typecheck may EITHER never be emitted at all OR be emitted and reported
  `NOT_VIABLE`; both behaviours are measured, so neither one can be predicted,
- lives in a file that **errored** or **timed out** during the run,
- was judged `NOT_VIABLE` (did not compile), or
- is a **no-op** (`original == mutated`), which the worklist drops before any count and
  which no assertion can ever kill.

Because a non-typechecking mutation lands in the first case OR the third, **`go build` on
the mutated line is the cheap way to settle which** — and the only thing that settles it.
Both were measured: the ordering variants in precondition 1 below were never generated at
all (`notViable` **0**), while 20 orphaned-binding and `%`-on-float mutations in a single
file WERE generated and reported `NOT_VIABLE`, each independently confirmed non-compiling
by `go build` with its own compiler error. So a `NOT_VIABLE` row MUST NOT be read as a
tool defect — nor, per the MUST NOT below, as proof of non-compilation.

So absence on its own is suggestive, not decisive. All three preconditions below MUST
hold before absence is read as killed.

1. **The mutant's identity MUST come from a worklist row, not from a prediction.** Only
   a row some run actually printed proves the mutation exists. Measured on one module,
   `u == nil` at `client.go:50` was given only `"==" -> "!="` and `runner == nil` at
   `secret.go:95` only 3 mutants in total: the 8 ordering variants (`<`, `<=`, `>`,
   `>=`) were never generated, and `notViable` was **0** at both lines. A predicted
   mutant that no worklist ever listed is absent from every later one too, and MUST NOT
   be called killed.
2. **The run MUST have used `--workers 1`, and MUST report `errors` 0, `timedOut` 0, and
   `notViable` 0.** Any of the three counts nonzero leaves absence ambiguous between
   killed and never-compiled/never-run. Above one worker the counts are themselves
   unreliable in BOTH directions — measured, a non-compiling mutant reported `KILLED`
   and a viable one reported `NOT_VIABLE` — so the gate can pass on a run whose buckets
   are wrong and can also fail spuriously. The `--workers` value is not in the `--json`
   payload, so the check below cannot verify it for you; passing it is on you. When the
   counts are nonzero, narrow the target to the single package holding the mutant and
   re-run; if they are still nonzero there, the outcome is **undecided** and MUST be
   reported as undecided, never as killed.
3. **The mutation type MUST appear in `statistics.mutationTypes`.** If the type is
   missing, gomu generated none of that type anywhere in the target, so the absence of
   your row says nothing about your line.

Read all three from one `--json` payload — `$file`, `$line` and `$type` are the
worklist row you are chasing:

```bash
pg-go-mutate ./internal/collect --workers 1 --json >pgm.json

jq --arg file collect.go --argjson line 95 --arg type branch_condition '
  { runDecisive: (.statistics.errors == 0 and .statistics.timedOut == 0
                  and .statistics.notViable == 0),
    errors: .statistics.errors, timedOut: .statistics.timedOut,
    notViable: .statistics.notViable,
    typeEverGenerated: ((.statistics.mutationTypes // {}) | has($type)),
    stillSurviving: [ .survivors[]
                      | select(.file == $file and .line == $line and .type == $type) ] }
' pgm.json
```

| `stillSurviving` | `runDecisive` | `typeEverGenerated` | Verdict                                                 |
| ---------------- | ------------- | ------------------- | ------------------------------------------------------- |
| non-empty        | —             | —                   | still a gap — the assertion is still missing            |
| empty            | true          | true                | **killed**, given precondition 1                        |
| empty            | —             | false               | **never generated** — report that, do not report killed |
| empty            | false         | true                | **undecided** — narrow the target and re-run, per 2     |

`runDecisive` covers only the three counters; `--workers 1` is the fourth precondition and
is not observable in the payload, so a `true` reading from a parallel run proves nothing.

`buildTagsNotRun` does not weaken a killed verdict: tag-gated tests that did not run
can only ADD survivors, never hide one. Read these bucket counts as run-health
preconditions and discard them — they are not a score, and the MUST NOT below still
forbids recording one.

**Phrase acceptance criteria accordingly.** "The mutants at `file:line` are reported
killed" is not satisfiable from this tool, because nothing reports a positive verdict.
Write "absent from the survivor worklist of a decisive run" instead, and treat "never
generated" as a legitimate outcome rather than a failure to verify.

Reaching for the pinned engine directly to read positive `KILLED` / `NOT_VIABLE` rows
SHOULD be a last resort: it roughly doubles the cost of an already expensive run, its
`NOT_VIABLE` verdicts are measurably unreliable (see the MUST NOT below), and the
short-`$TMPDIR` requirement in the MUST below still applies.

## MUST

- **MUST** pass `--workers 1` on any run whose rows will be acted on — which is every run
  you read. The default is 2 (the engine's own default is 4), and above 1 the per-mutant
  verdicts are not reproducible: measured on one package, two mutants that `go build`
  PROVES non-compiling were reported `KILLED`, a viable mutant was reported `NOT_VIABLE`,
  and — the dangerous direction — a non-compiling mutant printed as a **SURVIVOR**, so
  parallelism manufactures phantom gaps for a verifier to chase. The same tree at
  `--workers 1` was bit-identical across repeats (the full 62-row table, twice), and
  `notViable` settled to exactly the mutations `go build` proves non-compiling.
- **MUST** verify per-mutant, never by comparing survivor counts. Two module-wide runs
  over an identical tree disagreed on **13 of 451** mutants (4 survived only in the
  first run, 9 only in the second), so a count that moves by a few is indistinguishable
  from noise. Instability MUST NOT be predicted from code shape: a package with no
  `httptest` anywhere still disagreed run to run at the default worker count — including
  on pure-logic arithmetic and branch mutants — with whole-package buckets moving from
  `killed 39 / survived 18 / notViable 20` to `36 / 19 / 22`, and one `ERROR` row
  carrying `null` output. The lever is `--workers 1`, per the MUST above, not a
  judgement about whether a test stands up a server or races a timeout.
- **MUST** check for the build-tag note in the output before writing assertions.
  If a package gates tests behind custom tags (`contract`, `smoke`, `hostile`),
  mutants covered only by those tests appear as survivors. Re-run with
  `--tags contract,smoke` before treating them as real gaps.
- **MUST** treat a non-zero exit as an operational failure, not as a finding. The
  command exits 0 whenever it completed an analysis, however many mutants
  survived.
- **MUST** stop and write a test first if it reports that the package has no test
  files. Mutation testing reports missing assertions; with no tests, every mutant
  trivially survives and the worklist is meaningless.
- **MUST** run it through `pg-go-mutate` rather than invoking the pinned `gomu` engine
  directly. The engine writes its overlay to
  `$TMPDIR/gomu_overlay_<pid>_<ns>/mutant_<absolute-source-path>_<n>/overlay.json` — the
  target's ABSOLUTE path is embedded in a directory name, so a long `$TMPDIR` plus a
  deep worktree path (`.worktrees/<bead-id>` under a repo under the workspace root is
  routine here) overflows the filesystem path limit, and the failed write surfaces as
  `ERROR` or as spurious `NOT_VIABLE`. Measured: with `$TMPDIR` set to a deep scratchpad
  path, 23 not-viable plus one `ERROR` naming the overflow, against 14 and 13 not-viable
  from `pg-go-mutate` over the identical tree. `pg-go-mutate` is immune because it makes
  its own `mktemp -d`, keeping `$TMPDIR` short. If you do reach for the engine
  directly — wanting the non-survivor verdicts the wrapper's worklist omits is the
  usual reason — export a short `$TMPDIR` first.

## MUST NOT

- **MUST NOT** record the score anywhere — not in a file, not in a bead, not in a
  commit message. This is a diagnostic, not a tracked metric.
- **MUST NOT** add it to CI, a pre-commit hook, or a `checks.*` derivation. It is
  too slow, and outside a `--workers 1` run its result is not even reproducible.
- **MUST NOT** point it at `./...`; that pattern is rejected. Pass a directory —
  it is walked recursively, so a single package works. Single-file targets are
  rejected too.
- **MUST NOT** accept `NOT_VIABLE` as proof that a mutation cannot compile. One run
  reported `config.go:55` `"!=" -> "=="` and `secret.go:92` `"==" -> "!="` as
  `NOT_VIABLE`; both were `KILLED` in another run, and a string `==` cannot fail to
  compile, so the verdict was simply wrong. Re-run before treating any row as
  non-compiling, suspect the `$TMPDIR` overflow above or a `--workers` above 1, and
  settle it with `go build` on the mutated line.

## Where the highest-value gaps usually are

In this workspace's Go code, the consistent finding is that **returned errors are
almost never asserted on**. Mutating `err != nil` to `false` survived 70 times
across a sixteen-module sweep, and `error_nilify` (replacing a returned error with
`nil`) survived 44 of 48 completed cases. Branch and conditional coverage is
otherwise respectable. So when the worklist is long, start with the error paths.
