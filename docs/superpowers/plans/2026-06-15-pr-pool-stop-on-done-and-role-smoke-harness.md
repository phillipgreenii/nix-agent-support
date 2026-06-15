# pr-pool: stop-on-done + role smoke harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pr-pool` stop waiting the moment a dispatched session is `done` (instead of idling to `MaxWait`), and add `run-query`/`run-role` subcommands to smoke-test a single role.

**Architecture:** A one-line semantic change to the orchestrator wait loop (`live()` → `active()`, treating ccpool `done` as terminal while keeping `needs_input` as "keep waiting"), a forward-compatible `DispatchContext` with fail-fast validation, a per-role discovery seam (`discover.ForRole`), and two new pure-routed CLI subcommands backed by a single-dispatch `Orchestrator.RunOne`.

**Tech Stack:** Go (module `github.com/phillipgreenii/pr-pool`), standard `testing`, fakes already present in each package's `*_test.go`.

**Spec:** `docs/superpowers/specs/2026-06-15-pr-pool-stop-on-done-and-role-smoke-harness-design.md`
**Branch:** `pr-pool-stop-on-done-spec` (continues from the committed spec)
**Working dir for all commands:** `packages/pr-pool/` inside the `phillipgreenii-nix-agent-support` repo.

---

## File Structure

- `internal/orchestrator/orchestrator.go` — `live()`→`active()` (add `StateDone` to the stop set), new `Orchestrator.RunOne`.
- `internal/orchestrator/orchestrator_test.go` — stop-on-done tests, `needs_input` lock test, `active()` table test, `RunOne` tests.
- `internal/discover/discover.go` — rename `Dispatch`→`DispatchContext` + `Validate()`, add `ForRole`, refactor `Discover` to compose it.
- `internal/discover/discover_test.go` — `Validate()` and `ForRole` tests.
- `cmd/pr-pool/args.go` — `routeRunRole`/`routeRunQuery`, `routeResult` fields, `route()` cases, `parseRunRoleArgs`/`parseRunQueryArgs`, help text.
- `cmd/pr-pool/args_test.go` — route/parse tests for the new subcommands.
- `cmd/pr-pool/runrole.go` (new) — `runRunRole`, `runRunQuery`, `resolveRole`.
- `cmd/pr-pool/runrole_test.go` (new) — `resolveRole` test.
- `cmd/pr-pool/main.go` — dispatch the two new routes.

---

## Task 1: Stop-on-`done` (the wait-loop fix)

**Files:**

- Modify: `internal/orchestrator/orchestrator.go` (`live()` at lines 262-277, its one call site at line 221)
- Test: `internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestWaitDone_workerDoneStopsFast_failure(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateDone}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("worker done-without-close must add human; updates=%v", bd.updates)
	}
	// active() is the ONLY List caller in waitDone, so a single List call proves
	// the loop stopped on the first check instead of polling to MaxWait.
	if cc.listIdx != 1 {
		t.Errorf("done must stop on first check (listIdx=1), got %d (looped to MaxWait?)", cc.listIdx)
	}
}

func TestWaitDone_feedbackDoneStopsFast_unclaims(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateDone}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback done-without-close must unclaim; updates=%v", bd.updates)
	}
	if cc.listIdx != 1 {
		t.Errorf("done must stop on first check (listIdx=1), got %d", cc.listIdx)
	}
}

// Regression guard (passes before AND after the fix): a session that reaches done
// in the same instant its bead closes must still be a SUCCESS via the re-check.
func TestWaitDone_doneStopsFast_successRace(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateDone}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err != nil {
		t.Fatalf("bead closed as the turn ended = success, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("success must not unclaim/flag; updates=%v", bd.updates)
	}
}

// Lock-in (passes before AND after): needs_input is NOT terminal — a human may
// attach. The loop must keep waiting to MaxWait, then time out and apply OnFailure.
func TestWaitDone_needsInputWaitsUntilMaxWait(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("needs_input that never resolves should time out (failure)")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("needs_input timeout must apply OnFailure (feedback unclaim); updates=%v", bd.updates)
	}
	if cc.listIdx < 10 {
		t.Errorf("needs_input must keep waiting to MaxWait; listIdx=%d (stopped early?)", cc.listIdx)
	}
}
```

- [ ] **Step 2: Run the tests to verify the two `done` failures are red**

Run: `go test ./internal/orchestrator/ -run 'TestWaitDone_workerDoneStopsFast_failure|TestWaitDone_feedbackDoneStopsFast_unclaims' -v`
Expected: FAIL — both fail the `cc.listIdx != 1` assertion (old code treats `done` as alive and loops to `MaxWait`, so `listIdx` is ~51). The `successRace` and `needsInput` tests pass already (regression guards).

- [ ] **Step 3: Implement the fix — rename `live()` to `active()` and treat `done` as terminal**

In `internal/orchestrator/orchestrator.go`, replace the `live()` function and its doc comment (lines 262-277) with:

```go
// active reports whether it is still worth waiting on the named session. A session
// is active while it can still make progress: starting/ready/working, and
// needs_input (paused awaiting a human who may attach and move it along — still
// bounded by MaxWait). It is NOT active once it reaches done (the agent finished its
// turn and nothing re-nudges it) or failed, or once it is absent from ccpool list.
// A list error is treated as active (can't tell ⇒ keep waiting; MaxWait bounds us).
func (o *Orchestrator) active(ctx context.Context, name string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume active; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Live && s.State != ccpool.StateFailed && s.State != ccpool.StateDone
		}
	}
	return false // absent ⇒ gone
}
```

Then update the single call site at line 221, changing `if !o.live(ctx, name) {` to:

```go
		if !o.active(ctx, name) {
```

- [ ] **Step 4: Run the tests to verify all four pass**

Run: `go test ./internal/orchestrator/ -run TestWaitDone -v`
Expected: PASS for all (including the pre-existing `TestWaitDone_*` cases — the rename does not change `failed`/absent/`working` behavior).

- [ ] **Step 5: Add the `active()` state-mapping table test**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestActive_stateMapping(t *testing.T) {
	cases := []struct {
		name string
		sess []ccpool.Session
		want bool
	}{
		{"working-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateWorking}}, true},
		{"needs_input-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateNeedsInput}}, true},
		{"done-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateDone}}, false},
		{"failed-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateFailed}}, false},
		{"working-not-live", []ccpool.Session{{Name: "s", Live: false, State: ccpool.StateWorking}}, false},
		{"absent", []ccpool.Session{{Name: "other", Live: true, State: ccpool.StateWorking}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch(&fakeCC{listSeq: [][]ccpool.Session{tc.sess}}, &scriptBD{}, fastCfg())
			if got := o.active(context.Background(), "s"); got != tc.want {
				t.Errorf("active(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 6: Run the full orchestrator package and commit**

Run: `go test ./internal/orchestrator/ -v`
Expected: PASS.

```bash
git add internal/orchestrator/orchestrator.go internal/orchestrator/orchestrator_test.go
git commit -m "fix(pr-pool): stop waiting once a session is done, not just failed (active())

A dispatched session that finishes its turn (ccpool done) without closing the
bead is never re-nudged, so waiting to MaxWait was pure waste. Rename live() to
active() and treat StateDone as terminal (final bead re-check then OnFailure);
needs_input stays 'keep waiting'. Adds the Live:true+StateDone tests the bug
slipped through, plus a needs_input-waits-to-MaxWait lock and an active() table."
```

---

## Task 2: `DispatchContext` rename + `Validate()`

**Files:**

- Modify: `internal/discover/discover.go` (the `Dispatch` type + the `[]Dispatch` returns)
- Modify: `internal/orchestrator/orchestrator.go` (param types at lines 82, 105, 149, 194, 257)
- Modify: `internal/orchestrator/orchestrator_test.go` (every `discover.Dispatch{...}` literal)
- Test: `internal/discover/discover_test.go`

- [ ] **Step 1: Write the failing `Validate()` test**

Append to `internal/discover/discover_test.go`:

```go
func TestDispatchContext_Validate(t *testing.T) {
	reg := roles.NewRegistry(config.Default())
	cases := []struct {
		name    string
		d       DispatchContext
		wantErr bool
		wantSub string
	}{
		{"valid", DispatchContext{Role: reg.Worker, BeadID: "zr-1"}, false, ""},
		{"missing-bead", DispatchContext{Role: reg.Worker}, true, "bead"},
		{"missing-role", DispatchContext{BeadID: "zr-1"}, true, "role"},
		{"missing-both", DispatchContext{}, true, "role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err %q should mention %q", err, tc.wantSub)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails to compile**

Run: `go test ./internal/discover/ -run TestDispatchContext_Validate`
Expected: FAIL — `undefined: DispatchContext` (the type is still named `Dispatch`).

- [ ] **Step 3: Rename the type and add `Validate()`**

In `internal/discover/discover.go`, replace the `Dispatch` type definition with:

```go
// DispatchContext is everything one dispatch needs. Today: role + bead. It is the
// explicit growth point for future fields (repo, self_login, template variables);
// keeping it a struct keeps run-role's call shape stable as it accretes fields.
type DispatchContext struct {
	Role   roles.Role
	BeadID string
}

// Validate reports every required field that is missing, so callers (run-role) can
// fail fast with a complete diagnostic rather than dispatching a half-filled context.
func (d DispatchContext) Validate() error {
	var missing []string
	if d.Role.Name == "" { // every real role has a Name; Kind 0 is a valid kind, so it can't signal "unset"
		missing = append(missing, "role")
	}
	if d.BeadID == "" {
		missing = append(missing, "bead")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dispatch context missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
```

`discover.go` already imports `fmt` and `strings`, so no import changes.

- [ ] **Step 4: Sweep the remaining `Dispatch` references to `DispatchContext`**

Find every reference:

Run: `grep -rn 'discover\.Dispatch\b\|\[\]Dispatch\b\|Dispatch{' --include='*.go' .`

Apply these replacements (mechanical, no behavior change):

- In `internal/discover/discover.go`: every `[]Dispatch` → `[]DispatchContext`, and `out = append(out, Dispatch{...})` → `append(out, DispatchContext{...})` in `discoverFeedback` and `discoverWorker`, and `Discover`'s return type.
- In `internal/orchestrator/orchestrator.go`: every `discover.Dispatch` (parameter types on `workOne`, `waitDone`, `fail`, `drain`'s `[]discover.Dispatch`, and `workerWaitWithWatchdog`) → `discover.DispatchContext`.
- In `internal/orchestrator/orchestrator_test.go`: every `discover.Dispatch{` → `discover.DispatchContext{` (including the four literals added in Task 1).

- [ ] **Step 5: Build and run the affected packages**

Run: `go build ./... && go test ./internal/discover/ ./internal/orchestrator/ -v`
Expected: PASS (compiles cleanly; `TestDispatchContext_Validate` and all existing tests green).

- [ ] **Step 6: Commit**

```bash
git add internal/discover/discover.go internal/discover/discover_test.go internal/orchestrator/
git commit -m "refactor(pr-pool): rename discover.Dispatch to DispatchContext + add Validate()

The dispatch payload is the forward-compatible growth point for run-role's
context; Validate() names every missing required field so run-role can fail fast."
```

---

## Task 3: `discover.ForRole` per-role discovery seam

**Files:**

- Modify: `internal/discover/discover.go` (add `ForRole`; refactor `Discover` to compose it; move the empty-`selfLogin` guard into the feedback path)
- Test: `internal/discover/discover_test.go`

- [ ] **Step 1: Write the failing `ForRole` tests**

Append to `internal/discover/discover_test.go`:

```go
func TestForRole_feedbackOwnershipBypassesEnabled(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-mine","issue_type":"task","title":"process-feedback: A","parent":"zr-prA"}]`,
		readyWorker:   `[]`,
		show:          map[string]string{"zr-prA": `{"id":"zr-prA","metadata":{"author":"phillipg"}}`},
	}
	cfg := config.Default()
	cfg.FeedbackEnabled = false // ForRole must run the query anyway (Enabled is bypassed)
	reg := roles.NewRegistry(cfg)
	got, err := ForRole(context.Background(), rr, reg, reg.Feedback, "phillipg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-mine" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("ForRole(feedback) = %+v (want only zr-mine)", got)
	}
}

func TestForRole_workerIgnoresSelfLogin(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[{"id":"zr-w1"},{"id":"zr-w2"}]`}
	reg := roles.NewRegistry(config.Default())
	got, err := ForRole(context.Background(), rr, reg, reg.Worker, "") // empty selfLogin is fine for worker
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role.Kind != roles.Worker {
		t.Fatalf("ForRole(worker) = %+v", got)
	}
}

func TestForRole_feedbackEmptySelfLoginErrors(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[]`}
	reg := roles.NewRegistry(config.Default())
	if _, err := ForRole(context.Background(), rr, reg, reg.Feedback, ""); err == nil {
		t.Error("ForRole(feedback) with empty selfLogin must error (cannot resolve ownership)")
	}
}
```

- [ ] **Step 2: Run them to verify they fail to compile**

Run: `go test ./internal/discover/ -run TestForRole`
Expected: FAIL — `undefined: ForRole`.

- [ ] **Step 3: Implement `ForRole` and refactor `Discover`**

In `internal/discover/discover.go`, replace the body of `Discover` and add `ForRole`. The empty-`selfLogin` guard moves into the feedback path so it protects both `Discover` and direct `ForRole(feedback)` callers:

```go
// Discover returns feedback dispatches (owned by selfLogin) then worker dispatches,
// in priority order, honoring each role's Enabled flag.
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry, selfLogin string) ([]DispatchContext, error) {
	var out []DispatchContext
	if reg.Feedback.Enabled {
		fb, err := ForRole(ctx, br, reg, reg.Feedback, selfLogin)
		if err != nil {
			return nil, err
		}
		out = append(out, fb...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Feedback.Name)
	}
	if reg.Worker.Enabled {
		wk, err := ForRole(ctx, br, reg, reg.Worker, selfLogin)
		if err != nil {
			return nil, err
		}
		out = append(out, wk...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Worker.Name)
	}
	return out, nil
}

// ForRole runs ONE role's discovery query, regardless of the role's Enabled flag
// (the smoke harness must be able to query a role disabled in config). The
// feedback path requires a non-empty selfLogin for the parent-author ownership
// join; the worker path ignores it.
func ForRole(ctx context.Context, br beads.Runner, reg roles.Registry, role roles.Role, selfLogin string) ([]DispatchContext, error) {
	switch role.Kind {
	case roles.Feedback:
		if selfLogin == "" {
			return nil, fmt.Errorf("discover: empty self_login (cannot resolve feedback ownership)")
		}
		return discoverFeedback(ctx, br, role, selfLogin)
	case roles.Worker:
		return discoverWorker(ctx, br, role)
	default:
		return nil, fmt.Errorf("discover: unknown role kind %v", role.Kind)
	}
}
```

Leave `discoverFeedback` and `discoverWorker` as they are (their return types became `[]DispatchContext` in Task 2). The previous top-of-`Discover` `if selfLogin == ""` guard is now removed (its job is done by `ForRole`'s feedback case).

- [ ] **Step 4: Run the discover package**

Run: `go test ./internal/discover/ -v`
Expected: PASS — the three new `ForRole` tests, plus every pre-existing `TestDiscover_*` (notably `TestDiscover_emptySelfLoginErrors`: default reg has feedback enabled, so `Discover("")` calls `ForRole(feedback,"")` which errors and propagates).

- [ ] **Step 5: Commit**

```bash
git add internal/discover/discover.go internal/discover/discover_test.go
git commit -m "refactor(pr-pool): add discover.ForRole per-role seam (Enabled-bypassing)

Discover composes ForRole for both roles; ForRole runs a single role's query for
the smoke harness, bypassing the Enabled gate and guarding empty self_login only
on the feedback (ownership-join) path."
```

---

## Task 4: CLI routing for `run-query` / `run-role`

**Files:**

- Modify: `cmd/pr-pool/args.go` (`routeKind`, `routeResult`, `route()`, new parsers, help text)
- Test: `cmd/pr-pool/args_test.go`

- [ ] **Step 1: Write the failing route/parse tests**

Append to `cmd/pr-pool/args_test.go`:

```go
func TestRoute_runSubcommands(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want routeKind
	}{
		{"run-role-ok", []string{"pr-pool", "run-role", "feedback", "zr-1"}, routeRunRole},
		{"run-role-missing-bead", []string{"pr-pool", "run-role", "feedback"}, routeUsageErr},
		{"run-role-unknown-role", []string{"pr-pool", "run-role", "bogus", "zr-1"}, routeUsageErr},
		{"run-role-extra-arg", []string{"pr-pool", "run-role", "feedback", "zr-1", "x"}, routeUsageErr},
		{"run-query-ok", []string{"pr-pool", "run-query", "worker"}, routeRunQuery},
		{"run-query-missing-role", []string{"pr-pool", "run-query"}, routeUsageErr},
		{"run-query-unknown-role", []string{"pr-pool", "run-query", "bogus"}, routeUsageErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := route(tc.argv).kind; got != tc.want {
				t.Errorf("route(%v).kind = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

func TestParseRunRoleArgs_carriesRoleAndBead(t *testing.T) {
	r := parseRunRoleArgs([]string{"worker", "zr-9"})
	if r.kind != routeRunRole || r.role != "worker" || r.bead != "zr-9" {
		t.Errorf("parseRunRoleArgs = %+v, want routeRunRole role=worker bead=zr-9", r)
	}
}
```

- [ ] **Step 2: Run them to verify they fail to compile**

Run: `go test ./cmd/pr-pool/ -run 'TestRoute_runSubcommands|TestParseRunRoleArgs_carriesRoleAndBead'`
Expected: FAIL — `undefined: routeRunRole` / `routeRunQuery` / `parseRunRoleArgs` and `routeResult` has no `role`/`bead` fields.

- [ ] **Step 3: Add the route kinds, result fields, route cases, and parsers**

In `cmd/pr-pool/args.go`, extend the `routeKind` constants:

```go
const (
	routeDrain    routeKind = iota // run a drain with .rest as the subcommand args
	routeVersion                   // print the version and exit 0
	routeHelp                      // print usage and exit 0
	routeUsageErr                  // print .msg + usage to stderr and exit 2
	routeRunRole                   // dispatch one bead through a role (.role, .bead)
	routeRunQuery                  // run a role's discovery query read-only (.role)
)
```

Extend `routeResult`:

```go
type routeResult struct {
	kind routeKind
	rest []string // drain subcommand args (routeDrain only)
	msg  string   // diagnostic for routeUsageErr
	role string   // run-role / run-query role name
	bead string   // run-role bead id
}
```

Add the two cases to the `switch args[0]` in `route()`, next to `case "drain":`:

```go
	case "run-role":
		return parseRunRoleArgs(args[1:])
	case "run-query":
		return parseRunQueryArgs(args[1:])
```

Add the parsers and the known-role set (place near `parseDrainArgs`):

```go
// knownRoles is the set of role names run-query/run-role accept. Today it is the
// fixed feedback/worker set; under the planned TOML extraction it becomes the set of
// configured role names. Kept here so arg parsing stays pure (no config load), per
// the pg2-52rn "no fall-through to a real dispatch on bad input" guarantee.
var knownRoles = map[string]bool{"feedback": true, "worker": true}

// parseRunRoleArgs validates `run-role <role> <bead>`. Pure: a missing/unknown role
// or missing bead yields routeUsageErr (exit 2) before any config load or dispatch.
func parseRunRoleArgs(args []string) routeResult {
	if len(args) < 1 || !knownRoles[args[0]] {
		return routeResult{kind: routeUsageErr, msg: "run-role: unknown or missing role (want: feedback|worker)"}
	}
	if len(args) < 2 || args[1] == "" {
		return routeResult{kind: routeUsageErr, msg: "run-role: missing bead id"}
	}
	if len(args) > 2 {
		return routeResult{kind: routeUsageErr, msg: "run-role: unexpected argument: " + args[2]}
	}
	return routeResult{kind: routeRunRole, role: args[0], bead: args[1]}
}

// parseRunQueryArgs validates `run-query <role>`. Pure, same fail-fast contract.
func parseRunQueryArgs(args []string) routeResult {
	if len(args) < 1 || !knownRoles[args[0]] {
		return routeResult{kind: routeUsageErr, msg: "run-query: unknown or missing role (want: feedback|worker)"}
	}
	if len(args) > 1 {
		return routeResult{kind: routeUsageErr, msg: "run-query: unexpected argument: " + args[1]}
	}
	return routeResult{kind: routeRunQuery, role: args[0]}
}
```

Update `usageLine` and the `helpText` subcommands section:

```go
const usageLine = "usage: pr-pool [--version | --help] [drain | run-query <role> | run-role <role> <bead>]"
```

In `helpText`, under `Subcommands:`, add after the `drain` line:

```
  run-query <role>        run a role's discovery query and print matches (read-only)
  run-role <role> <bead>  dispatch one bead through a role, then tear down (smoke test)
```

- [ ] **Step 4: Run the cmd package tests**

Run: `go test ./cmd/pr-pool/ -run 'TestRoute|TestParseRun' -v`
Expected: PASS (new tests plus the existing `TestRoute*`/`TestParseDrainArgs*`, which are unaffected).

- [ ] **Step 5: Commit**

```bash
git add cmd/pr-pool/args.go cmd/pr-pool/args_test.go
git commit -m "feat(pr-pool): route run-query/run-role subcommands (pure, fail-fast)

Adds routeRunRole/routeRunQuery, role/bead fields on routeResult, and pure
parsers that reject a missing/unknown role or bead with exit 2 before any
side effect; help text updated."
```

---

## Task 5: `Orchestrator.RunOne` + cmd wiring

**Files:**

- Modify: `internal/orchestrator/orchestrator.go` (add `RunOne`)
- Test: `internal/orchestrator/orchestrator_test.go`
- Create: `cmd/pr-pool/runrole.go`
- Create: `cmd/pr-pool/runrole_test.go`
- Modify: `cmd/pr-pool/main.go` (dispatch the two routes)

- [ ] **Step 1: Write the failing `RunOne` tests**

Append to `internal/orchestrator/orchestrator_test.go`:

```go
func TestRunOne_feedbackClosesSession(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.RunOne(context.Background(), d); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !contains(cc.ensured, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must Ensure the session; ensured=%v", cc.ensured)
	}
	if !contains(cc.closed, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must close its one session; closed=%v", cc.closed)
	}
}

func TestRunOne_doneWithoutCloseFlagsAndCloses(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateDone}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.RunOne(context.Background(), d); err == nil {
		t.Fatal("done-without-close should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback failure must unclaim; updates=%v", bd.updates)
	}
	if !contains(cc.closed, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must still close its session on failure; closed=%v", cc.closed)
	}
}
```

- [ ] **Step 2: Run them to verify they fail to compile**

Run: `go test ./internal/orchestrator/ -run TestRunOne`
Expected: FAIL — `o.RunOne undefined`.

- [ ] **Step 3: Implement `RunOne`**

In `internal/orchestrator/orchestrator.go`, add (after `DrainOnce`):

```go
// RunOne dispatches a single DispatchContext through the full workOne path and then
// closes that one session (the drain's pass-level teardownAll is not involved). It is
// the single-bead entry behind `pr-pool run-role`: smoke-test one role against one
// bead without running discovery.
func (o *Orchestrator) RunOne(ctx context.Context, d discover.DispatchContext) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	defer func() {
		if err := o.CC.Close(ctx, name); err != nil {
			slog.Warn("run-one teardown close failed", "session", name, "err", err)
		}
	}()
	return o.workOne(ctx, d)
}
```

`slog` and `discover` are already imported in `orchestrator.go`.

- [ ] **Step 4: Run the `RunOne` tests**

Run: `go test ./internal/orchestrator/ -run TestRunOne -v`
Expected: PASS.

- [ ] **Step 5: Write the `resolveRole` test**

Create `cmd/pr-pool/runrole_test.go`:

```go
package main

import (
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestResolveRole(t *testing.T) {
	reg := roles.NewRegistry(config.Default())
	if r, ok := resolveRole(reg, "feedback"); !ok || r.Kind != roles.Feedback {
		t.Errorf("feedback should resolve to the feedback role (ok=%v kind=%v)", ok, r.Kind)
	}
	if r, ok := resolveRole(reg, "worker"); !ok || r.Kind != roles.Worker {
		t.Errorf("worker should resolve to the worker role (ok=%v kind=%v)", ok, r.Kind)
	}
	if _, ok := resolveRole(reg, "bogus"); ok {
		t.Errorf("unknown role must not resolve")
	}
}
```

- [ ] **Step 6: Run it to verify it fails to compile**

Run: `go test ./cmd/pr-pool/ -run TestResolveRole`
Expected: FAIL — `undefined: resolveRole`.

- [ ] **Step 7: Implement the cmd handlers**

Create `cmd/pr-pool/runrole.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// resolveRole maps a CLI role name to the registry's role. The arg parser already
// rejects unknown names; this is the defensive in-process resolution.
func resolveRole(reg roles.Registry, name string) (roles.Role, bool) {
	switch name {
	case "feedback":
		return reg.Feedback, true
	case "worker":
		return reg.Worker, true
	}
	return roles.Role{}, false
}

// runRunRole dispatches a single bead through one role and tears down its session.
// It does NOT run discovery and does NOT resolve self_login: the bead is explicit,
// and workOne does not consume self_login today (it will return when DispatchContext
// gains a self_login field). precheck still validates the store/prefix.
func runRunRole(roleName, beadID string) int {
	ctx := context.Background()
	cfg := config.Load()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	reg := roles.NewRegistry(cfg)
	role, ok := resolveRole(reg, roleName)
	if !ok {
		printUsageErr("run-role: unknown role: " + roleName)
		return exitUsage
	}
	dctx := discover.DispatchContext{Role: role, BeadID: beadID}
	if err := dctx.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitUsage
	}
	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: reg,
		Cfg: cfg,
	}
	if err := o.RunOne(ctx, dctx); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	return exitOK
}

// runRunQuery runs one role's discovery query read-only and prints the matches
// (id, type, title). It resolves self_login because the feedback query's
// parent-author join needs it.
func runRunQuery(roleName string) int {
	ctx := context.Background()
	cfg := config.Load()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	selfLogin, err := resolveSelf(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve self:", err)
		return exitPrecheck
	}
	reg := roles.NewRegistry(cfg)
	role, ok := resolveRole(reg, roleName)
	if !ok {
		printUsageErr("run-query: unknown role: " + roleName)
		return exitUsage
	}
	dispatches, err := discover.ForRole(ctx, br, reg, role, selfLogin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-query:", err)
		return exitGeneric
	}
	for _, d := range dispatches {
		iss, err := beads.ShowObj(ctx, br, d.BeadID)
		if err != nil {
			fmt.Printf("%s\t(show failed: %v)\n", d.BeadID, err)
			continue
		}
		fmt.Printf("%s\t%s\t%s\n", iss.ID, iss.Type, iss.Title)
	}
	fmt.Printf("# %d %s dispatch(es)\n", len(dispatches), role.Name)
	return exitOK
}
```

- [ ] **Step 8: Wire the routes into `main.go`**

In `cmd/pr-pool/main.go`, add two cases to the `switch r.kind` block, after `case routeDrain:`:

```go
	case routeRunRole:
		os.Exit(runRunRole(r.role, r.bead))
	case routeRunQuery:
		os.Exit(runRunQuery(r.role))
```

- [ ] **Step 9: Build and run the full module**

Run: `go build ./... && go test ./... -v`
Expected: PASS across every package; `pr-pool` binary builds.

- [ ] **Step 10: Verify the help text and a usage error by hand**

Run: `go run ./cmd/pr-pool --help`
Expected: the `run-query <role>` and `run-role <role> <bead>` lines appear under `Subcommands:`.

Run: `go run ./cmd/pr-pool run-role bogus zr-1; echo "exit=$?"`
Expected: stderr names the bad role + usage line; `exit=2`.

- [ ] **Step 11: Commit**

```bash
git add internal/orchestrator/orchestrator.go internal/orchestrator/orchestrator_test.go cmd/pr-pool/runrole.go cmd/pr-pool/runrole_test.go cmd/pr-pool/main.go
git commit -m "feat(pr-pool): run-role/run-query smoke harness (RunOne single dispatch)

run-role dispatches one bead through a role (fail-fast on an incomplete
DispatchContext) and tears down its one session via Orchestrator.RunOne;
run-query prints a role's discovery matches read-only. Wires both into main."
```

---

## Final verification

- [ ] **Run the whole suite and the formatters the pre-commit hook enforces**

Run: `go test ./... && gofmt -l . && go vet ./...`
Expected: all tests PASS; `gofmt -l` prints nothing; `go vet` clean. (The repo's `golangci-lint-pr-pool` pre-commit hook runs on commit; if it flags anything, fix and amend.)

- [ ] **Confirm the spec's behavioral claim end to end (optional, live)**

From a normal shell (NOT nested in a Claude session), against a safe bead:
Run: `PR_POOL_REPO_ROOT=/Volumes/ziprecruiter/monorepo go run ./cmd/pr-pool run-query feedback`
Expected: prints the feedback dispatches (id/type/title) + a count, read-only, exit 0.

---

## Self-review notes (author)

- **Spec coverage:** stop-on-`done` (Task 1) · `needs_input` stays waiting + lock test (Task 1) · `active()` rename + doc comment (Task 1) · success-race test (Task 1) · `DispatchContext` + `Validate()` (Task 2) · `discover.ForRole` with Enabled-bypass and feedback-only `selfLogin` guard (Task 3) · `run-query`/`run-role` routing + fail-fast (Task 4) · `RunOne` + single-session teardown + cmd wiring + help (Task 5). All spec sections map to a task.
- **Deviation from spec, flagged:** the spec's §4 flow listed `resolveSelf` for `run-role`; the plan omits it because `workOne`/`RunOne` do not consume `self_login` today (YAGNI — it returns when `DispatchContext` gains that field). `run-query` keeps `resolveSelf` (the feedback join needs it).
- **Deferred (not in this plan):** event-model role/query split (`pg2-r6cf`); `needs_input` operator notification + teardown survival (`pg2-th35`); richer query/role config (spec B/C).
