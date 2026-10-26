---
name: go-test-gaps
description: Use ONLY when the operator explicitly asks to run mutation testing or a test-gap analysis on a named Go package; or, while changing the mutation tooling itself (pg-go-mutate, pg-go-mutate-tui), to run it against a small synthetic fixture project only. It is a slow, intensive, deliberate analysis whose output is a triage bead of areas to improve. Do NOT use in implementation, planning, review, validation or landing work; do NOT use when working a go-test-gaps triage bead (it carries its own instructions), writing or strengthening tests, closing the gaps a past analysis found, improving coverage, or deciding what to test; do NOT use for non-Go code or for a coverage or mutation score.
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
  named package, or while changing the mutation tooling itself (`pg-go-mutate`,
  `pg-go-mutate-tui`). Tooling work MAY run the tool only against a small
  synthetic fixture project, never against a workspace package, and never to
  confirm that a gap is closed.
- You MUST NOT run it, or put it in a brief, plan, packet, acceptance criterion or
  validation step, as part of implementing, fixing, reviewing, strengthening tests
  for, or landing code. That includes a bead that closes gaps a past analysis
  found, even when the package lives inside the tool's own source. "Is this test
  any good?" is answered by a hand-applied mutant (reintroduce the defect once,
  watch the test fail), never by this tool.
- Whole-package coverage across the workspace is the job of the background sweep
  (`pg-go-mutate-tui`), not of any work item. The sweep starts only when the
  operator launches it; nothing schedules it.

## Use 1: the analysis (the operator's request)

Run one package at a time and turn the worklist into a bead. Do not write the
missing tests in the same pass unless the operator asked you to.

1. Run it scoped to one package. `pg-go-mutate` forces one worker, and `--workers`
   is accepted but ignored; do not pass it. Verdicts still vary a little between
   runs (see MUST below). Never point it at a whole large module:

   ```bash
   pg-go-mutate ./internal/collect
   ```

2. Read the worklist ("Reading the worklist" below). Each entry is `file`, line,
   the mutation, and the operator. If the tool exits `10` (no test files), first
   check whether the package's tests all sit behind build tags
   (`rg -l '^//go:build' --glob '*_test.go' <pkg>`): the tool checks for tests
   before it prints the build-tag note, so a tag-gated package also exits `10`.
   If so, re-run with `--tags <those tags>`. Only a package with no test files at
   all has the finding "no tests", and the bead in step 3 says so.
3. File the result as ONE `P3` triage bead per package, in the tracker of the repo
   that holds the package (`bd -C <tracker-root> ...`), titled
   `go-test-gaps triage: <pkgPath>` and labeled `go-test-gaps` plus that repo's
   label. `<pkgPath>` is the package directory relative to its module root, with
   no leading `./` (for example `internal/collect`), the same form the sweep uses.
   - First run `bd -C <tracker-root> search "go-test-gaps triage: <pkgPath>"`.
     Treat only an OPEN bead whose title matches exactly as a match: `bd search`
     also returns closed beads and substring hits (`internal/collector`). If one
     exists, amend it with your rows instead of filing a second; otherwise file a
     new bead. (The background sweep will file this same bead shape once
     `pg2-tkm9g0` wires it; today it files none.)
   - The bead body names the package and carries the exact command you ran, the
     worklist rows (`file:line`, mutation, operator), and the instructions in
     "Working a triage bead" below, copied in so the agent who works the bead has
     them without loading this skill. Never an epic (an open epic never leaves
     `bd ready`). No survivor or kill counts, percentages or scores go in it.

## Working a triage bead (analysis of recorded results, not a use of this tool)

Operator ruling (Phillip, 2026-10-26, recorded verbatim on `pg2-tkm9g0`): when a
package's run completes, its triage bead is the signal for an agent to "do a deep
dive on the results to generate the appropriate fixes". Copy this block into
every triage bead body:

```markdown
## How to work this bead

- Analyze the results recorded in this bead. Do NOT re-run `pg-go-mutate` or
  `pg-go-mutate-tui`.
- Decide which tests would close the gaps. A row no assertion can kill (a no-op
  or equivalent mutant) needs no test; say so.
- File one new bead per fix with `--deps "discovered-from:<this bead's id>"`
  (never a blocking edge). Its acceptance criteria name each missed path
  (`file:line` and the behavior) and the test(s) that must exist.
- Close this bead with a reason that lists the fix beads filed, or that says
  why no fix is needed.
```

## Closing the gaps a past analysis found (ordinary test work, not a use of this tool)

A fix bead's gaps are closed by writing tests, as any other implementation
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
  mutation by hand and running the suite.
- Nonzero `errors`, `timedOut` or `notViable` counts in the run mean part of the
  package was not decisively analyzed; say so in the bead. These counts are run
  health, not a score, and the MUST NOT below still forbids recording them.
- A mutation that does not typecheck may be omitted or reported `NOT_VIABLE`;
  `go build` on the mutated line settles which.

## MUST

- **MUST** judge each row on its own, never by comparing survivor counts between runs.
  Two module-wide runs over an identical tree disagreed on **13 of 451** mutants (4
  survived only in the first run, 9 only in the second), so a count that moves by a few
  is indistinguishable from noise. Instability MUST NOT be predicted from code shape: a
  package with no `httptest` anywhere still disagreed run to run, including on
  pure-logic arithmetic and branch mutants, and one `ERROR` row carried `null` output.
- **MUST** check for the build-tag note in the output before filing the bead.
  If a package gates tests behind custom tags (`contract`, `smoke`, `hostile`),
  mutants covered only by those tests appear as survivors. Re-run with
  `--tags contract,smoke` before treating them as real gaps.
- **MUST** treat a non-zero exit as an operational failure, not as a finding, with
  one exception: the guard exit `10` (no test files) is itself the finding "no
  tests". The command exits 0 whenever it completed an analysis, however many
  mutants survived. The other guard exit codes (`11` not enumerable, `12`
  unhealthy, `13` environment, `14` target absent or not a directory or `.go` file)
  say why it never ran, and `3` means every semaphore slot stayed held for the
  whole wait (`PGM_SEM_TIMEOUT`, one hour by default). Exit `10` can also mean
  the tests sit behind build tags; see step 2 of Use 1.
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
  landing work, or on your own initiative (see "When this applies"). Tooling work
  against a small synthetic fixture project is a self-test, not mutation testing,
  and is not covered by this rule.
- **MUST NOT** add it to CI, a pre-commit hook, a `checks.*` derivation, a work
  packet or an acceptance criterion. It is too slow. (A self-test of the tooling's
  own source against a small synthetic fixture project is not a mutation run of user
  code and is exempt.)
- **MUST NOT** point it at `./...`; that pattern is rejected. Pass a package
  directory: it is walked recursively, so a single package works. A single `.go`
  file is accepted too, but it still analyzes that file inside its containing
  package, whose tests must pass first.
- **MUST NOT** accept `NOT_VIABLE` as proof that a mutation cannot compile. One run
  reported `config.go:55` `"!=" -> "=="` and `secret.go:92` `"==" -> "!="` as
  `NOT_VIABLE`; both were `KILLED` in another run, and a string `==` cannot fail to
  compile, so the verdict was simply wrong. Re-run before treating any row as
  non-compiling, suspect the `$TMPDIR` overflow above, and settle it with `go build`
  on the mutated line.

## Where the highest-value gaps usually are

In this workspace's Go code, the consistent finding is that **returned errors are
almost never asserted on**. Mutating `err != nil` to `false` survived 70 times
across a sixteen-module sweep, and `error_nilify` (replacing a returned error with
`nil`) survived 44 of 48 completed cases. Branch and conditional coverage is
otherwise respectable. So when the worklist is long, start with the error paths.
