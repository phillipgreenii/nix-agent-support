# pg-assistants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A minimal per-user daemon, `pg-assistants`, that keeps configured "permanent" Claude Code assistants running in tmux and recycles each session through a nudge, interrupt, kill ladder driven by its context fill and the account's usage blocks.

**Architecture:** A new Go module `packages/pg-assistants` with small internal packages (config, tmux client, control client, block tracker, pure ladder engine) and a supervisor that runs one isolated goroutine per assistant. All process interaction goes through `x/cmdrun` with per-call contexts and timeouts; live context and block readings come from a new `usage` verb on `pg-claude-control` (mod and command). A home-manager module renders the TOML config, checks it at build time, pre-trusts the assistants' directories and registers a launchd agent.

**Tech Stack:** Go 1.26 (`mkGoApp`, gomod2nix), `github.com/BurntSushi/toml`, `github.com/phillipgreenii/x` (`cmdrun`, `cmdruntest`, `exitcode`, `jsonllogger`, `lifecycle`, `pprofserver`), TypeScript mod tests under node, a Nix HM module with the launchd registry.

**Spec:** `docs/superpowers/specs/2026-11-16-pg-assistants-design.md` (read it first; this plan argues from it).

## Global Constraints

- Public repo: no ZipRecruiter details and no user identifiers other than the operator's own; tests use `example-*` names.
- No bead ids anywhere in the repo (code, docs, commit messages).
- Code MUST NOT cite `docs/superpowers/specs/`; cite ADR 0128 (Task 10) instead.
- **Isolation (spec G6):** nothing one assistant does may delay, block or stop another's loop. Shared state (block tracker, status board) is mutex-guarded and no lock is held across I/O.
- **Context:** the root context comes from `x/lifecycle`; each loop, tick and external call derives its own child context (`context.WithTimeout` per call, matching `cmdrun.Call.Timeout`). No `context.Background()`/`TODO()` below `main`. Every exported function that does I/O takes `ctx context.Context` first. The module is in the golangci lint lists (`noctx`, `contextcheck`, `.golangci.yml:14-20`) and one `cmdruntest.AssertCallsBounded` test guards every `cmdrun.Call` (Task 3).
- Call timeouts: tmux 10 s; `usage`, `submit`, `interrupt`, `stop --signal` 15 s; graceful `stop` `grace.kill + 15s`.
- Tests use `cmdruntest.Fake`, never re-exec doubles; time-dependent tests use `testing/synctest`, no injected clock.
- Exit codes come from `x/exitcode`: `Usage` 2, `Unavailable` 3, `Degraded` 5, `Config` 7; 1 is generic only.
- Real-tmux tests carry `//go:build integration` and run only in `checks.<system>.pg-assistants-integration-tests` (on demand), never in the unit gate.
- Direct `go test` runs as `nice -n 10 go test -p 4 ./...` with `GOCACHE=/Volumes/gitrepos/build_tmp/shared/go-build`.
- `nix build .#checks...` runs in the background (`bgrun` + `bgwait --timeout 540`, Bash timeout 600000).
- Versions are per-source digests: never hand-bump a version.
- Markdown: wrap paths, globs and identifiers in backticks (prettier).

## Review Focus

1. **One assistant's `pg-claude-control` call hangs until its timeout:** every other assistant keeps ticking on time. Test: Task 8 `TestSlowAssistantDoesNotDelayOthers`.
2. **One assistant is misconfigured or its `cwd` was deleted:** the daemon starts, that assistant shows its error in `status`, the others run. Tests: Task 3 `TestLoadIsolatesAssistantProblems`, Task 8 `TestMissingCwdStartFailsAndBacksOff`, Task 9 `TestDaemonRunsWithOneBadAssistant`.
3. **A nudged assistant ends its turn without running `exit`:** the ladder escalates on time. Tests: Task 5 `TestMeterNudgeThenIdleEscalates`, Task 8 `TestNudgedIdleSessionIsRecycled`.
4. **The 5-hour block reaches 95% while assistants run:** all are nudged to stop, none restarts until the reset, all restart after it. Test: Task 8 `TestBlockLadderAndHold`.
5. **SIGTERM while a graceful stop is in flight:** the call is cancelled and the daemon exits within its shutdown deadline, sessions untouched. Test: Task 8 `TestCancelStopsInFlightCalls`.

---

## File Structure

```text
claude-marketplace/pg-claude-control/hooks/{core.ts,register.ts,core.test.ts}   # usage verb (Task 1)
packages/pg-claude-control/cmd/pg-claude-control/{main.go,send.go,main_test.go} # usage subcommand (Task 2)
packages/pg-assistants/
  cmd/pg-assistants/{main.go,daemon.go,status.go,exit.go,*_test.go}
  internal/config/{config.go,windows.go,config_test.go}
  internal/tmux/{tmux.go,tmux_test.go,tmux_integration_test.go}
  internal/control/{control.go,control_test.go}
  internal/blocks/{blocks.go,blocks_test.go}
  internal/ladder/{ladder.go,ladder_test.go}
  internal/supervisor/{launch.go,status.go,exitreq.go,messages.go,supervisor.go,*_test.go}
  bounded_test.go
  completions/{_pg-assistants,pg-assistants.bash}
  share/tldr/pages.common/pg-assistants.md
  default.nix, go.mod, gomod2nix.toml, update-deps.sh, README.md
home/programs/pg-assistants/default.nix
tests/pg-assistants-hm-launchd.nix
docs/adr/0128-pg-assistants-minimal-supervisor-for-permanent-assistants.md
```

---

### Task 1: `usage` verb in the pg-claude-control mod

**Files:**

- Modify: `claude-marketplace/pg-claude-control/hooks/core.ts` (`VERBS` :11, `Io` :157-168, `Controller.execute` :309-351)
- Modify: `claude-marketplace/pg-claude-control/hooks/register.ts` (`io` :31-45)
- Modify: `claude-marketplace/pg-claude-control/README.md` (verb table :38-48), `.claude-plugin/plugin.json` description
- Test: `claude-marketplace/pg-claude-control/hooks/core.test.ts`

**Interfaces:**

- Produces: request line `usage` (no argument) answers `{ok:true, result:{context?:{percent?:number,tokens?:number}, rateLimits:[{kind:string, percentUsed:number, resetsAt?:string}], cost?:{usd?:number}}}`, or `{ok:false, reason:"usage unavailable: <message>"}` when the engine throws. Like `read`, a request without a reply path runs and writes nothing.

- [ ] **Step 1: Write the failing tests** next to the `read`/`ping` tests (:198):
  - `usage replies with the engine figures`: `usage()` resolves `{context:{percent:41.5,tokens:83000}, rateLimits:[{kind:"five_hour",percentUsed:62,resetsAt:"2026-11-16T20:00:00Z"}], cost:{usd:1.25}, extra:"x"}`; the reply is `{ok:true,result:{context:{percent:41.5,tokens:83000},rateLimits:[{kind:"five_hour",percentUsed:62,resetsAt:"2026-11-16T20:00:00Z"}],cost:{usd:1.25}}}` (unknown keys dropped).
  - `usage with no figures yet`: `{}` gives `{ok:true,result:{rateLimits:[]}}`.
  - `usage when the engine throws`: `{ok:false,reason:"usage unavailable: boom"}`.
  - `usage parses as a known verb`: `parseRequest("[id=a] usage reply=/tmp/r")` succeeds.
- [ ] **Step 2: Run** `nix build .#checks.aarch64-darwin.pg-claude-control-mod-node-tests -L` (background). Expected: FAIL on the new tests.
- [ ] **Step 3: Implement.** Add `'usage'` to `VERBS`, `usage: () => Promise<unknown>` to `Io`, the `usage` case in `execute` mapping only the three fields, and `usage: () => $.session.usage()` in `register.ts`. Update the README table and plugin description.
- [ ] **Step 4: Run** the same check plus `pg-claude-control-plugin-validate`. Expected: PASS.
- [ ] **Step 5: Commit** `pg-claude-control: add the usage verb to the mod`.

### Task 2: `usage` subcommand in the pg-claude-control command

**Files:**

- Modify: `packages/pg-claude-control/cmd/pg-claude-control/main.go` (dispatch :141; `usageText` :50-72, also correcting the stale exit-code text at :66 and :68-70 to match `errors.go`)
- Modify: `packages/pg-claude-control/cmd/pg-claude-control/send.go` (`command()` :219-251, output :289-315)
- Modify: `packages/pg-claude-control/README.md` (synopsis :3-17)
- Test: `packages/pg-claude-control/cmd/pg-claude-control/main_test.go`

**Interfaces:**

- Consumes: Task 1's reply.
- Produces: `pg-claude-control usage <target> [--json] [--wait d] [--timeout d]`, always with a reply path. `--json` prints the existing report object `{sent,confirmed,pid,session,id,result}` with `result` = the mod's `result`. Without `--json` it prints one line: `context 83000 tokens (41.5%); five_hour 62% resets 2026-11-16T20:00:00Z; cost $1.25` (absent parts omitted). Existing exit codes (3, 4, 6, 9, 21, 22).

- [ ] **Step 1: Write the failing tests**, modeled on `TestExit6RefusedAndRepliesCarryResults` (:340): `TestUsageJSONCarriesResult` (`.result.context.tokens == 83000`, exit 0); `TestUsageTextLine` (prints `context 83000 tokens (41.5%)`); `TestUsageRequestCarriesReply` (the FIFO line contains `usage` and `reply=`); `TestUsageNoReplyTimesOut` (`--timeout 50ms`, exit 6).
- [ ] **Step 2: Run** `cd packages/pg-claude-control && nice -n 10 go test -p 4 ./cmd/...`. Expected: FAIL.
- [ ] **Step 3: Implement** the case as `req{verb:"usage"}` with `o.reply = true` (the `read` pattern, :236-238) and the text formatter.
- [ ] **Step 4: Run** again. Expected: PASS.
- [ ] **Step 5: Commit** `pg-claude-control: add the usage subcommand`.

### Task 3: Scaffold `packages/pg-assistants` and the config package

**Files:**

- Create: `packages/pg-assistants/{go.mod,default.nix,update-deps.sh,README.md,bounded_test.go}`, `gomod2nix.toml` (generated by `./update-deps.sh`), `cmd/pg-assistants/main.go` (`var Version = "dev"`, `config check` only for now)
- Create: `packages/pg-assistants/internal/config/{config.go,windows.go,config_test.go}`
- Modify: `flake.nix`: overlay entry beside `pg-keep-awake` (:415-417); `simpleGoLintModules` (:2102); `pg-assistants-go-tests` (`mkGoTest`) beside `pg-keep-awake-go-tests` (:8021-8026); `packages` re-export (:21862)
- Modify: `scripts/check-identifier-allowlist.sh`: a `MODULES` row with scope `wide` and a `kind:go` canary (as the pg-claude-control row has); create `scripts/identifier-allowlists/pg-assistants.txt`

Module path `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants`; `default.nix` mirrors `packages/pg-keep-awake/default.nix` (Pattern A, `subPackages = ["cmd/pg-assistants"]`).

**Interfaces:**

- Produces (package `config`):

```go
type Config struct {
    Tick       time.Duration
    Grace      Grace
    Blocks     Blocks
    Restart    Restart
    Paths      Paths
    Assistants []Assistant
    Problems   []FieldError // global fields that fell back to their defaults
}
type Grace struct{ Interrupt, Kill time.Duration }
type Points struct{ Nudge, Interrupt, Kill float64 }
type Blocks struct {
    Points
    UnknownResetHold time.Duration
}
type Restart struct{ MinUptime, BackoffInitial, BackoffMax time.Duration }
type Paths struct{ Tmux, Claude, PgClaudeControl, Shell, StateDir string }
type Assistant struct {
    ID, Name, Model, Effort, Cwd, Prompt string
    Enabled  bool
    Tick     time.Duration // resolved: own value or Config.Tick
    Env      map[string]string
    Context  Context
    Problems []FieldError  // non-empty: the daemon does not start this assistant
}
type Context struct {
    Limit, Window int64 // Window resolved from the model table unless set
    Points
}
type FieldError struct{ Field, Msg string } // Error(): "<field>: <msg>", e.g. "assistant[example-a].context.kill: ..."
// Load returns an error only for an unreadable or unparseable file or an
// invalid [paths] entry. Every other problem is recorded in Problems.
func Load(path string) (Config, error)
// Strict joins every Problem (global and per assistant); config check uses it.
func (c Config) Strict() error
func ContextWindow(model string) (int64, bool)
```

- Defaults: `tick 1m`, `grace.interrupt 5m`, `grace.kill 2m`, `blocks` nudge `0.95` / interrupt `0.98` / kill `1.00`, `blocks.unknown_reset_hold 1h`, `restart.min_uptime 1m`, `backoff_initial 1m`, `backoff_max 30m`, `context.kill 1.00`. TOML keys are the snake_case of the spec's config sketch.
- Rules are the spec's list. A failing global field is replaced by its default and recorded in `Config.Problems`; a failing assistant rule is recorded in that assistant's `Problems` (duplicate ids and shared `cwd` mark every assistant involved). `cwd` existence is never checked here.
- `windows.go`: an exact-id table seeded from pa-monitor's `internal/core/models/windows.go` (header comment naming the origin) WITHOUT its family fallback, plus the `[1m]` suffix rule and the current ids (`claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, `claude-fable-5-1`), each verified against the Models API `max_input_tokens` as pa-monitor's `docs/runbooks/refresh-model-tables.md` describes, with the date in the comment.
- `pg-assistants config check --config <path>`: `Load` then `Strict`; exit 0 printing `ok: <n> assistants`; exit 7 printing each problem on its own line; no `--config` exits 2.

- [ ] **Step 1: Write the failing tests** (`config_test.go`, table-driven over TOML written to `t.TempDir()`):
  - `TestLoadAppliesDefaults`: every default above; `Assistants[0].Tick == time.Minute`.
  - `TestPerAssistantTickOverrides`: `tick = "30s"` gives `30*time.Second`.
  - `TestAssistantRules`, one case per rule, asserting the `Field` in that assistant's `Problems`: duplicate id (both marked); id `Bad_ID`; relative `cwd`; shared `cwd` (both marked); effort `turbo`; empty prompt; prompt `--resume`; context `nudge 0.9 >= interrupt 0.8`; `interrupt 0.95 > kill 0.9`; `limit 200000, kill 1.05` on a 200000 window; unknown model `claude-sonnet-9` without `context.window`.
  - `TestLoadIsolatesAssistantProblems`: one bad and one good assistant: `Load` returns no error, the good one has no `Problems`.
  - `TestGlobalProblemFallsBackToDefault`: `grace.kill = "0s"` gives `2m` and a `Config.Problems` entry for `grace.kill`; `blocks.interrupt = 0.5` (below nudge) falls back the whole `[blocks]` points to defaults with a problem.
  - `TestLoadFatalErrors`: unparseable TOML and a relative `paths.tmux` return an error.
  - `TestStrictJoinsAll`: the bad-assistant file gives a `Strict()` error naming its field.
  - `TestValidateAcceptsKillAboveOne`: `limit 200000, kill 1.05` on a 1000000 window passes.
  - `TestMissingCwdIsNotAProblem`: a nonexistent absolute `cwd` passes.
  - `TestContextWindowExactIDsOnly`: `claude-sonnet-5-5` is known; `claude-sonnet-9` is not.
  - `cmd/pg-assistants` `TestConfigCheckExitCodes`: valid 0, any problem 7 naming the field, no flag 2.
  - `bounded_test.go` `TestCallsBounded`: `cmdruntest.AssertCallsBounded(t, ".")`.
- [ ] **Step 2: Run** `cd packages/pg-assistants && nice -n 10 go test -p 4 ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** `config.go`, `windows.go`, the `config check` path; run `./update-deps.sh`.
- [ ] **Step 4: Run** the tests; then `nix build .#pg-assistants .#checks.aarch64-darwin.pg-assistants-go-tests -L` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: scaffold the package and the config loader`.

### Task 4: tmux client

**Files:**

- Create: `packages/pg-assistants/internal/tmux/{tmux.go,tmux_test.go,tmux_integration_test.go}`
- Modify: `flake.nix`: `taggedGoLintModules` (:2123); `pg-assistants-integration-tests`, modeled on pg-keep-awake's (:8039-8100), with `tmux` on its PATH

**Interfaces:**

- Produces:

```go
type Client struct {
    Bin    string // absolute path to tmux
    Socket string // "pg-assistants"
    Run    cmdrun.Runner
}
type SessionState struct{ Exists, Dead bool } // Dead: the pane's process has exited
func (c Client) NewSession(ctx context.Context, name, cwd string, env, argv []string) error
func (c Client) State(ctx context.Context, name string) (SessionState, error)
func (c Client) PanePID(ctx context.Context, name string) (int, error)
func (c Client) Kill(ctx context.Context, name string) error // nil when already gone
```

- Every call is `tmux -L <Socket> -f /dev/null ...` with a 10 s `context.WithTimeout` and `Call.Timeout`; targets use `=<name>`. `NewSession` runs `new-session -d -s <name> -c <cwd> -e K=V... -- <argv...>` with env in the given order. `State` runs `has-session -t =<name>` first and only then `display-message -p -t =<name> '#{pane_dead}'` (ccpool `internal/tmux/client.go` :203-243 explains the order). "no server running" and "can't find session" mean `Exists: false`, not an error. A cancelled `ctx` returns `ctx.Err()` wrapped.

- [ ] **Step 1: Write the failing tests:**
  - `tmux_test.go` (Fake runner): `TestNewSessionArgv` (exact argv including `-L pg-assistants -f /dev/null`, env order kept, `--` before argv); `TestStateChecksHasSessionFirst`; `TestStatePaneDead` (`1` gives `{true,true}`); `TestStateNoServer`; `TestKillWhenGone`; `TestCancelledContext` (a `Respond` rule that blocks until `ctx.Done()`; cancelling returns an error wrapping `context.Canceled`).
  - `tmux_integration_test.go` (`//go:build integration`, real tmux, socket `pg-assistants-test-<random>`, server killed in `t.Cleanup`):
    - `TestRealLifecycle`: start `sh -c 'echo $$ > <dir>/pid; echo "$X" > <dir>/x; exec sleep 30'` with env `X=1`; `State == {true,false}`, `PanePID` equals the pid in `<dir>/pid`, `<dir>/x` holds `1`; `Kill`; `State.Exists == false`.
    - `TestRealExitEndsSession`: start `true`; `State.Exists` becomes false within 2 s.
    - `TestRealDeadPaneRecovery`: start `sleep 1`, then run `tmux -L <socket> set-option -t =<name> remain-on-exit on` from the test; wait until `State == {true,true}`; a same-name `NewSession` fails; after `Kill` it succeeds.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 ./internal/tmux/` and the same with `-tags integration`. Expected: FAIL.
- [ ] **Step 3: Implement** `tmux.go`.
- [ ] **Step 4: Run** both. Expected: PASS. Build `checks.aarch64-darwin.pg-assistants-integration-tests` once (background).
- [ ] **Step 5: Commit** `pg-assistants: add the tmux client`.

### Task 5: Pure ladder engine

**Files:**

- Create: `packages/pg-assistants/internal/ladder/{ladder.go,ladder_test.go}`

**Interfaces:**

- Produces:

```go
type Kind int   // KindNone, KindMeter, KindDisabled, KindExit
type Rung int   // RungNone, RungNudge, RungInterrupt, RungKill, RungForceKill
type Action int // ActNone, ActNudge, ActInterrupt, ActKill, ActForceKill, ActGracefulStop
type State struct {
    Kind Kind
    Rung Rung
    At   time.Time // when the current rung fired
}
type Meter struct {
    Name     string  // "context", "five_hour", "seven_day"
    Fraction float64
    Points   config.Points
    Detail   string  // e.g. "83000 of 200000 tokens"
}
type Input struct {
    Now           time.Time
    Enabled       bool
    ExitRequested bool
    Meters        []Meter // only the meters with a current reading
}
type Grace struct{ Interrupt, Kill time.Duration }
// Step returns the next state, the one action to take now (or ActNone), and a
// reason naming the meter that set the target, e.g. "context at 81% (162000
// of 200000 tokens)". The caller advances the state whether or not the action
// succeeds.
func Step(s State, in Input, g Grace) (State, Action, string)
```

- Rules (spec "Ladders"): priority Exit > Disabled > Meters; the rung never moves down; at most one action per call.
  - Exit: below `RungKill` → `ActGracefulStop`, rung `RungKill`; at `RungKill`/`RungForceKill` → `ActForceKill`, rung `RungForceKill`.
  - Disabled: `RungNone` → `ActNudge`; `RungNudge` and `Now-At >= g.Interrupt` → `ActInterrupt`; `RungInterrupt` and `Now-At >= g.Kill` → `ActKill`; `RungKill`/`RungForceKill` → `ActForceKill` (every call).
  - Meters (enabled): the target is the highest rung any meter reaches against its own `Points`; the next rung up fires when the target is above the current rung. With or without meters: `RungNudge` and `Now-At >= g.Interrupt` → `ActInterrupt`; `RungInterrupt` and `Now-At >= g.Kill` → `ActKill`; `RungKill`/`RungForceKill` → `ActForceKill`. At `RungNone` with no meter at its nudge: `ActNone`.

- [ ] **Step 1: Write the failing tests** (table-driven; context meter points `{0.8,0.95,1.0}`, block points `{0.95,0.98,1.0}`, `g = Grace{5*time.Minute, 2*time.Minute}`):
  - `TestBelowNudgeDoesNothing`: context 0.75 → `ActNone`.
  - `TestContextNudge`: context 0.80 → `ActNudge`, reason contains `context` and `80%`.
  - `TestOneRungPerStep`: context 1.0 from `RungNone` → `ActNudge`, `ActInterrupt`, `ActKill` on three calls.
  - `TestHighestMeterWins`: context 0.5 and five_hour 0.98 → target interrupt, reason names `five_hour`.
  - `TestBlockMeterLadder`: five_hour 0.95 → `ActNudge`; 0.98 → `ActInterrupt`; 1.0 → `ActKill`.
  - `TestNeverMovesDown`: at `RungInterrupt`, context 0.05, `Now = At+1m` → `ActNone`.
  - `TestMeterNudgeThenIdleEscalates`: at `RungNudge`, context 0.81, `Now = At+5m` → `ActInterrupt`; `At+4m59s` → `ActNone`.
  - `TestInterruptTimeFallback`: at `RungInterrupt`, `Now = At+2m` → `ActKill`; `At+119s` → `ActNone`.
  - `TestForceKillRepeats`: `RungKill` then `RungForceKill` → `ActForceKill` both times.
  - `TestKillAboveOne`: context kill 1.05 at `RungInterrupt`, `Now = At+1m`: 1.025 → `ActNone`; 1.05 → `ActKill`.
  - `TestDisabledLadderTiming`: `ActNudge`; `+4m59s` `ActNone`; `+5m` `ActInterrupt`; `+2m` `ActKill`; next `ActForceKill`.
  - `TestExitRequestWins`: at `RungNudge` → `ActGracefulStop`; next → `ActForceKill`.
  - `TestNoMetersAtRungNone`: enabled, no meters → `ActNone`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 ./internal/ladder/`. Expected: FAIL.
- [ ] **Step 3: Implement** `Step`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the ladder engine`.

### Task 6: Control client and block tracker

**Files:**

- Create: `packages/pg-assistants/internal/control/{control.go,control_test.go}`
- Create: `packages/pg-assistants/internal/blocks/{blocks.go,blocks_test.go}`

**Interfaces:**

- Consumes: Task 2's `usage --json` output; `ladder.Meter`, `config.Blocks`.
- Produces (package `control`; calls inherit the daemon's environment, which carries the operator's session variables, Task 10):

```go
type Client struct {
    Bin string // absolute path to pg-claude-control
    Run cmdrun.Runner
}
type Usage struct {
    HaveContext   bool
    ContextTokens int64
    ContextPct    float64 // 0 when absent
    Limits        []Limit
}
type Limit struct {
    Kind     string    // "five_hour" or "seven_day"; other kinds dropped
    Fraction float64   // percentUsed / 100; readings outside 0..100 dropped
    ResetsAt time.Time // zero when absent or not RFC 3339
}
var (
    ErrNoControl = errors.New("no control mod")    // exit 3
    ErrNoProcess = errors.New("no such process")   // exit 4
    ErrNoReply   = errors.New("no reply")          // exit 6
    ErrBusy      = errors.New("mod not accepting") // exit 9
    ErrRefused   = errors.New("refused by mod")    // exit 21
    ErrAmbiguous = errors.New("ambiguous target")  // exit 22
)
func (c Client) Usage(ctx context.Context, pid int) (Usage, error)              // usage pid:<n> --json --timeout 5s
func (c Client) Submit(ctx context.Context, pid int, text string) error          // submit pid:<n>, text on stdin
func (c Client) Interrupt(ctx context.Context, pid int) error                    // interrupt pid:<n>
func (c Client) Stop(ctx context.Context, pid int, escalate time.Duration) error // stop pid:<n> --escalate <d>
func (c Client) Signal(ctx context.Context, pid int) error                       // stop pid:<n> --signal
```

Errors wrap the sentinel with stderr; a cancelled `ctx` returns an error wrapping `ctx.Err()`. Timeouts per Global Constraints.

- Produces (package `blocks`):

```go
type Tracker struct{ /* mutex-guarded; no I/O under the lock */ }
func NewTracker(cfg config.Blocks) *Tracker
func (t *Tracker) Observe(limits []control.Limit, now time.Time)
// Meters returns the current block meters (readings whose reset time has not
// passed), for every running session's ladder.
func (t *Tracker) Meters(now time.Time) []ladder.Meter
// Hold reports whether starts are held: any current block at or above
// cfg.Nudge holds until its ResetsAt, or until the reading time +
// cfg.UnknownResetHold when ResetsAt is zero.
func (t *Tracker) Hold(now time.Time) (bool, time.Time)
func (t *Tracker) Snapshot(now time.Time) []control.Limit
```

- [ ] **Step 1: Write the failing tests:**
  - `control_test.go` (Fake runner): `TestUsageParsesResult` (stdout `{"sent":true,"result":{"context":{"tokens":83000,"percent":41.5},"rateLimits":[{"kind":"five_hour","percentUsed":62,"resetsAt":"2026-11-16T20:00:00Z"},{"kind":"spend","percentUsed":5},{"kind":"seven_day","percentUsed":1.7e9}]}}` gives `HaveContext`, 83000, and only the five_hour limit at `0.62` with its time); `TestUsageNoContextYet`; `TestUsageMapsExitCodes` (3, 4, 6, 9, 21, 22 via `errors.Is`); `TestSubmitSendsStdin`; `TestStopArgv` (`stop pid:42 --escalate 2m0s`, timeout `2m15s`); `TestControlCancelledContext`.
  - `blocks_test.go`: `TestMetersCarryBlockPoints` (five_hour 0.96 gives a meter named `five_hour` with points `{0.95,0.98,1.0}`); `TestExpiredReadingDiscarded` (after `ResetsAt`, no meter and no hold); `TestHoldAtNudgeLevel` (0.95 holds until `ResetsAt`; 0.94 does not); `TestHoldUnknownReset` (0.96 with zero `ResetsAt` holds until reading time + 1h); `TestNoReadingNoHold`; `TestConcurrentObserveAndRead` (parallel `Observe`/`Meters`/`Hold` under `-race`).
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/control/ ./internal/blocks/`. Expected: FAIL.
- [ ] **Step 3: Implement** both packages.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the control client and the block tracker`.

### Task 7: Launch command, status board and exit requests

**Files:**

- Create: `packages/pg-assistants/internal/supervisor/{launch.go,status.go,exitreq.go,launch_test.go,status_test.go,exitreq_test.go}`

**Interfaces:**

- Consumes: `config.Assistant`, `config.Paths`, `control.Limit`.
- Produces:

```go
// LaunchArgv is the pane command from the spec's Launch section:
// <Shell> -l -i -c 'exec "$@"' <Shell> <Claude> --session-id <id> --name <Name>
//   --model <Model> --effort <Effort> --allowed-tools 'Bash(pg-assistants exit)' <Prompt>
func LaunchArgv(p config.Paths, a config.Assistant, sessionID string) []string
// LaunchEnv: DISABLE_AUTO_COMPACT=1, PG_ASSISTANT_ID, PG_ASSISTANTS_STATE_DIR,
// the five blanked CLAUDE* markers, then a.Env sorted (a.Env cannot override
// any of the first eight).
func LaunchEnv(a config.Assistant, stateDir string) []string

type AssistantStatus struct {
    ID              string    `json:"id"`
    Enabled         bool      `json:"enabled"`
    Running         bool      `json:"running"`
    PID             int       `json:"pid,omitempty"`
    SessionID       string    `json:"session_id,omitempty"`
    StartedAt       time.Time `json:"started_at,omitzero"`
    ContextTokens   int64     `json:"context_tokens,omitempty"`
    ContextFraction float64   `json:"context_fraction,omitempty"`
    Ladder          string    `json:"ladder,omitempty"` // meter | disabled | exit
    Rung            string    `json:"rung,omitempty"`   // nudge | interrupt | kill | force-kill
    Reason          string    `json:"reason,omitempty"`
    WaitUntil       time.Time `json:"wait_until,omitzero"` // backoff or block hold
    Hint            string    `json:"hint,omitempty"`
    LastError       string    `json:"last_error,omitempty"`
    ConfigError     string    `json:"config_error,omitempty"`
    LastTick        time.Time `json:"last_tick,omitzero"`
}
type Status struct {
    WrittenAt  time.Time         `json:"written_at"`
    Assistants []AssistantStatus `json:"assistants"`
    Blocks     []control.Limit   `json:"blocks"`
}
type StatusBoard struct{ /* mutex-guarded; Flush copies under the lock and writes outside it */ }
func NewStatusBoard(path string) *StatusBoard
func (b *StatusBoard) Set(s AssistantStatus)
func (b *StatusBoard) SetBlocks(l []control.Limit)
func (b *StatusBoard) Flush(now time.Time) error // atomic tmp+rename, mode 0600

func ValidID(id string) bool                               // [a-z0-9-]+
func WriteExitRequest(stateDir, id string, at time.Time) error // <stateDir>/exit-requests/<id>, dir 0700
func TakeExitRequest(stateDir, id string) (bool, error)     // true and deletes when present
func ClearExitRequest(stateDir, id string) error
```

- [ ] **Step 1: Write the failing tests:**
  - `TestLaunchArgv`: for `Paths{Shell:"/bin/zsh", Claude:"/c/claude"}` and `{ID:"example-a", Name:"Example A", Model:"claude-sonnet-5-5", Effort:"medium", Prompt:"Resume."}`, session `u1`: exactly `["/bin/zsh","-l","-i","-c","exec \"$@\"","/bin/zsh","/c/claude","--session-id","u1","--name","Example A","--model","claude-sonnet-5-5","--effort","medium","--allowed-tools","Bash(pg-assistants exit)","Resume."]`.
  - `TestLaunchEnv`: holds `DISABLE_AUTO_COMPACT=1`, `PG_ASSISTANT_ID=example-a`, `CLAUDECODE=`; `a.Env{"PG_ASSISTANT_ID":"x","FOO":"bar"}` keeps `example-a` and adds `FOO=bar`; no `PATH` entry (the login shell sets it).
  - `TestStatusBoardFlush`: two `Set` calls then `Flush` give one 0600 file with both, valid JSON.
  - `TestStatusBoardConcurrent`: parallel `Set` and `Flush` under `-race`.
  - `TestExitRequestRoundTrip`: write, take gives true and deletes; take again false; clear on a missing file nil; `ValidID("../x") == false`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/supervisor/`. Expected: FAIL.
- [ ] **Step 3: Implement** the three files.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add launch, status and exit-request helpers`.

### Task 8: Supervisor loop

**Files:**

- Create: `packages/pg-assistants/internal/supervisor/{supervisor.go,messages.go,supervisor_test.go}`

**Interfaces:**

- Consumes: Tasks 3-7.
- Produces:

```go
type Tmux interface {
    NewSession(ctx context.Context, name, cwd string, env, argv []string) error
    State(ctx context.Context, name string) (tmux.SessionState, error)
    PanePID(ctx context.Context, name string) (int, error)
    Kill(ctx context.Context, name string) error
}
type Control interface {
    Usage(ctx context.Context, pid int) (control.Usage, error)
    Submit(ctx context.Context, pid int, text string) error
    Interrupt(ctx context.Context, pid int) error
    Stop(ctx context.Context, pid int, escalate time.Duration) error
    Signal(ctx context.Context, pid int) error
}
type Deps struct {
    Tmux    Tmux
    Control Control
    Blocks  *blocks.Tracker
    Status  *StatusBoard
    Events  *slog.Logger // events: start, start_failed, adopt, exited, nudge, interrupt, kill, force_kill,
                         // graceful_stop, usage_failed, window_mismatch, action_failed, config_problem, loop_panic
    NewID   func() string                          // fresh UUID v4
    Stat    func(path string) (os.FileInfo, error) // os.Stat in production
}
// Run starts one goroutine per assistant without Problems (first tick
// immediately, then every a.Tick), records config errors for the others in
// the status board, starts a heartbeat that calls Status.Flush every 15 s,
// and returns when ctx is done and every goroutine has returned. It never
// touches tmux sessions on shutdown.
func Run(ctx context.Context, cfg config.Config, d Deps) error
func NudgeText(reason string) string
func StopText(reason string) string
```

- Each assistant's goroutine runs `loop(ctx)`, wrapped so a panic is recovered, logged as `loop_panic` with the stack, and the loop restarted after the restart backoff. Each tick runs under `tickCtx, cancel := context.WithTimeout(loopCtx, a.Tick + cfg.Grace.Kill + 30*time.Second)`; each call inside derives its own timeout (Global Constraints). Nothing in a tick waits on another assistant.
- Per tick (spec "Reconcile loop"):
  - `State`. Exists and alive: record "last seen alive". Exists but dead: `Kill` (event `exited`), treat as not running.
  - Not running: if a session was known, compute uptime as last-seen-alive minus start (0 if never seen alive), log `exited`, clear the ladder; a requested exit restarts on the next tick without touching backoff; otherwise uptime < `MinUptime` doubles the backoff from `BackoffInitial` up to `BackoffMax`, a longer uptime resets it. Start when enabled, past any backoff, and `Blocks.Hold(now)` is false: `Stat(cwd)` (missing → `start_failed`, backoff), `ClearExitRequest`, `NewSession`, `PanePID`.
  - A session alive at the loop's first tick is adopted (event `adopt`; start time = now).
  - Running: `TakeExitRequest`; `Usage` (error → `usage_failed`; three consecutive `ErrNoControl` after a start set `Hint` to `control mod not loaded (trust dialog? plugin disabled?)`); on success `Blocks.Observe` and, when `tokens*100/percent` differs from `Context.Window` by more than 5%, `window_mismatch`; build `Meters` (context meter when `HaveContext`, plus `Blocks.Meters(now)`); `ladder.Step`; perform the action (`ActNudge` → `Submit(NudgeText(reason))`; `ActInterrupt` → `Interrupt`, then `Submit(StopText(reason))`; `ActKill` → `Signal`; `ActForceKill` → `Tmux.Kill`; `ActGracefulStop` → `Stop(GraceKill)`); a failed action logs `action_failed` and the rung still advances.
  - Every tick: `Status.Set` (with `LastTick`), `Status.SetBlocks(Blocks.Snapshot(now))`.
- `messages.go`: neither text starts with `/`. Both name the reason; `NudgeText` asks the assistant to finish its current step, write its notes, run `pg-assistants exit` as its last action and end its turn; `StopText` says to stop now and do the same. Both say never to wait for input.

- [ ] **Step 1: Write the failing tests** (`synctest.Test`, hand-written recording fakes; assistant `example-a`, tick 1m, context points `{0.8,0.95,1.0}`, limit 200000):
  - `TestStartsEnabledAssistant` (one `NewSession` with `LaunchEnv`/`LaunchArgv` output); `TestDoesNotStartDisabled`.
  - `TestAdoptsRunningSessionOnStart`: alive at the first tick → no `NewSession`, event `adopt`.
  - `TestContextLadderSequence`: usage 160000 → `Submit(NudgeText)`; 195000 → `Interrupt` then `Submit(StopText)`; 200000 → `Signal`; still alive → `Kill`; still alive → `Kill` again.
  - `TestNudgedIdleSessionIsRecycled`: 165000 every tick → nudge, `Interrupt` 5m later, `Signal` 2m after that.
  - `TestBlockLadderAndHold`: two running assistants; five_hour 0.95 resetting in 3h → both nudged; 0.98 → both interrupted; sessions exit → no start until the reset; one tick after the reset both start.
  - `TestDisabledLadderWithoutControlMod`: `Usage` → `ErrNoControl`, disabled → failed nudge logged as `action_failed`, `Interrupt` at +5m, `Signal` at +7m, `Kill` at +8m.
  - `TestNoControlModHint`: three `ErrNoControl` ticks after a start set the hint.
  - `TestExitRequestStopsGracefully`: request → `Stop(pid, 2m)`, file deleted; the next tick after the session is gone starts a new one; backoff unchanged.
  - `TestLeftoverExitRequestClearedBeforeStart`.
  - `TestManualKillResetsLadder`: at `RungInterrupt` the session vanishes → the next session begins at `RungNone`.
  - `TestCrashBeforeFirstTickBacksOff`: gone at the first tick after each start → restarts after 1m, 2m, 4m; a session seen alive for 10m resets to 1m; never beyond 30m.
  - `TestDeadPaneIsKilledBeforeStart`: `State {true,true}` → `Kill` then `NewSession`.
  - `TestMissingCwdStartFailsAndBacksOff`: `Stat` error → `start_failed`, `LastError`, retry after backoff; a second assistant still starts.
  - `TestAssistantWithProblemsIsNotStarted`: an assistant with `Problems` gets no goroutine and its status shows `ConfigError`; the other assistant starts.
  - `TestSlowAssistantDoesNotDelayOthers`: assistant A's `Usage` blocks until its context is done; assistant B (tick 1m) still ticks 5 times in 5 minutes, and A's call returns with a deadline error at its 15 s timeout.
  - `TestPanicInOneLoopIsContained`: A's fake `State` panics once → `loop_panic` logged, A's loop restarts after backoff, B unaffected.
  - `TestCancelStopsInFlightCalls`: cancel the root context while A is in `Stop` → `Run` returns within 1 s, the fake saw a cancelled context, no `Kill` was called.
  - `TestPerAssistantTicks`: 30s and 1m assistants tick 4 and 2 times in 2 minutes.
  - `TestMessagesNeverStartWithSlash`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/supervisor/`. Expected: FAIL.
- [ ] **Step 3: Implement** `supervisor.go` and `messages.go`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the supervisor loop`.

### Task 9: CLI subcommands `daemon`, `status`, `exit`

**Files:**

- Modify: `packages/pg-assistants/cmd/pg-assistants/main.go`
- Create: `packages/pg-assistants/cmd/pg-assistants/{daemon.go,status.go,exit.go}` with tests
- Create: `packages/pg-assistants/completions/{_pg-assistants,pg-assistants.bash}`, `share/tldr/pages.common/pg-assistants.md`; `default.nix` `postInstall` copies them (pg-keep-awake pattern)

**Interfaces:**

- Consumes: `config.Load`, `supervisor.Run`, `tmux.Client`, `control.Client`, `blocks.NewTracker`, `NewStatusBoard`, `WriteExitRequest`, `ValidID`.
- Produces:
  - `pg-assistants daemon --config <path> [--shutdown-deadline 8s]` (`lifecycle.DeadlineFlag`): `config.Load` (exit 7 only on its error); each `Config.Problems` entry logged as `config_problem`; create `Paths.StateDir` 0700 before opening anything in it; `events.jsonl` through `jsonllogger.OpenRotatingFile` with `maxBytes` 10 MiB; pprof through `x/pprofserver` when `PG_ASSISTANTS_PPROF_ADDR` is set (`phillipg-nix-repo-base` `docs/go-daemons.md`); `supervisor.Run` with the `x/lifecycle` group's context until SIGTERM/SIGINT.
  - `pg-assistants status [--json] [--config <path>]` (default `$XDG_CONFIG_HOME/pg-assistants/config.toml`, else `~/.config/pg-assistants/config.toml`): reads `<state_dir>/status.json` and prints a table `ID ENABLED RUNNING CONTEXT LADDER WAIT NOTE` (`NOTE` = config error, hint or last error), or the raw JSON. If `written_at` is older than 1 minute it warns `status is <age> old; is the daemon running?` on stderr and exits 5. A missing file exits 3.
  - `pg-assistants exit`: needs `PG_ASSISTANT_ID` (valid per `ValidID`) and `PG_ASSISTANTS_STATE_DIR`; writes the request; prints `exit requested for <id>; end your turn now`. A missing or invalid id exits 2 with `PG_ASSISTANT_ID is not set: run this from inside a pg-assistants session` (or `... is not a valid id`).

- [ ] **Step 1: Write the failing tests:** `TestExitWritesRequest`; `TestExitWithoutAssistantID` (exit 2, nothing written); `TestExitRejectsBadID` (`../x`); `TestStatusTable` (includes a config-error row); `TestStatusStale` (exit 5 and the warning); `TestStatusMissing` (exit 3); `TestDaemonRejectsUnparseableConfig` (exit 7); `TestDaemonRunsWithOneBadAssistant` (starts, logs the problem, supervises the good one, through an internal constructor with fake deps); `TestDaemonCreatesPrivateStateDir` (0700); `TestDaemonStopsOnContextCancel` (returns within the shutdown deadline, no `Kill`).
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 ./cmd/...`. Expected: FAIL.
- [ ] **Step 3: Implement** the subcommands, completions and the tldr page (examples: `status`, `status --json`, `exit`, `config check --config`, `tmux -L pg-assistants attach -t <id>`).
- [ ] **Step 4: Run** the tests and `nix build .#pg-assistants .#checks.aarch64-darwin.pg-assistants-go-tests -L` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the daemon, status and exit subcommands`.

### Task 10: Home-manager module, launchd agent, ADR and docs

**Files:**

- Create: `home/programs/pg-assistants/default.nix` (model: `home/programs/pg-keep-awake/default.nix`; trust activation modeled on `home/programs/ccpool/default.nix` :53-80)
- Create: `tests/pg-assistants-hm-launchd.nix`; register `checks.<system>.test-pg-assistants-hm-launchd` beside `test-pg-keep-awake-hm-launchd` (:19866)
- Modify: `home/default.nix` import list (:40)
- Create: `docs/adr/0128-pg-assistants-minimal-supervisor-for-permanent-assistants.md`; modify `docs/adr/index.md`
- Modify: `packages/pg-assistants/README.md` (usage, config reference, attaching, and the AGENTS.md contract: keep notes in the directory; when nudged or done, write notes, run `pg-assistants exit` as the last action and end the turn; never wait for input, nobody is attached; an assistant that should poll less often gets a longer `tick`), repo `README.md` module list, and a short `CLAUDE.md` section beside the other HM-scoped agents

**Interfaces:**

- Options under `phillipgreenii.programs.pg-assistants`: `enable`, `package`, `pprof.enable` (default true), `tick`, `grace.{interrupt,kill}`, `blocks.{nudge,interrupt,kill,unknownResetHold}`, `restart.{minUptime,backoffInitial,backoffMax}`, `claudePath` (default `"${pkgs.llm-agentsPkgs.claude-code}/bin/claude"`, the package `home/programs/claude-code` installs), `loginShell` (default `"/bin/zsh"`; the operator's login shell), and `assistants.<id>` submodules (`name`, `enabled` default true, `model`, `effort`, `cwd`, `prompt`, `tick` nullable, `env`, `context.{limit,window,nudge,interrupt,kill}`).
- Renders `config.toml` with `pkgs.formats.toml` to `xdg.configFile."pg-assistants/config.toml"`, filling `[paths]`: tmux and pg-claude-control store paths, `claudePath`, `shell = loginShell`, `state_dir = ${config.xdg.stateHome}/pg-assistants`. Checked at build time by running `pg-assistants config check --config` in a `runCommand` (strict: any problem fails the build).
- `home.packages = [ cfg.package ]` and `programs.tldr.customPages` (as pg-keep-awake :240-245).
- `home.activation.pgAssistantsTrust` (after `writeBoundary`, best effort): mark every assistant `cwd` trusted in `~/.claude.json`, exactly as ccpool's activation does.
- launchd entry `pg-assistants-daemon`, label `com.phillipg.pg-assistants-daemon`, `runAtLoad`, `keepAlive`, `serviceConfig.AbandonProcessGroup = true`, `ExitTimeOut` = 8 s deadline + 7 s = 15, `StandardOutPath`/`StandardErrorPath` under `${config.xdg.stateHome}/pg-assistants-launchd` (outside the 0700 state dir), `EnvironmentVariables` = `HOME`, `PG_ASSISTANTS_CONFIG_HASH`, and `PG_ASSISTANTS_PPROF_ADDR` when `pprof.enable`. `script` = `exec ${loginShell} -l -c 'exec "$@"' ${loginShell} <pkg>/bin/pg-assistants daemon --config <rendered> --shutdown-deadline 8s`, so the daemon and every `pg-claude-control` call it makes see the operator's session variables.
- Assertions: `phillipgreenii.programs.pg-claude-control.enable` (gated on the option being declared, as the recorder module does); every `cwd` and `loginShell` absolute. Linux: no entry, one warning.

- [ ] **Step 1: Write the failing eval test** `tests/pg-assistants-hm-launchd.nix` with `tests/launchd-registry-stub.nix` and the stubs `CLAUDE.md` requires (`assertions`, `warnings`, `home.homeDirectory`, `home.file`), covering: the darwin entry (label, `AbandonProcessGroup`, `ExitTimeOut == 15`, the env keys, log paths outside the state dir); the rendered TOML (one assistant round-trips; `[paths]` filled; block defaults 0.95/0.98/1.00); `home.packages` contains the package; the trust activation lists the `cwd`; the pg-claude-control assertion fires when it is disabled; the non-darwin warning; and the rendered `script` run under `env -i` with `loginShell` set to a fake shell that records its argv and then execs the rest, against a fake `pg-assistants` that records its own argv.
- [ ] **Step 2: Run** `nix build .#checks.aarch64-darwin.test-pg-assistants-hm-launchd -L` (background). Expected: FAIL.
- [ ] **Step 3: Implement** the module; write ADR 0128 (context; decision: a separate minimal daemon, ccpool not used; consequences: chain-of-sessions model, auto-compact off, `limit*kill` below the window, per-assistant isolation, sessions run through the operator's login shell, folder trust handled by the module) and the docs.
- [ ] **Step 4: Run** the eval test, `pg-assistants-go-tests`, and `pn workspace flake-check --no-build <worktree path>` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the home-manager module, launchd agent and ADR 0128`.

### After landing (not a task)

What tests cannot prove is the behavior on this machine after `pn workspace apply`. File a verification bead gated on the apply (`pb:pb-gate-lifecycle`) that enables one throwaway assistant with a small `context.limit` (for example 30000) and checks: it starts and `status` shows it; inside the session `echo $PATH` matches the operator's terminal and `pg-assistants` resolves; `usage` returns live figures; a full nudge, exit request and restart cycle in the events log; that `launchctl kickstart -k gui/$UID/com.phillipg.pg-assistants-daemon` leaves the session running and adopted; and that interrupt-then-submit lands as a new turn on a live session. It cites the Task 8 tests as the logic evidence.
