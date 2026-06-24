# ccpool-referencing Beads — Review & Execution Roadmap

> **Status:** Planning roadmap (review + sequencing). This is the meta-plan.
> Per-bead executable plans (bite-sized, TDD, checkbox steps) will be written into
> `docs/superpowers/plans/` _after_ the open decisions below are resolved, then handed
> to subagents.
>
> **Scope:** the 11 open beads that reference `ccpool` (text-match across title/description/
> notes/labels), reviewed against the **live code as of 2026-06-23** — not just bead text.
> Several bead descriptions are stale; corrected scope is called out per bead.

**Repos involved**

- `phillipgreenii-nix-agent-support/packages/{ccpool, pr-pool, claude-transcript}` — Go
- `phillipgreenii-nix-agent-support/{home,darwin}/modules/ccpool` — nix
- `phillipg-nix-ziprecruiter/modules/pg-pr-zr` + `phillipg-nix-repo-base` — nix (pg2-wtjz only)

---

## Part 0 — The 11 beads at a glance

| Bead         | P   | Type    | Repo(s)                  | ccpool change?                   | Status                           |
| ------------ | --- | ------- | ------------------------ | -------------------------------- | -------------------------------- |
| `pg2-oois.5` | P3  | task    | ccpool (Go)              | **Yes — core**                   | Ready. Closes epic `pg2-oois`.   |
| `pg2-yvnp`   | P2  | task    | ccpool (Go+nix)          | **Yes — core**                   | Ready. Has a live-Loki AC.       |
| `pg2-01ys`   | P3  | feature | ccpool (Go)              | **Yes — core**                   | BLOCKED on decision **D1**.      |
| `pg2-3msk`   | P1  | bug     | ccpool + pr-pool         | **Yes (adds `--allowed-tools`)** | Newly unblocked (`pg2-ee6y` ✓).  |
| `pg2-yukh`   | P1  | bug     | ccpool + pr-pool         | **Yes (ingestion guard)**        | Ready. Multi-cause.              |
| `pg2-th35`   | P2  | task    | pr-pool                  | No (consumes ccpool notify)      | Ready, plan-ready.               |
| `pg2-2f9d`   | P3  | task    | pr-pool (± ccpool)       | **Maybe — decision D2**          | Ready.                           |
| `pg2-wgg0`   | P3  | feature | pr-pool                  | No (mirrors ccpool pattern)      | Ready; **scope shrank** (D4).    |
| `pg2-yt0n`   | P3  | bug     | pr-pool                  | No                               | Ready, plan-ready.               |
| `pg2-wtjz`   | P3  | task    | ziprecruiter + repo-base | No (pattern ref only)            | Ready; independent track.        |
| `pg2-oois`   | P1  | epic    | —                        | —                                | Auto-closes when `oois.5` lands. |

---

## Part 1 — Bead-by-bead review (corrected scope)

### pg2-oois.5 — ccpool adopts the shared registry-status reader _(ccpool core, P3)_

- **Bead asks:** ccpool consumes the shared `claude-transcript` registry reader where it agrees with its own state, keeps pane-derived substate, documents the mapping, no regression. Closes epic `pg2-oois` (last of 4 children; `pg2-oois.2/.3/.4` ✓).
- **Reality:** The library exists and is production-proven in pa-monitor.
  - Lib: `github.com/phillipgreenii/claude-transcript` — `ClassifyActivity(reg, awaitingInput, lastActivity, freshWindow) ActivityVerdict`, plus `ReadSessionFile`, `ReadSessionRegistry`, `PidAlive`, `IsAwaitingInput`, `LastMessageActivity` (`packages/claude-transcript/registry.go`, `awaiting.go`).
  - ccpool's own classifier: `internal/state/state.go:23-40` (enum `Idle/Working/WaitingForHuman/Error/NotLive` + `thinking/streaming`), reconciliation `state.go:145-194`, signal gather `state.go:196-267`, CLI `cmd/ccpool/state.go`.
  - pa-monitor reference wiring: `packages/pa-monitor/internal/core/poller/poller.go:210-247` (pid-gate → `ClassifyActivity` → map to its enum).
- **Corrected scope:** Add the registry verdict as an **input signal** to ccpool's `Gather`/`Classify` — pid-gate first (`PidAlive`), trust `busy`, cross-check `waiting`. Map `Active→working`, `WaitingForHuman→waiting-for-human`, `Idle→idle`, keep ccpool's pane `thinking/streaming` substate (registry can't provide it). Document the mapping; regression-test `needs_input`/pending-question.
- **Files:** `packages/ccpool/internal/state/state.go` (+ `state_test.go`), `packages/ccpool/go.mod` (add lib dep — in-repo sibling, gomod2nix Pattern B).
- **Synergy:** the registry signal also detects "row=working but zero model turns / pid alive but transcript stale" — directly useful to `pg2-yukh`.
- **Risk/decision:** none blocking. Pure additive signal.

### pg2-yvnp — ccpool structured JSONL logs + register logSources _(ccpool core, P2)_

- **Bead asks:** ccpool emits structured JSONL (`time`/`level`/`msg`) to `${XDG_STATE_HOME}/ccpool/*.jsonl` replacing free-form `hook.log`/`reap` text; bind reap `StandardOutPath`/`StandardErrorPath` to their own (non-tailed) paths; set `phillipgreenii.observability.logSources.ccpool` (stub-backed); live-verify a `level:error` line is queryable in Loki.
- **Reality:**
  - Free-form diagnostics: `cmd/ccpool/hook.go:269` → `<state-dir>/hook.log` (append, plain text).
  - There **is** an `internal/eventlog` (`events.jsonl`, transitions/inputs) — but that's a _domain_ event log, **not** the diagnostic log the bead targets. Keep it distinct.
  - Reap launchd: `darwin/modules/ccpool/default.nix:51-52` (`reap.err.log`/`reap.out.log`).
  - **No** `observability.logSources` reference anywhere in ccpool's nix modules (confirmed).
  - State dir: `internal/config/config.go:125-130` (`$XDG_STATE_HOME/ccpool`).
- **Corrected scope:** (1) replace `hook.log` free-form writes with a structured JSONL logger (`time`/`level`/`msg` lowercase); (2) nix: set `phillipgreenii.observability.logSources.ccpool` (stub-backed, must eval standalone in agent-support CI); (3) keep reap stdout/err on their own paths, **not** the tailed JSONL.
- **Files:** `packages/ccpool/cmd/ccpool/hook.go` (+ a small `internal/log` JSONL writer), `home/programs/ccpool/default.nix`, `darwin/modules/ccpool/default.nix`.
- **Live-verify gap:** the Loki query AC is **non-hermetic** — needs the running observability stack. Plan will deliver everything hermetic + a documented manual verification runbook; the live step gets flagged for the operator (you).
- **Dep:** `pg2-45ab.3` (cross-flake stubs) ✓.

### pg2-01ys — ccpool first-class session-state query + session metadata/search _(ccpool core, P3, BLOCKED)_

- **Bead asks:** (1) generalize session-state as a first-class query; (2) attach arbitrary metadata to a session (bead id, role, pool/group) + query/filter by it.
- **Reality:** sub-feature (1) is **subsumed** by Unit B's `ccpool state` (`cmd/ccpool/state.go`, reconciled classifier). Only sub-feature (2) **session metadata + search** is genuinely unbuilt.
- **Corrected scope (if pursued):** re-scope to _just_ session metadata + search (store schema for arbitrary KV per session + `ccpool list --filter`/query). Drop the state-query half.
- **BLOCKING DECISION D1:** Does pursuing metadata/search commit to "Option 2" (pr-pool consuming ccpool as a library)? This is a product-direction call — see Part 3.

### pg2-3msk — pr-pool: default deny-by-default + constrained allowed-tools _(ccpool + pr-pool, P1 bug)_

- **Bead asks:** default `--dangerously-skip-permissions` OFF; constrain workers via an allowed-tools allowlist (prompt-injection→RCE hardening). Was blocked on `pg2-ee6y` — now ✓.
- **Reality (bead file-refs are STALE):**
  - pr-pool already uses `PermissionMode` (default **`"bypassPermissions"`**) — `internal/config/config.go:80`; launches via `--permission-mode` (`internal/ccpool/cli.go:111-135`). The `--dangerously-skip-permissions` flag no longer exists.
  - ccpool supports `--permission-mode` incl. `dontAsk` (`internal/launch/launch.go:14-23`, `cmd/ccpool/new.go:27,39-42`) — but **NO `--allowed-tools` flag** exists in ccpool.
- **Corrected scope (3 parts):**
  1. **ccpool (new):** add an `--allowed-tools` passthrough flag (`new.go` + `EnsureOpts` + `launch.BuildNew/appendFlags`). _This is the hidden ccpool change._
  2. **pr-pool:** flip default `PermissionMode` `bypassPermissions`→`dontAsk` (deny-by-default; safe + non-interactive).
  3. **pr-pool:** emit a constrained default `AllowedTools` set via the new ccpool flag.
- **Why coupled (per bead comments):** with `dontAsk`, un-pre-approved tools auto-deny instead of stalling a human-less worker; an allowlist is only safe + non-interactive under deny-by-default.
- **Files:** ccpool `cmd/ccpool/new.go`, `internal/session/session.go` (`EnsureOpts`), `internal/launch/launch.go`; pr-pool `internal/config/config.go`, `internal/ccpool/cli.go`.
- **DECISION D3:** carve the ccpool `--allowed-tools` addition into its own bead (clean dep) or fold into `pg2-3msk`? (Recommend: small dedicated ccpool bead that `pg2-3msk` depends on.)

### pg2-yukh — worker did nothing: lost initial nudge + harmful reminder _(ccpool + pr-pool, P1 bug)_

- **Bead asks:** (1) detect a worker that never ingested its initial nudge and fail fast; (2) the budget reminder must never be the first prompt and must be bead-explicit; (3) workers run in a fresh per-bead worktree; (4) regression test for the lost-initial-prompt path with NO writes to other beads.
- **Reality (all three causes confirmed):**
  - **Worktree:** `internal/executor/ccpool.go:44` launches **every** session at `Cfg.RepoRoot`, despite the worker prompt referencing `{{.WorktreeDir}}` (`internal/roles/builtin.go:26`). → workers run on whatever branch the monorepo is on.
  - **Reminder:** `ReminderMsg`/`WrapUpMsg` (`internal/config/config.go:57`, fired `internal/watchdog/watchdog.go:72,77`) are **not bead-explicit** and fire on a timer regardless of whether the first turn happened.
  - **Ingestion:** `internal/session/send.go:87-102` records paste/Enter as _actions_, with **no confirmation the model ingested** the prompt; turn-wait keys off generation/hook advance only.
- **Corrected scope (split by repo):**
  - **ccpool:** add a post-delivery ingestion guard in `send.go` — confirm the model actually started a turn within a bounded window (the `claude-transcript` registry `busy` + first-message signal from `pg2-oois.5` is the natural detector). Surface a distinct failure if not.
  - **pr-pool:** (a) assign a **fresh per-bead worktree** at dispatch (`executor/ccpool.go:44`); (b) gate the budget reminder so it never fires before the first model turn, and make it **bead-explicit** (name the bead / no "this bead" ambiguity).
- **Files:** ccpool `internal/session/send.go`; pr-pool `internal/executor/ccpool.go`, `internal/watchdog/watchdog.go`, `internal/config/config.go`.
- **Live-verify gap:** AC #4 wants a live lost-prompt repro. Plan delivers a deterministic unit/integration regression (inject a dropped paste) + documents the live repro as a manual check.
- **Sequencing note:** benefits from `pg2-oois.5` landing first (registry signal = the ingestion detector).

### pg2-th35 — pr-pool surfaces needs*input + survives teardown *(pr-pool, P2)\_

- **Bead asks:** alert the operator when a session hits `needs_input` (name the tmux session), fire once on the edge (keep non-terminal semantics), and preserve `needs_input` sessions from the end-of-pass teardown (or explicitly accept-and-document the kill).
- **Reality:** pr-pool has **no** needs_input notification. ccpool **does** (`internal/notify`, default `On=[needs_input,failed]`, desktop adapter). pr-pool `orchestrator.go:262-279 teardownAll()` closes **all** `<SessionPrefix>*` sessions unconditionally; `executor/ccpool.go:291-302 active()` treats `NeedsInput` as active (polls to MaxWait).
- **Corrected scope:** emit a distinct pr-pool eventlog record + log line (naming the tmux session) on the **edge** into `needs_input`; make `teardownAll()` skip `needs_input` sessions (or document the kill). Lean on ccpool's existing desktop notifier; don't rebuild it.
- **Files:** pr-pool `internal/orchestrator/orchestrator.go`, `internal/executor/ccpool.go` (+ tests).
- **Risk/decision:** the teardown-skip vs accept-the-kill is an in-AC choice; recommend **skip + preserve** with a TTL.

### pg2-2f9d — prevent AskUserQuestion stalls for autonomous workers _(pr-pool ± ccpool, P3)_

- **Bead asks:** autonomous workers must never stall on an AskUserQuestion picker. Two levers (both spike-verified): prompt-forbid + a blocking `PreToolUse` hook that exits 2 (deny).
- **Reality:** pr-pool has **no** PreToolUse hook wiring; worker prompt is `internal/roles/builtin.go:26`. ccpool already renders `hooks.json` for sessions and has a (non-blocking) `ccpool hook ask` for _detection_ (`pg2-7a5b`). A blocking hook is feasible either as pr-pool worker config or a ccpool launch flag.
- **Corrected scope:** prompt-forbid (edit worker prompt/skill) **plus** a blocking `PreToolUse:AskUserQuestion` hook. Must NOT break ccpool's non-blocking `needs_input` detection (`pg2-7a5b`).
- **BLOCKING DECISION D2:** does the blocking hook live in **pr-pool worker config** or as a **ccpool launch flag**? Determines whether this is a pr-pool-only change or also touches ccpool.

### pg2-wgg0 — pr-pool pool-level budget config (XDG/TOML) _(pr-pool, P3)_

- **Bead asks (decisions already confirmed in-bead):** two-mode ccpool-style resolver — global `$XDG_CONFIG_HOME/pr-pool/config.toml` + optional repo-local override; scope=RepoRoot by file location; flat budget-only schema; precedence file < env < (future per-role).
- **Reality (grooming note is STALE):** pr-pool **already** loads TOML (`internal/config/config.go Load()` reads `<RepoRoot>/.pr-pool/config.toml` or `$PR_POOL_CONFIG`; `[pool]` can already set budget). `BurntSushi/toml` already a dep. The role-externalization work (`2026-06-16-pr-pool-externalize-roles-toml-phase1.md`) landed after this bead was groomed.
- **Corrected scope (SHRUNK):** the repo-local TOML budget layer largely exists. Remaining delta = add the **XDG-global** `$XDG_CONFIG_HOME/pr-pool/config.toml` layer + the two-mode precedence (XDG global < repo-local < env), reusing `stateHome()` (`config.go:200-205`). **Verify** during planning exactly which budget keys the current `[pool]` loader honors.
- **DECISION D4:** confirm the reduced scope (XDG-global layer only) — or re-groom the bead.
- **Interaction:** touches the same budget-serialization surface as `pg2-yt0n` — do `yt0n` first or together.

### pg2-yt0n — ExampleTOML omits per-role budget (round-trip bug) _(pr-pool, P3 bug)_

- **Bead asks:** `emitRole` should emit `[role.ccpool.budget]` (representing unlimited explicitly, e.g. `time="0s"`), and a round-trip test should assert `feedback` ends up unlimited.
- **Reality (confirmed exactly):** `internal/config/example.go:56-73 emitRole` emits actor/completion/on_failure/on_dispatch_fail/authorship_guard/prompt — **not** budget. `feedback` role = `budget.Budget{}` unlimited (`internal/roles/builtin.go:48`); `worker` = pool default (`builtin.go:58`). `TestExampleTOML_roundTrips` (`example_test.go:8-21`) never asserts feedback's budget → `print-defaults` reload silently gives feedback a 25m watchdog.
- **Corrected scope:** as written; small + well-scoped. TDD: failing round-trip test asserting `feedback` unlimited → emit `[role.ccpool.budget]` → green.
- **Files:** pr-pool `internal/config/example.go`, `internal/config/example_test.go`.

### pg2-wtjz — nix-build pg-pr-zr cross-repo (drop out-of-band build) _(ziprecruiter + repo-base, P3, independent track)_

- **Bead asks:** build `pg-pr-cicd-captains-log` + `pg-pr-issues-jira-zr` inside nix (no out-of-band `go build`); jira module installs the nix-built binary; pg-pr edits need no `vendorHash` bump.
- **Reality:** ADR 0008 (gomod2nix) is the current standard and **explicitly names this bead** as the cross-repo follow-up. In-repo replaces are native (`buildGoApplication` symlinks the sibling). Cross-repo `../` escapes the store tree (gomod2nix #101). agent-support `flake.nix` exposes built packages but **not** `pg-pr` _source_. The out-of-band build + jira `realBinary` wrapper live in `phillipg-nix-ziprecruiter/modules/pg-pr-zr/default.nix:22-103`.
- **Corrected scope (2 parts, cross-repo):**
  1. **agent-support:** expose `packages/pg-pr` _source_ as a flake output (fileset/derivation).
  2. **ziprecruiter:** root `mkGoApp` `src` over a fileset unioning `modules/pg-pr-zr` + the exposed `pg-pr` source, set `modRoot = "pg-pr-zr"` (ADR 0008 Pattern B, sibling from a flake input); drop the out-of-band build and the `realBinary` user-build requirement (the secret-injection wrapper can stay).
- **Note:** only tangentially "ccpool" (pattern reference). Fully independent of the Go-app logic beads → **parallel track**.

---

## Part 2 — What ccpool itself needs (consolidated)

The user's explicit ask: _"plan what needs to be done for ccpool."_ Across all beads, the **ccpool code/nix changes** are:

| #   | ccpool change                                                                     | Driven by    | Files                                                                           | Decision?      |
| --- | --------------------------------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------- | -------------- |
| C1  | Adopt `claude-transcript` registry verdict as a state input signal                | `pg2-oois.5` | `internal/state/state.go`, `go.mod`                                             | —              |
| C2  | Add `--allowed-tools` passthrough flag (new + resume)                             | `pg2-3msk`   | `cmd/ccpool/new.go`, `internal/session/session.go`, `internal/launch/launch.go` | D3 (own bead?) |
| C3  | Post-delivery **ingestion guard** in `send.go` (confirm model started a turn)     | `pg2-yukh`   | `internal/session/send.go`                                                      | depends C1     |
| C4  | Structured JSONL diagnostic logger replacing `hook.log` + `logSources` nix wiring | `pg2-yvnp`   | `cmd/ccpool/hook.go`, `internal/log/*`, `home/darwin modules`                   | —              |
| C5  | Blocking `PreToolUse:AskUserQuestion` launch flag _(only if D2 = ccpool)_         | `pg2-2f9d`   | `cmd/ccpool/new.go`, rendered `hooks.json`                                      | D2             |
| C6  | Session **metadata + search** (KV per session + filter) _(only if D1 = pursue)_   | `pg2-01ys`   | ccpool store schema, `cmd/ccpool/list`                                          | D1             |

C1 → C3 is a natural chain (the registry signal is the ingestion detector). C2 is small and unblocks the P1 `pg2-3msk`. C4 is self-contained. C5/C6 are decision-gated.

---

## Part 3 — Decisions (resolved 2026-06-23, except D4)

- **D1 — `pg2-01ys` — RESOLVED: PURSUE + commit to Option 2.** pr-pool will consume ccpool as a library. Bead unblocked (status open) and re-scoped to **session metadata + search only** (sub-feature 1 is subsumed by `ccpool state`). The metadata/search API is designed as a ccpool library surface pr-pool consumes. → moves to Wave 4 as committed work (no longer decision-gated). C6 is in scope.
- **D2 — `pg2-2f9d` — RESOLVED: ccpool launch flag (C5) + mandatory prompt-forbid.** Investigation confirmed: when a `PreToolUse:AskUserQuestion` hook denies (`permissionDecision:"deny"` + reason, or exit 2), the tool never executes, no picker appears, the session does **not** idle, and there's **no error** — the model receives the denial as feedback and **continues**. _Caveat:_ because AskUserQuestion is the trained user-interaction channel, a bare denial can read as "user channel closed" → the model may give up. So the blocking hook **must** be paired with the prompt-forbid lever (autonomous-mode context: proceed with best judgment / record questions via `bd comment`). Design: a **ccpool launch flag** (autonomous mode) wires the blocking hook (reusable; aligns with Option 2); pr-pool sets it for autonomous workers. For autonomous workers the blocking-deny **supersedes** ccpool's non-blocking `ccpool hook ask` detection (`pg2-7a5b`); attended sessions keep detection. No race (PreToolUse fires before the picker).
- **D3 — `pg2-3msk` — RESOLVED: dedicated ccpool bead.** Created **`pg2-sjrl`** ("ccpool: add `--allowed-tools` passthrough flag", P1, label `ccpool`); `pg2-sjrl` **blocks** `pg2-3msk`.
- **D4 — `pg2-wgg0` — RE-GROOMED; one open question (precedence).** Re-grooming the live code found **most of the bead's confirmed AC already shipped**, and a **precedence conflict**:
  - Repo-local budget config **already works**: `[pool].budget.{tokens,cost,time}` in `<RepoRoot>/.pr-pool/config.toml` (or `$PR_POOL_CONFIG`) via `overlayConfigBudget` (`internal/config/registry.go:38-48,119,263-275`). Per-role `[role.ccpool].budget` overlay also already exists (`registry.go:82,221-222,247-262`).
  - **Genuine remaining delta = the XDG-global layer only**: `Load()` reads _only_ the repo-local path (`config.go:120`); there is **no** `$XDG_CONFIG_HOME/pr-pool/config.toml` global layer.
  - **PRECEDENCE — RESOLVED (Phillip, 2026-06-23):** keep the shipped precedence (file > env). Total order low→high: **`Default() < PR_POOL_* env < XDG-global file < repo-local file`** (files win over env; repo-local most specific). This supersedes the bead's stale "env > file" AC line. Bead AC re-groomed accordingly.

> **claude allowlist flag pinned:** `claude` accepts `--allowedTools`/`--allowed-tools <tools...>` (comma/space-separated, e.g. `"Bash(git *)"`), plus `--disallowedTools`/`--disallowed-tools`. ccpool uses kebab elsewhere → use `--allowed-tools`. (Verified via `claude --help`, 2026-06-23.)

### Detailed per-bead executable plans (written to `docs/superpowers/plans/`)

All 11 written 2026-06-23. ⚠ = needs a human decision before execution (see below).

| Plan file                                             | Bead                             | Wave           |
| ----------------------------------------------------- | -------------------------------- | -------------- |
| `2026-06-23-ccpool-allowed-tools-flag.md`             | `pg2-sjrl`                       | 1              |
| `2026-06-23-ccpool-registry-status-adoption.md`       | `pg2-oois.5` (closes `pg2-oois`) | 1              |
| `2026-06-23-pr-pool-deny-by-default-allowlist.md` ⚠   | `pg2-3msk`                       | 1              |
| `2026-06-23-pr-pool-worker-lost-nudge.md`             | `pg2-yukh`                       | 1              |
| `2026-06-23-pr-pool-needs-input-notify-teardown.md`   | `pg2-th35`                       | 2              |
| `2026-06-23-ccpool-structured-jsonl-logs.md`          | `pg2-yvnp`                       | 2              |
| `2026-06-23-pr-pool-example-toml-budget-roundtrip.md` | `pg2-yt0n`                       | 3              |
| `2026-06-23-pr-pool-xdg-global-budget.md`             | `pg2-wgg0`                       | 3              |
| `2026-06-23-autonomous-askuserquestion-block.md`      | `pg2-2f9d`                       | 3              |
| `2026-06-23-ccpool-session-metadata-search.md` ⚠      | `pg2-01ys`                       | 4              |
| `2026-06-23-pg-pr-zr-nix-cross-repo-build.md`         | `pg2-wtjz`                       | parallel track |

### Open decisions surfaced during detailed planning (resolve before executing the ⚠ plans)

- **`pg2-3msk` default allowlist (SECURITY sign-off).** Proposed default: `Read,Edit,Write,Glob,Grep` + scoped git verbs (**`push` excluded**) + `Bash(bd:*)` + Go/nix/prek build verbs. Open: keep `push` excluded? per-subcommand vs coarse `Bash(git *)`? build tools pool-wide vs per-role? verify the exact `--allowed-tools` matcher grammar at impl time. The rest of the plan is ready; only the allowlist literal needs sign-off.
- **`pg2-01ys` Option-2 "library" shape.** Reality check: pr-pool consumes ccpool **only via the CLI today** (it does not import ccpool; ccpool's `store` is under `internal/`). The plan delivers metadata/search as an **additive CLI/JSON contract** (non-breaking, lowest friction) with exported store methods for a _future_ in-process import. A _true_ Go-library import would require promoting the API out of `internal/` (bigger commitment). Confirm the CLI/JSON contract satisfies "Option 2", or ask for the out-of-`internal/` promotion.
- **`pg2-yukh` ingestion detector (low-stakes).** Designed around the transcript first-message signal (`claude-transcript.LastMessageActivity`, already imported), **not** hard-depending on `pg2-oois.5`'s registry signal — so the P1 isn't blocked on the registry work. Confirm (recommended) or require the registry signal. Default confirm-ingest window = 90s.
- **`pg2-yvnp` logSource path (minor).** Plan pins the source to `diagnostics.jsonl` (not a broad `ccpool/*.jsonl` glob) so the domain `events.jsonl` isn't swept into the diagnostics→severity pipeline. Confirm or widen.
- **`pg2-wtjz` module import (impl-time).** `modules/pg-pr-zr/default.nix` isn't imported into any machine config today; the executor confirms whether to add the machine import or treat the module as dormant. Also: `pg-pr-zr` is missing `go.sum`/`gomod2nix.toml` (plan generates them); producer change must land before consumer.

---

## Part 4 — Proposed execution sequence (waves)

Ordering respects dependencies, puts the **P1 safety/reliability** work first, and groups by repo so subagents don't collide. `pg2-wtjz` runs as an independent parallel track (different repos).

**Wave 0 — Decisions** (D1–D4) + write per-bead executable plans for the chosen scope.

**Wave 1 — ccpool foundations + P1s**

1. **C1 / `pg2-oois.5`** — ccpool registry-reader adoption _(closes epic `pg2-oois`; provides the signal Wave-1.3 needs)_.
2. **C2 / `pg2-sjrl`** — ccpool `--allowed-tools` flag → then **`pg2-3msk`** (pr-pool deny-by-default + allowlist) **[P1 security; `pg2-sjrl` blocks `pg2-3msk`]**.
3. **`pg2-yukh`** **[P1]** — ccpool `send.go` ingestion guard (C3, uses C1) + pr-pool per-bead worktree + bead-explicit/gated reminder.

**Wave 2 — P2** 4. **`pg2-th35`** — pr-pool needs_input notification + teardown survival. 5. **C4 / `pg2-yvnp`** — ccpool structured JSONL + `logSources` (hermetic; live Loki check flagged for operator).

**Wave 3 — P3 polish** 6. **`pg2-yt0n`** — example.go budget round-trip _(before/with #7; shared surface)_. 7. **`pg2-wgg0`** — XDG-global budget layer _(per D4)_. 8. **`pg2-2f9d`** — AskUserQuestion stall prevention _(per D2; C5 if ccpool)_.

**Wave 4 — committed (Option 2)** 9. **C6 / `pg2-01ys`** — session metadata/search, designed as a ccpool library API pr-pool consumes **[committed per D1]**.

**Parallel track (anytime):** **`pg2-wtjz`** — nix cross-repo build (agent-support exposes pg-pr source → ziprecruiter Pattern-B build).

---

## Live-verification gaps (cannot be fully closed by subagents)

- `pg2-yvnp` AC#4 — Loki query needs the running observability stack (manual runbook).
- `pg2-yukh` AC#4 — live dropped-prompt repro (deterministic regression test substitutes; live check manual).

## Beads whose text I recommend updating (stale vs code)

- `pg2-3msk` — file refs (`config.go:70`, `cli.go:61-63`) predate the PermissionMode migration; add the `--allowed-tools` finding.
- `pg2-wgg0` — grooming note "no TOML loading" is stale; record the shrunk scope.
- `pg2-01ys` — record that sub-feature (1) is subsumed (already in comments); confirm re-scope to metadata-only.
