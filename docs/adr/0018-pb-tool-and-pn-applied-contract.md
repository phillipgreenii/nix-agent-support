# pb tool + pn:applied gate contract

**Status**: Accepted
**Date**: 2026-06-26
**Deciders**: Phillip Green II (with Claude)
**Tracking**: pg2-k43p.4

## Context

Agents finishing a unit of work routinely create a follow-up bead — canonically
"verify the code works" — that MUST NOT become workable until the change it
depends on has actually been _applied_ to the relevant checkout by a
`pn workspace apply`. Today nothing enforces that ordering: a follow-up bead is
immediately `bd ready`, so a fleet of agents can grab it before the change ships,
verify against stale state, and report a false result.

Three forces shape a solution:

1. **Rebase survival.** The change's commit SHA is unstable — the workflow rebases
   freely, so a follow-up keyed to a SHA would never match after a rebase. The diff
   itself is stable across the rebases this workflow performs, so the key MUST be
   the change's `git patch-id` (`--stable`), not its SHA.
2. **Pull, not push.** "Has this been applied?" is answered by Phase 1's applied-state
   API — `pn workspace info --json` (repo-base ADR 0012) — rather than by a hook
   firing on every git operation. A single consumer scans the workspace and resolves
   whatever is now satisfiable.
3. **Topology-agnostic, fail-closed.** The consumer cannot assume one beads DB, one
   repo, or a clean tree, and a wrong guess MUST fail closed (leave the bead blocked)
   rather than open (release it early).

Phase 1 delivered the producer half (`pn workspace info`, the applied-state store,
and the `wsid` registry). Phase 2 needs the bead-side half: a tool that **writes**
`pn:applied` gates keyed to a patch-id and **resolves** them by consulting the
applied-state API.

## Decision

Introduce **`pb`** ("phillip-beads"), a standalone Go cobra binary packaged in
agent-support (`packages/pb`), with two subcommands — `pb gate create` and
`pb gate check` — and the following `pn:applied` gate contract.

### The `pn:applied` gate

A gate is a beads issue (`issue_type: "gate"`) with:

- `await_type` = `"pn:applied"` (a custom type; bd round-trips it verbatim).
- `await_id` = `"<wsid>:<repo>:<patch-id>"`. Consumers MUST split on the **first two**
  `:` only (`SplitN(s, ":", 3)`) — a patch-id never contains `:`, and this keeps the
  grammar stable if a repo key ever does. `wsid` is the workspace id from repo-base
  ADR 0002's `[workspace].id`; `repo` is a `pn workspace info` repo key.
- `metadata.applied_baseline` = the repo's `applied_ref` at create time. It MAY be
  empty (a repo with no applied-state record yet).

### Co-location invariant

A gate MUST be created in the **same beads DB as the bead it blocks**. A `blocks`
edge across two distinct DBs does **not** hold the blocked bead out of `bd ready`
(verified — `pb`'s contract suite asserts this). `pb gate create` therefore discovers
the workspace's distinct DBs and creates the gate in the one where `bd show <bead>`
succeeds, rather than assuming a fixed root DB.

### Multi-DB discovery + dedupe key

`pb gate check` discovers candidate DBs by walking up from each repo path (and the
workspace root) for a `.beads` directory, **bounded at the workspace root** (it MUST
NOT ascend above the root, or a foreign `.beads` plus a matching `wsid` slug could
cross-resolve). Discovered DBs are deduped by **Dolt identity**
`"<dolt_server_host>:<port>|<dolt_database>|<project_id>"` (host+port from
`.beads/metadata.json` + the sibling `.beads/dolt-server.port`), **NOT** the `.beads`
path or issue prefix — those differ per repo even when several repos map to one shared
Dolt project. Today all repos resolve to one `pg2` project; the dedupe keeps the scan
correct if that ever diverges.

### `pb gate create`

Takes an existing `--blocks <beadid>` and attaches gate(s). It validates `--repo`
against `pn workspace info` (unknown repo ⇒ error, never guess), computes the
patch-id of `--commit` (default `HEAD`) — or one gate per commit for `--commits
<range>` — in that repo's path, creates the co-located `pn:applied` gate, and writes
`applied_baseline`. It does **NOT** create or un-defer the bead (design decision D1):
the fleet-race-safe lifecycle (`bd create --defer` → `pb gate create` → un-defer) is
the caller's responsibility, taught by the Phase-3 plugin.

### `pb gate check`

Run inside a workspace (e.g. as the apply post-hook). For each discovered DB it lists
open gates (`bd gate list --limit 0 --json`, with `BD_JSON_ENVELOPE=1` pinned), keeps
the `pn:applied` gates whose `wsid` matches, and for each repo with a non-empty
`applied_ref` chooses a scan range: `baseline..applied_ref` when the baseline is an
ancestor of `applied_ref`, else the last `--last-n` commits (default 100). It scans
that range with `git log -p | git patch-id --stable` and **resolves in the gate's own
DB** every gate whose patch-id is now present.

- **Dirty repos** are scanned leniently (committed history only) by default; `--strict`
  skips them.
- **`--dry-run`** mutates nothing on either the resolve or the stale path.
- **Stale handling:** a still-unresolvable gate older than `--stale-after` (default
  `3d`; units `ms`..`d`, values `<1ms` rejected) gets the `--stale-handler` action:
  `convert-to-human` (adds the `human` label → surfaces in `bd human list`) or `close`
  (resolves it, unblocking the bead).
- **Best-effort:** undeterminable gates are skipped and reported; the command exits
  non-zero iff anything was skipped.

### Packaging

`pb` is a Pattern-A `mkGoApp` (gomod2nix, no local replace; ADR 0008). `bd` and `git`
are wrapped onto `PATH` via `wrapProgram`. **`pn` is NOT wrapped** — it is an ambient
runtime `PATH` dependency: agent-support is standalone/no-external-flake-deps and cannot
reference repo-base's `pn` overlay, and `pn` is already on the apply-env `PATH` (spec
Component 3) and on dev shells.

## Consequences

### Positive

- Follow-up beads stay blocked until their change is genuinely applied; a fleet cannot
  grab them early (the fleet-race test proves the deferred→gate→undefer→resolve path).
- Keying on `git patch-id --stable` survives the local rebases this workflow performs.
- Discovery is topology-agnostic (multi-DB, multi-repo, dirty trees) and fails closed.
- The contract is pinned by build-tagged contract tests against real `bd`/`git`, so a
  bd/git surface drift is caught explicitly rather than silently.

### Negative

- A squash, or a rebase that lands a change **within the ~3-line diff context** of the
  gated hunk, changes the patch-id — the gate then never auto-resolves and falls to the
  stale-handler (convert-to-human/close). This is the deliberate fail-closed trade-off,
  not a bug; both behaviours are pinned by the contract suite.
- `pn` being an ambient (unwrapped) dependency means `pb` errors at runtime if `pn` is
  absent from `PATH`, rather than failing at build/closure time.

### Neutral

- The **OverridePaths** limitation (a `pn workspace apply --override-path` reports a
  non-canonical repo path, so the await_id repo key can mismatch) is **out of scope**
  (design decision D2). `pb` targets the documented common no-override apply; the gap is
  documented in repo-base ADR 0012 and tracked as `pg2-k43p.3`.
- Today all repos share one `pg2` Dolt project, so multi-DB discovery resolves to a
  single DB; the dedupe and co-location logic are forward-looking.

## Alternatives Considered

### Key on the commit SHA

Rejected: the SHA changes on every rebase, so a follow-up bead would never match its
change after the first rebase. `git patch-id` is the rebase-stable identifier.

### `git notes` / per-repo state files instead of the pn applied-state API

Rejected: per-repo config drifts and leaves stale copies across checkouts; the pn
applied-state store (ADR 0012) is the single machine-local source of truth, consumed
read-only via `pn workspace info`.

### A single rolling "human review" gate per workspace

Rejected: too coarse — it cannot say _which_ change a given follow-up waits on, and
collapses independent changes into one manual chokepoint.

## Related Decisions

See also: phillipg-nix-repo-base docs/adr/0012-pn-applied-state-store-and-info-api.md
(the consumed `pn workspace info` / applied-state API).

See also: phillipg-nix-repo-base docs/adr/0002-pn-workspace-toml-schema.md
(the `[workspace].id` / `wsid` that forms the first field of `await_id`).

See also: phillipgreenii-nix-agent-support docs/adr/0008-adopt-gomod2nix-for-go-packages.md
(the Pattern-A `mkGoApp` packaging used by `pb`).
