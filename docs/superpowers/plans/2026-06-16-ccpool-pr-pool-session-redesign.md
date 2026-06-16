# ccpool / pr-pool session redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development for every task. Steps use checkbox (`- [ ]`) syntax. Work in the worktree at
> `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.worktrees/session-redesign`.
> Go modules: `packages/ccpool` and `packages/pr-pool` (run `go` from each module dir).
> Verify gates before claiming done: `go test ./...` + `go vet ./...` per module; repo-level `nix flake check` and `prek run --files <changed>`.

**Goal:** Make ccpool track observed **session facts** (keyed by a surrogate id, addressed by `external_id`, resumed by `claude_session_id`, pruned when the Claude session is gone) instead of standing in for a bead's work lifecycle; make pr-pool key sessions per-attempt and never resume.

**Architecture:** See `docs/adr/0015-ccpool-session-facts-not-work-judgments.md`. Three layers: bead lifecycle in `bd`; session lifecycle in ccpool; pr-pool maps 1 bead → N sessions. No production data exists, so the ccpool schema is dropped & recreated.

**Tech Stack:** Go, modernc.org/sqlite, tmux, Claude Code hooks/CLI.

---

## Canonical contracts (all tasks MUST conform to these names/signatures)

### ccpool state vocabulary (`packages/ccpool/internal/store/store.go`)

```go
const (
	Starting   State = "starting"
	Ready      State = "ready"
	Working    State = "working"
	NeedsInput State = "needs_input"
	Idle       State = "idle"    // was Done   — Claude Stop hook (turn ended)
	Errored    State = "errored" // was Failed — Claude StopFailure hook (API error)
)
```

There is **no** `Terminal()` method and no terminal concept. Every former use of
`Done`/`Failed`/`Terminal()` is reworked (see tasks). `cold` remains absent.

### ccpool `store.Session`

```go
type Session struct {
	ID              int64  // surrogate PK (autoincrement)
	ExternalID      string // unique; caller's handle; sessions are addressed by this
	ClaudeSessionID string // unique; the Claude session UUID; used to resume
	Name            string // optional display label; nullable, NON-unique
	CWD             string
	TranscriptPath  string
	State           State
	Generation      int64
	CreatedAt       int64
	LastActivityAt  int64
	TmuxSession     string
	Model           string
	Flags           string
	PendingQuestion string
}
```

### ccpool store ops (all addressing moves from `name` → `external_id`)

```go
Insert(ctx, Session) error                      // requires ExternalID; assigns ID
GetByExternalID(ctx, externalID string) (Session, bool, error)
GetByClaudeSessionID(ctx, csid string) (Session, bool, error)
List(ctx) ([]Session, error)
Transition(ctx, externalID string, to State, claudeSessionID, transcriptPath string) (prior State, error)
Delete(ctx, externalID string) error
Upsert(ctx, externalID, claudeSessionID, name string) error  // hook "start"; inserts Starting if absent
Poll(ctx, externalID string) (generation int64, state State, ok bool, error) // implements wait.Poller
SetPendingQuestion(ctx, externalID, q string) error
// turns table keyed by external_id:
InsertTurn / GetTurn / ResolveOldestPendingTurn  — replace name param with external_id
```

### ccpool hook resolution (`packages/ccpool/cmd/ccpool/hook.go`)

```go
// eventState: start->Ready, stop->Idle, fail->Errored, notify->NeedsInput
// resolve order: GetByClaudeSessionID(payload.session_id) -> else GetByExternalID(os.Getenv("CCPOOL_EXTERNAL_ID"))
```

### ccpool session service (`packages/ccpool/internal/session/session.go`)

```go
// Ensure addresses by external_id; tmux session name = Prefix + external_id.
Ensure(ctx, externalID, cwd, model string, opts EnsureOpts) (Handle, error)
// Handle gains ExternalID + ClaudeSessionID fields.
```

`ensureLocked` decision order:

1. tmux session for `externalID` is alive → reuse (return handle, no relaunch).
2. canonicalize cwd (EvalSymlinks) + `Trust.EnsureTrusted(cwd)`.
3. row exists AND Claude session exists on disk → **resume** via
   `claude --resume <claude_session_id>` from the recorded cwd; wait for ready.
4. row exists AND Claude session GONE on disk → **prune** the row
   (`Store.Delete`), then fall through to (5). Guard: do NOT prune a row whose
   `State == Starting` AND `now - CreatedAt < pruneGrace` (fresh-session race).
5. no row → brand-new: generate `claude_session_id`, Insert(Starting),
   launch `claude --session-id <csid> [--name <name>]`; wait for ready.

`Close(ctx, externalID, purge)`: deliver `/exit`, wait, else kill tmux. If
`purge` → `Store.Delete(externalID)`. If not purge → **do nothing else** (no
fabricated state; the row keeps its last observed state and is pruned later when
the Claude session is gone).

### "Claude session exists on the machine" helper (`packages/ccpool/internal/session/`)

```go
// claudeSessionExists reports whether a resumable transcript exists for csid
// under the recorded cwd: ~/.claude/projects/<encoded-cwd>/<csid>.jsonl, where
// <encoded-cwd> replaces os.PathSeparator runs with '-' (Claude's convention).
func claudeSessionExists(home, cwd, claudeSessionID string) bool
```

Injected/seamed for tests (a `SessionStore` interface or a func field on the
service) so tests don't touch a real `~/.claude`.

### pr-pool (`packages/pr-pool`)

- `roles.Role.ExternalID(prefix, beadID, stamp string) string` → `prefix + role + "-" + beadID + "-" + stamp`.
  Keep `SessionName` (or add `DisplayName`) → `prefix + role + "-" + beadID` as the optional ccpool `--name`.
- `internal/ccpool/cli.go`: `Ensure` passes external_id + `--name`; `Close` passes `--purge`.
- A clock/stamp seam on the orchestrator (`stamp func() string`, default time-based) so tests are deterministic.

---

## File structure

- `packages/ccpool/internal/store/migrations/004_session_redesign.sql` — NEW: drop & recreate `sessions` (+ `turns` FK to external_id).
- `packages/ccpool/internal/store/store.go` — state enum rename, `Session` fields, remove `Terminal()`.
- `packages/ccpool/internal/store/ops.go` — re-key ops to external_id; add `GetByClaudeSessionID`; turns by external_id.
- `packages/ccpool/internal/store/schema.go` — unchanged (migration runner).
- `packages/ccpool/cmd/ccpool/hook.go` — resolve by claude_session_id/env; new state map.
- `packages/ccpool/internal/session/session.go` — ensure/resume/prune; Handle fields; resume by claude_session_id.
- `packages/ccpool/internal/session/cancel_close.go` — Close no longer fabricates state; cancel sets Ready (unchanged).
- `packages/ccpool/internal/session/reap.go` — prune-when-gone pass; address by external_id.
- `packages/ccpool/internal/session/sessionexists.go` — NEW: `claudeSessionExists` + seam.
- `packages/ccpool/internal/launch/launch.go` — `BuildResume` uses `--resume <claude_session_id>`; keep `--name`.
- `packages/ccpool/internal/wait/wait.go` — poller addressing (no signature change if it takes a Poller).
- `packages/ccpool/cmd/ccpool/*.go` — every subcommand: positional `<external_id>`, optional `--name`; launch env `CCPOOL_EXTERNAL_ID`.
- `packages/pr-pool/internal/roles/roles.go` — `ExternalID()` + display name.
- `packages/pr-pool/internal/ccpool/cli.go` — Ensure(external_id, name), Close(--purge).
- `packages/pr-pool/internal/orchestrator/orchestrator.go` — per-attempt external_id; purge teardown; completion via ccpool idle/errored + bead judgment; stuck-bead escalation.
- All corresponding `_test.go` files.

---

## Phase A — ccpool store foundation

### Task A1: schema migration (drop & recreate)

**Files:** Create `packages/ccpool/internal/store/migrations/004_session_redesign.sql`

- [ ] **Step 1 — write the migration**

```sql
DROP TABLE IF EXISTS turns;
DROP TABLE IF EXISTS sessions;

CREATE TABLE sessions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    external_id       TEXT NOT NULL UNIQUE,
    claude_session_id TEXT UNIQUE,
    name              TEXT,
    cwd               TEXT NOT NULL DEFAULT '',
    transcript_path   TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL,
    generation        INTEGER NOT NULL DEFAULT 1,
    created_at        INTEGER NOT NULL,
    last_activity_at  INTEGER NOT NULL,
    tmux_session      TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    flags             TEXT NOT NULL DEFAULT '',
    pending_question  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE turns (
    turn_id         TEXT PRIMARY KEY,
    external_id     TEXT NOT NULL,
    prompt          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    transcript_path TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    resolved_at     INTEGER
);
```

- [ ] **Step 2 — commit** `git add` the migration; `git commit -m "feat(ccpool): drop+recreate sessions schema (surrogate id, external_id, claude_session_id) — ADR 0015"`

> Note: editing prior migrations is forbidden; this is a new forward migration. Existing dev DBs re-run only 004 (drops & recreates — acceptable, no real data).

### Task A2: state vocabulary rename + Session struct

**Files:** Modify `packages/ccpool/internal/store/store.go`, `packages/ccpool/internal/store/store_test.go`

- [ ] **Step 1 — failing test:** add `TestState_idleErroredReplaceDoneFailed` asserting the constants `Idle == "idle"`, `Errored == "errored"` exist and `Done`/`Failed`/`Terminal` are gone (compile-level: reference `store.Idle`/`store.Errored`). Run; expect compile failure.
- [ ] **Step 2 — implement:** rename `Done`→`Idle` (`"idle"`), `Failed`→`Errored` (`"errored"`); delete `Terminal()`. Update `Session` to the canonical struct (add `ID int64`, rename `UUID`→`ClaudeSessionID`, add `ExternalID`). Update the package doc comment to "session facts" wording.
- [ ] **Step 3 — run** `go build ./...` in ccpool; expect failures across files that reference `Done`/`Failed`/`Terminal`/`UUID`/`Name`-keys — those are fixed in later tasks. For THIS task, get `internal/store` compiling + its tests green: `go test ./internal/store/...`.
- [ ] **Step 4 — commit** `feat(ccpool): rename done/failed -> idle/errored; surrogate-id Session model (ADR 0015)`

### Task A3: store ops re-keyed to external_id

**Files:** Modify `packages/ccpool/internal/store/ops.go`, `packages/ccpool/internal/store/ops_test.go`, `packages/ccpool/internal/store/turns_test.go`

- [ ] **Step 1 — failing tests** (`ops_test.go`): for each new signature, a focused test:
  - `Insert` then `GetByExternalID` round-trips all fields incl. assigned `ID > 0`.
  - `GetByClaudeSessionID` finds the row by `claude_session_id`.
  - `Transition(externalID, Idle, csid, tpath)` bumps generation, sets state, updates claude_session_id/transcript when non-empty, returns prior.
  - `Delete(externalID)` removes; deleting missing is no-error.
  - `Upsert(externalID, csid, name)` inserts `Starting` when absent, no-op when present.
  - `Poll(externalID)` returns generation+state+ok.
  - turns: `InsertTurn`/`GetTurn`/`ResolveOldestPendingTurn` keyed by external_id.
    Run; expect compile/failures.
- [ ] **Step 2 — implement** the canonical ops signatures above. Update `cols` to the new column list; `scanRow` to the new fields (claude_session_id nullable via `sql.NullString`). `INSERT` lets SQLite assign `id` (omit it from the column list or pass NULL); read it back via `last_insert_rowid()` into `Session.ID` if convenient (not required by callers).
- [ ] **Step 3 — run** `go test ./internal/store/...`; expect PASS.
- [ ] **Step 4 — commit** `feat(ccpool): re-key store ops to external_id + claude_session_id (ADR 0015)`

---

## Phase B — ccpool hooks + launch

### Task B1: hook resolution + state map

**Files:** Modify `packages/ccpool/cmd/ccpool/hook.go`, `packages/ccpool/cmd/ccpool/hook_test.go`

- [ ] **Step 1 — failing tests:**
  - `TestHook_start_resolvesByClaudeSessionID_setsReady`: payload `session_id` matches an existing row's `claude_session_id` → row transitions to `Ready`.
  - `TestHook_start_resolvesByEnvExternalID_whenNoRowForSessionID`: unknown `session_id`, `CCPOOL_EXTERNAL_ID` set → `Upsert` then `Ready`.
  - `TestHook_stop_setsIdle` / `TestHook_fail_setsErrored` / `TestHook_notify_setsNeedsInput`.
  - `TestHook_unresolvable_noErrorNoRow` (no session_id, no env) → returns nil, no row.
- [ ] **Step 2 — implement:** `eventState{start:Ready, stop:Idle, fail:Errored, notify:NeedsInput}`. `resolveExternalID(ctx, st, sessionID, envExternalID)`: try `GetByClaudeSessionID(sessionID)` → its `ExternalID`; else `envExternalID` if non-empty; else ok=false. Read env via `os.Getenv("CCPOOL_EXTERNAL_ID")`. On `start`, `Upsert(externalID, sessionID, "")` then `Transition(externalID, Ready, sessionID, transcriptPath)`.
- [ ] **Step 3 — run** `go test ./cmd/ccpool/ -run TestHook`; expect PASS.
- [ ] **Step 4 — commit** `feat(ccpool): resolve hooks by claude_session_id/external_id; idle/errored states (ADR 0015)`

### Task B2: launch resume by claude_session_id

**Files:** Modify `packages/ccpool/internal/launch/launch.go`, `packages/ccpool/internal/launch/launch_test.go`

- [ ] **Step 1 — failing test:** `TestBuildResume_resumesByClaudeSessionID` → `BuildResume(Spec{ClaudeBin:"claude", ClaudeSessionID:"u1", PluginDir:"/p"})` yields `["claude","--resume","u1","--plugin-dir","/p", ...flags]`. Keep `--name` passthrough for new. Add `ClaudeSessionID` to `launch.Spec` (and keep `UUID`→rename to `ClaudeSessionID` for new's `--session-id`).
- [ ] **Step 2 — implement.**
- [ ] **Step 3 — run** `go test ./internal/launch/...`; PASS.
- [ ] **Step 4 — commit** `feat(ccpool): resume by --resume <claude_session_id> (ADR 0015)`

---

## Phase C — ccpool session service

### Task C1: claudeSessionExists helper + seam

**Files:** Create `packages/ccpool/internal/session/sessionexists.go` + `_test.go`

- [ ] **Step 1 — failing tests:** table test for the cwd→`~/.claude/projects/<encoded>` encoding (e.g. `/Volumes/ziprecruiter/monorepo` → `-Volumes-ziprecruiter-monorepo`), and `claudeSessionExists(home, cwd, csid)` true when `<home>/.claude/projects/<enc>/<csid>.jsonl` exists (use `t.TempDir()` as home), false otherwise.
- [ ] **Step 2 — implement** the encoding + stat. Expose a `SessionExister` interface or func field on `Service.d` so the service can be tested with a fake.
- [ ] **Step 3 — run** `go test ./internal/session/ -run SessionExists`; PASS.
- [ ] **Step 4 — commit** `feat(ccpool): claudeSessionExists transcript probe (ADR 0015)`

### Task C2: ensureLocked resume/prune/new + Close no-fabrication

**Files:** Modify `packages/ccpool/internal/session/session.go`, `cancel_close.go`, `reap.go`, and their tests.

- [ ] **Step 1 — failing tests** (use the existing fake tmux/store/wait harness; add a fake `SessionExister`):
  - `TestEnsure_reusesLiveTmux` (tmux alive → no relaunch; returns handle).
  - `TestEnsure_resumesWhenSessionExists` (tmux gone, row exists, exister=true → launch contains `--resume <csid>`; waits ready).
  - `TestEnsure_prunesAndCreatesFreshWhenSessionGone` (tmux gone, row exists, exister=false, row not fresh-starting → row Deleted, then new `--session-id` launch).
  - `TestEnsure_doesNotPruneFreshStartingRow` (row State=Starting, age<grace, exister=false → NOT deleted).
  - `TestEnsure_brandNewWhenNoRow`.
  - `TestClose_purgeDeletesRow` / `TestClose_nonPurgeKeepsRowNoStateChange` (assert state is NOT changed to Idle/Errored; only tmux killed).
  - `TestReap_prunesRowsWhoseSessionGone` (a dead, gone row is Deleted by reconcile).
- [ ] **Step 2 — implement** `ensureLocked` per the decision order; address by external_id; tmux name = `Prefix + externalID`; resume via `BuildResume(ClaudeSessionID: row.ClaudeSessionID)`. Add `pruneGrace` (e.g. `30 * time.Second`, a field on the service, default set in cmd wiring). Rework `Close` to drop the non-purge "reconcile to Done" block entirely. Add a prune step to `Reap` (or a `reconcile`): for each row, if tmux dead AND `!exister(row)` AND not fresh-starting → `Delete`.
- [ ] **Step 3 — run** `go test ./internal/session/...`; PASS.
- [ ] **Step 4 — commit** `feat(ccpool): resume-by-session-id, prune-when-gone, close stops fabricating state (ADR 0015)`

---

## Phase D — ccpool cmd + wait + turns wiring

### Task D1: cmd subcommands address by external_id + --name; launch env

**Files:** Modify `packages/ccpool/cmd/ccpool/{new,close,cancel,send,reply,state,list,result}.go` + tests; `internal/session` launch env.

- [ ] **Step 1 — failing tests / integration:** update existing cmd tests (`*_integration_test.go`, `contract_*`) to the new positional `<external_id>` + `--name` and assertions on `claude_session_id`. Add `TestNew_generatesClaudeSessionID_injectsExternalIDEnv` (launch env contains `CCPOOL_EXTERNAL_ID`, argv has `--session-id <csid>`).
- [ ] **Step 2 — implement:** every subcommand takes `<external_id>` positional; `new`/`reply`/`send`... add `--name` where a display name is meaningful (at least `new`). `new` generates `claude_session_id` (uuid), inserts, launches `--session-id <csid> [--name <name>]`, env `CCPOOL_EXTERNAL_ID=<external_id>`. `list`/`state` output: show `external_id`, `name`, `claude_session_id`, `state`, liveness. Update help text.
- [ ] **Step 3 — run** `go test ./cmd/ccpool/...`; PASS.
- [ ] **Step 4 — commit** `feat(ccpool): CLI addresses sessions by external_id, adds --name (ADR 0015)`

### Task D2: wait poller + turns + any remaining references

**Files:** Modify `packages/ccpool/internal/wait/*.go`, `internal/store` turns callers, grep for stragglers.

- [ ] **Step 1 — sweep:** `rg -n "GetByName|\.UUID\b|store\.Done|store\.Failed|\.Terminal\(" packages/ccpool` → every hit fixed or justified.
- [ ] **Step 2 — failing tests / fix:** ensure `wait` polls by external_id; turns resolve by external_id (port `turns_test.go`).
- [ ] **Step 3 — run** full ccpool: `go test ./...` + `go vet ./...` + `gofmt -l .`; all green.
- [ ] **Step 4 — commit** `feat(ccpool): finish external_id migration across wait/turns (ADR 0015)`

---

## Phase E — pr-pool integration

### Task E1: per-attempt external_id + display name

**Files:** Modify `packages/pr-pool/internal/roles/roles.go` + `roles_test.go`

- [ ] **Step 1 — failing test:** `TestRole_ExternalID_includesStamp` → `Feedback.ExternalID("pr-pool-", "zr-1", "20260616T010203")` == `"pr-pool-feedback-processor-zr-1-20260616T010203"`; `DisplayName` == `"pr-pool-feedback-processor-zr-1"`.
- [ ] **Step 2 — implement** `ExternalID(prefix, beadID, stamp)` and `DisplayName(prefix, beadID)` (rename/keep `SessionName` as `DisplayName`).
- [ ] **Step 3 — run** `go test ./internal/roles/...`; PASS.
- [ ] **Step 4 — commit** `feat(pr-pool): per-attempt external_id + display name (ADR 0015)`

### Task E2: ccpool CLI wrapper — external_id, name, purge

**Files:** Modify `packages/pr-pool/internal/ccpool/cli.go` + `cli_test.go`

- [ ] **Step 1 — failing tests:** `Ensure` invokes `ccpool new <external_id> --cwd <cwd> --name <display> ...`; `Close` invokes `ccpool close <external_id> --purge`.
- [ ] **Step 2 — implement.** Thread `externalID`, `name` through `Ensure`/`Close` signatures (update the `Runner` interface + the orchestrator fakes accordingly).
- [ ] **Step 3 — run** `go test ./internal/ccpool/...`; PASS.
- [ ] **Step 4 — commit** `feat(pr-pool): ccpool wrapper uses external_id/name, purges on close (ADR 0015)`

### Task E3: orchestrator — stamp seam, purge teardown, completion via bd, escalation

**Files:** Modify `packages/pr-pool/internal/orchestrator/orchestrator.go` + `orchestrator_test.go`; `internal/config` if a knob is needed.

- [ ] **Step 1 — failing tests:**
  - `TestWorkOne_usesPerAttemptExternalID` (Ensure called with a stamped external_id; stamp injected via `o.stamp`).
  - `TestTeardownAll_purges` (Close called with purge=true for pr-pool-prefixed sessions).
  - `TestWaitDone_*` ported: completion is decided by reading the **bead** once ccpool reports the session reached `Idle`/`Errored` (session-fact trigger) — keep the existing success/flag outcomes (bead closed = success; otherwise OnFailure), but drive the loop off the ccpool fact instead of only bead polling. Preserve all pg2-c1vp watchdog single-terminal guarantees.
  - `TestStuckBead_escalatesAfterN` (N consecutive Ensure failures for a bead → human label; mechanism: a bead label/comment counter — see step 2).
- [ ] **Step 2 — implement:** add `stamp func() string` seam (default `time.Now().UTC().Format("20060102T150405")`, overridable in tests via `newOrch`). `SessionName` call sites → `ExternalID(prefix, beadID, o.stamp())` for Ensure/Close/wait; pass `DisplayName` as the ccpool `--name`. `teardownAll` → `Close(..., purge=true)`. Completion: when `active()`-equivalent reports the ccpool session reached `Idle`/`Errored`, re-read the bead and apply the existing success/`OnFailure` decision (this replaces "done"/"failed" ccpool-state checks — update the `ccpool.State` constants used in `active()`/`waitDone` to `Idle`/`Errored`). Escalation: maintain a per-bead consecutive-launch-failure count via a bead label (e.g. add `pool-launch-fail` on first Ensure failure; if already present on the next failure, add `human` and stop retrying). Keep it simple and testable through the fake `bd` runner.
- [ ] **Step 3 — run** `go test ./...` (pr-pool) + `go vet`; PASS.
- [ ] **Step 4 — commit** `feat(pr-pool): per-attempt sessions, purge teardown, bd-owned completion, stuck-bead escalation (ADR 0015)`

---

## Phase F — integration verification + docs

### Task F1: cross-module build + gates

- [ ] `cd packages/ccpool && go test ./... && go vet ./...`
- [ ] `cd packages/pr-pool && go test ./... && go vet ./...`
- [ ] `prek run --files <all changed files>` (or `--all-files`); fix `gofmt`/lint.
- [ ] `nix flake check` at repo root; must print "all checks passed!".
- [ ] **Commit** any fmt/lint fixes.

### Task F2: docs

- [ ] Update `docs/adr/index.md` to add ADR 0015.
- [ ] Update `packages/ccpool` README/usage if it documents `<name>` addressing or `done`/`failed` states.
- [ ] **Commit** `docs: index ADR 0015; update ccpool usage for external_id/idle/errored`

---

## Self-review checklist (run before handing to review)

- [ ] No `store.Done`/`store.Failed`/`.Terminal()`/`GetByName`/bare `.UUID` left in ccpool (`rg` clean).
- [ ] Every cmd subcommand addresses by external_id; `--name` optional everywhere it was a name.
- [ ] pr-pool never calls Close without `--purge`; never reuses an external_id across attempts.
- [ ] Completion judgments are bd-based; ccpool states are only used as session facts (idle/errored as triggers).
- [ ] All tests assert real behavior; no log-string assertions; deterministic (no real `~/.claude`, injected clock/stamp).
