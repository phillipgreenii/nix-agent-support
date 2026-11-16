# pb unit-test cost measurement

Follow-up to the pb unit-test performance audit. Measured with the load-independent method in
`home/programs/agent-rules/code-file-standards.md` (Speed And Scope, "Measuring Test Cost On A Loaded Host"),
not on a quiet host: `go test -c -race` binaries, run from the package directory under
`nice -n 10 /usr/bin/time -l`, `-test.count=1`. Host: Apple M3 Pro (5 performance, 6 efficiency
cores, 11 logical), load average 16 to 72 during the runs (recorded per run, information only).
Thresholds: about 1 s per test and about 10 s per package, CPU (user+sys, includes git children)
plus deliberate waits. The raw per-run files were kept in scratch space and are summarised here.

## Per-package cost before the change (single run, net of a `-test.run '^$'` baseline)

The baseline run costs 0.00 user, 0.00 sys and about 55 to 68 M instructions for every package, so
the full-run figures below are already net of it.

| package          | user s | sys s | CPU s | instructions retired | tests passing |
| ---------------- | -----: | ----: | ----: | -------------------: | ------------: |
| internal/drain   |   8.39 | 16.08 | 24.47 |               8.56 G |            35 |
| internal/gate    |   4.88 | 10.20 | 15.08 |               4.99 G |            79 |
| internal/patchid |   0.85 |  1.65 |  2.50 |               1.00 G |            19 |
| internal/unstick |   1.09 |  0.16 |  1.25 |               9.95 G |           105 |
| cmd/pb           |   0.21 |  0.21 |  0.42 |               2.39 G |            57 |
| internal/run     |   0.04 |  0.07 |  0.11 |               0.14 G |            12 |
| all others       |  <0.02 | <0.02 | <0.04 |              <0.12 G |         small |

GOMAXPROCS was the host default (11). Instructions retired exclude git children, so the git-heavy
packages (drain, gate, patchid) are judged on CPU. Over the 10 s package threshold: drain and gate.

## Slow tests (3 single-test runs each; minimum wall and minimum CPU)

Wall time at load 20 to 70 is dominated by process start (a no-op run took 1.4 to 3.9 s wall), so
it cannot pass or fail a test; CPU plus deliberate waits decides. CPU in s (user+sys).

| test                                                          | min CPU s | deliberate wait s | cost s | over 1 s |
| ------------------------------------------------------------- | --------: | ----------------: | -----: | :------: |
| drain TestIsolate_concurrentIsolateCallsStayAnchoredToRepo    |      3.43 |                 0 |   3.43 |   yes    |
| drain TestIsolate_bundleStatesWriteNoFileAndReportState       |      2.75 |                 0 |   2.75 |   yes    |
| drain TestIsolate_hungWorktreeAddIsBoundedAndCleansOnlyItsOwn |      0.73 |                 2 |   2.73 |   yes    |
| drain TestIsolate_hungAddOfParkedBranchKeepsTheBranch         |      0.46 |                 2 |   2.46 |   yes    |
| drain TestIsolate_hooksKeyFollowingState                      |      1.92 |                 0 |   1.92 |   yes    |
| drain TestReclaim_primaryMovedOnStillZeroCommits              |      1.36 |                 0 |   1.36 |   yes    |
| drain TestReclaim_wtdoneRefusalLeavesWorktreeAndWritesNoNote  |      1.35 |                 0 |   1.35 |   yes    |
| drain TestReclaim_noteFailureAfterRemovalIsTyped              |      1.35 |                 0 |   1.35 |   yes    |
| drain TestReclaim_cleanZeroCommitWorktreeIsRemovedAndNoted    |      1.31 |                 0 |   1.31 |   yes    |
| drain TestReclaim_noWorktreeIsANoOpAndIdempotent              |      1.28 |                 0 |   1.28 |   yes    |
| gate (every Check/RebaseCtx git test, 24 tests)               | 0.37-0.96 |                 0 |   <1.0 |    no    |
| patchid (6 listed slow tests)                                 | 0.37-0.44 |                 0 |   <0.5 |    no    |

Deliberate waits (source): the two hung-git tests pass `GitTimeout: 2 * time.Second` and the fake
git hangs until the process group is killed
(`internal/drain/gitbound_test.go`, `TestIsolate_hungWorktreeAddIsBoundedAndCleansOnlyItsOwn` and
`TestIsolate_hungAddOfParkedBranchKeepsTheBranch`), so each sits out one 2 s timeout. The run
package has one deliberate 1 s wall-clock timeout test (CPU 0.11 s for the whole package; not
changed). No other test in pb sleeps.

Git spawns explaining the cost: a fixture repository was `init` + 3 `config` + (`add`, `commit`,
`rev-parse`) per seeded commit. The gate no-baseline fixture seeded 7 commits (4 + 21 = 25 spawns
plus patch-id, 2 `rev-parse`), the pre-baseline fixture 4 commits (16 plus patch-id), the rebase
fixture 1 commit plus 12 scenario calls; a drain fixture 4 + 3 = 7 spawns. The fixture cost was paid
by every test. (Counts are from the fixture code, not from a counting shim.)

## The change

`internal/gittemplate` (test-only package) seeds an `x/gitfixture` repository once per test process
and hands each test a directory copy wrapped in a `gitfixture.Repo` whose client carries the
template's hermetic options. gate uses three templates (no-baseline, pre-baseline, and the fully
built rebase scenario, whose dates are explicit); drain uses one (`newRepo`). Git stays real and the
hung-git tests keep their semantics and their 2 s timeout (shortening it would risk a healthy
cleanup git call timing out on a starved host, and was not done).

## Interleaved before/after (6 pairs, whole-package, user+sys CPU s, race build)

Pairs ran before, after, before, after at whatever the load was (16 to 72).

| package | side   | median CPU s | min-max CPU s | median wall s | min-max wall s | median instr |
| ------- | ------ | -----------: | ------------: | ------------: | -------------: | -----------: |
| gate    | before |        12.03 |    9.29-14.26 |         19.55 |    13.48-23.49 |       5.18 G |
| gate    | after  |         4.26 |     3.87-5.50 |          7.39 |     6.78-15.19 |       4.74 G |
| drain   | before |        19.02 |   18.58-24.71 |         34.50 |    32.33-56.29 |       8.77 G |
| drain   | after  |        14.52 |   13.76-18.78 |         27.25 |    24.39-34.84 |       9.61 G |

Per pair, user+sys CPU (before to after): gate 11.73 to 4.08, 12.32 to 4.43, 13.21 to 4.78, 14.26 to
5.50, 9.29 to 3.87, 9.75 to 3.95; drain 19.14 to 14.44, 18.90 to 14.60, 24.71 to 18.78, 22.67 to
16.18, 18.67 to 13.76, 18.58 to 13.88. Every pair improved. gate went from over the 10 s package
threshold to under it (median 4.26 s, a 65 percent cut). drain improved by 24 percent but is still
over (median 14.52 s). Instructions retired in the Go process rose in drain (8.77 G to 9.61 G)
because copying the template runs in-process, while the git children it replaced are not counted
there; CPU, which includes them, fell.

## Left over (not fixed here)

- drain is still about 14.5 s CPU per package. The remaining cost is the code under test and the
  test's own `git worktree add`, `status` and `worktree list` calls (each Isolate call spawns
  several git processes), not fixture setup. Tests still over 1 s by the table above need a further
  pass: the five `TestReclaim_*` tests that run a real Isolate then Reclaim through `wtdone`,
  `TestIsolate_concurrentIsolateCallsStayAnchoredToRepo`, `TestIsolate_bundleStatesWriteNoFileAndReportState`
  and `TestIsolate_hooksKeyFollowingState` (subtests each running Isolate).
- The two hung-git tests sit out 2 s each by design; they stay over 1 s.
- Per-test figures for the changed tests were not re-measured individually after the change; only
  the whole-package pairs above were.
