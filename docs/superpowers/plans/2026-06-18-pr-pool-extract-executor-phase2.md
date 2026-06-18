# pr-pool spec C Phase 2 — Extract `Executor` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract pr-pool's per-dispatch execution out of the orchestrator into a new `internal/executor` package (`Executor` interface + `Deps` bag + `ccpoolExecutor`/`commandExecutor`), moving the `pg2-c1vp` watchdog/single-terminal race code verbatim, and fold in the `pg2-kj7j` report failure-verb fidelity fix.

**Architecture:** New `internal/executor` package declares `Executor.Dispatch(ctx, discover.DispatchContext, Deps) (report.Result, error)`. Concrete `ccpoolExecutor`/`commandExecutor` are unexported, selected by an exported `executor.For(roleType)`. The race-critical methods move with **identical signatures** (only receiver + field-refs rebind: `o.X` → `r.deps.X`). The orchestrator keeps its drive loop (`DrainOnce`/`drain`/`RunOne`) and pre/post snapshot diff; `Dispatch` returns only the **failure action it actually performed**, which `buildResult` merges with the snapshot-derived `Created`/`Indeterminate`.

**Tech Stack:** Go 1.25, `go test -race`, gomod2nix (`nix build .#pr-pool`), `nix flake check`, prek pre-commit.

**Spec:** `docs/superpowers/specs/2026-06-18-pr-pool-extract-executor-phase2-design.md`

**Working dir for all commands:** `packages/pr-pool/` (relative paths below are under it).

> **Note on the working tree:** another session may be editing `flake.nix` / `home/programs/tuicr/*` concurrently. Phase 2 touches only `packages/pr-pool/**`. **Always `git add` explicit paths** (never `git add -A`/`.`) and verify each commit's file list, OR run this plan in an isolated `git worktree`.

---

## File Structure

**New files:**

- `internal/dtest/dtest.go` — shared test fakes (exported `FakeCC`, `ScriptBD`, `ManualClock`, `RampReader` + helpers), moved verbatim from `orchestrator_test.go`.
- `internal/executor/executor.go` — `Executor` interface, `Deps` struct + nil-default accessors, `For(roleType)` selector, `failureAction` helper.
- `internal/executor/ccpool.go` — unexported `ccpoolExecutor` + `ccpoolRun` (holds `Deps`); the moved ccpool dispatch + race code.
- `internal/executor/command.go` — unexported `commandExecutor`; moved `runCommand`/`renderArgv`.
- `internal/executor/ccpool_test.go` — the moved `TestWaitDone_*` + `TestActive_stateMapping` + the new `pg2-kj7j` `TestDispatch_*` failure-verb tests, plus a `newExec` helper.

**Modified files:**

- `internal/orchestrator/orchestrator.go` — delete the moved methods; `workOne`/`workOneWithID` become the `Deps`-building seam returning `(report.Result, error)`; `buildResult` takes `execRes` and uses it for the failure verb.
- `internal/orchestrator/orchestrator_test.go` — switch fakes to `dtest.*`; remove the moved tests.

**Unchanged (guards):** `internal/orchestrator/golden_test.go`, `internal/report/*`, `internal/watchdog/*`, `internal/roles/*`, `internal/discover/*`, `cmd/pr-pool/*`.

---

## Task 1: Extract shared test fakes into `internal/dtest`

The moved race tests (Task 3) need the same fakes the orchestrator tests use. Extract them first so both test packages import one copy.

**Files:**

- Create: `internal/dtest/dtest.go`
- Modify: `internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Create `internal/dtest/dtest.go`** by moving these declarations **verbatim** from `internal/orchestrator/orchestrator_test.go`, renaming each to its **exported** name and putting them in `package dtest` (a normal `.go` file, not `_test.go`, so other packages' tests can import it):

  | from `orchestrator_test.go`                                                                                                                                        | → exported in `dtest`                                     |
  | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------- |
  | `rampReader` (struct + `Read`)                                                                                                                                     | `RampReader`                                              |
  | `fakeCC` (struct + all methods; export fields used cross-package: `Ensured`,`EnsureNames`,`Sent`,`Closed`,`ClosedPurge`,`SendErr`,`EnsureErr`,`ListSeq`,`ListIdx`) | `FakeCC`                                                  |
  | `scriptBD` (struct + `Run`; export fields: `StatusSeq`,`Idx`,`Updates`,`Ready`,`ReadyErr`,`Show`,`ShowErrOnce`)                                                    | `ScriptBD`                                                |
  | `manualClock` (struct + `now`→`Now`, `tickAdvancing`→`TickAdvancing`)                                                                                              | `ManualClock` (export `T`)                                |
  | `contains`                                                                                                                                                         | `Contains`                                                |
  | `join`                                                                                                                                                             | `join` (keep unexported; used only inside `ScriptBD.Run`) |
  | `hasUpdate(bd *scriptBD, sub)`                                                                                                                                     | `HasUpdate(bd *ScriptBD, sub string)`                     |
  | `const testStamp`                                                                                                                                                  | `const TestStamp`                                         |
  | `var errSend`                                                                                                                                                      | `ErrSend`                                                 |

  Keep the `sync.Mutex` guards exactly as-is (they make the fakes race-safe under the watchdog goroutine). The fakes must keep satisfying `ccpool.Runner` (`FakeCC`) and `beads.Runner` (`ScriptBD`) — verify method sets against `internal/ccpool` and `internal/beads`.

- [ ] **Step 2: Update `orchestrator_test.go` to use `dtest`.** Delete the moved declarations from it; add `import "github.com/phillipgreenii/pr-pool/internal/dtest"`; replace every `fakeCC`→`dtest.FakeCC`, `scriptBD`→`dtest.ScriptBD`, `manualClock`→`dtest.ManualClock`, `rampReader`→`dtest.RampReader`, `contains`→`dtest.Contains`, `hasUpdate`→`dtest.HasUpdate`, `testStamp`→`dtest.TestStamp`, `errSend`→`dtest.ErrSend`, and field accesses to their exported names (`.ensured`→`.Ensured`, `.statusSeq`→`.StatusSeq`, `.updates`→`.Updates`, `.listSeq`→`.ListSeq`, `.listIdx`→`.ListIdx`, `.ready`→`.Ready`, `.show`→`.Show`, `.sendErr`→`.SendErr`, `.ensureErr`→`.EnsureErr`, `.showErrOnce`→`.ShowErrOnce`, `.readyErr`→`.ReadyErr`). Keep `newOrch`, `testRoleSet`, `roleByName`, `feedbackRole`, `workerRole`, `fastCfg`, `writeTemp` in `orchestrator_test.go` (orchestrator-only).

- [ ] **Step 3: Run the orchestrator suite to verify green (no behavior change)**

  Run: `go test ./internal/orchestrator/... -count=1`
  Expected: PASS (all existing tests still pass through the renamed fakes).

- [ ] **Step 4: Run the whole module under -race**

  Run: `go test -race ./... -count=1`
  Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dtest/dtest.go internal/orchestrator/orchestrator_test.go
git commit -m "refactor(pr-pool): extract shared test fakes to internal/dtest (pg2-hnz7)"
```

---

## Task 2: Create the `internal/executor` package skeleton

Just the types — no moved logic yet — so it compiles and the next task only moves code.

**Files:**

- Create: `internal/executor/executor.go`

- [ ] **Step 1: Write `internal/executor/executor.go`**

```go
// Package executor runs one (role, item) dispatch. It owns the ccpool
// ensure→send→wait path (including the pg2-c1vp watchdog/single-terminal race)
// and the command path. The orchestrator selects an Executor by role.Type via
// For and hands it a Deps seam bag; Dispatch returns the failure action the
// executor itself performed on the bead (empty on success) — the orchestrator
// merges the observed created/closed/handed-back actions around it.
package executor

import (
	"context"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// Executor dispatches one item for a role and reports the failure action it took.
type Executor interface {
	Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error)
}

// Deps is the explicit seam bag the executor needs from the orchestrator. Fields
// are exported because the orchestrator builds Deps from a different package. The
// per-attempt ExternalID is resolved ONCE by the orchestrator (so the same id is
// reused for the deferred teardown Close); the executor never re-stamps.
type Deps struct {
	CC          ccpool.Runner
	BD          beads.Runner
	Cmd         query.Commander
	Log         *eventlog.Writer // may be nil (no-op)
	Cfg         config.Config
	Now         func() time.Time                           // clock seam; nil ⇒ time.Now
	Tick        func(context.Context, time.Duration) error // cancellable wait; nil ⇒ select poll
	UsageReader usage.Reader                               // nil ⇒ usage.NewTranscriptReader()
	ExternalID  string                                     // resolved once by the orchestrator
}

func (d Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) reader() usage.Reader {
	if d.UsageReader != nil {
		return d.UsageReader
	}
	return usage.NewTranscriptReader()
}

func (d Deps) commander() query.Commander {
	if d.Cmd != nil {
		return d.Cmd
	}
	return query.OSCommander{}
}

func (d Deps) waitPoll(ctx context.Context, dur time.Duration) error {
	if d.Tick != nil {
		return d.Tick(ctx, dur)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(dur):
		return nil
	}
}

// For returns the concrete executor for a role type. Any non-"command" type
// takes the ccpool path (matches today's orchestrator.workOneWithID switch).
func For(roleType string) Executor {
	if roleType == "command" {
		return commandExecutor{}
	}
	return ccpoolExecutor{}
}

// failureAction builds a single-bead Result for one failure verb.
func failureAction(verb report.Verb, beadID string) report.Result {
	return report.Result{Actions: []report.Action{{Verb: verb, Refs: []report.Ref{{Type: "bead", ID: beadID}}}}}
}
```

- [ ] **Step 2: Add temporary stubs so the package compiles** (deleted in Task 3 when the real types arrive). Append to `executor.go`:

```go
type ccpoolExecutor struct{}

func (ccpoolExecutor) Dispatch(_ context.Context, _ discover.DispatchContext, _ Deps) (report.Result, error) {
	panic("not yet implemented")
}

type commandExecutor struct{}

func (commandExecutor) Dispatch(_ context.Context, _ discover.DispatchContext, _ Deps) (report.Result, error) {
	panic("not yet implemented")
}
```

- [ ] **Step 3: Verify it builds**

  Run: `go build ./internal/executor/...`
  Expected: success (no output).

- [ ] **Step 4: Verify no import cycle / vet clean**

  Run: `go vet ./internal/executor/...`
  Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/executor/executor.go
git commit -m "feat(pr-pool): internal/executor skeleton — Executor iface + Deps + For (pg2-hnz7)"
```

---

## Task 3: Move the ccpool + command dispatch (pure structural)

Move the dispatch code verbatim. **No behavior change in this task**: `Dispatch` returns an **empty** `report.Result{}`; the failure actions (escalate/unclaim/fail) still fire exactly as today, and `buildResult` still derives the failure verb from `OnFailure` (untouched). The `pg2-kj7j` fix is Task 4.

**Files:**

- Create: `internal/executor/ccpool.go`, `internal/executor/command.go`
- Create: `internal/executor/ccpool_test.go`
- Modify: `internal/executor/executor.go` (delete the Task 2 stubs)
- Modify: `internal/orchestrator/orchestrator.go`, `internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Create `internal/executor/ccpool.go`.** Delete the two stub types from `executor.go`. Define the real `ccpoolExecutor` + an internal `ccpoolRun` that holds `Deps`:

```go
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/watchdog"
)

type ccpoolExecutor struct{}

func (ccpoolExecutor) Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error) {
	r := &ccpoolRun{deps: deps}
	err := r.run(ctx, d)
	return report.Result{}, err // Task 4 attaches the failure action here
}

// ccpoolRun carries Deps so the moved methods keep their original signatures
// (only o.X → r.deps.X). It exists per-Dispatch; no cross-dispatch state.
type ccpoolRun struct{ deps Deps }
```

Then move these methods from `orchestrator.go` onto `*ccpoolRun`, **byte-for-byte except** receiver `(o *Orchestrator)` → `(r *ccpoolRun)` and field rebinds `o.CC`→`r.deps.CC`, `o.BD`→`r.deps.BD`, `o.Log`→`r.deps.Log`, `o.Cfg`→`r.deps.Cfg`, `o.clock()`→`r.deps.clock()`, `o.waitPoll(`→`r.deps.waitPoll(`, `o.reader()`→`r.deps.reader()`, `o.now`→`r.deps.Now`, and self-calls `o.waitDone`/`o.workerWaitWithWatchdog`/`o.active`/`o.fail`/`o.escalateLaunchFailure`/`o.renderNudge`→`r.…`:

- `runCCPool` → **rename to `run`**, and change its signature from `(ctx, d, externalID string)` to `(ctx context.Context, d discover.DispatchContext) error`; inside, replace the `externalID` parameter with `r.deps.ExternalID`. Everything else identical.
- `workerWaitWithWatchdog(ctx, d, name) error` — verbatim; `wd.Now = r.deps.Now`, `Reader: r.deps.reader()`, etc.
- `waitDone(ctx, claimTerminal func() bool, d, name) error` — **verbatim, identical signature.** Every `won()`/`lose()`/`ctx.Err()` gate unchanged.
- `fail(ctx, d, reason) error` — verbatim.
- `active(ctx, externalID string) bool` — verbatim.
- `escalateLaunchFailure(ctx, beadID string)` — verbatim (still returns nothing in this task).
- `renderNudge(cc *roles.CCPoolConfig, d) string` — verbatim.
- package-level `budgetUnlimited(b budget.Budget) bool` — move into `ccpool.go` unchanged.

- [ ] **Step 2: Create `internal/executor/command.go`** with `commandExecutor` + the moved command path:

```go
package executor

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/report"
)

type commandExecutor struct{}

func (commandExecutor) Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error) {
	r := &commandRun{deps: deps}
	return report.Result{}, r.run(ctx, d)
}

type commandRun struct{ deps Deps }
```

Move `runCommand` (→ `run`, signature `(ctx, d discover.DispatchContext) error`) and `renderArgv` from `orchestrator.go` onto `*commandRun`, rebinding `o.commander()`→`r.deps.commander()`, `o.Cfg`→`r.deps.Cfg`. Verbatim otherwise.

- [ ] **Step 3: Rewire the orchestrator.** In `orchestrator.go`:
  1. Delete the now-moved functions/methods (`runCCPool`, `workerWaitWithWatchdog`, `waitDone`, `fail`, `active`, `escalateLaunchFailure`, `renderNudge`, `runCommand`, `renderArgv`, `budgetUnlimited`) and the now-unused imports (`atomic`, `budget`, `prompt`, `watchdog`, `complete` — verify with goimports which remain used by `buildResult`/etc.).
  2. Add `import "github.com/phillipgreenii/pr-pool/internal/executor"`.
  3. Add a `buildDeps` helper and change `workOneWithID` to build `Deps`, select via `executor.For`, and return `(report.Result, error)`:

```go
func (o *Orchestrator) buildDeps(externalID string) executor.Deps {
	return executor.Deps{
		CC: o.CC, BD: o.BD, Cmd: o.commander(), Log: o.Log, Cfg: o.Cfg,
		Now: o.now, Tick: o.tick, UsageReader: o.usageReader, ExternalID: externalID,
	}
}

// workOneWithID dispatches one item with the per-attempt external_id pinned by the
// caller, selecting the executor by role.Type.
func (o *Orchestrator) workOneWithID(ctx context.Context, d discover.DispatchContext, externalID string) (report.Result, error) {
	return executor.For(d.Role.Type).Dispatch(ctx, d, o.buildDeps(externalID))
}

func (o *Orchestrator) workOne(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	externalID := d.Role.ExternalID(o.Cfg.SessionPrefix, d.Item.ID, o.attemptStamp())
	return o.workOneWithID(ctx, d, externalID)
}
```

4. Update the two call sites to consume the `(report.Result, error)` pair. In `drain` (the `workOne` call): `res, err := o.workOne(ctx, d)` then `o.emitResult(ctx, role, d.Item.ID, o.buildResult(ctx, role, d, pre, preOK, err))` — **keep passing `err` only to `buildResult` for now** (Task 4 threads `res`); discard `res` with `_ = res` to keep behavior identical this task. In `RunOne`: `res, err := o.workOneWithID(ctx, d, externalID)` then `o.buildResult(ctx, d.Role, d.Item.ID-args…, err)` likewise; `_ = res`. Keep `buildResult`'s current signature and body unchanged this task.

> Note: `o.commander()`, `o.attemptStamp()`, `o.clock()`, `o.waitPoll()`, `o.reader()` may now be unused in `orchestrator.go` (they moved to `Deps`). Keep `attemptStamp` (still used by `workOne`/`RunOne`). Delete `clock`/`waitPoll`/`reader`/`commander` from `orchestrator.go` only if `go vet`/compile flags them unused; `buildDeps` still reads `o.commander()`/`o.usageReader`/`o.now`/`o.tick`, so `commander` stays.

- [ ] **Step 4: Move the executor-internal tests** into `internal/executor/ccpool_test.go` (`package executor`). Move from `orchestrator_test.go` **verbatim in logic**, rewriting construction: all 14 `TestWaitDone_*` (`workerCloses`, `workerHandbackToOpen`, `workerTimeoutAddsHumanNoUnclaim`, `paneDiesAsBeadCloses_success`, `paneDiesStillInProgress_failure`, `feedbackTimeoutUnclaims`, `transientStatusErrorKeepsPolling`, `ctxCancelDoesNotFail`, `ctxCancelledBeforeDeathPathNoFail`, `lostRace_deathPathNoFail`, `lostRace_openNotReportedSuccess`, `workerDoneStopsFast_failure`, `feedbackDoneStopsFast_unclaims`, `doneStopsFast_successRace`, `needsInputWaitsUntilMaxWait`) and `TestActive_stateMapping`. Add a `newExec` helper and role helpers local to this package:

```go
package executor

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/dtest"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// newExec builds a *ccpoolRun with injected clock/tick + fakes, mirroring the
// orchestrator's newOrch but for the executor seam.
func newExec(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config) *ccpoolRun {
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	return &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg,
		Now: clk.Now, Tick: clk.TickAdvancing(),
	}}
}

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	return c
}

func testRoleSet(cfg config.Config) roles.RoleSet {
	return roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir: cfg.WorktreeDir, SkillMD: cfg.SkillMD, WorkerSkillMD: cfg.WorkerSkillMD,
		MaxFeedback: cfg.MaxFeedback, MaxWorker: cfg.MaxWorker, WorkerBudget: cfg.WorkerBudget(),
	})
}

func roleByName(cfg config.Config, name string) roles.Role {
	for _, r := range testRoleSet(cfg) {
		if r.Name == name {
			return r
		}
	}
	panic("test: role not found: " + name)
}
func feedbackRole(cfg config.Config) roles.Role { return roleByName(cfg, "feedback") }
func workerRole(cfg config.Config) roles.Role   { return roleByName(cfg, "worker") }
```

In each moved test, replace `o := newOrch(cc, bd, cfg); … o.waitDone(ctx, nil, d, name)` with `e := newExec(cc, bd, cfg); … e.waitDone(ctx, nil, d, name)`, and `o.active(…)`→`e.active(…)`. Replace `workerRole(o)`→`workerRole(cfg)` / `feedbackRole(o)`→`feedbackRole(cfg)`. Use `dtest.FakeCC`/`dtest.ScriptBD`/`dtest.HasUpdate` etc. (this file is `package executor`, importing `dtest`). **Delete these tests from `orchestrator_test.go`.** (Keep `TestWorkOne_*`, `TestStuckBead_*`, `TestRunOne_*`, `TestDrainOnce_*`, `TestDrain_*`, `TestTeardownAll_*`, `TestCreatedByActor*`, and the golden test in `orchestrator`.)

- [ ] **Step 5: Verify executor tests fail-then-pass is N/A (verbatim move) — run both suites**

  Run: `go test -race ./internal/executor/... ./internal/orchestrator/... -count=1`
  Expected: PASS. (The moved race tests now exercise `*ccpoolRun`; the orchestrator integration tests `TestWorkOne_*`/`TestStuckBead_*` still pass through `workOne → executor.Dispatch`.)

- [ ] **Step 6: Full module under -race + vet**

  Run: `go test -race ./... -count=1 && go vet ./...`
  Expected: PASS, vet clean. Confirms the `pg2-c1vp` race code did not regress.

- [ ] **Step 7: Commit**

```bash
git add internal/executor/ccpool.go internal/executor/command.go internal/executor/ccpool_test.go \
        internal/executor/executor.go internal/orchestrator/orchestrator.go internal/orchestrator/orchestrator_test.go
git commit -m "refactor(pr-pool): move ccpool/command dispatch into internal/executor (pg2-hnz7)

Pure structural: Dispatch returns empty report.Result; buildResult unchanged.
pg2-c1vp waitDone/workerWaitWithWatchdog moved byte-for-byte (signatures intact);
race + golden tests green under -race."
```

---

## Task 4: Fold in `pg2-kj7j` — failure-verb fidelity

Now `Dispatch` returns the failure action **actually taken**, and `buildResult` uses it instead of guessing from `OnFailure`.

**Files:**

- Modify: `internal/executor/ccpool.go`
- Create: tests in `internal/executor/ccpool_test.go` (append)
- Modify: `internal/orchestrator/orchestrator.go`

- [ ] **Step 1: Write the failing failure-verb tests** (append to `internal/executor/ccpool_test.go`). These assert `ccpoolExecutor{}.Dispatch(...)`'s returned `Result.Actions`:

```go
import "errors" // add to the test file's imports if absent
import "github.com/phillipgreenii/pr-pool/internal/ccpool" // add if absent
import "github.com/phillipgreenii/pr-pool/internal/discover"
import "github.com/phillipgreenii/pr-pool/internal/item"
import "github.com/phillipgreenii/pr-pool/internal/report"

func dispatchWorker(t *testing.T, cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config, ext string) (report.Result, error) {
	t.Helper()
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = ext
	return ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
}

func verbOf(res report.Result) report.Verb {
	if len(res.Actions) == 0 {
		return ""
	}
	return res.Actions[0].Verb
}

func TestDispatch_ensureFailFirst_noVerb(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":[]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	res, err := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if err == nil {
		t.Fatal("ensure failure should error")
	}
	if v := verbOf(res); v != "" {
		t.Errorf("first launch-fail (label only) must report NO verb, got %q", v)
	}
}

func TestDispatch_ensureFailRepeat_escalated(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{Show: map[string]string{"zr-w": `{"id":"zr-w","status":"open","labels":["pool-launch-fail"]}`}}
	cc := &dtest.FakeCC{EnsureErr: errors.New("ccpool new: did not reach ready")}
	res, _ := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("repeat launch-fail must report Escalated, got %q", v)
	}
}

func TestDispatch_sendFailWorkerLeave_noVerb(t *testing.T) {
	cfg := fastCfg() // worker on_dispatch_fail = leave
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	res, err := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if err == nil {
		t.Fatal("send failure should error")
	}
	if v := verbOf(res); v != "" {
		t.Errorf("worker send-fail (leave) must report NO verb, got %q", v)
	}
}

func TestDispatch_sendFailFeedbackUnclaim_unclaimed(t *testing.T) {
	cfg := fastCfg() // feedback on_dispatch_fail = unclaim
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: dtest.ErrSend}
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-feedback-zr-c"
	res, _ := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("feedback send-fail (unclaim) must report Unclaimed, got %q", v)
	}
}

func TestDispatch_waitFailWorkerTimeout_escalated(t *testing.T) {
	cfg := fastCfg() // worker on_failure = add-human
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	res, _ := dispatchWorker(t, cc, bd, cfg, "pr-pool-worker-zr-w")
	if v := verbOf(res); v != report.Escalated {
		t.Errorf("worker timeout must report Escalated, got %q", v)
	}
}

func TestDispatch_watchdogHardStop_unclaimed(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite cap so the ramp trips it
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "pr-pool-worker-zr-w"
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately >100%
	res, err := ccpoolExecutor{}.Dispatch(context.Background(), d, deps)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("budget hard-stop unclaims => must report Unclaimed (NOT Escalated), got %q", v)
	}
}
```

Add `import "github.com/phillipgreenii/pr-pool/internal/usage"` to the test file if absent.

- [ ] **Step 2: Run the new tests to verify they fail**

  Run: `go test ./internal/executor/... -run TestDispatch_ -count=1`
  Expected: FAIL (Dispatch currently returns empty `report.Result{}`, so every `verbOf` is `""`).

- [ ] **Step 3: Implement the verb derivation.** In `internal/executor/ccpool.go`:
  1. Change `escalateLaunchFailure` to **return `bool`** (did it add `human`?):

```go
// escalateLaunchFailure ... returns true iff it escalated to human (repeat
// failure); false on the first failure (label only) or on a bd read hiccup.
func (r *ccpoolRun) escalateLaunchFailure(ctx context.Context, beadID string) bool {
	already, err := beads.HasLabel(ctx, r.deps.BD, beadID, "pool-launch-fail")
	if err != nil {
		return false
	}
	if already {
		_ = beads.AddHuman(ctx, r.deps.BD, beadID)
		return true
	}
	_ = beads.AddLabel(ctx, r.deps.BD, beadID, "pool-launch-fail")
	return false
}
```

2. Change `run` to attach the failure `report.Result` at each return and return `(report.Result, error)`; update `Dispatch` to pass it through:

```go
func (ccpoolExecutor) Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error) {
	return (&ccpoolRun{deps: deps}).run(ctx, d)
}

func (r *ccpoolRun) run(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(r.deps.Cfg.SessionPrefix, d.Item.ID)
	env := map[string]string{
		"BEADS_ACTOR":    cc.Actor,
		"BEADS_DIR":      r.deps.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": r.deps.Cfg.RepoRoot,
	}
	if err := r.deps.CC.Ensure(ctx, r.deps.ExternalID, display, r.deps.Cfg.RepoRoot, env); err != nil {
		escalated := r.escalateLaunchFailure(ctx, d.Item.ID)
		var res report.Result
		if escalated {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("ensure %s: %w", r.deps.ExternalID, err)
	}
	_ = beads.RemoveLabel(ctx, r.deps.BD, d.Item.ID, "pool-launch-fail")
	nudge := r.renderNudge(cc, d)
	if err := r.deps.CC.Send(ctx, r.deps.ExternalID, nudge, ccpool.ModeNoWait); err != nil {
		var res report.Result
		if cc.OnDispatchFail == roles.DispatchUnclaim {
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			res = failureAction(report.Unclaimed, d.Item.ID)
		}
		return res, fmt.Errorf("send %s: %w", r.deps.ExternalID, err)
	}
	var werr error
	if budgetUnlimited(cc.Budget) {
		werr = r.waitDone(ctx, nil, d, r.deps.ExternalID)
	} else {
		werr = r.workerWaitWithWatchdog(ctx, d, r.deps.ExternalID)
	}
	return r.waitFailureResult(cc, d.Item.ID, werr), werr
}

// waitFailureResult maps a wait-path error to the verb actually applied to the
// bead: a budget hard-stop (watchdog won) always unclaimed; any other failure
// went through fail → complete.OnFailure(OnFailure). nil/ctx errors → no verb.
func (r *ccpoolRun) waitFailureResult(cc *roles.CCPoolConfig, beadID string, err error) report.Result {
	if err == nil {
		return report.Result{}
	}
	if errors.Is(err, watchdog.ErrBudgetExceeded) {
		return failureAction(report.Unclaimed, beadID)
	}
	switch cc.OnFailure {
	case roles.Unclaim:
		return failureAction(report.Unclaimed, beadID)
	case roles.AddHuman:
		return failureAction(report.Escalated, beadID)
	}
	return report.Result{}
}
```

Add `"errors"` to `ccpool.go` imports. Note: a ctx-cancelled loser returns `context.Canceled` (not `ErrBudgetExceeded`, not nil) → falls to the `OnFailure` switch. That is harmless because the loser path is internal to `workerWaitWithWatchdog`, which returns the **winner's** error, never the loser's. The only errors `run` sees from the wait are: `nil`, `ErrBudgetExceeded`, or a `fail()` error — all mapped correctly.

- [ ] **Step 4: Run the new tests to verify they pass**

  Run: `go test ./internal/executor/... -run TestDispatch_ -count=1`
  Expected: PASS.

- [ ] **Step 5: Thread `execRes` into `buildResult`** (`orchestrator.go`). Change the signature and replace the `OnFailure` guess:

```go
func (o *Orchestrator) buildResult(ctx context.Context, role roles.Role, d discover.DispatchContext, pre map[string]struct{}, preOK bool, execRes report.Result, dispatchErr error) report.Result {
	var actions []report.Action
	post, lerr := beads.List(ctx, o.BD, "--all")
	switch {
	case !preOK || lerr != nil:
		actions = append(actions, report.Action{Verb: report.Indeterminate, Refs: beadRefs([]string{d.Item.ID})})
	default:
		if created := createdByActor(pre, post, actorOf(role)); len(created) > 0 {
			actions = append(actions, report.Action{Verb: report.Created, Refs: beadRefs(created)})
		}
	}
	if dispatchErr != nil {
		actions = append(actions, execRes.Actions...) // verb the executor actually applied (pg2-kj7j)
	} else {
		switch status, _ := beads.Status(ctx, o.BD, d.Item.ID); status {
		case "closed":
			actions = append(actions, report.Action{Verb: report.Closed, Refs: beadRefs([]string{d.Item.ID})})
		case "open":
			actions = append(actions, report.Action{Verb: report.HandedBack, Refs: beadRefs([]string{d.Item.ID})})
		}
	}
	return report.Result{Actions: actions}
}
```

Update the two callers to pass `res`: in `drain`, `res, err := o.workOne(ctx, d)` … `o.buildResult(ctx, role, d, pre, preOK, res, err)`; in `RunOne`, `res, err := o.workOneWithID(ctx, d, externalID)` … `o.buildResult(ctx, d.Role, d, pre, preOK, res, err)`. Remove the `_ = res` discards from Task 3.

- [ ] **Step 6: Run the full module under -race + vet**

  Run: `go test -race ./... -count=1 && go vet ./...`
  Expected: PASS, vet clean. (Existing `TestStuckBead_*`/`TestWorkOne_sendFail*` still assert the same bead mutations; the new `TestDispatch_*` lock the verb fidelity.)

- [ ] **Step 7: Commit**

```bash
git add internal/executor/ccpool.go internal/executor/ccpool_test.go internal/orchestrator/orchestrator.go
git commit -m "fix(pr-pool): report the failure verb actually taken, not OnFailure guess (pg2-kj7j)

Dispatch returns the failure action it performed (launch-fail-escalate/unclaim/
add-human/budget-unclaim, or none for label-only/leave); buildResult merges it
with the snapshot-derived created/indeterminate. escalateLaunchFailure returns
whether it escalated to human."
```

---

## Task 5: Final verification gates

**Files:** none (verification only).

- [ ] **Step 1: Go suites under -race**

  Run: `go test -race ./... -count=1`
  Expected: PASS (including the moved `pg2-c1vp` race tests and golden).

- [ ] **Step 2: Import-DAG guard — confirm `report` stays a leaf and no cycle**

  Run: `go list -deps ./internal/executor/... | grep pr-pool/internal` and `go vet ./...`
  Expected: `executor` depends on `discover, roles, report, ccpool, watchdog, complete, …`; nothing imports `executor` except `orchestrator`; build succeeds (a cycle would have failed compilation).

- [ ] **Step 3: Nix build + flake check** (from repo root)

  Run: `nix build .#pr-pool && nix flake check`
  Expected: both green.

- [ ] **Step 4: Pre-commit**

  Run: `prek run --all-files` (or `pre-commit run --all-files`)
  Expected: all hooks pass.

- [ ] **Step 5: Close the beads**

```bash
bd close pg2-hnz7 --reason "Extracted internal/executor (Executor + Deps + ccpool/command executors); pg2-c1vp race code moved verbatim; race+golden green under -race; nix flake check green."
bd close pg2-kj7j --reason "Folded into Phase 2: Dispatch returns the failure verb actually taken; buildResult merges it. Covered by TestDispatch_* fidelity tests."
```

---

## Notes for the implementer

- **Verbatim means verbatim** for `waitDone`/`workerWaitWithWatchdog`: do not "improve" the `won()`/`lose()`/`ctx.Err()` gates or the buffered-channel/`cancel()` ordering. The atomic owner-claim is the `pg2-c1vp` fix; the race tests are its guard.
- **`Deps` has no `Stamp` field** by design — the orchestrator resolves `ExternalID` once (single `attemptStamp()` call) and the executor reuses it, preserving the `RunOne` single-stamp invariant.
- `commandExecutor.Dispatch` returns empty `report.Result{}` on both success and failure (command roles perform no bead mutation); `buildResult`'s failure branch appends nothing for them.
- Keep `git add` explicit (concurrent tuicr/flake.nix edits may be in the tree) and verify each commit's file list with `git show --stat HEAD`.
