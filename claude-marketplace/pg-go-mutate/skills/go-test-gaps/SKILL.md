---
name: go-test-gaps
description: Use ONLY when the operator explicitly asks to run mutation testing or a test-gap analysis on a named Go package, or when changing the mutation tooling itself (pg-go-mutate, pg-go-mutate-tui). It is a slow, intensive, deliberate analysis whose output is a triage bead of areas to improve. Do NOT use in implementation, planning, review, validation or landing work; do NOT use when writing or strengthening tests, closing the gaps a past analysis found, improving coverage, or deciding what to test; do NOT use for non-Go code or for a coverage or mutation score.
---

# Finding Go test gaps

Line coverage cannot tell you whether a test ASSERTS anything. A test can execute
an error branch and assert nothing, and coverage still reports the line green.
`pg-go-mutate` finds exactly that: it mutates the code and reports which mutations
the tests fail to catch.

**Every surviving mutant is an assertion the tests do not make.** It is not a bug
in production code, and you MUST NOT "fix" production code to make a mutant die.

## When this applies (operator-initiated only)

A mutation run is slow and intensive (cost is roughly
`mutants x the package's test-suite runtime`, hours for a real package), so it is
NEVER a step of ordinary work. Operator ruling (Phillip, 2026-10-26, verbatim):
"typical development flows must not invoke mutation tests. mutation tests are
something done outside of standard implementation flows. ie, i may decide to run
mutation tests on an app, one package at a time to generate beads for
improvements. each use of mutation tests should be intentional outside of
implementation work. it is used to generate areas which need to be improved."
Earlier rulings: 2026-10-20 and 2026-10-22 ("unless the story is about mutation
testing itself, none of the standard planned work should rely on running mutate
tests").

- You MUST run it only when the operator asked for a mutation or gap analysis of a
  named package, or when the bead you are working changes the mutation tooling
  itself (`pg-go-mutate`, `pg-go-mutate-tui`).
- You MUST NOT run it, or put it in a brief, plan, packet, acceptance criterion or
  validation step, as part of implementing, fixing, reviewing, strengthening tests
  for, or landing code. "Is this test any good?" is answered by a hand-applied
  mutant (reintroduce the defect once, watch the test fail), never by this tool.
- Whole-package coverage across the workspace is the job of the background sweep
  (`pg-go-mutate-tui`), not of any work item.

## Use 1: the analysis (the operator's request)

Run one package at a time and turn the worklist into a bead. Do not write the
missing tests in the same pass unless the operator asked you to.

1. Run it scoped to one package, with `--workers 1` (the default is 2, which makes
   per-mutant verdicts non-reproducible — see the MUST below). Never point it at a
   whole large module:

   ```bash
   pg-go-mutate ./internal/collect --workers 1
   ```

2. Read the worklist ("Reading the worklist" below). Each entry is `file`, line,
   the mutation, and the operator. If the tool reports that the package has no test
   files (exit `10`), there is no worklist: the finding is "no tests", and the bead
   in step 3 says so.
3. File the result as ONE `P3` triage bead per package, in the tracker of the repo
   that holds the package (`bd -C <tracker-root> ...`), titled
   `go-test-gaps triage: <pkgPath>` and labeled `go-test-gaps` plus that repo's
   label. First search that tracker for an existing bead with that title (the
   background sweep files the same one) and AMEND it instead of filing a second.
   The bead carries the worklist rows (`file:line`, mutation, operator) and the
   exact command you ran. Never an epic (an open epic never leaves `bd ready`). No
   survivor or kill counts, percentages or scores go in it.

## Closing the gaps a past analysis found (ordinary test work, not a use of this tool)

A triage bead's gaps are closed by writing tests, as any other implementation
bead is. Operator ruling (Phillip, 2026-10-26, verbatim): "i don't think we need
to re-run the mutation tests at all. i think what the mutation had uncovered when
it ran was the problem. the problem is fixed when a test is written which covers a
missed path. as long as the acceptance criteria includes the path and test(s)
which should exist, i think that is sufficent to fixing the problem. a mutation
test would run on this app/package later and it shouldn't find the same issue
twice."

- The acceptance criteria of a gap-closing bead name each missed path (`file:line`
  and the behavior) and the test(s) that must exist to cover it, for example "a
  test asserts that `collect.go:95` returns the error when the runner is nil".
- The work is done when those tests exist and the normal suite passes. A
  hand-applied mutant (reintroduce the defect once, watch the new test fail) MAY
  back that up.
- You MUST NOT re-run `pg-go-mutate` to confirm the gap is closed, and MUST NOT
  write "absent from a re-run's worklist" into an acceptance criterion. The next
  analysis or sweep over the package is the check, and it should not find the same
  gap twice.
- A row no assertion can kill (a no-op or equivalent mutant) is noted in the bead
  and closed without a test.

## Reading the worklist

The worklist, human output and `--json` alike, reports **survivors only**, so a
row is a lead to judge, not a verdict.

- A survivor may be a phantom: verify a suspicious row by applying that exact
  mutation by hand and running the suite. Running above one worker manufactures
  phantoms (see the MUST below).
- Nonzero `errors`, `timedOut` or `notViable` counts in the run mean part of the
  package was not decisively analyzed; say so in the bead. These counts are run
  health, not a score, and the MUST NOT below still forbids recording them.
- A mutation that does not typecheck may be omitted or reported `NOT_VIABLE`;
  `go build` on the mutated line settles which.

## MUST

- **MUST** pass `--workers 1` on any run whose rows will be acted on — which is every run
  you read. The default is 2 (the engine's own default is 4), and above 1 the per-mutant
  verdicts are not reproducible: measured on one package, two mutants that `go build`
  PROVES non-compiling were reported `KILLED`, a viable mutant was reported `NOT_VIABLE`,
  and — the dangerous direction — a non-compiling mutant printed as a **SURVIVOR**, so
  parallelism manufactures phantom gaps for a reader to chase. The same tree at
  `--workers 1` was bit-identical across repeats (the full 62-row table, twice), and
  `notViable` settled to exactly the mutations `go build` proves non-compiling.
- **MUST** judge each row on its own, never by comparing survivor counts between runs.
  Two module-wide runs over an identical tree disagreed on **13 of 451** mutants (4
  survived only in the first run, 9 only in the second), so a count that moves by a few
  is indistinguishable from noise. Instability MUST NOT be predicted from code shape: a
  package with no `httptest` anywhere still disagreed run to run at the default worker
  count — including on pure-logic arithmetic and branch mutants — with whole-package
  buckets moving from `killed 39 / survived 18 / notViable 20` to `36 / 19 / 22`, and one
  `ERROR` row carrying `null` output. The lever is `--workers 1`, per the MUST above, not
  a judgement about whether a test stands up a server or races a timeout.
- **MUST** check for the build-tag note in the output before filing the bead.
  If a package gates tests behind custom tags (`contract`, `smoke`, `hostile`),
  mutants covered only by those tests appear as survivors. Re-run with
  `--tags contract,smoke` before treating them as real gaps.
- **MUST** treat a non-zero exit as an operational failure, not as a finding. The
  command exits 0 whenever it completed an analysis, however many mutants
  survived. The guard exit codes (`10` no test files, `11` not enumerable, `12`
  unhealthy, `13` environment, `14` target absent) say why it never ran.
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

- **MUST NOT** record a score anywhere — no survivor or kill count, no percentage, not
  in a file, a bead or a commit message. The worklist rows are the product and may
  go in the triage bead; a number derived from them is not. This is a diagnostic,
  not a tracked metric.
- **MUST NOT** invoke it from implementation, planning, review, validation or
  landing work, or on your own initiative (see "When this applies").
- **MUST NOT** add it to CI, a pre-commit hook, a `checks.*` derivation, a work
  packet or an acceptance criterion. It is too slow, and outside a `--workers 1`
  run its result is not even reproducible. (A self-test of the tooling's own
  source against a small synthetic fixture project is not a mutation run of user
  code and is exempt.)
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
