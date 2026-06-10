# pg-pr Fingerprint-Driven Sync — Remaining Work & Handoff

**Date:** 2026-06-10
**Status:** Feature + gh-auth fix implemented and merged to `main`; **the dashboard still does not populate in production** due to a refresh-throughput problem (P0 below).

---

## TL;DR of current state

- The fingerprint-driven daemon (`pg-pr sync --daemon`) is **deployed and running** (launchd `org.nixos.pg-pr-sync`, pid varies; serves `/metrics` + `/api/v1/dashboard` on `127.0.0.1:9818`).
- The **gh-auth keychain bug is fixed and verified live** — no more `HTTP 401`s; `pg-pr` now injects `GH_TOKEN` per `gh` call + runs a startup preflight + restart-to-refresh escalation.
- **The dashboard is empty** (`mine=0 team=0`, `generated_at` frozen). Root cause is **not** auth — it's per-PR refresh **throughput collapse** (P0).
- The fingerprint **poll cadence** was stuck at `5m` (machine-config override); fixed to `1m` in commit `057a803` but **not yet rebuilt/applied**.

## What's DONE (don't redo)

- Design: `docs/superpowers/specs/2026-06-09-fingerprint-driven-sync-design.md`
- Plan: `docs/superpowers/plans/2026-06-09-fingerprint-driven-sync.md`
- ADR: `docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md`
- Feature (13 tasks, TDD, two reviews, full `go test ./...` green incl. bd integration): commits `04fdd1d`..`b300f5e` on `main` (`phillipgreenii-nix-agent-support`).
- gh-auth fix (token injection + `vcs.ErrAuthInvalid` + `CheckAuth` preflight + K=3 restart-to-refresh escalation + `pg_pr_gh_auth_failures_total`): commits `092b6d4`, `45021f1` on `main`.
- Interval default `1m` (`bdca110`) + machine-config override `1m` (`057a803`) on `phillipg-nix-ziprecruiter` `main`.

> **All of the above is committed but UNPUSHED** on `main` of both `phillipgreenii-nix-agent-support` and `phillipg-nix-ziprecruiter`.

---

## Remaining work

### P0 — Rebuild & verify the 1m interval (mechanical)

- `darwin-rebuild switch` so the regenerated `pg-pr-sync` wrapper carries `--interval 1m` (commit `057a803` is committed but not yet built into the running wrapper).
- Verify on the live daemon: `ps aux | grep '[p]g-pr sync --daemon'` shows `--interval 1m`, and `curl -s 127.0.0.1:9818/api/v1/dashboard | jq .sync_interval_seconds` is `60`.

### P0 — Fix refresh throughput collapse (the real blocker; needs design)

**Problem:** the daemon's two workers call `Engine.refreshPR` per changed PR, and `refreshPR` → `buildPRInput` fans out to **~10+ sequential `bd` calls per PR** (`EnsureMergeRequest`, `processFeedback` find/list/create, `FindByRepoAndNumber`, `DepTreeUp`, `HumanLabeledBeads`, `processReplyDrafts`) plus per-PR REST `gh` calls. Each `bd` invocation is ~1.6s baseline (worse under the concurrent dolt load from the gascity agents — see Gotchas), so throughput is ~**2 PRs in 14 min**. With ~24 PRs enqueued on cold start, the queue never drains and the dashboard never populates.

**Evidence (live):** `pg_pr_refresh_enqueued_total` mine=9/team=15 vs `pg_pr_sync_pr_duration_seconds_count` = 2; `pg_pr_refresh_queue_depth` mine=8/team=10 not draining; `generated_at` frozen; daemon children are live `bd dep list`/`bd list --type=merge-request`; `bd --version` alone = 1.6s.

**Why:** this is exactly the cost the design review flagged (finding "B1"). The old `Engine.Sync` (full sync) amortized cost via **one bulk `EnrichedPRs` GraphQL call + one `TickCache` bd bulk-load per repo** shared across _all_ PRs. The new per-PR refresh path **dropped that bulk fetch**, so the per-PR `bd`/`gh` fan-out dominates.

**Proposed direction (brainstorm/spec it — don't just code):** when a fingerprint tick produces a batch of changed PRs, do a **batched refresh** instead of per-PR:

- one `vcs.EnrichedPRs` GraphQL call scoped to the changed PR numbers (reuse `tryEnumerateEnriched`/`buildEnrichedSearchQuery`),
- one `beads.TickCache` bulk-load per repo workspace (reuse `LoadTickCache`),
- then process each changed PR from the cache (as `buildAndStoreSnapshot`/`Sync` already do), upserting snapshot entries.
- Consider whether the two-tier design should keep per-PR queues at all, or collapse to "fingerprint tick → bulk-refresh the changed set." Revisit the worker model.

**Key files:** `packages/pg-pr/internal/sync/refresh.go` (`refreshPR`), `internal/sync/sync.go` (`buildPRInput`, and the existing bulk path: `tryEnumerateEnriched`, `EnrichedPRs`, `LoadTickCache`, `buildAndStoreSnapshot`), `internal/sync/daemon.go` (`fingerprintTick` enqueues; `runWorker` drains), `internal/sync/snapshotowner.go`.

**Constraints (settled during brainstorm — don't re-litigate):** localhost-only daemon (no webhooks); two-tier fingerprint detector→refresh; drafts included in the fingerprint queries (refreshPR decides dormant-vs-active from `GetPR` state); change detection in-memory (`prevMine`/`prevTeam`), not bead-stored.

### P2 — `pg_pr_snapshot_present` always 0 (cosmetic)

The new `runSnapshotOwner` never calls `telemetry.SnapshotPresent.Set(1)` (only the retired `buildAndStoreSnapshot` did), so the Ops dashboard "snapshot present" tile reads 0. One-liner: set it to 1 after the first successful `store.Set` in `internal/sync/snapshotowner.go`.

### Housekeeping

- **Push** `main` on both repos when ready (currently ahead of `origin/main`, unpushed). User's call.
- Optionally an ADR for the gh-auth restart-to-refresh design (next number after 0012; OTLP-logs already took 0013).

---

## Gotchas / workspace facts (will save you time)

- **gascity background agents** (`pgii-gastown.mayor`/`.deacon` claude sessions) run continuously and hammer the **same dolt/bd backend**, so every `bd` call pg-pr makes is contended/slow. This aggravates the P0 throughput problem and is largely outside pg-pr's control.
- **Branches auto-merge to `main`** here (the apply/deploy step merges feature branches into `main` and deletes them). A "missing" feature branch is usually merged, not lost — check `git merge-base --is-ancestor <commit> main`.
- When dispatching **implementer subagents**, pin the branch explicitly ("you are on branch X; do not `git checkout`/create branches") — they otherwise create a feature branch off `main`.
- The `internal/sync` **test suite is slow** (real `bd`+dolt, 30+ min). For iteration, run targeted fast tests (`-run TestX`) + `go build ./...` + `go vet ./...`; run the full suite once before merging. Editor/LSP **diagnostics are frequently stale** after subagent edits — verify with real `go build`/`go test`, not the diagnostics.
- Run git pathspecs from the repo root with the full `packages/pg-pr/...` prefix.

## Live verification cheat-sheet

```bash
# is it running, with what interval / binary?
ps aux | grep '[p]g-pr sync --daemon'
# dashboard (the goal: non-empty mine/team, advancing generated_at)
curl -s 127.0.0.1:9818/api/v1/dashboard | jq '{mine:(.mine|length),team:(.team|length),generated_at,sync_interval_seconds}'
# metrics: auth health + throughput
curl -s 127.0.0.1:9818/metrics | grep -E 'pg_pr_(gh_auth_failures|refresh_enqueued|sync_pr_duration_seconds_count|refresh_queue_depth|fingerprint_poll)'
# daemon log (NOTE: logs go to .err, not .log)
tail -30 ~/Library/Logs/pg-pr-sync.err
```

---

## Next-agent kickoff prompt

> You're picking up the **pg-pr fingerprint-driven sync** work in `~/phillipg_mbp/phillipgreenii-nix-agent-support`. The feature and a gh-auth fix are implemented and merged to `main`, and the auth fix is verified working live — but **`/api/v1/dashboard` still doesn't populate**.
>
> **Read `docs/superpowers/2026-06-10-pg-pr-fingerprint-remaining-work.md` first** — it has the full state, open items, key files, commits, and workspace gotchas.
>
> **Top priority (P0): fix the per-PR refresh throughput collapse.** The daemon's workers do ~10+ slow `bd` calls per PR (the per-PR path dropped the old bulk `EnrichedPRs` + `TickCache` fetch), so the refresh queue never drains and the dashboard stays empty (~2 PRs/14min). Use the superpowers **brainstorming → writing-plans → subagent-driven-development** flow to design and implement a **batched refresh of the changed PR set** (one `EnrichedPRs` GraphQL + one `TickCache` bd bulk-load per tick, as `Engine.Sync` does) rather than per-PR fan-out. Then **verify on the live daemon** (`org.nixos.pg-pr-sync`, `127.0.0.1:9818`) that the dashboard populates and `generated_at` advances.
>
> Also pending: rebuild to pick up the `1m` interval (commit `057a803`, committed not built) and verify `sync_interval_seconds=60`; and the one-line `pg_pr_snapshot_present` fix.
>
> Heads-up: gascity background agents contend on the same dolt/bd backend (slow `bd`); `main` is unpushed; pin the branch when dispatching implementer subagents; LSP diagnostics are often stale (trust `go build`/`go test`).
