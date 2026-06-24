# Session metadata at dispatch (ccpool `new --meta`) + pr-pool consumption — Design

**Status**: Draft (awaiting review)
**Date**: 2026-06-24
**Deciders**: Phillip Green II
**Beads**: `pg2-87ly` (ccpool, blocker) → `pg2-5o5i` (pr-pool, the Option-2 payoff)

> Brainstorming output. The executable, task-by-task plans are produced separately
> (writing-plans) into `docs/superpowers/plans/`, one per bead.

---

## Context

`pg2-01ys` delivered ccpool's session-metadata foundation:

- `session_metadata` table (migration `006_session_metadata.sql`): one row per
  `(external_id, key)`, keyed to the caller's stable `external_id` handle (ADR 0015).
- The public `github.com/phillipgreenii/ccpool/sessionmeta` library
  (`Open`/`OpenPool`/`Set`/`Get`/`Meta`/`Delete`/`ListByMeta`) — the only exported
  ccpool Go API.
- The `ccpool meta set/get/list/rm` CLI + `ccpool list --filter k=v`.
- Cascade cleanup: `store.Delete` deletes a session's metadata rows
  (`internal/store/ops.go`), so `ccpool close --purge` leaves no orphans.
- pr-pool already depends on ccpool (`go.mod` require + `replace => ../ccpool`,
  gomod2nix Pattern B in `packages/pr-pool/default.nix`) and a smoke test proves it
  can import and call `sessionmeta` in-process.

`pg2-5o5i` is the actual payoff: make pr-pool **use** metadata to track each
dispatched session's bead/role/pool instead of relying on the implicit encoding in
the session name. Brainstorming surfaced two things that reshape the work:

1. **There is no rich hand-rolled bookkeeping to "replace."** pr-pool's drain is
   sequential and stateless across dispatches (`orchestrator.go:119-142`); cap is a
   local counter, not a live-session count. bead/role/pool exist today only as an
   _implicit convention_ inside the session name
   `ExternalID = <prefix><role>-<bead>-<stamp>` (`roles.go:48`) — nothing ever parses
   them back out. So the honest deliverable is "record bead/role/pool as first-class
   queryable metadata and add a reader that uses it," not "rip out a registry."

2. **The metadata model needs a clear scope/lifecycle contract** (the bulk of the
   design below), and **writing metadata must be atomic with dispatch** — which
   ccpool does not yet support, hence the `pg2-87ly` blocker.

---

## The metadata model

Two tiers. Only the second is built now.

### Session metadata (caller-facing — built)

- **Addressed via `external_id`** always; the internal keying (external_id vs
  `claude_session_id`) is an implementation detail.
- **Lifecycle is tied to the Claude session.** ccpool internally couples
  `external_id` + metadata + `claude_session_id`. Concretely:
  - Metadata **survives** a tmux shutdown and a **resume** — ccpool relaunches with
    `--resume <claude_session_id>` under the same `external_id` (`session.go:244`),
    keeping the same Claude session, so the metadata set "today" is still there
    "tomorrow" under that handle.
  - Metadata is **removed when the Claude session is removed** (purge), via the
    existing `store.Delete` cascade.
  - A **fresh** Claude session launched under a **reused** `external_id` (the
    not-resumable branch) is **"new"**: it must start with a blank slate. There is
    therefore never stale cross-session metadata, which removes any need for a
    separate "new vs resumed" disposition signal.

### Slot metadata (ccpool-internal — DEFERRED, captured only)

ccpool-internal bookkeeping tied to the tmux/slot, read-only if ever externalized
(possibly never). Not needed by any consumer today. Captured here so the model is
complete; filed as a separate deferred ccpool bead, **not built** in this effort.

---

## What exists vs. what is new

| Capability                                                                                    | Status                                                                       |
| --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| Session metadata keyed by `external_id`, surviving tmux death + resume, cleared only on purge | **Exists** — migration 006 + `session.go:244` resume path + `ops.go` cascade |
| `ccpool meta set/get/list/rm`, `list --filter`, `sessionmeta` Go API                          | **Exists** — `pg2-01ys`                                                      |
| Atomic metadata set **as part of dispatch** (CLI `--meta` + library `EnsureOpts.Meta`)        | **New** — `pg2-87ly`                                                         |
| Fresh launch under a reused `external_id` clears prior metadata (reuse ⇒ new)                 | **New** — `pg2-87ly`                                                         |
| pr-pool writes `prpool.*` at dispatch + reads it back via the query APIs                      | **New** — `pg2-5o5i`                                                         |
| Slot-metadata tier                                                                            | **New, deferred** — separate ccpool bead                                     |

---

## Decomposition

- **`pg2-87ly` — ccpool: session metadata at dispatch (the blocker).**
- **`pg2-5o5i` — pr-pool: consume it.** Depends on `pg2-87ly`.
- **`pg2-44k9` (deferred) — slot-metadata tier.** Captured, not built.
- **`pg2-ovu4` — SQLITE_BUSY retry/backoff.** Conditional/out of scope (act only if
  observed; ccpool holds only short txns today).

---

## `pg2-87ly` design — ccpool session metadata at dispatch

Two parts.

### Part 1 — atomic `--meta` at dispatch

- Add a repeatable `--meta key=value` flag to `ccpool new`
  (`cmd/ccpool/new.go` — mirrors the existing repeatable `--env` `flag.Value`).
- Add a `Meta map[string]string` field to `EnsureOpts`
  (`internal/session/session.go:149-176`).
- Wire `--meta` → `EnsureOpts.Meta` → an **atomic** metadata upsert performed as part
  of `Ensure`, on **all** paths: fresh create, reuse-live, and resume. "Atomic with
  dispatch" means the caller never observes a session that exists without its
  requested metadata, and there is no separate `meta set` round-trip.
- The existing `ccpool meta` CLI and `sessionmeta` library surface are unchanged.

### Part 2 — reuse ⇒ new (clear prior metadata on a fresh launch)

In the `Ensure` decision tree (`session.go:213-262`):

- **reuse-live** (tmux alive): metadata upsert applies; prior metadata preserved
  (same Claude session).
- **resume** (tmux dead, Claude session resumable): metadata **preserved** (same
  `claude_session_id`); the `--meta` upsert still applies on top.
- **fresh launch** (no resumable Claude session — new `claude_session_id` under a
  reused `external_id`): **clear** the handle's prior metadata before/at launch, then
  apply any `--meta` from this dispatch. This realizes "the metadata is removed when
  the Claude session is removed; a reused `external_id` is considered new."

### Acceptance (pg2-87ly)

- `ccpool new <ext> --meta k=v` (repeatable) sets metadata atomically with creation;
  present after `new` with **no** separate `meta set` call.
- `EnsureOpts.Meta` upserts on create, reuse-live, and resume.
- A fresh launch under a reused `external_id` clears prior metadata; resume preserves
  it (a test covers both).
- Existing `ccpool meta`/`sessionmeta` surface unchanged; still cleared on
  `close --purge`.
- `go test ./...`, `prek run --all-files`, `nix flake check` all green.

---

## `pg2-5o5i` design — pr-pool consumption

### Namespace

Prefixed keys, centralized as Go consts in pr-pool:

- `prpool.bead` = the bead id
- `prpool.role` = the role name
- `prpool.pool` = `pr-pool` (owner tag)

Rationale: pr-pool writes into a KV namespace shared with ccpool (and any future
consumer). A `prpool.` prefix prevents collision with keys ccpool or another writer
might use; the cost is only slightly longer filters.

### Write — atomic, at dispatch

pr-pool's `ccpool new` wrapper (`internal/ccpool/cli.go:114`) appends
`--meta prpool.bead=<id> --meta prpool.role=<role> --meta prpool.pool=pr-pool` to the
dispatch in `Ensure` (`internal/executor/ccpool.go:63`). No separate set call; the
write rides ccpool's atomic dispatch (`pg2-87ly`). Because pr-pool mints a unique
`external_id` per attempt and never reuses it, this `external_id` coincides 1:1 with
the unit of work — the metadata is born at dispatch and purged at teardown.

### Read — exercise both query APIs

A new **read-only** `pr-pool sessions` command that uses **both** query APIs:

- `ListByMeta({"prpool.pool": "pr-pool"})` → this pool's session `external_id`s.
- `Meta(external_id)` per session → expand each to its bead + role.

This replaces "recover bead/role by parsing the session name" with a real metadata
lookup, gives an operator a pool view without a drain, and dovetails with `pg2-ju3r`
(audit dispatch scalars without a drain). It satisfies AC #2 (pr-pool reads the
metadata back, not just writes it).

### Cleanup

None added in pr-pool. Teardown already calls `close --purge` (`orchestrator.go:292`
in `teardownAll`; `orchestrator.go:109` in `RunOne`), and migration 006's
`store.Delete` cascade removes the metadata. Preserved `needs_input` sessions keep
their metadata on purpose (the session is still alive; an operator may attach).

### Opening the store / pool resolution

pr-pool opens `sessionmeta` once at the `drain`/`run-role` entry and threads it as a
**nilable seam** (nil ⇒ no-op, mirroring the existing `Log` seam). It must open the
**same** pool DB ccpool writes to. **Implementation-time correctness check**: confirm
`sessionmeta.OpenPool("")` resolves the same DB that pr-pool's `ccpool new` dispatch
uses (honoring `CCPOOL_POOL`/XDG identically); if not, resolve and pass the pool root
explicitly. A metadata store that points at a different DB than ccpool writes to would
make `ListByMeta` silently return nothing.

Note: with the write riding `ccpool new --meta` (ccpool's process), pr-pool needs
`sessionmeta` only for the **read** path. Reads tolerate failure (best-effort): a
store-open or query error degrades `pr-pool sessions` to empty/parse-fallback, never
fails a dispatch.

### Acceptance (pg2-5o5i — from the bead)

- pr-pool writes `prpool.bead`/`prpool.role`/`prpool.pool` at dispatch via the atomic
  `--meta` path.
- At least one pr-pool lookup path is backed by `ListByMeta`/`Meta` (the `pr-pool
sessions` view).
- Namespace decided + documented (prefixed `prpool.*` — this doc + code consts).
- Tests cover the metadata round-trip through the pr-pool → ccpool seam.
- No regression to dispatch/teardown.

---

## Sequencing

`pg2-5o5i` is **blocked by** `pg2-87ly`. Order this session: spec → implement
`pg2-87ly` → implement `pg2-5o5i`. (Same shape as `pg2-sjrl → pg2-3msk`.) Each bead
follows the established workflow: isolated worktree off **local** `main`, TDD,
`go test ./...` → `prek run --all-files` → `nix flake check` as the gate, then
rebase + FF-merge, close the bead, remove the worktree.

---

## Out of scope / deferred

- **Slot-metadata tier** — `pg2-44k9` (deferred ccpool bead).
- **`pg2-ovu4`** SQLITE_BUSY retry/backoff — conditional; act only if observed.
- pr-pool's single-session lookups (`active`/`sessionState`/`transcriptPath`) and
  `teardownAll`'s prefix-based stray reaping are **left as-is** — they need live
  ccpool facts or the stray-safety net, not metadata. Pool-scoped teardown was
  considered and rejected (YAGNI: no multi-pool coexistence today; replacing the
  prefix reaper would risk leaking crash-before-write strays).

---

## Decisions captured (brainstorming 2026-06-24)

- Namespace: **prefixed** `prpool.*` (not bare `bead`/`role`/`pool`).
- Write: **atomic at dispatch** via `ccpool new --meta` (not a separate set call).
- Scope/lifecycle: session metadata **tied to the Claude session**, addressed via
  `external_id`; reuse of an `external_id` whose Claude session is gone is **new**.
- Disposition "new vs old" signal: **dropped** — made unnecessary by the lifecycle
  coupling.
- Read path: a read-only `pr-pool sessions` view exercising **both** `ListByMeta` and
  `Meta`.
- Slot-metadata tier: **deferred**, captured.
