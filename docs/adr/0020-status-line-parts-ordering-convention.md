# Status line parts ordering convention

**Status**: Accepted
**Date**: 2026-06-30
**Deciders**: Phillip Green II

## Context

`phillipgreenii.programs.claude.status-line-parts` is a `listOf str` option that any
module MAY contribute to (the wrapper runs each part in list order and width-wraps the
results — see ADR 0019). Today there are exactly two contributors:

- `phillipgreenii-nix-agent-support` defines the base set (env, session, worktree, git,
  repo, pr, model, version, effort, thinking, output_style, vim, agent).
- `your-private-flake` (a downstream consumer) appends `aws` and `workspace` parts with `lib.mkAfter`.

NixOS/home-manager merges a `listOf` by concatenating definitions in ascending order of
their merge priority (`lib.mkOrder N`, default `N = 1000`; `lib.mkBefore = mkOrder 500`,
`lib.mkAfter = mkOrder 1500`). **Within the same priority, definitions concatenate in
module-evaluation order**, which depends on import order.

With one base definition and one `mkAfter`, the result is deterministic. But the base used
a _plain assignment_ (implicitly priority 1000), so a second plain-assignment contributor
would land in the same band and its position relative to the base would be decided by
import order — implicit and fragile. As more modules contribute parts, this becomes a
latent ordering bug.

## Decision

Adopt an explicit **priority-band convention** for `status-line-parts`, documented here and
in the repo `CLAUDE.md`, and make the base band explicit in code:

- The base default set is defined at `lib.mkOrder 1000` (was a plain assignment; functionally
  identical priority, now explicit and self-documenting).
- Contributors MUST place their parts with an explicit order helper:
  - `lib.mkBefore` (band 500) — parts that lead, before the base set.
  - `lib.mkAfter` (band 1500) — parts that trail the base set (e.g. ZR's `aws` / `workspace`).
  - `lib.mkOrder N` — for finer placement, pick `N` between bands.
- Contributors MUST NOT use plain assignment for `status-line-parts` (it lands in the base
  band 1000 and orders by module-import order — non-deterministic across contributors).
- Within a single definition list, order is the list order (already explicit).

## Consequences

### Positive

- Cross-module render order is deterministic and predictable regardless of import order.
- The convention is declarative and visible at each contribution site (`mkBefore`/`mkAfter`/
  `mkOrder`), and the base band is explicit in `default.nix`.
- No change to the option type or the wrapper — backwards compatible. The existing base +
  ZR `mkAfter` ordering is unchanged.

### Negative

- The convention is enforced by documentation and review, not by the type system. A
  contributor who ignores it and uses plain assignment reintroduces the fragility (they
  would, however, collide in the base band — a reviewable smell).

### Neutral

- `lib.mkOrder 1000` is the default priority, so wrapping the base in it is purely
  declarative — it changes intent/readability, not the merge result.

## Alternatives Considered

### Keyed / ordered submodule entries (change the option type)

Replace `listOf str` with an attrset or `listOf submodule` carrying explicit order keys, so
ordering is data rather than merge-priority. Rejected for now: it is a far larger, breaking
change to the option, the wrapper, and every contributor, for a problem that a documented
band convention solves proportionately. Revisit if the contributor count grows enough that
band collisions become common.

### Leave it (rely on the current one-base-one-mkAfter determinism)

Rejected: it works only by accident of there being a single `mkAfter` contributor. The bead
(pg2-qgfg) exists precisely because that is fragile as contributors grow.

## Related Decisions

- ADR 0019 (this repo): status line width-aware wrapping — flagged ordering as out of scope.
- See also: phillipgreenii-nix-personal docs/adr/0034 — the `listOf` cross-module merging pattern.
- See also: your-private-flake modules/claude-code/default.nix — the `lib.mkAfter` contributor.
