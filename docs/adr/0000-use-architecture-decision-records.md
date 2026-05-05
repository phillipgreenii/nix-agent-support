# Use Architecture Decision Records

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

Architecture decisions in long-lived repositories accumulate tribal knowledge that
is not visible in code. Future contributors (or future-me) lack the rationale behind
structural choices, making changes risky. This repository was created as part of a
deliberate extraction effort; the decisions that shaped its design should be documented.

## Decision

Maintain Architecture Decision Records (ADRs) in `docs/adr/` following the
lightweight format already used in sibling repositories (`phillipgreenii-nix-personal`,
`phillipgreenii-nix-support-apps`, `phillipg-nix-ziprecruiter`).

- Draft ADRs: `docs/adr/draft-{short-title}.md`
- Accepted ADRs: `docs/adr/NNNN-{short-title}.md` (sequentially numbered)
- Index: `docs/adr/index.md`

Cross-repo references use: `See also: <repo-name> docs/adr/NNNN-title.md`

## Consequences

### Positive

Decisions are discoverable alongside the code they affect. Reviewers can read
context without spelunking git history. Deprecations and supersessions are explicit.

### Negative

Small overhead per architectural decision.

### Neutral

Follows the same convention as sibling repositories — no new tooling required.
