# pg-pr Throughput / Dashboard — Handoff (2026-06-11)

**Goal that is still not confirmed met:** `/api/v1/dashboard` (`127.0.0.1:9818`) populates `mine`/`team` and `generated_at` advances. The daemon detects changed PRs fine but per-PR **refreshes were not completing**, so the dashboard stayed empty.

**Status:** A stack of fixes is merged to `main` (UNPUSHED) but **not yet rebuilt/verified live**. The last live measurement (before the final dedup fix `b43de51`) still showed **0 completed refreshes** — that measurement predates the fix that targets the actual bottleneck. **Rebuild and re-measure is the immediate next action.**

---

## TL;DR of the root-cause story (important — read this)

Per-PR refresh throughput collapsed under the bd/dolt backend. Root cause was found in layers:

1. The per-PR path dropped the bulk fetch → re-ran workspace-wide bd work per PR. (Fixed: async maintenance goroutine + fetch-enrichment-once + threaded bead id.)
2. The **dominant** cost was `processFeedback`'s feedback **dedup**: per upstream event it called `findFeedbackForPR` → `ListChildrenOfPR` + per-cycle `ListFeedback`, and `ListFeedback(cycleID, …)` internally does a **per-bead `isChildOf` (`bd dep list`) scan over every feedback bead in the workspace** (`F = 475` live). So dedup was `O(events × cycles × F)` bd subprocesses per PR. (Fixed in two steps: hoisted dedup out of the per-event loop, then replaced it with a single scoped `bd dep tree <pr> --direction=up --json` via `beads.Client.PRFeedbackFingerprints`.)
3. A concurrent change (`d33eb0b`, not ours) repointed beads off the slow `/Volumes/ziprecruiter/monorepo/.beads` mount onto the local nix dolt server (`127.0.0.1:25252`, `~/.local/share/beads-dolt`) — bd calls are now fast individually.

### ⚠️ Most likely remaining bottleneck (check this first if still slow)

`processFeedback`'s **first pass** (the CI-failure→success resolver) still calls `bdc.ListFeedback(ctx, cycleID, false)` **unconditionally when an open processing-cycle exists** (`internal/sync/sync.go` ~line 1389-1395, the `cache == nil` branch). That `ListFeedback(cycleID)` is the SAME `O(F)` per-bead `isChildOf` scan — so **every** refresh of a PR with an open cycle still pays one ~475-call `bd dep list` fan-out, even after the dedup fix.

Recommended fix (if live re-measurement shows refreshes still not completing):

- **Cheap + high-value:** only run the first pass when there are CI-success events. The resolver only uses `open` to close prior `ci-failure` feedback, so for comment-only refreshes (the common case) skip the `ListFeedback(cycleID)` call entirely:
  ```go
  hasCISuccess := false
  for _, ev := range events {
      if ev.kind == beads.FeedbackKindCIFailure && ev.ciConclusion == "success" { hasCISuccess = true; break }
  }
  if found && hasCISuccess { /* existing first-pass body */ }
  ```
- **Better/unified:** the single `bd dep tree <pr> --direction=up --json` already returns every feedback node with `status` + `metadata` (`fingerprint`, `external_id`, `kind`). One dep-tree call could serve BOTH passes — the first-pass needs the cycle's _open_ feedback (status != closed) with `external_id`; the second-pass needs all fingerprints. Extend `PRFeedbackFingerprints` into a `PRFeedbackSnapshot(prBeadID)` returning `[]Feedback` (or both the fingerprint set and the open list) and have `processFeedback` consult it for both passes → **zero** `O(F)` scans per refresh.

---

## What's DONE (merged to `main`, UNPUSHED)

Repo `~/phillipg_mbp/phillipgreenii-nix-agent-support`, branch `main`:

- **Throughput restructure** (`46cab41`, `8f465e0`, `f873a17`, `9c6192d`, `07d27c9`, `e4b74a6`): per-repo `human`-label atomic cache + async maintenance goroutine (label refresh + reply-draft drain off the critical path); `buildPRInput` reads the cached label set + takes `knownMRID`; reply-drain moved out of `applyFetchedPR`; `refreshPR` fetches enrichment once and threads the upserted bead id. Spec/plan: `docs/superpowers/specs/2026-06-10-per-pr-refresh-throughput-design.md`, `docs/superpowers/plans/2026-06-10-per-pr-refresh-throughput.md`.
- **Observability** (`52abe39`): `runSnapshotOwner` sets `pg_pr_snapshot_present=1`; `runWorker` increments `pg_pr_sync_errors_total{repo}` on refresh failure (shutdown-guarded); `seedDaemonMetricSeries` 0-initializes `sync_errors`/`fingerprint_poll_truncated` so panels read flat-zero not "no data".
- **Feedback dedup — hoist** (`c98dbd1`): built the PR fingerprint set once per refresh instead of per event.
- **Feedback dedup — scoped dep tree** (`229c53e`, `b43de51`): `beads.Client.PRFeedbackFingerprints(prBeadID)` = one `bd dep tree <pr> --direction=up --json`, decoded via `parseBDList`, `issue_type=="feedback"` → `metadata.fingerprint`; daemon `existingFeedbackFingerprints` routes through it via a `feedbackFingerprinter` type-assert. Spec/plan: `docs/superpowers/specs/2026-06-11-pr-scoped-feedback-dedup-design.md`, `docs/superpowers/plans/2026-06-11-pr-scoped-feedback-dedup.md`.

Repo `~/phillipg_mbp/phillipgreenii-nix-support-apps`, branch `main` (`11ca5cb`, UNPUSHED): Ops dashboard `pg-pr-ops.json` — `Refresh queue depth` legend `{{group}}`, `GraphQL rate remaining` legend `{{job}}`, drop "(by group)" from the fingerprint-freshness title, `version` bump.

All Go work: TDD, `go build`/`go vet`/`prek` (gofmt+golangci-lint) green; targeted + real-bd `Feedback` regression pass. Full `internal/sync` suite NOT run end-to-end — `TestSync_CreatesBeadsForObservedPRs` hangs on real bd and **pre-dates** this work (do not attribute it to these changes).

---

## Immediate next steps

1. **`darwin-rebuild switch`** to deploy the new `pg-pr-sync` binary (currently the running binary predates `b43de51`). This should also re-bootstrap the launchd agent if it's unloaded (see Gotchas).
2. **Re-measure live** (cheat-sheet below). Expect: `refresh_queue_depth` draining, `pg_pr_sync_pr_duration_seconds_count` climbing, `snapshot_present` → 1, `mine`/`team` populating, `generated_at` advancing.
3. **If refreshes still don't complete:** apply the first-pass `ListFeedback(cycleID)` fix above (most likely culprit). Then re-measure. Use the superpowers brainstorming→plan→subagent flow if it grows beyond the one-liner.
4. **Push** `main` on both repos when satisfied (user's call — both are ~unpushed-ahead).

## Live verification cheat-sheet

```bash
ps aux | grep '[p]g-pr sync --daemon'        # binary path + --interval (expect 1m)
curl -s 127.0.0.1:9818/api/v1/dashboard | jq '{mine:(.mine|length),team:(.team|length),generated_at,sync_interval_seconds}'
curl -s 127.0.0.1:9818/metrics | grep -E 'pg_pr_(refresh_queue_depth|sync_pr_duration_seconds_count|snapshot_present|sync_errors_total|fingerprint_poll_truncated)'
tail -40 ~/Library/Logs/pg-pr-sync.err       # logs go to .err, not .log
# watch the daemon's live bd children (what it's spending calls on):
pid=$(pgrep -f '[p]g-pr sync --daemon'); ps -axo ppid,etime,command | awk -v p=$pid '$1==p'
```

If you need to inspect the monorepo beads directly (it's now on the nix dolt server):

```bash
cd /Volumes/ziprecruiter/monorepo
env -u BEADS_DIR -u WORKSPACE_ROOT bd <cmd>   # env -u avoids .envrc leak; bd resolves the .beads workspace here
```

---

## Gotchas / workspace facts

- **Two repos, both `main` unpushed:** `phillipgreenii-nix-agent-support` (the Go daemon) and `phillipgreenii-nix-support-apps` (the Grafana dashboard JSON). Branches here **auto-merge to `main`** on apply/deploy; this session merged each fix to `main` via ff after review.
- **gascity background agents** hammer the bd/dolt backend continuously — every pg-pr `bd` call is contended. The `/Volumes`→local dolt repoint (`d33eb0b`) helped a lot; if `/Volumes/ziprecruiter/monorepo/.beads/dolt` shows up as a running `dolt sql-server` again, beads may have drifted back to the slow mount.
- **The expensive bd primitive:** `ListFeedback(cycleID, …)` with a non-empty `cycleID` runs `bd list --type=feedback [--all]` then a per-bead `isChildOf` = `bd dep list <fb>` for EVERY feedback bead in the workspace (`pkg/beads/feedback.go:202-228`, `pkg/beads/processingcycle.go:130`). Avoid it on the hot path; prefer the scoped `bd dep tree <pr> --direction=up --json` (one call, returns the subtree with per-node `metadata`). `PRFeedbackFingerprints` (`pkg/beads/deptree.go`) is the template.
- **LSP diagnostics are frequently STALE** here (worse after worktree removal / branch switches) — they will report phantom "undefined method" / "wrong arg count". Trust `go build` / `go vet` / `go test`, never the diagnostics.
- **pre-commit hook reformats:** committing markdown/Go often aborts the first attempt ("files were modified by this hook"); just re-`git add` the same files and `git commit` again.
- **The full `internal/sync` test suite is slow** (real bd, 30+ min) and `TestSync_CreatesBeadsForObservedPRs` hangs (pre-existing). Iterate with targeted `-run`; the real-bd `-run Feedback` subset (~3 min) covers the dedup path.
- **launchd:** the service is `org.nixos.pg-pr-sync` (`~/Library/LaunchAgents/org.nixos.pg-pr-sync.plist`, `KeepAlive=true`, `RunAtLoad=true`). Earlier this session it got booted out by a rebuild activation and didn't re-bootstrap on its own (`launchctl print gui/$(id -u)/org.nixos.pg-pr-sync` → 502). If after a rebuild nothing is on 9818, load it: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/org.nixos.pg-pr-sync.plist`.
- **bd tracking issue:** `nas-64m` (prefix `nas`, the nix-agent-support beads workspace) tracks this work; update it with `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support && env -u BEADS_DIR -u WORKSPACE_ROOT bd update nas-64m ...` (occasionally errors with a bootstrap hint — retry).

---

## Next-agent kickoff prompt

> You're continuing the **pg-pr per-PR refresh throughput** work in `~/phillipg_mbp/phillipgreenii-nix-agent-support`. The dashboard (`127.0.0.1:9818/api/v1/dashboard`) still needs to be confirmed populating live.
>
> **Read `docs/superpowers/2026-06-11-pg-pr-throughput-handoff.md` first** — full state, commits, the root-cause story, the most-likely remaining bottleneck, gotchas, and the live cheat-sheet.
>
> A stack of throughput + feedback-dedup + observability fixes is merged to `main` (UNPUSHED) but **not yet rebuilt/verified live**. **First: `darwin-rebuild switch`, then re-measure** (cheat-sheet in the doc): `refresh_queue_depth` draining, `pg_pr_sync_pr_duration_seconds_count` climbing, `snapshot_present`→1, `mine`/`team` populating, `generated_at` advancing.
>
> **If refreshes still don't complete:** the prime suspect is `processFeedback`'s first pass still calling `ListFeedback(cycleID, false)` (an `O(workspace-feedback)` `isChildOf` scan) unconditionally when an open cycle exists — see the doc's "Most likely remaining bottleneck" section for the two fix options (skip when no CI-success events; or unify both passes onto the single `bd dep tree` call). Use the superpowers brainstorming→writing-plans→subagent-driven-development flow for any non-trivial change, and verify on the live daemon.
>
> Heads-up: gascity agents contend on the same dolt backend; `main` is unpushed on both `phillipgreenii-nix-agent-support` and `phillipgreenii-nix-support-apps`; LSP diagnostics are stale (trust `go build`/`go test`); the launchd agent `org.nixos.pg-pr-sync` may need re-bootstrapping after a rebuild.
