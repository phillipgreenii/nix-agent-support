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

1. Run it scoped to one package. Cost is roughly `mutants x the package's
test-suite runtime`, so never point it at a whole large module:

   ```bash
   pg-go-mutate ./internal/collect
   ```

2. Read the worklist. Each entry is `file`, line, the mutation, and the operator.
3. Write an assertion that would fail under that mutation.
4. Re-run and confirm **that specific mutant**, matched on `file:line:type`, is
   now killed.

## MUST

- **MUST** verify per-mutant, never by comparing survivor counts. The run-to-run
  variance is about one mutant, so a count that drops by one is indistinguishable
  from noise.
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

## MUST NOT

- **MUST NOT** record the score anywhere — not in a file, not in a bead, not in a
  commit message. This is a diagnostic, not a tracked metric.
- **MUST NOT** add it to CI, a pre-commit hook, or a `checks.*` derivation. It is
  too slow and its result is not reproducible enough to gate on.
- **MUST NOT** point it at `./...`; that pattern is rejected. Pass a directory
  (walked recursively) or a single `.go` file.

## Where the highest-value gaps usually are

In this workspace's Go code, the consistent finding is that **returned errors are
almost never asserted on**. Mutating `err != nil` to `false` survived 70 times
across a sixteen-module sweep, and `error_nilify` (replacing a returned error with
`nil`) survived 44 of 48 completed cases. Branch and conditional coverage is
otherwise respectable. So when the worklist is long, start with the error paths.
