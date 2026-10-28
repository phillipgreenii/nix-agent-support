# Real-process test suites: pg-rescue runner splits by cost driver; claude-hook-router and scriptout stay unit tests

**Status**: Proposed (spike `pg2-8h14y2`; follow-up of spike `pg2-8q541l`, Appendix A bead 10 of the
shared command runner ADR in repo `x`. The implementation is NOT part of this change: see
"Follow-up work")
**Date**: 2026-10-28
**Deciders**: the operator (Phillip) to accept; measured and drafted by the spike

## Context

Rule V-4 says a unit test needs minimal setup, runs in any order and takes milliseconds; a package
takes seconds; a whole app takes a minute or two with no hard limit. A test that runs several
processes or a whole daemon is not a unit test: it MUST sit behind a build tag or opt-in switch with
its own check (`phillipg-nix-repo-base` ADR 0024), MUST NOT run in the unit gate (`go test ./...`,
a `*-go-tests` check, the `run-unit-tests` hook), and a package timeout MUST NOT be raised to fit a
slow test. A fast single-process run of the real tool against a hermetic fixture stays a unit test
(V-3; the Unit Tests section of `home/programs/agent-rules/code-file-standards.md`).

The spike on a shared command runner in repo `x` measured `pg-rescue/internal/runner` at 24.0 s
even without `-race` and named three "class B" suites (real processes are the subject, or the
re-exec double is kept) that it left undecided: `pg-rescue/internal/runner`, `claude-hook-router`
and `pg-connector/pkg/scriptout`. This ADR decides each one from fresh measurements.

## Method

Each command ran from the module root as `env -C <module> nice -n 10 go test -p 4 -count=1 -v
[-race] <pkg>` on the worktree of this change (HEAD `bb275fbbe`), two runs each, one after the
other. The figure is the `ok <pkg> N.NNNs` line, which is test run time and excludes compilation.
Per-test figures are the `--- PASS: Test... (N.NNs)` lines of the top-level tests. Run 1 is the
first run in each pair; run 2 is the second.

The host was busy: the one-minute load average printed before each run was 25 to 44 for the first
runs (and 14 to 16 for the last experiment). That is the noise in the pairs below, and it is why
every figure is shown as both runs rather than one.

## Measurements

### pg-rescue `internal/runner` (16 `*_test.go` files, 102 top-level tests, 10 files use the re-exec helper)

| Variant                             | Run 1    | Run 2        | Mean                             |
| ----------------------------------- | -------- | ------------ | -------------------------------- |
| no `-race`                          | 31.683 s | 26.521 s     | (31.683 + 26.521) / 2 = 29.102 s |
| `-race`                             | 92.730 s | 98.721 s     | (92.730 + 98.721) / 2 = 95.725 s |
| `-race`, `GORACE=atexit_sleep_ms=0` | 29.154 s | not repeated | n/a                              |

- Spread between the two no-`-race` runs: 31.683 - 26.521 = 5.162 s. Between the two `-race` runs:
  98.721 - 92.730 = 5.991 s.
- `-race` costs 95.725 - 29.102 = 66.623 s more, a factor of 95.725 / 29.102 = 3.289.
- The tests run one after another (the sum of the top-level times, 31.41 s, is within 0.27 s of the
  31.683 s wall time of run 1).

Cost driver, no `-race`: five tests that wait on real time, with sleeps and timeouts in the fake
commands, account for most of the package.

| Test                                                                                                                                                                                                  | Run 1                                       | Run 2                                       |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- | ------------------------------------------- |
| `TestDisplayGolden` (20 subtests: 5 scenarios x 4 verbosity levels, each a real wrapper run against shell-script fakes; `deferred` alone is 7.18 s in run 1, four real runs of a 1 s handler timeout) | 16.51 s                                     | 14.76 s                                     |
| `TestVerifyTimeoutFailsTheAttempt`                                                                                                                                                                    | 3.04 s                                      | 3.03 s                                      |
| `TestTheFirstSignalWins`                                                                                                                                                                              | 2.02 s                                      | 2.01 s                                      |
| `TestCommandOutputThatArrivesAfterItExitsIsStillCaptured`                                                                                                                                             | 0.94 s                                      | 0.92 s                                      |
| `TestHandlerTimeoutIsTermThenKill`                                                                                                                                                                    | 0.77 s                                      | 0.72 s                                      |
| sum of the five                                                                                                                                                                                       | 16.51 + 3.04 + 2.02 + 0.94 + 0.77 = 23.28 s | 14.76 + 3.03 + 2.01 + 0.92 + 0.72 = 21.44 s |
| share of the top-level sum (31.41 s, 26.32 s)                                                                                                                                                         | 23.28 / 31.41 = 74.1 %                      | 21.44 / 26.32 = 81.4 %                      |
| everything else                                                                                                                                                                                       | 31.41 - 23.28 = 8.13 s                      | 26.32 - 21.44 = 4.88 s                      |

`TestDisplayGolden` alone is 16.51 / 31.41 = 52.5 % (run 1) and 14.76 / 26.32 = 56.1 % (run 2).
The other 97 tests: 28 (run 1) and 17 (run 2) take 0.1 s or more; only one more, `TestExitCodeTable`
(1.05 s run 1, 0.26 s run 2), moves with load.

Cost driver, `-race`: every re-executed test binary is race-built and the race runtime sleeps 1 s
at exit (`atexit_sleep_ms`, default 1000), so each helper spawn costs about 1 s. Evidence: with the
same run and `GORACE=atexit_sleep_ms=0` the package took 29.154 s, against 92.730 s and 98.721 s
without it, a drop of 95.725 - 29.154 = 66.571 s (one run, taken at a lower host load of 16 against
36, so treat the size as indicative; the direction is not in doubt, and the scriptout conformance
package below shows the same effect). `exec_test.go` in `scriptout` already records the same
finding in its header.

### claude-hook-router (module; 2 test packages, 48 top-level tests)

| Package                  | no `-race` run 1 / run 2                          | `-race` run 1 / run 2                             |
| ------------------------ | ------------------------------------------------- | ------------------------------------------------- |
| `cmd/claude-hook-router` | 1.143 s / 1.271 s                                 | 2.786 s / 2.485 s                                 |
| `internal/router`        | 3.534 s / 3.952 s                                 | 5.338 s / 4.600 s                                 |
| module sum               | 1.143 + 3.534 = 4.677 s / 1.271 + 3.952 = 5.223 s | 2.786 + 5.338 = 8.124 s / 2.485 + 4.600 = 7.085 s |

Cost driver: `TestDispatchHangingDelegateBoundedByWaitDelayMechanism` takes 2.02 s in all four runs,
which is 2.02 / 4.677 = 43.2 % and 2.02 / 5.223 = 38.7 % of the module sum without `-race`. It
waits out the production 2 s `delegateWaitDelay` (a constant in `dispatch.go`) on purpose.
`TestCrossProcessDataIsolation` (0.88 s to 1.40 s) is the other one: it runs `go build` once, then
two real router processes, and is 0.88 / 1.143 = 77.0 % and 1.03 / 1.271 = 81.0 % of the `cmd`
package without `-race`.

Finding that changes the classification: there is NO re-exec of the test binary in this module (no
`GO_WANT_*` and no `TestHelperProcess`). The delegates are `sh -c` one-liners (`printf`, `sleep`),
so every `-race` run pays no atexit sleep: the `-race` increase is 7.605 - 4.950 = 2.655 s
(means: (8.124 + 7.085) / 2 = 7.605 s and (4.677 + 5.223) / 2 = 4.950 s), which is the race-built
test binary and the router build, not a spawn cost.

### pg-connector `pkg/scriptout` (3 test packages, 151 top-level tests in the main one)

| Package                     | no `-race` run 1 / run 2                                          | `-race` run 1 / run 2                                               | `-race`, atexit sleep off       |
| --------------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------- | ------------------------------- |
| `pkg/scriptout`             | 6.148 s / 6.117 s                                                 | 7.387 s / 7.432 s                                                   | 6.046 s                         |
| `pkg/scriptout/conformance` | 0.221 s / 0.466 s                                                 | 3.864 s / 3.355 s                                                   | 0.249 s                         |
| `pkg/scriptout/schemas`     | 0.503 s / 0.284 s                                                 | 1.925 s / 1.443 s                                                   | 0.338 s                         |
| subtree sum                 | 6.148 + 0.221 + 0.503 = 6.872 s / 6.117 + 0.466 + 0.284 = 6.867 s | 7.387 + 3.864 + 1.925 = 13.176 s / 7.432 + 3.355 + 1.443 = 12.230 s | 6.046 + 0.249 + 0.338 = 6.633 s |

Cost driver (main package, no `-race`): deliberate real deadlines in in-process tests, not
processes. `TestServeLoop_ConfiguredListDeadlineIsEnforced` (2.50 s in all four runs: a 1.5 s slow
handler against a configured 1 s deadline, whose minimum is 1 s), then the `Invoke` and `ServeLoop`
budget tests at 0.80, 0.70, 0.40, 0.35 and 0.30 s. These six sum to 2.50 + 0.80 + 0.70 + 0.40 +
0.35 + 0.30 = 5.05 s, which is 5.05 / 6.148 = 82.1 % of the package.

Finding that changes the classification: `exec_test.go` no longer re-executes anything; its header
says it "used to" and that the real exec path is covered by `runner_test.go` with `sh -c`. The only
re-exec left is in the `conformance` sub-package
(`TestExecBackend_RealSubprocess_Success`, `TestRun_ExecBackend_RealSubprocess`, env
`GO_WANT_CONFORMANCE_HELPER_PROCESS`, helper `TestHelperProcess`); those two tests are the reason
`conformance` goes from 0.221 s and 0.466 s to 3.864 s and 3.355 s under `-race` (1.02 s and 1.06 s
each in the `-v` log), and back to 0.249 s with the atexit sleep off. The literal
`GO_WANT_HELPER_PROCESS` therefore appears in `scriptout` only inside a comment; the guard bead in
Appendix A of the `x` ADR (flag `GO_WANT_HELPER_PROCESS` in untagged test files) would not see the
`conformance` helper under its different name.

## Decision

### 1. `pg-rescue/internal/runner`: move the wall-clock-bound end-to-end tests behind `integration`; keep the rest unit

The package is not a unit test package under V-4: 26.5 s to 31.7 s without `-race` and 92.7 s to
98.7 s with it (the second figure alone is 95.725 / 60 = 1.60 minutes for one package, against a
whole-app allowance of "a minute or two"). A package timeout MUST NOT be raised to fit it.

- The five tests in the first table (`TestDisplayGolden`, `TestVerifyTimeoutFailsTheAttempt`,
  `TestTheFirstSignalWins`, `TestCommandOutputThatArrivesAfterItExitsIsStillCaptured`,
  `TestHandlerTimeoutIsTermThenKill`) MUST move behind `//go:build integration`. They span the
  wrapper and a chain of real child processes and wait on real timeouts and signals, which is the
  V-4 definition of an end-to-end test, and they are 74.1 % and 81.4 % of the package.
- The tag and the check already exist: `internal/integration` is tagged `integration` and
  `checks.<system>.pg-rescue-integration-tests` (`flake.nix`) runs `-tags integration` with
  `bash`, `git`, `jq` and the handler package on `PATH`, which is what these tests need. ADR 0024's
  requirement (a tag with a check that sets it) is therefore met without a new attribute. That check
  has no guard that the tagged tests really ran; it SHOULD gain one of the shape of
  `pg-connector-integration-tests-gating` before the move, or the tagged tests become the "suite
  nothing runs" ADR 0024 forbids.
- The rest (the two no-helper files, `report_test.go` and `gaps_internal_test.go`, and the helper
  based tests that do not wait on real time, for example `TestExitCodeTable`,
  `TestHandlerResultMapping`, `TestSignalMatrix`) STAYS in the unit tier as the "few real-process
  cases". Its measured top-level sum is 8.13 s (run 1) and 4.88 s (run 2) without `-race`.
- Under `-race` the remaining helper spawns would still cost about 1 s each, so the move alone does
  not make the untagged package fast under `-race`. The follow-up MUST set
  `GORACE=atexit_sleep_ms=0` for the children (for example `os.Setenv` in `TestMain` before the
  suite runs, which affects only processes the tests start and does not change how the gate runs
  `-race`) and MUST check that the production `ChainExecutor` hands the variable to the command.
  If it scrubs the environment, more of the helper-based tests MUST move behind the tag instead.
  The operator ruled on 2026-10-26 that the `-race` default is not changed; this does not change it.
- The follow-up's acceptance MUST report the package time before and after, with and without
  `-race`. Proposed target: at most 10 s in each of the four cases (a proposal for the operator to
  confirm; the measured residue of 4.88 s to 8.13 s without `-race` fits it).

An alternative that keeps `TestDisplayGolden` untagged is to run each of its 5 scenarios once and
render the four verbosity levels from that one run (4 real runs become 1), if the display can be
rebuilt from the run log. That is unverified here; the follow-up MAY try it before tagging, and the
tag stands if it is not possible.

### 2. `claude-hook-router`: stays a unit test; trim two tests

- 4.677 s to 5.223 s without `-race` and 7.085 s to 8.124 s with it for the whole module is
  seconds-level; there is no re-exec; the delegates are single `sh -c` processes, which is the V-3
  case ("fast single-process real tool"). No tag is needed for the package.
- `TestDispatchHangingDelegateBoundedByWaitDelayMechanism` (2.02 s) SHOULD be trimmed by making
  `delegateWaitDelay` a parameter of the internal `dispatch` the way the total budget already is
  (the tests that need the production value keep it), so the test waits tens of milliseconds, not 2 s.
  That is 2.02 s of 4.677 s to 5.223 s (38.7 % to 43.2 %).
- `TestCrossProcessDataIsolation` (`go build` plus two concurrent router processes, 0.88 s to
  1.40 s) is a multi-process end-to-end test, so under V-4 it SHOULD leave the unit tier. The own
  check already exists: `checks.<system>.claude-hook-router-e2e` runs the built binary through bats
  against stub delegates. The follow-up SHOULD port this case to that bats file (no Go build tag
  and no new check) and delete the Go test.

### 3. `pg-connector/pkg/scriptout`: stays a unit test; trim the deadline waits and the two re-exec tests

- 6.117 s to 6.148 s (main) and 6.867 s to 6.872 s (subtree) without `-race`, 12.230 s to 13.176 s
  with it, is seconds-level for a package and is made of in-process tests with deliberate
  deadlines. No tag is needed.
- The 2.50 s `TestServeLoop_ConfiguredListDeadlineIsEnforced` and the five `Invoke`/`ServeLoop`
  budget tests (together 5.05 s, 82.1 % of the main package) SHOULD be trimmed by shortening their
  delays where the code allows it (the configured minimum is 1 s, so that one needs the minimum
  behind a test hook or a faked clock); they are not blocking.
- `conformance`'s two re-exec tests are the only race-cost in the subtree
  (3.864 s and 3.355 s against 0.221 s and 0.466 s). They SHOULD be replaced by an `sh`-based
  `ExecBackend` (the way `runner_test.go` already covers the real exec path with `sh -c`), or the
  `GORACE` setting of decision 1 SHOULD be applied to them. Either keeps one real-process case in
  the unit tier, as V-3 allows. The follow-up that adds the guard from Appendix A of the `x` ADR
  MUST cover the `GO_WANT_CONFORMANCE_HELPER_PROCESS` name too, and MUST flip the allowance for
  `os.Args[0]` in `unit_tier_guard_test.go` consistently.

## Consequences

- `pg-rescue` unit runs lose about 21 s to 23 s without `-race`, and with the `GORACE` setting
  applied about 60 s to 70 s with it (the race penalty measured above is 66.6 s). The five moved
  tests still run under `pg-rescue-integration-tests`, on demand, which reruns the whole untagged
  suite as well (the check is a superset by construction).
- The corrected classification matters for the guard in Appendix A of the `x` ADR: only
  `pg-rescue/internal/runner` (and the `scriptout/conformance` sub-package, under another env name)
  re-execute the test binary; `claude-hook-router` and `scriptout/exec_test.go` do not.

## Not verified

- The remaining `pg-rescue` packages and the whole-module time against the "minute or two" allowance
  for an app; only `internal/runner` was timed.
- The `GORACE` effect on the production `ChainExecutor` environment, and the `atexit` experiment is
  one run per package.
- That the display can be rendered from one run per scenario.
- Whether `pg-rescue-integration-tests` is excluded from every hook (`run-unit-tests`,
  `pre-land`) by construction; the follow-up MUST confirm it before the move.

## Follow-up work

Recorded here for the orchestrator to file; this spike files nothing and changes no test.

1. **pg-rescue runner: move the five wall-clock-bound tests behind `integration` and add a gating
   guard to `pg-rescue-integration-tests`** (repo `nix-agent-support`, label `agent-support`).
2. **pg-rescue runner: neutralise the race atexit sleep for re-exec children** (`GORACE` in
   `TestMain`, verify `ChainExecutor` passes it; report times before and after with and without
   `-race`).
3. **claude-hook-router: make the wait delay injectable and port `TestCrossProcessDataIsolation` to
   the bats e2e check.**
4. **scriptout: trim the deadline-bound tests and replace or de-race the two `conformance` re-exec
   tests; make the Appendix A guard cover `GO_WANT_CONFORMANCE_HELPER_PROCESS`.**
5. **Correct the class B list in the `x` ADR, Appendix A bead 9 and bead 10** (repo `x`): only
   `pg-rescue/internal/runner` and `scriptout/conformance` re-exec; `claude-hook-router` does not.
