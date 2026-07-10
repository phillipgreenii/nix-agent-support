# Behavior docs split into method / generic-tool / per-project-overlay sets

**Status**: Accepted
**Date**: 2026-07-10
**Deciders**: Phillip Green II

## Context

`docs/behavior/` began as a single set describing pr-pool's intended behavior, with
stable invariant IDs (`INV-*` / `GOAL-*`) and a downstream-disposable
spec → design → plan → code chain sitting below it. Reviewing that set surfaced that
it was really **three different things wearing one hat**:

1. The **method** — what a behavior doc is, the in/out rubric, the invariant-ID +
   downstream-citation convention, base/overlay layering, change-control, and drift
   detection. This is reusable across every tool (pr-pool, bd, …), not specific to
   pr-pool. It was scattered through the set's `README.md`, per-file `**Status:**`
   headers, and the preamble of `invariants.md` / `glossary.md`.
2. The **generic tool behavior** — pr-pool's org-agnostic workflows, reviews,
   cross-cutting invariants, operating model, and capability map.
3. The **per-project overlay** — how pr-pool is actually _used_ at a specific org
   (ZipRecruiter): watched labels, team, integration style, configured roles, and
   the `.pr-pool` config. This is org-specific and MUST NOT live in this public
   flake.

Mixing the three caused concrete problems: the generic set asserted an org-specific
"triage role"; the public repo risked carrying ZR specifics; and the method's rules
(e.g. "cite the invariant ID") were restated per-file instead of stated once.

Inline review notes (2026-07-10) added direction: drop the "source-of-truth" label
in favor of just "behavior docs"; every behavior doc is living by definition, so
per-doc `**Status:**` headers are noise (debate belongs in the PR, and a change
merges only once there is agreement); the method's meta-content belongs in a set of
its own; and org-specific roles/workflows should move to a ZR-owned set.

## Decision

**Split the behavior docs into three sets, each at its scope's `docs/behavior`.**

- **Scope convention.** A behavior-doc set describes exactly one scope and lives at
  `SCOPE-ROOT/docs/behavior` — a repo at `<repo-root>/docs/behavior`, a tool at
  `<tool-root>/docs/behavior`.
- **Set 1 — the method** (`behavior-docs/docs/behavior/`, this repo). The
  behavior-docs method treated as its own tool; self-describing. Holds the rubric,
  the invariant-ID convention, base/overlay layering, change-control, drift, and its
  own glossary. New method invariants `INV-METHOD-*` / `GOAL-METHOD-*`.
- **Set 2 — pr-pool generic** (`packages/pr-pool/docs/behavior/`, this repo). The
  org-agnostic pr-pool behavior. Co-located with the `pr-pool` tool per the scope
  convention. Its `docs/` is excluded from the package's build fileset so doc edits
  do not rebump the pr-pool version (repo `CLAUDE.md` "Versioning").
- **Set 3 — pr-pool @ ZR** (`phillipg-nix-ziprecruiter · pr-pool-components/docs/behavior`).
  The ZR overlay: watched labels, team, integration style, configured roles, `.pr-pool`
  config. Lives in the private ZR repo, never here.

**Cross-set / cross-repo references use textual citation, not markdown links.**
Sets refer to each other as `<repo-name> · <set-path> · <ID-or-section>` — e.g.
`phillipgreenii-nix-agent-support · packages/pr-pool/docs/behavior · INV-AUTH-2`.
`<repo-name>` is the repository's directory/workspace name (not its flake-input alias
or GitHub slug). Relative markdown links are used only _within_ a single set. This
generalizes the ADR-only "See also: `<repo-name>` docs/adr/NNNN" convention to
behavior-doc rule citations, and is required because Set 3 references Set 2 across a
flake-input boundary where a relative path could not resolve.

**Folded-in note decisions:**

- Drop the "source-of-truth" label; the docs are "behavior docs".
- Remove per-doc `**Status:**` headers (`INV-METHOD-4`): every behavior doc is
  living; debate stays in the PR; merged content is the agreed behavior.
- State the invariant-ID convention once in the method (`INV-METHOD-3`); per-tool
  `invariants.md` cites the method rather than restating it.
- "Triage" is an **activity**; whether a deployment runs a dedicated triage role is
  an overlay choice. The generic set no longer asserts a triage role.

## Consequences

### Positive

- Each set has one scope and one audience; the method is reusable by bd and future
  tools without copy-paste.
- ZR specifics are structurally kept out of the public flake (`GOAL-METHOD-6`,
  `GOAL-SIMPLE-2`).
- The method's rules are stated once and cited, so they can't drift per-file.
- Doc edits no longer rebuild the `pr-pool` binary.

### Negative

- Three homes (two repos) to keep in sync; cross-set references are textual, so a
  moved/renamed ID is not caught by a link checker (mitigated by the invariant-ID
  citation discipline and a future drift linter).
- The scope convention yields the triple-"behavior" path `behavior-docs/docs/behavior/`
  for the method set — self-referential but consistent.

### Neutral

- Only three files referenced the old `docs/behavior/` path (`docs/pr-review-flow.md`,
  `packages/pr-pool/README.md`, `packages/pg-pr/pg-pr.md`); all now point at Set 2.
  No `.nix` / `.toml` / lock references existed, so the move has no
  flake-evaluation impact beyond the intentional fileset exclusion.

## Alternatives Considered

### Keep one set, tag sections by audience

Rejected: the method is reusable across tools and the ZR overlay must live in the
private repo — a single set cannot satisfy both without leaking org specifics into
the public flake.

### Put the method set under `packages/` beside the code tools

Rejected: `packages/` is for buildable artifacts; the method is docs-only. A
repo-root `behavior-docs/` scope keeps the semantic separation and avoids the
build/fileset machinery.

## Related Decisions

- Supersedes the two-layer "shared base vs per-project overlay" framing previously
  described inside `docs/behavior/README.md`.
- See also: phillipg-nix-ziprecruiter `pr-pool-components/docs/behavior/README.md`
  (the Set 3 overlay).
- Relates to `GOAL-SIMPLE-2` (org/repo config lives in org repos) in
  `packages/pr-pool/docs/behavior/operating-pr-pool.md`.
