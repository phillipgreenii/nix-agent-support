# pr-pool ExampleTOML per-role budget round-trip — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-yt0n` (P3, bug, label `pr-pool`).

**Goal:** Make `emitRole` (`internal/config/example.go`) emit a `[role.ccpool.budget]` table so that `pr-pool config --print-defaults` output, when reloaded, reproduces each role's in-memory budget exactly — in particular the `feedback` role must round-trip to a FULLY UNLIMITED budget (no 25m watchdog), matching `roles.BuiltinRoleSet`.

**Architecture:** `ExampleTOML()` is GENERATED from `roles.BuiltinRoleSet(...)`, but `emitRole` currently omits the budget table. On reload, `buildCCPool` (`registry.go:221-222`) seeds every ccpool role's budget from `c.WorkerBudget()` (the pool default, `Time=25m`) and only overlays a per-role `[role.ccpool.budget]` if present. With no emitted budget table, BOTH example roles silently inherit the 25m watchdog on reload — diverging from the generated role set, where both example roles carry a zero/unlimited budget (verified: `BuiltinParams.WorkerBudget` is left zero by `ExampleTOML`). The fix emits each role's actual `Budget` field-by-field (`tokens`, `cost`, `time`), so the overlay restores the exact in-memory value. The unlimited representation is `tokens = 0`, `cost = 0`, `time = "0s"` — verified to round-trip to `Tokens.Unlimited() && Cost.Unlimited() && Time <= 0` (the exact predicate `executor.budgetUnlimited` uses to decide "no watchdog").

**Tech Stack:** Go (stdlib `fmt`/`strings`, `BurntSushi/toml` decode). Repo: `phillipgreenii-nix-agent-support`, package `packages/pr-pool`. Budget semantics: `budget.Limit.Unlimited()` is `<= 0` (`internal/budget/budget.go:16`); `duration.UnmarshalText` uses `time.ParseDuration` (`internal/config/registry.go:21-28`); `time.Duration(0).String() == "0s"` and `(25*time.Minute).String() == "25m0s"` both round-trip through `time.ParseDuration`.

**Tech facts already verified (do not re-derive):**

- Current bug (probed): reloading `ExampleTOML()` gives `feedback` budget `Tokens=0 Cost=0 Time=25m0s` → `time<=0` is **false** → an unwanted 25m watchdog (same harmful-reminder class as `pg2-yukh`).
- Fix (probed): a `[role.ccpool.budget]` with `tokens = 0`, `cost = 0`, `time = "0s"` reloads to `Tokens=0 Cost=0 Time=0s` → all three unlimited → `true`.
- `ExampleTOML`'s role set has BOTH `feedback` AND `worker` at zero budget (`BuiltinParams.WorkerBudget` is unset in `example.go:43-47`). So emitting the actual `r.CCPool.Budget` for every role yields `tokens=0/cost=0/time="0s"` for both, and both round-trip to unlimited — making `print-defaults` self-consistent. (In real `Load()`, the worker correctly gets the pool default because `BuiltinParams.WorkerBudget = c.WorkerBudget()` — that path is unaffected by this change.)
- `budgetUnlimited` (`internal/executor/ccpool.go:103-104`) is **unexported**, so the round-trip test in package `config` cannot call it; assert the equivalent triple inline: `b.Tokens.Unlimited() && b.Cost.Unlimited() && b.Time <= 0`.

**Interaction with `pg2-wgg0` (same budget-serialization surface):** `pg2-wgg0` adds an XDG-global `[pool].budget` layer; it touches `overlayConfigBudget` / `Load()`, NOT `emitRole`. This plan's generic field-by-field emission (`time = "<dur>.String()"`, verified to round-trip non-zero values like `25m0s` too) is forward-compatible: if a future change gives the example a non-zero worker budget, `emitRole` will serialize it correctly without edits. Do `pg2-yt0n` first or together; there is no code conflict (different functions).

**Branch:** `pr-pool-example-budget-roundtrip` (off `main`).

---

### Task 1: Failing round-trip test — feedback budget must be unlimited

**Files:**

- Test: `packages/pr-pool/internal/config/example_test.go:8-21` (extend the existing `TestExampleTOML_roundTrips`)

- [ ] **Step 1: Create the branch off main**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git fetch origin
git switch --create pr-pool-example-budget-roundtrip origin/main
```

(If `origin/main` is not the right base in this checkout, branch off local `main`: `git switch --create pr-pool-example-budget-roundtrip main`.)

- [ ] **Step 2: Add the failing assertion to the existing round-trip test**

In `packages/pr-pool/internal/config/example_test.go`, replace the body of `TestExampleTOML_roundTrips` (currently lines 8-21) with the version below. It keeps every existing assertion and adds a `feedback`-budget-unlimited check. (`budgetUnlimited` is unexported in package `executor`; we assert the identical triple inline.)

```go
func TestExampleTOML_roundTrips(t *testing.T) {
	writeCfg(t, ExampleTOML())
	c, err := Load()
	if err != nil {
		t.Fatalf("example config must load: %v\n---\n%s", err, ExampleTOML())
	}
	if len(c.Roles) != 2 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" {
		t.Fatalf("example must reproduce built-in feedback+worker: %+v", c.Roles)
	}
	// The worker's authorship guard and completion mode must survive the round-trip.
	if c.Roles[1].CCPool == nil || !c.Roles[1].CCPool.AuthorshipGuard || c.Roles[1].CCPool.Completion != "close-or-handback" {
		t.Fatalf("worker ccpool config did not round-trip: %+v", c.Roles[1].CCPool)
	}
	// The feedback role carries budget.Budget{} (fully unlimited => NO watchdog) in
	// roles.BuiltinRoleSet. Without an emitted [role.ccpool.budget] table, buildCCPool
	// seeds it from the pool default (Time=25m) and the example reload silently adds a
	// 25m watchdog. Assert the exact "fully unlimited" triple executor.budgetUnlimited
	// checks: Tokens<=0 && Cost<=0 && Time<=0.
	fb := c.Roles[0].CCPool
	if fb == nil {
		t.Fatalf("feedback ccpool config missing after round-trip: %+v", c.Roles[0])
	}
	if !fb.Budget.Tokens.Unlimited() || !fb.Budget.Cost.Unlimited() || fb.Budget.Time > 0 {
		t.Fatalf("feedback budget did not round-trip to unlimited (got Tokens=%d Cost=%d Time=%v); "+
			"print-defaults reload added a watchdog", int64(fb.Budget.Tokens), int64(fb.Budget.Cost), fb.Budget.Time)
	}
}
```

- [ ] **Step 3: Run the test to verify it FAILS**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestExampleTOML_roundTrips -v`
Expected: FAIL with `feedback budget did not round-trip to unlimited (got Tokens=0 Cost=0 Time=25m0s); print-defaults reload added a watchdog`.

- [ ] **Step 4: Commit the failing test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pr-pool/internal/config/example_test.go
git commit -m "test(pr-pool): assert ExampleTOML feedback budget round-trips to unlimited (pg2-yt0n)"
```

---

### Task 2: Emit `[role.ccpool.budget]` from `emitRole`

**Files:**

- Modify: `packages/pr-pool/internal/config/example.go:56-73` (`emitRole`)

- [ ] **Step 1: Emit each role's actual budget field-by-field**

In `packages/pr-pool/internal/config/example.go`, inside `emitRole`, add a `[role.ccpool.budget]` table at the END of the `if r.CCPool != nil { ... }` block — i.e. AFTER the `prompt = '''...'''` line (currently line 70) and BEFORE the closing brace of the `if`. The duration is serialized via `time.Duration.String()` (`0` → `"0s"`, `25*time.Minute` → `"25m0s"`), both of which round-trip through the decoder's `time.ParseDuration`.

The full updated function (showing the inserted lines in context):

```go
func emitRole(b *strings.Builder, r roles.Role) {
	fmt.Fprintf(b, "[[role]]\nname = %q\ntype = %q\ncap = %d\nenabled = %t\n", r.Name, r.Type, r.Cap, r.Enabled)
	emitQuery(b, r.Query)
	if r.CCPool != nil {
		cc := r.CCPool
		b.WriteString("[role.ccpool]\n")
		fmt.Fprintf(b, "actor = %q\n", cc.Actor)
		fmt.Fprintf(b, "completion = %q\n", string(cc.Completion))
		fmt.Fprintf(b, "on_failure = %q\n", string(cc.OnFailure))
		fmt.Fprintf(b, "on_dispatch_fail = %q\n", string(cc.OnDispatchFail))
		fmt.Fprintf(b, "authorship_guard = %t\n", cc.AuthorshipGuard)
		// '''...''': a newline right after the opening delimiter is trimmed by TOML,
		// so the decoded value equals PromptBody exactly (PromptBody has no trailing
		// newline). PromptBody never contains ''' so no escaping is needed.
		fmt.Fprintf(b, "prompt = '''\n%s'''\n", cc.PromptBody)
		// Emit the budget EXPLICITLY so a print-defaults reload reproduces the in-memory
		// budget. Without it, buildCCPool seeds every role from the pool default
		// (Time=25m), silently giving the feedback role an unwanted watchdog (pg2-yt0n).
		// tokens/cost are Limit (<=0 == unlimited); time uses Duration.String()
		// ("0s" for unlimited), which time.ParseDuration round-trips.
		b.WriteString("[role.ccpool.budget]\n")
		fmt.Fprintf(b, "tokens = %d\n", int64(cc.Budget.Tokens))
		fmt.Fprintf(b, "cost = %d\n", int64(cc.Budget.Cost))
		fmt.Fprintf(b, "time = %q\n", cc.Budget.Time.String())
	}
	b.WriteString("\n")
}
```

- [ ] **Step 2: Verify the package still compiles (no new imports needed)**

`emitRole` already imports `fmt` and `strings`; `cc.Budget.Time.String()` needs no import (`time.Duration` is already the field type, carried via `roles`/`budget`). Confirm:

Run: `cd packages/pr-pool && go build ./internal/config/`
Expected: builds clean (no `imported and not used` / undefined errors).

- [ ] **Step 3: Run the round-trip test to verify it PASSES**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestExampleTOML_roundTrips -v`
Expected: PASS. (Probed: feedback reloads to `Tokens=0 Cost=0 Time=0s` → all unlimited.)

- [ ] **Step 4: Run the whole config package to confirm no regression**

Run: `cd packages/pr-pool && go test ./internal/config/ -v`
Expected: PASS — all existing tests (`TestDefault`, the role-decode tests, etc.) still pass; the worker role's `authorship_guard`/`completion` assertions are unaffected.

- [ ] **Step 5: Commit the fix**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pr-pool/internal/config/example.go
git commit -m "fix(pr-pool): emit [role.ccpool.budget] in ExampleTOML so feedback round-trips unlimited (pg2-yt0n)"
```

---

### Task 3: Full verification + repo checks

**Files:** none (verification only).

- [ ] **Step 1: Full pr-pool package test + vet**

Run: `cd packages/pr-pool && go test ./... && go vet ./...`
Expected: all PASS. (Watch the `internal/executor` and `internal/config` packages especially — they share the budget surface.)

- [ ] **Step 2: Manual smoke — print-defaults emits a budget table that round-trips**

Run: `cd packages/pr-pool && go run ./cmd/pr-pool config --print-defaults | grep -n -A3 'role.ccpool.budget'`
Expected: TWO `[role.ccpool.budget]` blocks (feedback + worker), each showing:

```
[role.ccpool.budget]
tokens = 0
cost = 0
time = "0s"
```

(If the subcommand path differs, discover it first: `go run ./cmd/pr-pool config --help` — the flag is `--print-defaults` per `ExampleTOML`'s doc comment.)

- [ ] **Step 3: Repo-wide checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`):

```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```

Expected: both PASS. No `go.mod` / `gomod2nix.toml` change (no new dependency added), so no gomod2nix regenerate needed.

- [ ] **Step 4: Final commit (only if Step 3 produced formatting changes)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git status --short
# If prek/pre-commit reformatted files:
git add -A && git commit -m "chore(pr-pool): formatting from pre-commit (pg2-yt0n)"
```

(If `git status` is clean, skip this step.)

---

## Self-review checklist (done while writing)

- **Spec coverage (bead AC):**
  - "`emitRole` should emit `[role.ccpool.budget]` (representing unlimited explicitly, e.g. `time="0s"`)" → Task 2 Step 1 (emits `tokens`/`cost`/`time`; unlimited = `tokens=0 cost=0 time="0s"`, verified).
  - "a round-trip test should assert `feedback` ends up unlimited (`budgetUnlimited` true)" → Task 1 Step 2 asserts the exact `budgetUnlimited` triple inline (`Tokens.Unlimited() && Cost.Unlimited() && Time <= 0`), since `budgetUnlimited` is unexported.
  - Files match the bead: `internal/config/example.go` + `internal/config/example_test.go`.
- **Unlimited representation (the BLOCKING unknown):** RESOLVED empirically. Emitting `tokens = 0`, `cost = 0`, `time = "0s"` reloads to `Tokens.Unlimited()=true && Cost.Unlimited()=true && Time<=0=true`. The `time` key MUST be present and `"0s"`: the reload base `c.WorkerBudget()` is `Time=25m`, and `overlayBudget` only overrides a field when its TOML pointer is non-nil — omitting `time` would leave the 25m watchdog. Emitting `tokens`/`cost` as `0` is also done explicitly so an operator's `PR_POOL_BUDGET_TOKENS`/`_COST` env can't leak a finite ceiling into the example reload.
- **Type consistency:** `cc.Budget.Tokens`/`cc.Budget.Cost` are `budget.Limit` (an `int64`) → emitted via `int64(...)` + `%d`; `cc.Budget.Time` is `time.Duration` → emitted via `.String()` + `%q`; TOML keys `tokens`/`cost`/`time` exactly match `budgetTOML` struct tags (`registry.go:44-48`). Test reads `c.Roles[0].CCPool.Budget` (a `budget.Budget`), matching the field on `roles.CCPoolConfig`.
- **No placeholders:** every code/command step shows the actual edit or command + expected output.
- **TDD + frequent commits:** failing test (Task 1) → fix (Task 2) → verify/repo-checks (Task 3); one commit per logical step.
