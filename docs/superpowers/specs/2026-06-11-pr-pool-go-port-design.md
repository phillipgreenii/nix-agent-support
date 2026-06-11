# pr-pool Go port — design

**Status:** Draft (2026-06-11; revised after adversarial spec review)
**Bead:** `pg2-spgx`
**Builds on:**

- `docs/superpowers/specs/2026-06-11-pr-pool-worker-contract-design.md` (the
  current worker contract — the Go port preserves its semantics exactly).
- `docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md` (the
  two-role triager the Go port replaces).
- ccpool (`packages/ccpool/`) — the Go session manager pr-pool will delegate to.

**Related beads:** `pg2-y991` (B: budget watchdog, blocked-by this) ·
`pg2-01ys` (parking lot: generalize session-state/metadata into ccpool —
explicitly out of scope here).

## Summary

Rewrite the `pr-pool.sh` bash orchestrator as a Go application that **delegates
all claude+tmux mechanics to ccpool** and keeps only orchestration. Phase 1 is a
**one-for-one capability port** of the current bash triager — identical
discovery, role model, per-role caps, and completion/failure semantics — built
with the workspace's established Go conventions (`mkGoApp`, the ccpool package
shape, dependency injection, reuse of existing client patterns). The bash
implementation is **removed within this chunk**, once the Go version is
live-verified.

The single most important structural decision: **all ccpool interaction goes
through one injectable interface** (`ccpool.Runner`). Phase 1 implements it by
shelling out to the `ccpool` CLI (**Option 1**); a future move to in-process
library use (**Option 2**) swaps only that interface's implementation, leaving
the orchestration untouched.

## Motivation

`pr-pool.sh` is bash-3.2, hand-rolls tmux/claude session management, and is the
wrong substrate for the next chunk (B: a per-session token+time budget watchdog —
token observation and a watchdog loop are awkward in bash). Meanwhile ccpool (Go)
already owns claude+tmux session lifecycle robustly (`new`/`reply`/`cancel`/
`close`/`list`, a session-state machine, generation-based wait, a verified
cancel). Consolidating pr-pool onto Go and delegating session mechanics to ccpool
removes the duplicated tmux code, fits the workspace's Go tooling, and sets up B.

## Decisions (settled in brainstorming + spec review)

1. **Option 1 (CLI shell-out) now, Option 2 (library) later** — isolated behind
   one `ccpool.Runner` interface so the later swap is contained.
2. **One-for-one capability port** — behavior identical to the current bash
   (post-worker-contract); Go best practices may restructure internals, and small
   changes that set up B / the ccpool integration are allowed, but no new
   orchestration behavior.
3. **Bash retired inside this chunk** — build Go → live-verify → delete
   `pr-pool.sh` + `scripts/tests/pr-pool.bats` + the one flake check that runs it,
   before B.
4. **Fresh session per bead, named for the bead** (key model change — resolves
   three ccpool gaps at once). Instead of the bash's durable role session + per-
   bead `/rename` + `/clear`-between-beads, the Go port uses one ccpool session
   **per bead**, named `pr-pool-<role>-<beadid>` (e.g. `pr-pool-worker-zr-lweh.2`):
   `Ensure` → `Send` → poll → `Close` per bead. This gives fresh context per bead
   (no `/clear` primitive needed), a legible per-bead name (no `/rename` needed),
   and env applied on every launch (see enhancement 1). At the current per-role
   cap of 1 this is **behaviorally identical** to the bash (one bead per role per
   pass, session torn down at pass end). At a future cap > 1 it composes cleanly
   (distinct session names, distinct contexts).
5. **`Send` is async (`ModeNoWait`); completion is bead-based; ccpool state is
   liveness-only.** pr-pool sends the nudge non-blocking, then polls the bead
   status (`done` = bead left `in_progress`, `seen_claimed`-guarded) up to
   `MAX_WAIT`, consulting `ccpool list` state only for liveness. It does **not**
   block on `ccpool reply` (which would deadlock a multi-turn worker against
   ccpool's per-turn `wait.timeout`).
6. **bd access is a local client** — copy ccpool/pg-pr's `Runner`/`CLIRunner`
   pattern into `internal/beads` (no cross-module `replace` on the heavy pg-pr
   module). The runner's `Dir`/`Env` carry the env-scrub (replacing the bash's
   top-level `unset BEADS_DIR WORKSPACE_ROOT`).
7. **ccpool needs three enhancements** (we own ccpool): per-session env, JSON
   listing, and claude launch-flag passthrough (see ccpool enhancements). The
   third is required for correctness, not nice-to-have.
8. **Module path** `github.com/phillipgreenii/pr-pool` (ccpool's short style).
9. **Completion stays bead-based; generalizing state/metadata into ccpool is
   parked** in `pg2-01ys`.

## Architecture & package layout

A new Go package `packages/pr-pool/`, modeled on ccpool, built via `mkGoApp`:

```
packages/pr-pool/
  go.mod / go.sum            module github.com/phillipgreenii/pr-pool
  default.nix                mkGoApp { pname="pr-pool"; ... } ; wrapProgram PATH += [ ccpool bd pg-pr ]
  update-deps.sh             copied from ccpool/pg-pr
  cmd/pr-pool/
    main.go                  stdlib flag + manual subcommand dispatch (ccpool style)
    drain.go                 the default subcommand (one drain pass)
  internal/
    config/                  TOML + XDG defaults() (roles, caps, skill paths, WORKTREE_DIR, timeouts, sentinels)
    roles/                   the role registry (Role struct: actor/skill/nudge/convo-name/cap)
    beads/                   local bd CLI runner (Run(ctx, args...) (stdout, err)); Dir=REPO_ROOT + scrubbed Env
    discover/                bd ready per role -> []Dispatch{Role, BeadID}
    complete/                role-aware completion: done-signal / fail-action (human|unclaim)
    ccpool/                  the Runner interface + CLI implementation (Option-1)
    orchestrator/            drain_once: per-role bounded drain + teardown-all
```

- `internal/` is private; no `pkg/` yet (nothing external imports pr-pool).
- `pg-pr` is on the wrapped PATH so `precheck`/`resolve_self` can shell
  `pg-pr config show --json` (it stays a shell-out, not a library call).

## The `ccpool.Runner` interface (the anti-corner-painting boundary)

All session mechanics flow through this one interface. Phase-1 shells out to the
`ccpool` CLI; the methods are shaped so an Option-2 in-process impl (wrapping
ccpool's `session.Service`) is a drop-in replacement.

```go
type SessionState string // store states: starting|ready|working|needs_input|done|failed

type Session struct {
    Name           string
    State          SessionState // ccpool store state
    Live           bool         // tmux has-session (derived live-vs-store; NOT a store state)
    TranscriptPath string       // for B's token observation
}

type SendMode int
const ( ModeNoWait SendMode = iota; ModeInterrupt; ModeQueue )

type Runner interface {
    // Ensure creates a fresh named session in cwd with env + launch flags applied,
    // returns when ready. Maps to: ccpool new <name> --cwd --env... --claude-flag... [--model]
    Ensure(ctx context.Context, name, cwd string, env map[string]string) error
    // Send delivers a prompt (async for the orchestrator). Maps to: ccpool reply <name> <prompt> --no-wait
    Send(ctx context.Context, name, prompt string, mode SendMode) error
    // Cancel interrupts the current turn (B uses this at 90/100%). Maps to: ccpool cancel <name>
    Cancel(ctx context.Context, name string) error
    // Close gracefully exits + tears down. Maps to: ccpool close <name>
    Close(ctx context.Context, name string) error
    // List returns all sessions + state + liveness (+ transcript path). Maps to: ccpool list --all --json
    List(ctx context.Context) ([]Session, error)
}
```

The CLI impl (`internal/ccpool/cli.go`) wraps `exec.Command("ccpool", …)` behind
an injectable `run func(...) ([]byte,error)` exactly like ccpool's `tmux.Client` /
pg-pr's `beads.Runner` — tests inject a fake, zero real processes.

## ccpool enhancements (three; in scope, since we own ccpool)

1. **Per-session env injection.** Surface a `--env KEY=VAL` (repeatable) on
   `ccpool new`, threaded through `session.Service.Ensure` →
   `launchAndWait` → `tmux.NewSession` (which already accepts an env map). **Not
   CLI-only:** `Ensure`'s signature and the currently-hardcoded env map
   (`{CCPOOL_NAME,CCPOOL_UUID,PA_MONITOR_NO_NUDGE}`) must take caller env and
   merge. Env applies at launch; the fresh-session-per-bead model (decision 4)
   means every bead gets a fresh launch, so create-only application is sufficient.
2. **`ccpool list --all --json`.** Structured output exposing `state`,
   **liveness separately** (the live-vs-store reconciliation `list` already
   renders as a column), and `transcript_path`. `--all` so a reaped/retention-
   hidden row can't read as `absent` mid-flight.
3. **Claude launch-flag passthrough (REQUIRED — correctness, not polish).**
   ccpool's `launch.BuildNew/BuildResume` emit only
   `--session-id/--name/--plugin-dir/--model`; the bash launches `claude
--dangerously-skip-permissions --effort max`. The autonomous worker **needs
   `--dangerously-skip-permissions`** (it does unattended git/commits — without it
   it stalls on the first tool prompt) and the contract assumes max effort. Add a
   way to pass these (a `claude.extra_flags`/`claude.effort`/`claude.dangerous`
   config block, or per-`new` flags). Without this the Go port dispatches a
   non-functional worker.

## Session model under ccpool (decision 4, expanded)

Per dispatched bead: `Ensure(name="pr-pool-<role>-<beadid>", cwd=REPO_ROOT,
env={BEADS_ACTOR,BEADS_DIR,WORKSPACE_ROOT})` → `Send(name, nudge, ModeNoWait)` →
`wait_done` (poll bead) → `Close(name)`. Consequences vs the bash:

- **`/clear` (context reset between beads)** — gone; each bead is a fresh
  session, so context never carries over. (cap > 1 future: still fine — distinct
  sessions; no `/clear` primitive ever needed.)
- **`claude_rename` / `/rename`** — gone; the session name _is_ the per-bead
  label, legible in `ccpool list`.
- **`wait_ready`** (pane-glyph poll) — gone; ccpool's `Ensure` blocks on its
  store-generation readiness internally.
- **durable role session names** (`PR FEEDBACK PROCESSOR`/`WORKER`, `-L pgpool`)
  — gone; sessions live on ccpool's socket (`cc-` prefix) named per bead. This is
  a deliberate behavioral change (monitoring keys on the new names).

## One-for-one capability map (bash → Go)

| `pr-pool.sh` unit                                                        | Go home                                                      | Notes                                                                                                           |
| ------------------------------------------------------------------------ | ------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------- |
| `discover_feedback`/`discover_worker`/`discover`                         | `internal/discover`                                          | `bd ready` per role; worker route `--label worker-ready --exclude-label human`; emits `[]Dispatch{Role,BeadID}` |
| `role_*` resolvers                                                       | `internal/roles`                                             | `Role` struct + registry; per-role cap                                                                          |
| `cycle_label`/`worker_label`                                             | `internal/roles`                                             | parent-walk via `bd show --json` → used as the ccpool **session name**                                          |
| `nudge_text_feedback`/`nudge_text_worker`                                | `internal/roles` templates                                   | identical text; worker nudge has a B-seam for a budget line                                                     |
| `ensure_session`                                                         | `ccpool.Runner.Ensure`                                       | env injected (enhancement 1); launch flags (enhancement 3); **no tmux**                                         |
| `send_nudge`/`submit_line`                                               | `ccpool.Runner.Send` (ModeNoWait)                            | ccpool owns paste+Enter                                                                                         |
| `claude_rename`                                                          | — (dropped)                                                  | session name encodes the bead (decision 4)                                                                      |
| `wait_ready`                                                             | — (subsumed)                                                 | ccpool `Ensure` store-generation readiness                                                                      |
| `clear_context`                                                          | — (dropped)                                                  | fresh session per bead (decision 4)                                                                             |
| `teardown_session`/`teardown_all`                                        | `ccpool.Runner.Close` + `orchestrator`                       | close every role's per-bead session at pass end                                                                 |
| `pane_alive`                                                             | `ccpool.Runner.List` liveness                                | re-check-after-death preserved (see completion)                                                                 |
| `done_signal`/`wait_done`/`wait_done_fail`/`bead_status`                 | `internal/complete`                                          | bead-based; `human` on failure (worker, never unclaim) / unclaim (feedback)                                     |
| `unclaim`/`mark_human`                                                   | `internal/complete` via `beads` runner                       | `bd update --status=open --assignee=""` / `--add-label human`                                                   |
| `gated` (quota/cicd sentinels)                                           | `cmd/pr-pool` + `config`                                     | ported                                                                                                          |
| `precheck`/`resolve_self`                                                | `cmd/pr-pool` (shells `pg-pr config show --json`) + `config` | stays a shell-out; asserts `.beads` prefix                                                                      |
| top-level `unset BEADS_DIR WORKSPACE_ROOT`                               | `internal/beads` runner `Dir`/`Env`                          | scrubbed env so pr-pool's own bd resolves the right store                                                       |
| `REPO_ROOT`/`WORKTREE_DIR`/`MAX_WAIT`/`POLL_INTERVAL`/actors/skills/caps | `internal/config`                                            | TOML/XDG + env; `WORKTREE_DIR` interpolated into the worker nudge                                               |
| `LOG_DIR` + `log`                                                        | `internal/config` + `slog` (or stderr)                       | logging story: structured to a log file under XDG                                                               |

## Completion logic (role-aware), restated for Go

`wait_done(role, beadID, sessionName)`:

- **done** = `complete.DoneSignal(role, beadStatus, seenClaimed)`: feedback →
  `closed`; worker → `closed`, or (`seenClaimed` ∧ `open`). `seenClaimed` set once
  the bead is observed `in_progress`.
- **liveness** = `ccpool.List` for this session: only **not-`Live`** (or store
  `failed`) counts as death. ccpool store `done`/`needs_input` (a finished or
  paused _turn_) is **normal multi-turn operation**, NOT a bead failure.
- **re-check-after-death (must preserve):** on detecting death, re-read the bead
  status once more before failing — a bead that closed in the same instant the
  session ended is a success, not a failure (carries the bash's `pr-pool.sh:316`
  ordering + the "pane dies as cycle closes" bats cases).
- **failure action**: worker → add `human`, never unclaim; feedback → unclaim.

## B-seams (no B logic in this chunk)

`ccpool.Runner.List()` surfaces `TranscriptPath` (B's token reader) and `Cancel()`
is present (B's 90/100% cancels); the worker nudge template has a budget-line
interpolation point; the per-bead drive loop in `orchestrator` is where a
watchdog goroutine attaches. Shaped only — not implemented.

## Testing

- **Go table-driven tests** with injected fakes (`ccpool.Runner`, `beads` runner)
  — no live processes. Port every current bats scenario: discover routing
  (feedback ownership; worker `--label`/`--exclude-label`), role caps
  (no starvation, cap=0 skip), `done_signal` (close / hand-back assignee-cleared /
  not-done-while-`in_progress`), `wait_done_fail` (`human`, no unclaim),
  re-check-after-death, drain + teardown-all, session-name-per-bead.
- ccpool's three enhancements get ccpool-side tests (argv assertions like its
  `tmux/client_test.go`; `--env`, `--json` shape, launch-flag passthrough).
- All under `nix flake check` (gofmt + golangci-lint + `go test`).

## Verification & bash retirement (close-out of this chunk)

1. `nix flake check` green (new pr-pool Go checks + ccpool checks).
2. **Live smoke** via the Go `pr-pool`: re-run a real worker pass against the
   `zr` store (same shape as the `zr-lweh.*` run) — confirm ccpool dispatch with
   `--dangerously-skip-permissions`/`--effort max`, env injection, completion via
   bead status, `human` on a forced failure, clean teardown.
3. **Remove the bash (exact unwiring — verified):**
   - delete `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
   - delete `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
   - remove the `test-pgii-pack-pr-support-bats` flake check (`flake.nix:488-501`)
     — the **only** nix wiring that references it. The
     `check-pgii-pack-pr-support-layout` block does **not** assert pr-pool.sh
     exists, so it needs no edit.

## Out of scope

- **B** (budget/time watchdog) — `pg2-y991`.
- **C** (work-breakdown skill) — `pg2-96gj`.
- **Option 2** (pr-pool importing ccpool as a library) — the `ccpool.Runner`
  interface is the seam.
- **Generalizing session-state/metadata into ccpool** — `pg2-01ys`.
- Worker push, N>1 per-role caps, epic/PR session-name gluing (prior deferrals).

## Open questions (resolve in implementation)

- **`ccpool list --json` exact schema** — confirm field names when implementing;
  the `Session` struct mirrors the store columns + the live column.
- **Implementation branch base** — must include the worker-contract merge
  (`0409ba4`); the worktree's current `pr-pool.sh` is the **pre-contract** version,
  so base the impl branch on a main that has `0409ba4` (or cherry-pick it) before
  porting, to capture the correct (`human`/status-based) semantics.
- **Claude launch-flag mechanism** — config block vs per-`new` flags for
  `--dangerously-skip-permissions`/`--effort`/model (enhancement 3); pick during
  the ccpool change.
- **Model pinning** — whether pr-pool pins a `--model` or inherits ccpool's
  default (bash pinned none).
- **ccpool reap/`MaxSessions` interaction** — a _working_ session isn't idle so
  reap won't kill it; acknowledge but no action at cap 1×2 roles.

## Related

- `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — the bash being
  ported + retired.
- `packages/ccpool/` — session manager + the three enhancement sites
  (`cmd/ccpool`, `internal/session`, `internal/launch`, `cmd/ccpool/list.go`).
- `packages/pg-pr/pkg/beads/runner.go` — the bd-runner pattern to copy locally.
- `docs/superpowers/specs/2026-06-11-pr-pool-worker-contract-design.md` — the
  semantics this port preserves.
