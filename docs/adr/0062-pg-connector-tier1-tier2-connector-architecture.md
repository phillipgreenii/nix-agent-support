# pg-connector: Tier-1 umbrella + Tier-2 backend connector architecture

**Status**: Accepted
**Date**: 2026-09-06
**Deciders**: Phillip Green II

## Context

`pg-pr`, `pr-pool`, and `work-activity-tracker` each independently reimplemented overlapping
GitHub/Jira/beads sync and correlation logic. A design pass (recorded at the time in
`docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`, landed on `main`)
replaced that duplication with a pluggable connector suite: `pg-connector` (Tier 1) as the
generic, org-agnostic umbrella; one thin binary per (entity type, backend) pair (Tier 2)
implementing it for one external system each; a ZR-specific consumer layer (Tier 3, outside this
repo). `pr-pool` remains the cross-system workflow dispatcher, unchanged in its own core.

Ten code packets landed against that design before this ADR did: the four entity-type capabilities
(pr, issue, ci, scm), their shared schema/provider/wire-protocol packages, and four Tier-2 backends
(`pg-connector-pr-github`, `pg-connector-ci-github-actions`, `pg-connector-scm-git`,
`pg-connector-issue-beads`). None of them was accompanied by an ADR or a `docs/behavior/` set, even
though the design's own acceptance criteria required exactly that set to be authored as
pg-connector's **first** work packet, before any code-producing packet, "so later packets have
real behavior-IDs to cite from day one." That ordering was not honored. This ADR, together with
the `packages/pg-connector/docs/behavior/` set it accompanies, is that gap filled retroactively
(bead `pg2-wajat`).

A design spec under `docs/superpowers/specs/` is not this repo's durable citation target (see this
repo's `CLAUDE.md`, "Architecture Decision Records" → "Citation conventions", rule 3) — the ~254
existing `[design: §N]` comments scattered through `packages/pg-connector`'s source predate that
convention being applied here and are tracked for a rewrite in bead `pg2-hidkm`, which needs a
durable, non-spec target to retarget them at. This ADR and the behavior-docs set are that target.

Independently of the retroactive-documentation gap, three post-landing review passes found real
divergences between the design's stated contract and the shipped code, each fixed and closed before
this ADR was written: version negotiation was specified but never wired (`pg2-p2z7o`); a caller
input error was misreported as backend ill-health because the error taxonomy had no code for it
(`pg2-r9iok`, which added the sixth `invalid_argument` code); and a Tier-2 backend was shelling out
to the Tier-1 umbrella that dispatches it, an authorization the design never actually granted
(`pg2-0vwcc`). This ADR's Decision below records the architecture **as built after those fixes**,
not the design document's first draft.

## Decision

Adopt, as this repo's architecture for connecting agent/human tooling to external
PR/issue/CI/SCM systems, the Tier-1 umbrella + Tier-2 backend model:

1. **One umbrella, N pluggable backends, scoped by capability, never by system.** `pg-connector` is
   the sole user-facing CLI and the sole holder of the shared entity-type schemas (`pr`, `issue`,
   `ci`, `scm`) and the wire protocol. It MUST know nothing about any backend's external system
   (GitHub, Jira, beads, git) — that knowledge lives entirely inside a Tier-2 backend binary,
   reached only through the wire protocol. In design-pattern terms: the umbrella is a **Facade**
   presenting one coherent CLI over N interchangeable backends; each capability's Provider Go
   interface (`pr.Provider`, `issue.Provider`, `ci.Provider`, `scm.Provider`) is a **Strategy** the
   umbrella selects among via its registry (today exactly one strategy is registered per capability,
   except `scm`, whose registry entry is single-valued by design — see invariant `INV-REG-1`); and
   each Tier-2 backend is an **Adapter** translating one external system's own shape into that
   capability's generic wire contract, realized as a **process-boundary adapter** — a separate OS
   process speaking a small JSON protocol, not an in-language object — because a backend's own
   dependencies (a `gh` binary, a `bd` binary, Cloudflare Access credentials) MUST NOT become the
   umbrella's own transitive dependencies.
2. **A capability, never a system, is the unit of interface design.** An interface's name and
   method set MUST correspond to exactly one capability (`pr`/`issue`/`ci`/`scm`, and any future
   entity type) and MUST name no backend/system. A single interface spanning one backend's own
   PR+CI+Issue operations looks unified but actually branches per system internally with no shared
   shape — exactly what this rejects (see "Alternatives Considered" below).
3. **The wire protocol is a small, versioned, JSON-over-stdio envelope with a closed six-value
   error taxonomy** — `not_found`, `unauthenticated`, `unavailable`, `unknown_op`,
   `version_mismatch`, and `invalid_argument` — and two independently-versioned numbers: one global
   `protocolVersion` for the envelope shape itself, and one `schemaVersion` per schema-bearing
   capability, so a breaking change to one capability's schema never forces every unrelated backend
   to redeploy in lockstep.
4. **pg-connector's own CLI exit codes are a layer separate from the wire protocol's plain `0`/`1`,
   and MUST NOT be built from or confused with it.** They split by op shape: a **fan-out** op
   (queries every backend registered for a type/capability) reports `0`/`2`/`3`
   (all-succeeded / degraded-partial / total-failure); a **targeted** op (resolves to exactly one
   backend) reports `0`/`4`/`1` (success / `not_found` — a well-formed negative, not a failure /
   any other error). Every multi-source response carries a `sources[]` row per backend actually
   queried, never collapsed into one pass/fail signal.
5. **A Tier-2 backend MUST resolve a cross-capability data need through its own direct, already-
   declared system access, and MUST NOT execute the `pg-connector` umbrella or a sibling Tier-2
   backend binary to satisfy its own op.** This reverses this repo's own first cut at the CI
   backend's PR→branch lookup, which briefly shipped by shelling out to `pg-connector pr show`
   before `pg2-0vwcc` found and fixed it.
6. **Credential resolution, auth checking, and a backend's own local store are each that backend's
   own concern.** Auth checking is asserted structurally through an optional `AuthChecker`
   sub-interface (a type-check, never a required method), so a backend with nothing to check (the
   local-git `scm` backend, which has no remote credential concept at all) simply does not implement
   it, and is reported as a well-formed "disabled: not applicable" rather than a forced or
   meaningless answer. pg-connector ships no shared credential-resolution library of its own.
7. **Adding a backend is a registry-config change, never an umbrella code change.** The umbrella's
   `connector.<type>` registry entries are bare binary names, with no `exec:`-prefix or other
   built-in/external distinction, because nothing is compiled into the umbrella itself.

## Consequences

### Positive

- The ten already-landed code packets (the four entity-type capabilities, their shared
  schema/provider/wire-protocol packages, and the four Tier-2 backends) already conform to this
  model; this ADR gives that architecture a durable record instead of leaving it to live only in
  code comments and a design document this repo's own conventions treat as non-durable.
- Capability-scoped interfaces keep a future fifth entity type or a second interchangeable backend
  for an existing one (e.g. a Forgejo PR backend) a registry-config change, not a rewrite of an
  interface that would otherwise have to grow a new backend-specific branch.
- The three post-landing fixes (`pg2-p2z7o`, `pg2-r9iok`, `pg2-0vwcc`) are now durable invariants
  with citable IDs (`packages/pg-connector/docs/behavior/invariants.md`), closing the risk of the
  same regression landing unnoticed a second time.

### Negative

- The full design's remaining scope — the `attention`/`search` cross-cutting capabilities,
  dashboard/alert conventions, the deferred `Thread`/`Note` entity types, `pg-pr`'s actual
  retirement, and the `df-categorize`/`df-feedback` pr-pool roles — is not yet built and is
  therefore deliberately **not** covered by this ADR's Decision or by the accompanying
  behavior-docs set's extent: only what has landed is recorded as intended behavior today. A future
  packet that builds one of those pieces MUST amend both this ADR's scope and the behavior-docs set
  in the same change, per this repo's own documentation rule (`CLAUDE.md`, "pg-pr / pr-pool
  Development Rules").
- Behavior-docs-first ordering, having been skipped for the first ten packets, cannot be
  retroactively un-skipped; this ADR and its behavior-docs set are a backfill, not evidence the
  process was followed from day one.

### Neutral

- This ADR and the behavior-docs set deliberately do not restate the design document's own numbered
  sections as their citation target. The design-spec-citation cleanup tracked in bead `pg2-hidkm` is
  expected to retarget pg-connector's existing `[design: §N]` code comments at this ADR and the
  behavior-docs set's own element IDs instead.

## Alternatives Considered

### Keep per-system interfaces (one interface spanning a backend's own PR+CI+Issue operations)

Rejected: this is exactly the shape a prior ZR-side interface (`INTF-ZR-CODEHOST`) took, and it
looks unified while actually branching per system internally with no shared shape — the opposite of
what capability-scoping buys, and it would make every new backend a change to an
already-multi-purpose interface rather than an implementation of a small, focused one.

### Let a Tier-2 backend call back into the umbrella (or a sibling backend) for cross-capability data

Rejected, and reversed after briefly shipping this way (`pg2-0vwcc`): it creates an undeclared
runtime dependency on `pg-connector` itself being on `PATH` and registered, a multi-process chain
per call, and a duplicated error-code-to-sentinel map, for data a backend can almost always resolve
directly against a system it already holds credentials for (the CI backend already had its own `gh`
gateway and needed only one more direct `gh pr view` call).

### A shared credential-resolution library

Rejected: the three backends already landed resolve credentials three different, legitimate ways
(GitHub's env-then-`gh auth token` chain, a keychain-backed CLI for beads/Jira-style tooling, and a
Cloudflare Access JWT for Captain's Log-style tooling); forcing one shape onto all three would fit
none of them well, and a future backend is free to pick whatever chain fits its own token model.

## Related Decisions

- Realizes the Tier-1/Tier-2 split first proposed in
  `docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`, filed for
  retroactive documentation as bead `pg2-wajat`.
- Records, as durable invariants, the fixes from `pg2-p2z7o` (version negotiation), `pg2-r9iok`
  (the `invalid_argument` wire code and `not_found` reachability), and `pg2-0vwcc` (the
  composition-boundary rule) — see `packages/pg-connector/docs/behavior/invariants.md`.
- Continues, under a renamed binary, the PR-data-interface/workflow-owner split recorded in
  [0034](0034-pg-pr-prpool-review-ownership-split.md).
- Is the intended retargeting point for the design-spec citation cleanup tracked in bead
  `pg2-hidkm`.
