# PR-pool triaging — session handoff & remaining work

**Date:** 2026-06-10
**Context:** The `pr-pool` work-triaging chunk was designed, planned, implemented (subagent-driven TDD), merged to `main`, and live-verified. This doc captures what's done, the live-verification findings, and all remaining/follow-up work.

---

## DONE (this session)

- **Spec:** `docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md`
- **Plan:** `docs/superpowers/plans/2026-06-09-pr-pool-work-triaging.md` (all task checkboxes ticked except the live-smoke box)
- **Code (merged to `main`; rebased SHAs):** `e5fa721` per-role config resolvers + worker nudge/label → `a5b3c25` role-aware `wait_done` (worker `needs-push` / `worker-stuck`) → `8e05fc9` `bd ready` discovery + per-role drain/teardown → `21caba3` `pg-pr-work-bead` worker SKILL → `6cf69c6` final-review fixes (pane `WORKSPACE_ROOT`, worker no-unclaim gate, +2 tests, treefmt).
- **Files:**
  - `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` (the triager)
  - `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` (39 tests, green)
  - `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` (worker contract)
  - feedback SKILL (unchanged): `…/skills/pg-pr-process-feedback/SKILL.md`
- **What it does:** mechanical 2-role triager over `bd ready` — `feedback-processor` for my open `process-feedback:` cycles, `worker` for beads labelled `worker-ready`; per-role config resolvers (`role_session/actor/skill/nudge/convo_name/max`); per-role caps (no cross-role starvation); role-aware completion (feedback closes+unclaims; worker completes on `needs-push`, stamps `worker-stuck` on failure, **never unclaims**); teardown of all role sessions. Worker SKILL: claim → bead-first PR/branch resolve (`$WORKSPACE_ROOT`, no `gh`) → author assert → reuse/create worktree → implement → **commit, no push** → record-then-swap `worker-ready`→`needs-push` → leave `in_progress`.

### Live verification results (against the real `zr` store)

- ✅ **Triager (read-only):** `precheck` + `discover` correct — 33 all-author `process-feedback:` cycles → 16 mine (author filter works); native `bd ready --label worker-ready`; wrong-workspace guards refuse loudly. `nix flake check` green.
- ✅ **Feedback path (cycle `zr-mi1x`, PR #93270):** full end-to-end — dispatched, claimed (`pgii-pool__process-feedback`), processed 17 feedback children, **dedup confirmed** ("7 actionable linked discovered-from to existing work beads zr-lweh.1–.5, no duplicates created", 10 non-actionable closed), cycle closed, clean teardown.
- ✅ **Worker path (bead `zr-lweh.4`):** dispatched into `WORKER` session, claimed (`pgii-pool__worker`), bead-first resolve + author assert, **correctly handled already-done work** (verified fix present at branch HEAD `ca587a1`/`a13e520`; no fake commit, no false `needs-push`, no close; recorded verification comment) — **no push** (remote head unchanged), clean teardown.

---

## REMAINING WORK

### P1 — Worker contract gap: no terminal signal for "already-done" work

**The finding.** When a `worker-ready` bead's work is _already done_ (nothing to commit), the worker has **no clean completion signal**: it can't legitimately swap to `needs-push` (no commit), and the SKILL says don't close → the orchestrator's `wait_done` (success == `needs-push` label) polls until `MAX_WAIT` and stamps `worker-stuck`. The agent itself flagged this as "the one spot where the standard protocol doesn't fit" and paused for human input.
**Fix to design+implement.** Add an "already-done / verified, nothing-to-commit" terminal action. Options: a distinct label (e.g. `verified`/`already-done`) that `done_signal worker` also treats as success; or allow the worker to close-with-reason when verifiably complete on-branch. Touches `pr-pool.sh` (`done_signal`/`wait_done`) + the worker SKILL + new bats. Use brainstorming→plan→TDD.

### P1 — Worker commit→`needs-push` HAPPY path never exercised live

The worker smoke target (`zr-lweh.4`) was already-fixed, so the real **worktree-create → implement → commit (no push) → swap `worker-ready`→`needs-push`** path was never run. Re-run the worker on a bead with **genuine undone work** (inspect `zr-lweh.1/.2/.3/.5`, or another PR's work beads). NOTE: live smoke mutates real `zr` beads + creates a worktree → **confirm the target bead with the user first**.

### P2 — Clean up `zr-lweh.4`

Left `in_progress` + `worker-ready` + verification comment (claimed by `pgii-pool__worker`); work is verifiably done (`ca587a1`). Likely just close it: `bd close zr-lweh.4 --reason="…verified fixed in ca587a1"` (run from monorepo root, env-scrubbed).

### P2 — pg-pr-sync daemon: `context canceled (is bd/gh on PATH?)` warnings

`~/Library/Logs/pg-pr-sync.err` has recurring `WARN "refresh failed … create feedback: bd create … context canceled (is bd on PATH?)"` and `"team fingerprint poll failed … (is gh on PATH?)"`. Daemon launchd `PATH` is just `/usr/bin:/bin:/usr/sbin:/sbin` (no nix bins). BUT the gh auth preflight succeeds and feedback beads _have_ been created, and the errors cluster around the ~15:23 restart → reads as transient restart cancellations, not a hard PATH break. **Confirm**: are these benign (restart timing) or is `bd`/`gh` intermittently off the daemon's PATH (→ producer intermittently fails to create feedback)? Separate from the gh-auth change.

### P2 — ccpool changes not fully verified (the user's parallel work; deployed via `apply`)

- `5caf580` ccpool `--cwd`/`default_cwd` + interspersed-flag parse + tmux `-c` + cwd canonicalize (folder-trust). **`ccpool list` runs clean**, but the **cwd/folder-trust behavior was not driven live** — needs `ccpool new --cwd <safe-dir>` (launches a real claude session).
- `ca78db1` exempt `ccpool-reap` from the launchd health check. `com.phillipg.ccpool-reap` is registered; the exemption _mechanism_ wasn't deep-verified.
- pg-pr gh-auth (`092b6d4` `CheckAuth`+GH_TOKEN, `45021f1` startup preflight) — **verified PASS** (`pg-pr auth status` → github OK w/ real scopes, exit 1 on jira-missing; daemon logs `"gh auth preflight ok"`).

### P3 — Deferred from the triaging spec (future chunks)

- **Epic/PR-gluing** of worker session names (`"WORKER: epic: <id>"` / `"WORKER: PR #<n>"`); `role_session` is already a function so it's additive. (Needed before per-role cap >1 — two `worker-ready` beads on the same branch would collide on `git worktree add`.)
- **N>1 per-role pool**, idle-timeout/watchdog, **daemonization**, and the **Go rewrite**. ⚠️ **Clarify the `pr-pool.sh` ↔ `ccpool` relationship** — `packages/ccpool/` (Go) now exists and may BE the intended Go successor; if so, the bash triager's per-role config table + `wait_done` semantics should port to it.
- **Auto-applying `worker-ready`** (currently manual by the user).
- **Worker push** (currently commit-only; human reviews+pushes).
- **Triager-side ownership walk for the worker route** (currently trusts the label; worker SKILL has a belt-and-suspenders author assert).
- **pg-pr producer correctness** — reliable cycle reuse + PR-close cascade; also the observed **duplicate cycles per PR** (`zr-mi1x` + `zr-zfcv` both for #93270) — separate producer ticket.
- **Explicit producer-stamped routing field** to replace the heuristic classification.

### Friction / notes worth fixing

- **No `--dry-run`/`--discover-only` flag** on `pr-pool.sh` — to observe the triager you must source the script with the final `main "$@"` stripped (`sed '$d'`). A dry-run flag (print `discover` output and exit) would help ops + verification.
- **`claude` on PATH is a cmux wrapper** (`…/cmux.app/…/bin/claude`) that injects `--settings <hooks json>` when `CMUX_SURFACE_ID` is set (source of the "node: command not found" pane-hook noise) and takes ~1–2 min to boot. It correctly skips a duplicate `--session-id`. Environment dependency; works, but note it.

---

## Critical context / conventions for the next agent

- **Tracking:** use `bd` (beads) and the plan's checkboxes — **NOT TodoWrite** (per CLAUDE.md). `bd remember` for persistent knowledge.
- **Tests:** `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`. Authoritative gate: **`nix flake check`** — its `treefmt-check` is stricter than the pre-commit `treefmt` hook, so run **`nix fmt -- <files>`** to satisfy it. **Don't trust `cmd | tail` exit codes** (the pipe masks them) — run unmasked (`cmd; echo $?`).
- **Repo state:** work is on `main` (now at `ca78db1`, carrying unrelated ccpool/pg-pr commits on top). The `phillipg.pr-pool-triaging` worktree was removed. The main checkout's index has unrelated `.gc/**` WIP — **path-scope every commit** (`git commit -m … -- <paths>`).
- **Live runs mutate real `zr` beads** (gas-city-managed dolt server) — **confirm the target with the user first**. Run from `/Volumes/ziprecruiter/monorepo` with `env -u BEADS_DIR -u WORKSPACE_ROOT` (or rely on `pr-pool.sh`'s top-level `unset BEADS_DIR WORKSPACE_ROOT`). `self_login = phillipgziprecruiter`.
- **Hard constraints (unchanged):** no writes to `~/gc`, no `gc` commands, own `-L pgpool` tmux socket. Bash must stay 3.2-compatible (`set -uo pipefail`).
- **Smoke driver scripts from this session:** `/tmp/smoke-feedback-zr-mi1x.sh`, `/tmp/smoke-worker-zr-lweh4.sh`, `/tmp/verify-pr-pool-discover.sh` (templates for re-running smokes; they source the applied `pr-pool.sh` with `main` stripped + a background pane-capture loop).
