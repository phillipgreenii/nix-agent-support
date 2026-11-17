# pg-assistants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A minimal per-user daemon, `pg-assistants`, that keeps configured "permanent" Claude Code assistants running in tmux and recycles each session through a nudge, interrupt, kill ladder driven by its context fill and the account's usage blocks.

**Architecture:** A new Go module `packages/pg-assistants` with small internal packages (config, tmux client, control client, block tracker, pure ladder engine, folder trust) and a supervisor that runs one isolated goroutine per assistant and per orphan session. All process interaction goes through `x/cmdrun` with per-call contexts and timeouts; live context and block readings come from a new `usage` verb on `pg-claude-control` (mod and command). A home-manager module renders the TOML config, checks it at build time, pre-trusts the assistants' directories and registers a launchd agent.

**Tech Stack:** Go 1.26 (`mkGoApp`, gomod2nix), `github.com/BurntSushi/toml`, `github.com/phillipgreenii/x` (`cmdrun`, `cmdruntest`, `exitcode`, `jsonllogger`, `jsonlloggertest`, `lifecycle`, `osdirs`, `pprofserver`), `go.uber.org/goleak`, TypeScript mod tests under node, a Nix HM module with the launchd registry.

**Spec:** `docs/superpowers/specs/2026-11-16-pg-assistants-design.md` (read it first; this plan argues from it).

## Global Constraints

- Public repo: no ZipRecruiter details and no user identifiers other than the operator's own; tests use `example-*` names.
- No bead ids anywhere in the repo (code, docs, commit messages).
- Code MUST NOT cite `docs/superpowers/specs/`; cite ADR 0128 (Task 10) instead.
- Line references in this plan were taken on `main` as of 2026-11-16; re-find them by the named symbol if they have moved.
- **Isolation (spec G6):** nothing one assistant does may delay, block or stop another's loop. Shared state (block tracker, status board) is mutex-guarded and no lock is held across I/O.
- **Context:** the root context comes from `x/lifecycle`; each loop, tick and external call derives its own child context. The supervisor wraps every `Tmux`/`Control` call in `context.WithTimeout` (tmux 10 s; `usage`, `submit`, `interrupt`, `stop --signal` 15 s; graceful `stop` `grace.kill + 15s`), and the clients set the same value as `cmdrun.Call.Timeout`, which `AssertCallsBounded` needs. No `context.Background()`/`TODO()` below `main`. Every exported function that does I/O takes `ctx context.Context` first. The module is in the golangci lint lists (`noctx`, `contextcheck`, `.golangci.yml:14-20`) and one `cmdruntest.AssertCallsBounded` test guards every `cmdrun.Call` (Task 3).
- Tests use `cmdruntest.Fake`, never re-exec doubles; time-dependent tests use `testing/synctest`, no injected clock. **Fake limits** (x `cmdruntest/fake.go`): a `Respond` function does not receive the context and runs under the Fake's lock, so it MUST NOT block; the Fake's runner ignores a done context. A cancellation test scripts `cmdrun.Result{Code: -1, CtxErr: context.Canceled}` and asserts the wrapped error; a call that must really block until cancelled uses a hand-written `cmdrun.Runner` closure that waits on `ctx.Done()`.
- **Shared code from `x` (`github.com/phillipgreenii/x`) is used, not re-written.** Before writing a helper, check `x`'s README for it. This plan uses:
  - `cmdrun` / `cmdruntest`: every process spawn and its test double (Tasks 4, 6, 8; `AssertCallsBounded`, Task 3).
  - `exitcode`: every exit status: `Usage` 2, `Unavailable` 3, `NotFound` 4, `Degraded` 5, `Config` 7, `ShutdownOverrun` 10; 1 is generic only.
  - `lifecycle`: the daemon's run group, shutdown deadline (`DeadlineFlag`), watchdog and shutdown events. Each assistant loop, orphan loop, the orphan sweep and the heartbeat is a named loop (`g.Go`), so an overrun names what hung (Tasks 8 and 9).
  - `jsonllogger`: the events log (`NewLogger` over `OpenRotatingFile`, `WithService("pg-assistants")`), and `jsonllogger/jsonlloggertest` for every test that asserts on events (`jsonlloggertest.New`, `Buffer.Records`).
  - `osdirs`: the default config path of `status` and `attach` (`osdirs.ConfigDir("pg-assistants")`) and the default state dir when `config check` reports it.
  - `pprofserver`: `StartFromEnv("PG_ASSISTANTS_PPROF_ADDR", ...)` (required for a daemon with a pprof endpoint by `x`'s `CLAUDE.md`).
  - Not used: `otelsetup` (no metrics on day 1), `gitclient` / `gitfixture` / `gittest` (no git).
- **Real-tool tests** (real tmux, the real `pg-claude-control`, the real launch chain) carry `//go:build integration` and run only in `checks.<system>.pg-assistants-integration-tests` (on demand), never in the unit gate. That check runs `go test -tags integration ./...`, sets `PG_ASSISTANTS_REQUIRE_TOOLS=1` (a test then FAILS instead of skipping when its tool is missing, the `PG_TASK_FOCUS_REQUIRE_NODE` precedent), `TMUX_TMPDIR="$TMPDIR"` and `__darwinAllowLocalNetworking = true` (as the existing tmux sandbox check does, `flake.nix:20367-20371`). Time bounds in these tests are generous (10 s): the host runs at a high load average.
- CLI tests call the subcommands' run functions in-process (pg-keep-awake style), never a built binary, so they stay unit tests.
- Direct `go test` runs as `nice -n 10 go test -p 4 ./...` with `GOCACHE=/Volumes/gitrepos/build_tmp/shared/go-build`.
- `nix build .#checks...` runs in the background (`bgrun` + `bgwait --timeout 540`, Bash timeout 600000).
- Versions are per-source digests: never hand-bump a version.
- Markdown: wrap paths, globs and identifiers in backticks (prettier).

## Review Focus

1. **One assistant's `pg-claude-control` call hangs until its timeout:** every other assistant keeps ticking on time. Test: Task 8 `TestSlowAssistantDoesNotDelayOthers`.
2. **One assistant is misconfigured or its `cwd` was deleted:** the daemon starts, that assistant shows its error in `status`, the others run. Tests: Task 3 `TestLoadIsolatesAssistantProblems`, Task 8 `TestMissingCwdRetriesEveryTick`, Task 9 `TestDaemonRunsWithOneBadAssistant`.
3. **A nudged assistant ends its turn without running `exit`:** the ladder escalates on time. Tests: Task 5 `TestMeterNudgeThenIdleEscalates`, Task 8 `TestNudgedIdleSessionIsRecycled`.
4. **The 5-hour block reaches 95% while assistants run:** all are nudged to stop, none restarts until the reset (also across a daemon restart), all restart after it. Tests: Task 8 `TestBlockLadderAndHold`, `TestHoldSurvivesDaemonRestart`.
5. **SIGTERM while a graceful stop is in flight:** the call is cancelled, the daemon exits within its shutdown deadline, sessions untouched, and the exit request is still on file for the next daemon. Tests: Task 8 `TestCancelStopsInFlightCalls`, `TestExitRequestSurvivesCancelledStop`.
6. **A session that never comes up** (shell startup waiting, trust dialog, plugin off): it is killed after `start_timeout` and backs off, and `status` says why. Test: Task 8 `TestStartStallIsKilledAndBacksOff`.
7. **A session nothing supervises** (removed, renamed or misconfigured assistant): it is stopped through the disabled ladder. Test: Task 8 `TestOrphanSessionGetsDisabledLadder`.

---

## File Structure

```text
claude-marketplace/pg-claude-control/hooks/{core.ts,register.ts,core.test.ts}   # usage verb (Task 1)
packages/pg-claude-control/cmd/pg-claude-control/{main.go,send.go,main_test.go} # usage subcommand (Task 2)
packages/pg-assistants/
  cmd/pg-assistants/{main.go,daemon.go,status.go,attach.go,exit.go,trust.go,*_test.go}
  internal/config/{config.go,windows.go,config_test.go}
  internal/tmux/{tmux.go,tmux_test.go,tmux_integration_test.go}
  internal/control/{control.go,control_test.go,control_integration_test.go}
  internal/blocks/{blocks.go,blocks_test.go}
  internal/ladder/{ladder.go,ladder_test.go}
  internal/trust/{trust.go,trust_test.go}
  internal/supervisor/{launch.go,status.go,exitreq.go,lastexit.go,messages.go,supervisor.go,orphans.go,*_test.go,launch_integration_test.go}
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
- Modify: `claude-marketplace/pg-claude-control/hooks/register.ts` (`io` :31-46)
- Modify: `claude-marketplace/pg-claude-control/README.md` (verb table :38-48), `.claude-plugin/plugin.json` description
- Test: `claude-marketplace/pg-claude-control/hooks/core.test.ts`

**Interfaces:**

- Produces: request line `usage` (no argument) answers `{ok:true, result:{context?:{percent?:number,tokens?:number,window?:number}, rateLimits:[{kind:string, percentUsed:number, resetsAt?:string}], cost?:{usd?:number}}}`, or `{ok:false, reason:"usage unavailable: <message>"}` when the engine throws. Like `read`, a request without a reply path runs and writes nothing.

- [ ] **Step 1: Write the failing tests** next to the `read`/`ping` tests:
  - `usage replies with the engine figures`: `usage()` resolves `{context:{percent:41.5,tokens:83000,window:200000,extra:1}, rateLimits:[{kind:"five_hour",percentUsed:62,resetsAt:"2026-11-16T20:00:00Z"}], cost:{usd:1.25}, extra:"x"}`; the reply is `{ok:true,result:{context:{percent:41.5,tokens:83000,window:200000},rateLimits:[{kind:"five_hour",percentUsed:62,resetsAt:"2026-11-16T20:00:00Z"}],cost:{usd:1.25}}}` (unknown keys dropped).
  - `usage with no figures yet`: `{}` gives `{ok:true,result:{rateLimits:[]}}`.
  - `usage when the engine throws`: `{ok:false,reason:"usage unavailable: boom"}`.
  - `usage parses as a known verb`: `parseRequest("[id=a] usage reply=/tmp/r")` succeeds.
- [ ] **Step 2: Run** `nix build .#checks.aarch64-darwin.pg-claude-control-mod-node-tests -L` (background). Expected: FAIL on the new tests.
- [ ] **Step 3: Implement.** Add `'usage'` to `VERBS`, `usage: () => Promise<unknown>` to `Io`, the `usage` case in `execute` mapping only the listed fields, and `usage: () => $.session.usage()` in `register.ts`. Update the README table and plugin description.
- [ ] **Step 4: Run** the same check plus `pg-claude-control-plugin-validate`. Expected: PASS.
- [ ] **Step 5: Commit** `pg-claude-control: add the usage verb to the mod`.

### Task 2: `usage` subcommand in the pg-claude-control command

**Files:**

- Modify: `packages/pg-claude-control/cmd/pg-claude-control/main.go` (dispatch :141; `usageText` :50-72, adding the verb and correcting the two exit-code statements still stale there: `--reply` "exit 6 on refusal, 8 on no reply" (:66) is 21 and 6, and stop's "exits 9" (:60) is 20, per `errors.go`; the existing usage-text Go test pins the result)
- Modify: `packages/pg-claude-control/cmd/pg-claude-control/send.go` (`command()` :219-251, output :289-315)
- Modify: `packages/pg-claude-control/README.md` (synopsis :3-17)
- Test: `packages/pg-claude-control/cmd/pg-claude-control/main_test.go`

**Interfaces:**

- Consumes: Task 1's reply.
- Produces: `pg-claude-control usage <target> [--json] [--wait d] [--timeout d]`, always with a reply path. `--json` prints the existing report object `{sent,confirmed,pid,session,id,result}` with `result` = the mod's `result` (`session` is the registry's session id). Without `--json` it prints one line: `context 83000 of 200000 tokens (41.5%); five_hour 62% resets 2026-11-16T20:00:00Z; cost $1.25` (absent parts omitted). Existing exit codes (3, 4, 6, 9, 21, 22).

- [ ] **Step 1: Write the failing tests**, modeled on `TestExit6RefusedAndRepliesCarryResults`: `TestUsageJSONCarriesResult` (`.result.context.tokens == 83000`, `.session` set, exit 0); `TestUsageTextLine`; `TestUsageRequestCarriesReply` (the FIFO line contains `usage` and `reply=`); `TestUsageNoReplyTimesOut` (`--timeout 50ms`, exit 6); the usage-text test updated for the two corrected statements.
- [ ] **Step 2: Run** `cd packages/pg-claude-control && nice -n 10 go test -p 4 ./cmd/...`. Expected: FAIL.
- [ ] **Step 3: Implement** the case as `req{verb:"usage"}` with `o.reply = true` (the `read` pattern, :236-238) and the text formatter.
- [ ] **Step 4: Run** again. Expected: PASS.
- [ ] **Step 5: Commit** `pg-claude-control: add the usage subcommand`.

### Task 3: Scaffold `packages/pg-assistants` and the config package

**Files:**

- Create: `packages/pg-assistants/{go.mod,default.nix,update-deps.sh,README.md,bounded_test.go}`, `gomod2nix.toml` (generated by `./update-deps.sh`), `cmd/pg-assistants/{main.go,main_test.go}` (`var Version = "dev"`, `config check` only for now)
- Create: `packages/pg-assistants/internal/config/{config.go,windows.go,config_test.go}`
- Modify: `flake.nix`: overlay entry beside `pg-keep-awake` (:415-417); `simpleGoLintModules` (:2102); `pg-assistants-go-tests` (`mkGoTest`) beside `pg-keep-awake-go-tests` (:8021-8026); `packages` re-export (:21862)
- Modify: `scripts/check-identifier-allowlist.sh`: a `MODULES` row with scope `wide` and a `kind:go` canary (as the pg-claude-control row has); create `scripts/identifier-allowlists/pg-assistants.txt`

Module path `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants`; `go 1.26.8` (matching the other modules, or `scripts/check-go-deps-wired.sh` fails); `x` pinned at or above `v0.0.0-20261114222359-65f23cfd8d2d` (the first version with `jsonllogger.OpenRotatingFile`, already used by pa-monitor and the recorder); `update-deps.sh` is executable. `default.nix` mirrors `packages/pg-keep-awake/default.nix` (Pattern A, `subPackages = ["cmd/pg-assistants"]`) WITHOUT its `postInstall` copies of `completions/` and `share/tldr`, which Task 9 adds once those files exist.

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
type Restart struct{ MinUptime, BackoffInitial, BackoffMax, StartTimeout time.Duration }
type Paths struct{ Tmux, Claude, PgClaudeControl, Shell, StateDir, ClaudeJSON string }
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

- Defaults: `tick 1m`, `grace.interrupt 5m`, `grace.kill 2m`, `blocks` nudge `0.95` / interrupt `0.98` / kill `1.00`, `blocks.unknown_reset_hold 1h`, `restart.min_uptime 1m`, `backoff_initial 1m`, `backoff_max 30m`, `start_timeout 5m`, `context.kill 1.00`. TOML keys are the snake_case of the spec's config sketch.
- Rules are the spec's list. A failing global field is replaced by its default and recorded in `Config.Problems`; a failing assistant rule (including its own `tick`) is recorded in that assistant's `Problems` (duplicate ids and shared `cwd` mark every assistant involved). `cwd` existence is never checked here. Every `[paths]` entry, `claude_json` included, must be absolute.
- `windows.go`: an exact-id table seeded from pa-monitor's `internal/core/models/windows.go` (header comment naming the origin) WITHOUT its family fallback, plus the `[1m]` suffix rule and the current ids (`claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, `claude-fable-5-1`), each verified against the Models API `max_input_tokens` as pa-monitor's `docs/runbooks/refresh-model-tables.md` describes, with the date in the comment.
- `pg-assistants config check --config <path>`: `Load` then `Strict`; exit 0 printing `ok: <n> assistants`; exit 7 printing each problem on its own line; no `--config` exits 2.

- [ ] **Step 1: Write the failing tests** (`config_test.go`, table-driven over TOML written to `t.TempDir()`):
  - `TestLoadAppliesDefaults`: every default above; `Assistants[0].Tick == time.Minute`.
  - `TestPerAssistantTickOverrides`: `tick = "30s"` gives `30*time.Second`.
  - `TestAssistantRules`, one case per rule, asserting the `Field` in that assistant's `Problems`: duplicate id (both marked); id `Bad_ID`; relative `cwd`; shared `cwd` (both marked); empty `name`; empty `model`; effort `turbo`; empty prompt; prompt `--resume`; `limit 0`; context `nudge 0`; `nudge 0.9 >= interrupt 0.8`; `interrupt 0.95 > kill 0.9`; `limit 200000, kill 1.05` on a 200000 window; `limit*kill` exactly equal to the window; unknown model `claude-sonnet-9` without `context.window`; per-assistant `tick = "-1s"` and `tick = "soon"` (problem on the assistant, none global).
  - `TestAssistantRulesAccept`: `interrupt == kill`; unknown model with an explicit `context.window`; `claude-sonnet-5-5[1m]` with `limit 800000`.
  - `TestLoadIsolatesAssistantProblems`: one bad and one good assistant: `Load` returns no error, the good one has no `Problems`.
  - `TestGlobalProblemFallsBackToDefault`: `grace.kill = "0s"` and `grace.kill = "later"` give `2m` and a `Config.Problems` entry for `grace.kill`; `blocks.interrupt = 0.5` (below nudge) falls back the whole `[blocks]` points to defaults with a problem.
  - `TestLoadFatalErrors`: unparseable TOML, a relative `paths.tmux` and a relative `paths.claude_json` return an error.
  - `TestStrictJoinsAll`: the bad-assistant file gives a `Strict()` error naming its field.
  - `TestValidateAcceptsKillAboveOne`: `limit 200000, kill 1.05` on a 1000000 window passes.
  - `TestMissingCwdIsNotAProblem`: a nonexistent absolute `cwd` passes.
  - `TestContextWindowExactIDsOnly`: `claude-sonnet-5-5` is known; `claude-sonnet-9` is not; the `[1m]` suffix gives the 1M window.
  - `cmd/pg-assistants` `TestConfigCheckExitCodes`: valid 0, any problem 7 naming the field, no flag 2.
  - `bounded_test.go` `TestCallsBounded`: `cmdruntest.AssertCallsBounded(t, ".")`.
- [ ] **Step 2: Run** `cd packages/pg-assistants && nice -n 10 go test -p 4 ./...`. Expected: FAIL.
- [ ] **Step 3: Implement** `config.go`, `windows.go`, the `config check` path; run `./update-deps.sh`.
- [ ] **Step 4: Run** the tests; then `nix build .#pg-assistants .#checks.aarch64-darwin.pg-assistants-go-tests -L` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: scaffold the package and the config loader`.

### Task 4: tmux client

**Files:**

- Create: `packages/pg-assistants/internal/tmux/{tmux.go,tmux_test.go,tmux_integration_test.go}`
- Modify: `flake.nix`: `taggedGoLintModules` (:2123); `pg-assistants-integration-tests`, modeled on pg-keep-awake's (:8039-8100) but running `go test -tags integration ./...` with `tmux` on its PATH and the settings in Global Constraints

**Interfaces:**

- Produces:

```go
type Client struct {
    Bin    string // absolute path to tmux
    Socket string // "pg-assistants"
    Conf   string // <state dir>/tmux.conf, passed as -f
    Run    cmdrun.Runner
}
type SessionState struct{ Exists, Dead bool } // Dead: the pane's process has exited
type DeadInfo struct {
    Status, Signal int    // #{pane_dead_status}, #{pane_dead_signal}
    Tail           string // capture-pane -p -S -40
}
// WriteConf writes Conf holding only "set -g remain-on-exit on" (mode 0600).
func (c Client) WriteConf() error
func (c Client) NewSession(ctx context.Context, name, cwd string, env, argv []string) error
func (c Client) State(ctx context.Context, name string) (SessionState, error)
func (c Client) PanePID(ctx context.Context, name string) (int, error)
func (c Client) Dead(ctx context.Context, name string) (DeadInfo, error) // only for a dead pane
func (c Client) List(ctx context.Context) ([]string, error)               // session names
func (c Client) Kill(ctx context.Context, name string) error             // nil when already gone
```

- Every call is `tmux -L <Socket> -f <Conf> ...` with `Call.Timeout` 10 s; targets use `=<name>`. `NewSession` runs `new-session -d -s <name> -c <cwd> -e K=V... -- <argv...>` with env in the given order. `State` runs `has-session -t =<name>` first and only then `display-message -p -t =<name> '#{pane_dead}'` (copied from ccpool `internal/tmux/client.go` :199-243, with a header naming the origin and the reason). "no server running", "error connecting to" (a stale socket) and "can't find session" mean `Exists: false` (and an empty `List`), not an error. A cancelled `ctx` returns `ctx.Err()` wrapped.

- [ ] **Step 1: Write the failing tests:**
  - `tmux_test.go` (Fake runner): `TestNewSessionArgv` (exact argv including `-L pg-assistants -f <conf>`, env order kept, `--` before argv); `TestStateChecksHasSessionFirst`; `TestStatePaneDead` (`1` gives `{true,true}`); `TestStateNoServer` (both no-server messages); `TestDeadReadsStatusAndTail`; `TestListNoServerIsEmpty`; `TestKillWhenGone`; `TestCancelledContext` (scripted `CtxErr: context.Canceled`, per Global Constraints).
  - `tmux_integration_test.go` (`//go:build integration`, real tmux, socket `pg-assistants-test-<random>`, `WriteConf` into `t.TempDir()`, server killed in `t.Cleanup`):
    - `TestRealLifecycle`: start `sh -c 'echo $$ > <dir>/pid; echo "$X" > <dir>/x; exec sleep 30'` with env `X=1`; `State == {true,false}`, `PanePID` equals the pid in `<dir>/pid`, `<dir>/x` holds `1`; `Kill`; `State.Exists == false`.
    - `TestRealExitLeavesDeadPane`: start `sh -c 'echo boom >&2; exit 3'`; within 10 s `State == {true,true}`; `Dead` gives status 3 and a tail containing `boom`; a same-name `NewSession` fails; after `Kill` it succeeds. (`remain-on-exit` comes from the conf, so there is no race.)
    - `TestRealStateMissingWithAnotherAlive`: with `example-b` alive, `State("example-a").Exists == false`.
    - `TestRealExactMatchNotPrefix`: with only `example-ab` alive, `State("example-a").Exists == false` and `Kill("example-a")` leaves `example-ab` running.
    - `TestRealList`: two sessions listed; after the server is killed, an empty list and no error.
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
    Enabled       bool // false also for an orphan
    ExitRequested bool // true while the request is on file
    Meters        []Meter // only the meters with a current reading
}
type Grace struct{ Interrupt, Kill time.Duration }
// Step returns the next state, the one action to take now (or ActNone), and a
// reason naming the meter that set the target, e.g. "context at 81% (162000
// of 200000 tokens)". The caller advances the state whether or not the action
// succeeds.
func Step(s State, in Input, g Grace) (State, Action, string)
```

- Rules (spec "Ladders"): priority Exit > Disabled > Meters; the rung never moves down; at most one action per call; a change of `Kind` keeps `Rung` and `At`.
  - Exit: below `RungKill` → `ActGracefulStop`, rung `RungKill`; at `RungKill`/`RungForceKill` → `ActForceKill`, rung `RungForceKill`. `Kind` stays `KindExit` once set, even when `ExitRequested` is later false.
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
  - `TestTimeFallbackWithoutMeters`: at `RungNudge`, no meters (usage failed), `Now = At+5m` → `ActInterrupt`.
  - `TestInterruptTimeFallback`: at `RungInterrupt`, `Now = At+2m` → `ActKill`; `At+119s` → `ActNone`.
  - `TestForceKillRepeats`: `RungKill` then `RungForceKill` → `ActForceKill` both times.
  - `TestKillAboveOne`: context kill 1.05 at `RungInterrupt`, `Now = At+1m`: 1.025 → `ActNone`; 1.05 → `ActKill`.
  - `TestDisabledLadderTiming`: `ActNudge`; `+4m59s` `ActNone`; `+5m` `ActInterrupt`; `+2m` `ActKill`; next `ActForceKill`.
  - `TestDisabledBeatsMeters`: disabled with context 0.96 at `RungNone` → `ActNudge` with a disabled reason, not a context one.
  - `TestExitRequestWins`: at `RungNudge` → `ActGracefulStop`; next (request no longer set) → `ActForceKill`.
  - `TestExitBeatsDisabled`: disabled and exit requested at `RungNudge` → `ActGracefulStop`.
  - `TestKindChangeKeepsRung`: meter ladder at `RungInterrupt` fired at `T`; disabled at `T+1m` → `ActNone`; at `T+2m` → `ActKill`.
  - `TestNoMetersAtRungNone`: enabled, no meters → `ActNone`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 ./internal/ladder/`. Expected: FAIL.
- [ ] **Step 3: Implement** `Step`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the ladder engine`.

### Task 6: Control client and block tracker

**Files:**

- Create: `packages/pg-assistants/internal/control/{control.go,control_test.go,control_integration_test.go}`
- Create: `packages/pg-assistants/internal/blocks/{blocks.go,blocks_test.go}`
- Modify: `flake.nix`: `pg-claude-control` in `pg-assistants-integration-tests`' test inputs

**Interfaces:**

- Consumes: Task 2's `usage --json` output; `ladder.Meter`, `config.Blocks`.
- Produces (package `control`; calls inherit the daemon's environment, which carries the operator's session variables, Task 10):

```go
type Client struct {
    Bin string // absolute path to pg-claude-control
    Run cmdrun.Runner
}
type Usage struct {
    SessionID     string // the report's session
    HaveContext   bool
    ContextTokens int64
    ContextPct    float64 // 0 when absent
    Window        int64   // 0 when absent
    Limits        []Limit
}
type Limit struct {
    Kind       string    `json:"kind"`        // "five_hour" or "seven_day"; other kinds dropped
    Fraction   float64   `json:"fraction"`    // percentUsed / 100; readings outside 0..100 dropped
    ResetsAt   time.Time `json:"resets_at,omitzero"` // zero when absent or unparseable (RFC 3339, fractional seconds accepted)
    ObservedAt time.Time `json:"observed_at"`
}
var (
    ErrNoControl = errors.New("no control mod")    // exit 3
    ErrNoProcess = errors.New("no such process")   // exit 4
    ErrNoReply   = errors.New("no reply")          // exit 6
    ErrBusy      = errors.New("mod not accepting") // exit 9
    ErrRefused   = errors.New("refused by mod")    // exit 21
    ErrAmbiguous = errors.New("ambiguous target")  // exit 22
)
func (c Client) Usage(ctx context.Context, pid int, now time.Time) (Usage, error) // usage pid:<n> --json --timeout 5s
func (c Client) Submit(ctx context.Context, pid int, text string) error           // submit pid:<n>, text on stdin
func (c Client) Interrupt(ctx context.Context, pid int) error                     // interrupt pid:<n> --reply; a "no turn" refusal is nil
func (c Client) Stop(ctx context.Context, pid int, escalate time.Duration) error  // stop pid:<n> --escalate <d>
func (c Client) Signal(ctx context.Context, pid int) error                        // stop pid:<n> --signal
```

Errors wrap the sentinel with stderr; a cancelled `ctx` returns an error wrapping `ctx.Err()`. `Call.Timeout` per Global Constraints.

- Produces (package `blocks`):

```go
type Tracker struct{ /* mutex-guarded; no I/O under the lock */ }
func NewTracker(cfg config.Blocks) *Tracker
// Observe merges readings per kind: the latest ResetsAt wins; for an equal
// ResetsAt the higher Fraction wins; a zero ResetsAt never replaces a non-zero one.
func (t *Tracker) Observe(limits []control.Limit)
// Seed is Observe for readings from a previous status.json; expired ones are dropped.
func (t *Tracker) Seed(limits []control.Limit, now time.Time)
// Meters returns the current block meters (readings whose reset time has not
// passed), for every running session's ladder.
func (t *Tracker) Meters(now time.Time) []ladder.Meter
// Hold reports whether starts are held: any current block at or above
// cfg.Nudge holds until its ResetsAt, or until ObservedAt + cfg.UnknownResetHold
// when ResetsAt is zero.
func (t *Tracker) Hold(now time.Time) (bool, time.Time)
func (t *Tracker) Snapshot(now time.Time) []control.Limit
```

- [ ] **Step 1: Write the failing tests:**
  - `control_test.go` (Fake runner): `TestUsageParsesResult` (stdout `{"sent":true,"session":"s1","result":{"context":{"tokens":83000,"percent":41.5,"window":200000},"rateLimits":[{"kind":"five_hour","percentUsed":62,"resetsAt":"2026-11-16T20:00:00.123Z"},{"kind":"spend","percentUsed":5},{"kind":"seven_day","percentUsed":1.7e9},{"kind":"seven_day","percentUsed":-3}]}}` gives `SessionID s1`, `HaveContext`, 83000, window 200000, and only the five_hour limit at `0.62` with its time); `TestUsageNoContextYet`; `TestUsageMapsExitCodes` (3, 4, 6, 9, 21, 22 via `errors.Is`); `TestSubmitSendsStdin`; `TestInterruptArgv` (`interrupt pid:42 --reply`); `TestInterruptNoTurnIsSuccess` (exit 21 with `no turn` on stderr gives nil); `TestStopArgv` (`stop pid:42 --escalate 2m0s`, `Call.Timeout` `2m15s`); `TestSignalArgv`; `TestControlCancelledContext` (scripted, per Global Constraints).
  - `control_integration_test.go` (`//go:build integration`): `TestRealControlContract` runs the real `pg-claude-control` (path from `PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL`, set by the check) against a temp `CLAUDE_CONFIG_DIR` registry and a FIFO the test answers in-process, and checks that `Usage`, `Submit`, `Interrupt`, `Stop` and `Signal` send the argv and parse the replies and exit codes the real tool produces (a missing registry entry gives `ErrNoProcess`, a registry entry without an inbox `ErrNoControl`).
  - `blocks_test.go`: `TestMetersCarryBlockPoints` (five_hour 0.96 gives a meter named `five_hour` with points `{0.95,0.98,1.0}`); `TestExpiredReadingDiscarded` (after `ResetsAt`, no meter and no hold); `TestHoldAtNudgeLevel` (0.95 holds until `ResetsAt`; 0.94 does not); `TestHoldUnknownReset` (0.96 with zero `ResetsAt` holds until `ObservedAt` + 1h); `TestNoReadingNoHold`; `TestStaleLowerReadingDoesNotLowerHold` (0.96 then 0.70 with the same `ResetsAt` keeps 0.96; a later `ResetsAt` at 0.10 replaces it; a zero-`ResetsAt` reading does not replace either); `TestSeedDropsExpired`; `TestConcurrentObserveAndRead` (parallel `Observe`/`Meters`/`Hold` under `-race`).
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/control/ ./internal/blocks/` (and `-tags integration` for the contract test). Expected: FAIL.
- [ ] **Step 3: Implement** both packages.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the control client and the block tracker`.

### Task 7: Launch command, status board, exit requests, last exits and folder trust

**Files:**

- Create: `packages/pg-assistants/internal/supervisor/{launch.go,status.go,exitreq.go,lastexit.go,launch_test.go,status_test.go,exitreq_test.go,lastexit_test.go,launch_integration_test.go}`
- Create: `packages/pg-assistants/internal/trust/{trust.go,trust_test.go}`, copied from `packages/ccpool/internal/trust/` with its tests and a header comment naming the origin

**Interfaces:**

- Consumes: `config.Assistant`, `config.Paths`, `control.Limit`, `tmux.DeadInfo`.
- Produces:

```go
// LaunchArgv is the pane command from the spec's Launch section:
// <Shell> -l -i -c 'exec "$@"' <Shell> <Claude> --allowed-tools 'Bash(pg-assistants exit)'
//   --session-id <id> --name <Name> --model <Model> --effort <Effort> -- <Prompt>
// --allowed-tools is variadic, so it comes first and -- ends the options.
func LaunchArgv(p config.Paths, a config.Assistant, sessionID string) []string
// LaunchEnv: DISABLE_AUTO_COMPACT=1, PG_ASSISTANT_ID, PG_ASSISTANTS_STATE_DIR,
// the five blanked CLAUDE* markers, then a.Env sorted (a.Env cannot override
// any of the first eight).
func LaunchEnv(a config.Assistant, stateDir string) []string

type LastExit struct {
    Status   int     `json:"status"`
    Signal   int     `json:"signal,omitempty"`
    UptimeS  float64 `json:"uptime_s"`
}
type AssistantStatus struct {
    ID              string    `json:"id"`
    Enabled         bool      `json:"enabled"`
    Running         bool      `json:"running"`
    PID             int       `json:"pid,omitempty"`
    SessionID       string    `json:"session_id,omitempty"`
    StartedAt       time.Time `json:"started_at,omitzero"`
    ContextTokens   int64     `json:"context_tokens,omitempty"`
    ContextFraction float64   `json:"context_fraction,omitempty"`
    ContextAt       time.Time `json:"context_at,omitzero"`
    Ladder          string    `json:"ladder,omitempty"` // meter | disabled | exit
    Rung            string    `json:"rung,omitempty"`   // nudge | interrupt | kill | force-kill
    Reason          string    `json:"reason,omitempty"`
    WaitUntil       time.Time `json:"wait_until,omitzero"`
    WaitReason      string    `json:"wait_reason,omitempty"` // backoff | block_hold | missing_cwd
    Hint            string    `json:"hint,omitempty"`
    LastError       string    `json:"last_error,omitempty"`
    ConfigError     string    `json:"config_error,omitempty"`
    LastExit        *LastExit `json:"last_exit,omitempty"`
    LastTick        time.Time `json:"last_tick,omitzero"`
}
type DaemonStatus struct {
    PID              int       `json:"pid"`
    Version          string    `json:"version"`
    StartedAt        time.Time `json:"started_at"`
    ConfigPath       string    `json:"config_path"`
    ConfigHash       string    `json:"config_hash"`
    ControlStateDir  string    `json:"pg_claude_control_state_dir,omitempty"`
    ClaudeConfigDir  string    `json:"claude_config_dir,omitempty"`
    ConfigProblems   []string  `json:"config_problems,omitempty"`
}
type Status struct {
    SchemaVersion int               `json:"schema_version"` // 1
    WrittenAt     time.Time         `json:"written_at"`
    Daemon        DaemonStatus      `json:"daemon"`
    Assistants    []AssistantStatus `json:"assistants"`
    Orphans       []string          `json:"orphans,omitempty"`
    Blocks        []control.Limit   `json:"blocks"`
}
type StatusBoard struct{ /* mutex-guarded; Flush copies under the lock and writes outside it */ }
func NewStatusBoard(path string, d DaemonStatus) *StatusBoard
func (b *StatusBoard) Set(s AssistantStatus)
func (b *StatusBoard) SetOrphans(names []string)
func (b *StatusBoard) SetBlocks(l []control.Limit)
func (b *StatusBoard) Flush(now time.Time) error // atomic tmp+rename, mode 0600
func ReadStatus(path string) (Status, error)

func ValidID(id string) bool                                   // [a-z0-9-]+
func WriteExitRequest(stateDir, id string, at time.Time) error // <stateDir>/exit-requests/<id>, dir 0700
func HasExitRequest(stateDir, id string) (bool, error)         // never deletes
func ClearExitRequest(stateDir, id string) error               // nil when missing
func WriteLastExit(stateDir, id string, d tmux.DeadInfo, uptime time.Duration, at time.Time) error // <stateDir>/last-exit/<id>.txt
```

- Produces (package `trust`, from ccpool): `IsTrusted(path, cwd string) (bool, error)` and `Ensure(path string, cwds ...string) error` over `paths.claude_json` (merge-only, keeps every other key).

- [ ] **Step 1: Write the failing tests:**
  - `TestLaunchArgv`: for `Paths{Shell:"/bin/zsh", Claude:"/c/claude"}` and `{ID:"example-a", Name:"Example A", Model:"claude-sonnet-5-5", Effort:"medium", Prompt:"Resume your duties."}`, session `u1`: exactly `["/bin/zsh","-l","-i","-c","exec \"$@\"","/bin/zsh","/c/claude","--allowed-tools","Bash(pg-assistants exit)","--session-id","u1","--name","Example A","--model","claude-sonnet-5-5","--effort","medium","--","Resume your duties."]`.
  - `TestLaunchArgvVariadicFlagIsFollowedByAFlag`: the token after the `--allowed-tools` value starts with `--`, and the prompt is the only token after `--`.
  - `TestLaunchEnv`: holds `DISABLE_AUTO_COMPACT=1`, `PG_ASSISTANT_ID=example-a`, `CLAUDECODE=`; `a.Env{"PG_ASSISTANT_ID":"x","FOO":"bar"}` keeps `example-a` and adds `FOO=bar`; no `PATH` entry (the login shell sets it).
  - `TestStatusBoardFlush`: two `Set` calls, `SetOrphans` and `SetBlocks`, then `Flush`: one 0600 file; `ReadStatus` returns `schema_version 1`, the daemon object, both assistants with every field set in the test, the orphan, and the blocks with `observed_at`.
  - `TestStatusBoardConcurrent`: parallel `Set` and `Flush` under `-race`.
  - `TestExitRequestRoundTrip`: write; `HasExitRequest` true twice (it never deletes); clear; false; clear on a missing file nil; `ValidID("../x") == false`.
  - `TestWriteLastExit`: the file names the status, signal, uptime and holds the tail.
  - `trust_test.go`: ccpool's tests, plus `TestEnsureKeepsOtherKeys` and `TestIsTrustedMissingFile`.
  - `launch_integration_test.go` (`//go:build integration`): `TestRealLaunchChain` runs `LaunchArgv`/`LaunchEnv` through the real tmux client with `Shell=/bin/sh` and a fake `claude` script that records its argv, its environment and `$$`; it asserts `PanePID` equals the recorded `$$`, the prompt `Resume "now", please` arrives as one argument after `--`, `CLAUDECODE` is set and empty, and `FOO=bar` arrived. A second case runs `zsh -l -i` with a temp `ZDOTDIR` and `HOME` whose `.zshenv` exports `PATH=<dir>:$PATH` and checks the recorded `PATH` starts with `<dir>`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/supervisor/ ./internal/trust/` (and `-tags integration`). Expected: FAIL.
- [ ] **Step 3: Implement** the files.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add launch, status, exit-request, last-exit and trust helpers`.

### Task 8: Supervisor loop

**Files:**

- Create: `packages/pg-assistants/internal/supervisor/{supervisor.go,orphans.go,messages.go,supervisor_test.go,orphans_test.go}`

**Interfaces:**

- Consumes: Tasks 3-7.
- Produces:

```go
type Tmux interface {
    NewSession(ctx context.Context, name, cwd string, env, argv []string) error
    State(ctx context.Context, name string) (tmux.SessionState, error)
    PanePID(ctx context.Context, name string) (int, error)
    Dead(ctx context.Context, name string) (tmux.DeadInfo, error)
    List(ctx context.Context) ([]string, error)
    Kill(ctx context.Context, name string) error
}
type Control interface {
    Usage(ctx context.Context, pid int, now time.Time) (control.Usage, error)
    Submit(ctx context.Context, pid int, text string) error
    Interrupt(ctx context.Context, pid int) error
    Stop(ctx context.Context, pid int, escalate time.Duration) error
    Signal(ctx context.Context, pid int) error
}
type Trust interface {
    IsTrusted(cwd string) (bool, error)
    Ensure(cwd string) error
}
type Deps struct {
    Tmux    Tmux
    Control Control
    Trust   Trust
    Blocks  *blocks.Tracker // seeded by the caller from the previous status.json
    Status  *StatusBoard
    Events  *slog.Logger // a jsonllogger logger; event names: spec "Status and observability"
    // Go starts a named loop: lifecycle.Group.Go in production, an errgroup in
    // tests. A loop MUST return nil on context end (an error begins shutdown).
    Go      func(name string, fn func(ctx context.Context) error)
    NewID   func() string                          // fresh UUID v4
    Stat    func(path string) (os.FileInfo, error) // os.Stat in production
}
// Run starts, through d.Go, one loop per assistant without Problems
// ("assistant:<id>"; first tick immediately, then every a.Tick), the orphan
// sweep ("orphans", every cfg.Tick; each orphan gets "orphan:<name>") and the
// heartbeat ("heartbeat", Status.Flush every 15 s), and records config errors
// for the others in the status board. It returns once ctx is done; the caller's
// lifecycle.Group.Run waits for the loops. It never touches tmux sessions on
// shutdown.
func Run(ctx context.Context, cfg config.Config, d Deps) error
func NudgeText(reason string) string
func StopText(reason string) string
```

- Each assistant's goroutine runs `loop(ctx)`, wrapped so a panic is recovered, logged as `loop_panic` with the stack, and the loop restarted after the restart backoff (doubling per panic, shown in `LastError`). Each tick runs under `tickCtx, cancel := context.WithTimeout(loopCtx, a.Tick + cfg.Grace.Kill + 30*time.Second)`; each call inside gets its own `context.WithTimeout` (Global Constraints). Nothing in a tick waits on another assistant.
- Per tick (spec "Reconcile loop"):
  - `State`. Exists and alive: record "last seen alive". Exists but dead: `Dead`, `WriteLastExit`, `Kill`, event `exited` with status, signal and uptime, `Status.LastExit` set; treat as not running.
  - Not running: if a session was known, compute uptime as last-seen-alive minus start (0 if never seen alive), clear the ladder; if an exit request was on file, `ClearExitRequest` and restart on the next tick without touching backoff; otherwise uptime < `MinUptime` doubles the backoff from `BackoffInitial` up to `BackoffMax`, a longer uptime resets it. Start when enabled, past any backoff (`WaitReason backoff`), and `Blocks.Hold(now)` is false (`WaitReason block_hold`): `Stat(cwd)` (missing → `start_failed` logged once until it changes, `WaitReason missing_cwd`, retried next tick, backoff untouched); `Trust.IsTrusted` false → `Trust.Ensure`; `ClearExitRequest`; `NewSession`; `PanePID`.
  - A session alive at the loop's first tick is adopted (event `adopt`; start time = now; `PanePID` read; `SessionID` filled from the first successful `Usage`).
  - Running: `HasExitRequest` (sets `ExitRequested`); `Usage` (error → `usage_failed`; three consecutive `ErrNoControl`, `ErrNoProcess` or `ErrBusy` after a start set the hint from the spec; no success within `StartTimeout` of a start → `start_stalled`, `Signal`, then `Kill` on the next tick, counted as a crash); on success `Blocks.Observe` and, when `Usage.Window` is non-zero and differs from `Context.Window`, `window_mismatch` (once per session); build `Meters` (context meter when `HaveContext`, plus `Blocks.Meters(now)`); `ladder.Step` (it runs even when `Usage` failed); perform the action (`ActNudge` → `Submit(NudgeText(reason))`; `ActInterrupt` → `Interrupt`, then `Submit(StopText(reason))`, and when reached by the time fallback set the permission-prompt hint; `ActKill` → `Signal`; `ActForceKill` → `Tmux.Kill`; `ActGracefulStop` → `Stop(GraceKill)`); a failed action logs `action_failed` and the rung still advances. The exit request is cleared only when the session is seen gone.
  - Every tick: `Status.Set` (with `LastTick`), `Status.SetBlocks(Blocks.Snapshot(now))`.
- `orphans.go`: every `cfg.Tick`, `Tmux.List`; each name that is not a valid assistant (unknown id, or an assistant with `Problems`) and has no orphan goroutine gets one, which runs the disabled ladder (`Enabled: false`, no meters) through the same per-call timeouts until the session is gone, logging `orphan` once; `Status.SetOrphans`. Orphan goroutines follow the same panic and cancellation rules.
- `messages.go`: neither text starts with `/`. Both name the reason; `NudgeText` asks the assistant to finish its current step, write its notes, run `pg-assistants exit` as a command on its own (not chained) as its last action and end its turn; `StopText` says to stop now and do the same. Both say never to wait for input.
- Events are `jsonllogger` records (the message is the event name) with the keys from the spec (`assistant`, `session_id`, `pid`, `reason`, `error`, `uptime_s`, and extras per event). Tests read them with `jsonlloggertest.New` and `Buffer.Records`.

- [ ] **Step 1: Write the failing tests** (`synctest.Test`, hand-written recording fakes; assistant `example-a`, tick 1m, context points `{0.8,0.95,1.0}`, limit 200000; `TestMain` runs `goleak.VerifyTestMain`):
  - `TestStartsEnabledAssistant` (one `NewSession` with `LaunchEnv`/`LaunchArgv` output); `TestDoesNotStartDisabled`.
  - `TestAdoptsRunningSessionOnStart`: alive at the first tick → no `NewSession`, event `adopt`, `PanePID` read and the next `Usage` targets that pid, `SessionID` filled from it.
  - `TestContextLadderSequence`: usage 160000 → `Submit(NudgeText)`; 195000 → `Interrupt` then `Submit(StopText)`; 200000 → `Signal`; still alive → `Kill`; still alive → `Kill` again.
  - `TestNudgedIdleSessionIsRecycled`: 165000 every tick → nudge, `Interrupt` 5m later (permission-prompt hint set), `Signal` 2m after that.
  - `TestTimeFallbackWhileUsageFails`: nudged, then `Usage` fails every tick → `Interrupt` at +5m and `Signal` at +7m anyway.
  - `TestBlockLadderAndHold`: two running assistants; five_hour 0.95 resetting in 3h → both nudged; 0.98 → both interrupted; sessions exit → no start until the reset (`WaitReason block_hold`); one tick after the reset both start.
  - `TestHoldSurvivesDaemonRestart`: a tracker seeded with five_hour 0.96 resetting in 1h and no session running → no start until the reset.
  - `TestDisabledLadderWithoutControlMod`: `Usage` → `ErrNoControl`, disabled → failed nudge logged as `action_failed`, `Interrupt` at +5m, `Signal` at +7m, `Kill` at +8m.
  - `TestHintOnNoControlProcessOrBusy`: three ticks of each of `ErrNoControl`, `ErrNoProcess`, `ErrBusy` after a start set the hint naming `pg-assistants attach example-a`.
  - `TestStartStallIsKilledAndBacksOff`: `Usage` never succeeds → at 5m `start_stalled`, `Signal`, then `Kill`; the next start waits for the doubled backoff.
  - `TestExitRequestStopsGracefully`: a session alive for 30 s (under `min_uptime`) requests exit → `Stop(pid, 2m)`; the file stays until the session is gone, then is cleared; the next tick starts a new session; backoff unchanged.
  - `TestExitRequestSurvivesCancelledStop`: cancel the root context while in `Stop` → the request file is still there.
  - `TestAdoptedSessionWithPendingExitRequestIsStopped`: request on file at adoption → `Stop` on the first tick.
  - `TestExitRequestWithoutModForceKills`: `Stop` returns `ErrNoControl` → the next tick, session still alive, `Kill`.
  - `TestLeftoverExitRequestClearedBeforeStart`.
  - `TestManualKillResetsLadder`: at `RungInterrupt` the session vanishes → the next session begins at `RungNone`.
  - `TestCrashBeforeFirstTickBacksOff`: gone at the first tick after each start → restarts after 1m, 2m, 4m; a session seen alive for 10m resets to 1m; never beyond 30m.
  - `TestDeadPaneRecordsLastExit`: `State {true,true}`, `Dead` status 3 with tail `boom` → `WriteLastExit`, `Kill`, then `NewSession` on the next eligible tick; the `exited` event and `Status.LastExit` carry status 3.
  - `TestMissingCwdRetriesEveryTick`: `Stat` error → one `start_failed`, `WaitReason missing_cwd`, retried every tick without growing the backoff; it starts on the first tick after the directory appears; a second assistant still starts.
  - `TestUntrustedCwdIsTrustedBeforeStart`: `IsTrusted` false → `Ensure` before `NewSession`.
  - `TestAssistantWithProblemsIsNotStarted`: an assistant with `Problems` gets no loop and its status shows `ConfigError`; the other assistant starts.
  - `TestOrphanSessionGetsDisabledLadder`: `List` returns `example-gone` → event `orphan`, `Status.Orphans`, nudge, then interrupt at +5m, signal at +7m, kill at +8m; the goroutine returns once the session is gone.
  - `TestProblemAssistantSessionGetsDisabledLadder`: a running session of an assistant with `Problems` is handled as an orphan.
  - `TestSlowAssistantDoesNotDelayOthers`: assistant A's `Usage` blocks until its context is done; assistant B (tick 1m) still ticks 5 times in 5 minutes, and A's call sees a 15 s deadline and returns with a deadline error.
  - `TestHeartbeatFlushesWhileLoopBlockedInStop`: A blocked in `Stop` for 2m → `written_at` advances every 15 s and B keeps ticking.
  - `TestPanicInOneLoopIsContained`: A's fake `State` panics twice → `loop_panic` logged twice, A's loop restarts after 1m then 2m, `LastError` names the panic, B unaffected.
  - `TestCancelStopsInFlightCalls`: cancel the root context while A is in `Stop` → `Run` and every loop return within 1 s, the fake saw a cancelled context, no `Kill` was called, no goroutine leaked.
  - `TestLoopsAreNamed`: the `Go` fake records `assistant:example-a`, `orphans`, `heartbeat`, and `orphan:example-gone` once that orphan appears.
  - `TestPerAssistantTicks`: 30s and 1m assistants tick 4 and 2 times in 2 minutes.
  - `TestWindowMismatchLogged`: `Usage.Window` 1000000 against a configured 200000 → one `window_mismatch`.
  - `TestEventShape`: an `exited` event decodes with `event`, `assistant`, `session_id`, `pid`, `uptime_s`.
  - `TestNudgeTextNamesExitAndReason` and `TestMessagesNeverStartWithSlash`.
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 -race ./internal/supervisor/`. Expected: FAIL.
- [ ] **Step 3: Implement** `supervisor.go`, `orphans.go` and `messages.go`.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the supervisor loop`.

### Task 9: CLI subcommands `daemon`, `status`, `attach`, `exit`, `trust`

**Files:**

- Modify: `packages/pg-assistants/cmd/pg-assistants/main.go`
- Create: `packages/pg-assistants/cmd/pg-assistants/{daemon.go,status.go,attach.go,exit.go,trust.go}` with tests
- Create: `packages/pg-assistants/completions/{_pg-assistants,pg-assistants.bash}`, `share/tldr/pages.common/pg-assistants.md`
- Modify: `packages/pg-assistants/default.nix`: `postInstall` copies them (pg-keep-awake pattern)

**Interfaces:**

- Consumes: `config.Load`, `supervisor.Run`, `tmux.Client`, `control.Client`, `blocks.NewTracker`, `NewStatusBoard`, `ReadStatus`, `WriteExitRequest`, `ValidID`, `trust`.
- Produces:
  - `pg-assistants daemon --config <path> [--shutdown-deadline 8s]` (`lifecycle.DeadlineFlag`): `config.Load` (exit 7 only on its error); create `Paths.StateDir` 0700 before opening anything in it; `tmux.Client.WriteConf`; `events.jsonl` through `jsonllogger.OpenRotatingFile` with `maxBytes` 10 MiB (one `.1` backup); each `Config.Problems` entry logged as `config_problem` and put in `DaemonStatus.ConfigProblems`; `DaemonStatus` filled (pid, `Version`, config path and its SHA-256, the effective `PG_CLAUDE_CONTROL_STATE_DIR` and `CLAUDE_CONFIG_DIR`, both also logged at start); the tracker seeded from the previous `status.json` (`ReadStatus`; a missing or unreadable file seeds nothing); pprof through `pprofserver.StartFromEnv` when `PG_ASSISTANTS_PPROF_ADDR` is set; then `g := lifecycle.New(ctx, lifecycle.Options{Deadline: *deadline, ExitCode: exitcode.ShutdownOverrun, Log: <the events logger>})`, `supervisor.Run(g.Context(), cfg, Deps{Go: g.Go, ...})`, `g.SetPhase` at each shutdown step (`stop_loops`, `flush_status`), and `g.Run()`. The shutdown events are `lifecycle`'s own; the daemon writes no others.
  - `pg-assistants status [--json] [--check] [--config <path>]` (default `<osdirs.ConfigDir("pg-assistants")>/config.toml`): reads `<state_dir>/status.json`. Without `--json`: a header (`daemon pid <n>, up <age>, config <hash prefix>`, any config problems, one line per block such as `five_hour 96% (starts held until 20:00)`), then the table `ID ENABLED RUNNING CONTEXT LADDER WAIT NOTE` (`CONTEXT` with the age of the reading when older than two ticks; `WAIT` the time and reason; `NOTE` = config error, hint, last error, last exit, or `stalled` when `last_tick` is older than `tick + grace.kill + 45s`), then orphan rows with `not in config`. `--json` prints the file. It exits 0; a stale (over one minute) or missing file prints a warning on stderr naming `launchctl print gui/$UID/com.phillipg.pg-assistants-daemon` and the launchd stderr path. `--check`: exit 3 when the file is missing or stale, 5 when degraded (any config error or problem, hint, stalled loop, `WaitReason backoff`, or orphan), 0 otherwise, printing one line.
  - `pg-assistants attach <id> [--config <path>]`: validates the id, then execs `<paths.tmux> -L pg-assistants -f <conf> attach -t =<id>` with `TMUX` removed from the environment; an id with no session exits 4 (`exitcode.NotFound`) naming `pg-assistants status`; no tmux server exits 3 (`Unavailable`).
  - `pg-assistants exit`: needs `PG_ASSISTANT_ID` (valid per `ValidID`) and `PG_ASSISTANTS_STATE_DIR`; writes the request; prints `exit requested for <id>; end your turn now`. A missing or invalid id exits 2 with `PG_ASSISTANT_ID is not set: run this from inside a pg-assistants session` (or `... is not a valid id`).
  - `pg-assistants trust --claude-json <path> <cwd>...`: `trust.Ensure`; exit 0, or 1 printing the error.

- [ ] **Step 1: Write the failing tests** (in-process run functions): `TestExitWritesRequest`; `TestExitWithoutAssistantID` (exit 2, nothing written); `TestExitRejectsBadID` (`../x`); `TestStatusTable` (a config-error row, an orphan row, a stalled row, the block header); `TestStatusShowsHoldReason` (`block_hold` in `WAIT`); `TestStatusStaleWarnsAndExitsZero`; `TestStatusMissingNamesLaunchdLog`; `TestStatusCheckExitCodes` (missing 3, stale 3, degraded 5, healthy 0); `TestAttachArgvAndEnv` (exec captured through a seam: exact argv with `=example-a`, no `TMUX`); `TestTrustWritesClaudeJSON`; `TestDaemonRejectsUnparseableConfig` (exit 7); `TestDaemonRunsWithOneBadAssistant` (starts, supervises the good one, through an internal constructor with fake deps); `TestGlobalConfigProblemLogged` (`config_problem` event and `DaemonStatus.ConfigProblems`); `TestDaemonCreatesPrivateStateDir` (0700, `tmux.conf` written); `TestDaemonSeedsBlocksFromStatus`; `TestDaemonStopsOnContextCancel` (returns within the shutdown deadline, no `Kill`; the events log, read with `jsonlloggertest`, holds `lifecycle`'s `shutdown_signal` and `shutdown_complete` and a `loop_stopped` for `assistant:example-a`); `TestAttachExitCodes` (no session 4, no server 3).
- [ ] **Step 2: Run** `nice -n 10 go test -p 4 ./cmd/...`. Expected: FAIL.
- [ ] **Step 3: Implement** the subcommands, completions and the tldr page (examples: `status`, `status --check`, `attach <id>`, `exit`, `config check --config`).
- [ ] **Step 4: Run** the tests and `nix build .#pg-assistants .#checks.aarch64-darwin.pg-assistants-go-tests -L` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the daemon, status, attach, exit and trust subcommands`.

### Task 10: Home-manager module, launchd agent, ADR and docs

**Files:**

- Create: `home/programs/pg-assistants/default.nix` (model: `home/programs/pg-keep-awake/default.nix`)
- Create: `tests/pg-assistants-hm-launchd.nix`; register `checks.<system>.test-pg-assistants-hm-launchd` beside `test-pg-keep-awake-hm-launchd` (:19866)
- Modify: `home/default.nix` import list (:40)
- Create: `docs/adr/0128-pg-assistants-minimal-supervisor-for-permanent-assistants.md`; modify `docs/adr/index.md`
- Modify: `packages/pg-assistants/README.md`, repo `README.md` module list, and a short `CLAUDE.md` section beside the other HM-scoped agents

**Interfaces:**

- Options under `phillipgreenii.programs.pg-assistants`: `enable`, `package`, `pprof.enable` (default true), `tick`, `grace.{interrupt,kill}`, `blocks.{nudge,interrupt,kill,unknownResetHold}`, `restart.{minUptime,backoffInitial,backoffMax,startTimeout}`, `claudePath` (default `"${pkgs.llm-agentsPkgs.claude-code}/bin/claude"`, the package `home/programs/claude-code` installs), `loginShell` (default `"/bin/zsh"`; the operator's login shell, whose startup files home-manager must manage for the session variables to arrive), `claudeJson` (default `"${CLAUDE_CONFIG_DIR}/.claude.json"` when `home.sessionVariables.CLAUDE_CONFIG_DIR` is set, else `"${config.home.homeDirectory}/.claude.json"`), and `assistants.<id>` submodules (`name`, `enabled` default true, `model`, `effort`, `cwd`, `prompt`, `tick` nullable, `env`, `context.{limit,window,nudge,interrupt,kill}`, `window` nullable).
- Renders `config.toml` with `pkgs.formats.toml` to `xdg.configFile."pg-assistants/config.toml"`, filling `[paths]`: tmux and pg-claude-control store paths, `claudePath`, `shell = loginShell`, `state_dir = ${config.xdg.stateHome}/pg-assistants`, `claude_json = claudeJson`; null options are omitted. Checked at build time by running `pg-assistants config check --config` in a `runCommand` (strict: any problem fails the build); `internal.configUnchecked` exposes the unchecked rendering for the negative test (the pg-keep-awake precedent, `tests/pg-keep-awake-hm-launchd.nix:351-353`).
- `home.packages = [ cfg.package ]` and `programs.tldr.customPages` (as pg-keep-awake :240-245).
- `home.activation.pgAssistantsTrust` (after `writeBoundary`, best effort): `$DRY_RUN_CMD <pkg>/bin/pg-assistants trust --claude-json <claudeJson> <every cwd> || true`, the shape of ccpool's activation (`home/programs/ccpool/default.nix:69-72`).
- launchd entry `pg-assistants-daemon`, label `com.phillipg.pg-assistants-daemon`, `runAtLoad`, `keepAlive`, `serviceConfig.AbandonProcessGroup = true`, `ExitTimeOut` = 8 s deadline + 7 s = 15, `StandardOutPath`/`StandardErrorPath` under `${config.xdg.stateHome}/pg-assistants-launchd` (outside the 0700 state dir), `EnvironmentVariables` = `HOME`, `PG_ASSISTANTS_CONFIG_HASH`, and `PG_ASSISTANTS_PPROF_ADDR = ${config.xdg.stateHome}/pg-assistants/pprof.sock` when `pprof.enable`. `script` = `exec ${loginShell} -l -c 'exec "$@"' ${loginShell} <pkg>/bin/pg-assistants daemon --config <rendered> --shutdown-deadline 8s`, so the daemon and every `pg-claude-control` call it makes see the operator's session variables.
- Assertions: `phillipgreenii.programs.pg-claude-control.enable` (gated on the option being declared, as the recorder module does); `claude-code.marketplaces.overrides."pg-claude-control"` is not `false` (gated likewise); every `cwd` and `loginShell` absolute. Linux: no entry, one warning.

- [ ] **Step 1: Write the failing eval test** `tests/pg-assistants-hm-launchd.nix` with `tests/launchd-registry-stub.nix` and the stubs this module needs: `assertions`, `warnings`, `home.homeDirectory`, `home.file`, `home.packages`, `home.activation` with `lib.hm.dag` (precedent `tests/tmp-sweep.nix:24-25,83`), `home.sessionVariables`, `xdg.configFile`, `xdg.stateHome` and `programs.tldr`. It covers:
  - the darwin entry (label, `AbandonProcessGroup`, `ExitTimeOut == 15`, the env keys including the pprof socket path, log paths outside the state dir);
  - the rendered TOML for two assistants, one with `tick = null` and no `window` (omitted keys), `[paths]` filled including `claude_json`, block defaults 0.95/0.98/1.00;
  - `PG_ASSISTANTS_CONFIG_HASH` differs when one assistant's `prompt` changes and when an assistant is added;
  - the build-time check rejects bad renderings: `configUnchecked` with effort `turbo`, with `limit*kill` over the window, and with a duplicate `cwd` each fail `config check` in a check script;
  - `home.packages` contains the package; the trust activation runs `pg-assistants trust` with every `cwd`;
  - each assertion fires with its message (pg-claude-control disabled, the marketplace override false, a relative `cwd`, a relative `loginShell`); the non-darwin warning;
  - the rendered `script` run under `env -i` with `loginShell` set to a fake shell that records its argv and then execs the rest, against a fake `pg-assistants` that records its own argv.
- [ ] **Step 2: Run** `nix build .#checks.aarch64-darwin.test-pg-assistants-hm-launchd -L` (background). Expected: FAIL.
- [ ] **Step 3: Implement** the module; write ADR 0128 (context; decision: a separate minimal daemon, ccpool not used as a dependency, two pieces copied; consequences: chain-of-sessions model, auto-compact off, `limit*kill` below the window, per-assistant isolation, sessions run through the operator's login shell, folder trust written by `pg-assistants trust`, `tmux new-session -c` falling back to `$HOME` for a missing directory) and the docs. The package README covers usage, the config reference, `status` and `status --check`, attaching and detaching (`C-b d`), removing an assistant and uninstalling (from the spec's "Packaging"), and a copy-paste `AGENTS.md` block: keep notes in the directory; when nudged or done, write notes, then run `pg-assistants exit` as a command on its own as the last action and end the turn; never wait for input, nobody is attached; an assistant that should poll less often gets a longer `tick`.
- [ ] **Step 4: Run** the eval test, `pg-assistants-go-tests`, and `pn workspace flake-check --no-build <worktree path>` (background). Expected: PASS.
- [ ] **Step 5: Commit** `pg-assistants: add the home-manager module, launchd agent and ADR 0128`.

### After landing (not a task)

What tests cannot prove is the behavior on this machine after `pn workspace apply`. File a verification bead gated on the apply (`pb:pb-gate-lifecycle`) that enables one throwaway assistant with a small `context.limit` (for example 30000) and checks: it starts and `status` shows it; inside the session `echo $PATH` matches the operator's terminal and `pg-assistants` resolves; `usage` returns live figures, including a `seven_day` reading if the engine reports one; a full nudge, exit request and restart cycle in the events log; that `launchctl kickstart -k gui/$UID/com.phillipg.pg-assistants-daemon` leaves the session running and adopted; that interrupt-then-submit lands as a new turn on a live session; and that an assistant with a model id claude rejects (so it exits at once) leaves a readable `last-exit` file and backs off. It cites the Task 8 tests as the logic evidence.
