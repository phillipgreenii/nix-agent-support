# pg-pr fingerprint-driven daemon sync

**Status**: Accepted
**Date**: 2026-06-09
**Deciders**: Phillip Green II

## Context

`pg-pr sync --daemon` polls GitHub on a fixed interval (`5m` in the
`pg-pr-sync` launchd service) and does a **full pull** of every watched PR each
iteration: enumerate PRs per repo via a GraphQL search, enrich each
(reviews/comments/CI), upsert a merge-request bead, run the feedback pipeline,
and rebuild the `/api/v1/dashboard` snapshot.

The dashboard is therefore stale for up to the full interval after any PR
changes. Shrinking the interval is not viable — the full pull is expensive
(enrichment fan-out plus bead writes for every PR, every tick), so running it
each minute would multiply API and `bd` load for mostly-unchanged data.

Forces:

- We want ~1-minute dashboard freshness.
- `pg-pr` runs as a **localhost-only** launchd user agent — it cannot receive
  inbound webhooks without new public infrastructure.
- GitHub's cheap change signals are imperfect: `updated_at` does not reliably
  move on commit pushes, reviews, review comments, or CI changes; the Events
  API is 30s–6h stale and omits CI; the Notifications API only covers
  subscribed threads.
- A single GraphQL "fingerprint" query can return, for ~1 rate-limit point, a
  per-PR signature (`updatedAt` + last commit `oid` + CI rollup state + review
  /comment/thread counts + `isDraft` + `state`) for all watched PRs — and the
  commit `oid` + CI rollup close exactly the gaps `updated_at` misses.

## Decision

Replace the daemon's periodic full-sync with a **two-tier, fingerprint-driven**
model:

1. **Tier 1 — fingerprint poll (~60s), a pure detector.** Two slim, paginated
   GraphQL fingerprint queries (mine cross-repo; team per-repo) compute each
   open PR's signature. The detector diffs the fresh roster against the previous
   tick's hashes (for added/changed) and against the open merge-request beads
   (for disappeared). It **mutates nothing** — it only enqueues `(repo, number)`
   keys onto two dedup FIFO queues.
2. **Tier 2 — targeted refresh.** Two workers drain the queues and call a
   per-PR refresh (`refreshPR`, built on the existing single-PR bead path plus
   per-PR enrichment + dep tree) that does the expensive work for the changed
   PR only. A single snapshot-owner goroutine rebuilds and `Set`s the dashboard
   snapshot after each PR. **The worker is the sole authority that closes or
   removes a PR, and it decides from the PR's actual `GetPR` state.**

Removal detection has no separate reconciliation cadence: it falls out of the
per-tick "open bead not in the roster" comparison, which also catches PRs that
closed while the daemon was down (caught on the first tick). A transient query
error or pagination truncation marks that `(repo, group)` incomplete and skips
the disappeared check, so it can never mass-close beads.

The one-shot `pg-pr sync` (non-daemon) command keeps the full `Engine.Sync`.

## Consequences

### Positive

- Dashboard reflects a change within ~1 tick (~60s), not up to 5 minutes.
- Steady-state cost is two-ish cheap GraphQL queries per minute (~`1+R` points)
  plus per-PR refreshes only for PRs that actually changed.
- New-PR, closed/merged, and draft transitions all flow through one per-tick
  diff — fewer moving parts than a full-sync-plus-reconciliation design.

### Negative

- More daemon concurrency (detector + two workers + a snapshot-owner
  goroutine), so engine config must move behind an `atomic.Pointer` to keep
  SIGHUP reload race-free, and shutdown must drain workers cleanly.
- The fingerprint query needs real cursor pagination (the existing enrich query
  has none), with a truncation flag wired into the disappeared-check guard.
- The dashboard snapshot becomes incrementally maintained per PR rather than
  rebuilt wholesale; it must rebuild from a deterministically sorted set to
  avoid row churn.
- Metrics change: `pg_pr_last_sync_success_timestamp_seconds` is retired (no
  whole-repo sync), `pg_pr_sync_pr_duration_seconds` gains a `group` label
  (resetting that series), and fingerprint/queue/graphql metrics are added; the
  Ops Grafana dashboard must be updated in lockstep.

### Behavior change

- A teammate's PR that goes to **draft** is dropped from the dashboard and its
  feedback pipeline **pauses** (the merge-request bead is kept and marked
  draft, existing feedback/cycle children left open). Today team drafts run the
  full pipeline. Your **own** drafts are unaffected (still synced and shown).

### Neutral

- Change detection stays **in-memory** (previous-tick roster), so a daemon
  restart re-enqueues all active PRs once — which is needed anyway to repopulate
  the in-memory snapshot.

## Alternatives Considered

### Webhooks (GitHub App / repo / org)

Instant push, zero polling cost, complete coverage. Rejected: requires a
publicly reachable receiver; `pg-pr` is a localhost-only daemon, and a
smee/serverless bridge is new infrastructure out of scope for a personal tool.

### `updated_at` polling

Cheapest possible signal, but misses commit pushes, reviews, review comments,
and CI changes. Rejected as a sole trigger; the fingerprint deliberately
includes the commit `oid` and CI rollup to cover these.

### Events API / Notifications API

Events run 30s–6h stale and omit CI; Notifications cover only subscribed
threads. Neither is a complete, low-latency, repo-wide signal.

### Conditional REST (ETag → 304)

Free on a `304`, but a sub-resource change (commit/review/CI) does not reliably
bump the parent PR's ETag, so it would require many per-sub-resource conditional
polls. One GraphQL fingerprint is simpler and complete.

### Fingerprints persisted on beads

Would make change detection restart-stateless. Rejected for now: the in-memory
snapshot must repopulate on startup regardless (forcing a first-tick full
enqueue), so persisting fingerprints adds a bead-schema change for little gain.

### Simply shortening the full-sync interval

Rejected: the full pull is too expensive to run each minute (enrichment +
per-PR bead writes for every PR, mostly unchanged).

## Related Decisions

See also: phillipgreenii-nix-agent-support docs/adr/0009-pg-pr-bead-schema.md
See also: phillipgreenii-nix-agent-support docs/superpowers/specs/2026-06-09-fingerprint-driven-sync-design.md
See also: phillipgreenii-nix-agent-support docs/superpowers/plans/2026-06-09-fingerprint-driven-sync.md
