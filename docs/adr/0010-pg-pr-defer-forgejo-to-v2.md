# pg-pr: defer forgejo to v2

**Status**: Accepted
**Date**: 2026-05-20
**Deciders**: Phillip Green II

## Context

The pg-pr design provides for Forgejo as an alternate VCS provider, but v1 work targets only GitHub. Building both at once delays the value of consolidation. Nothing in the user's current workflow requires Forgejo.

## Decision

v1 ships GitHub-only. The VCS provider interface (`packages/pg-pr/pkg/provider/vcs/iface.go`) is defined now so a Forgejo implementation can be added without breaking changes.

## Consequences

### Positive

- Faster v1 delivery.
- Lower test surface for the initial release.

### Negative

- Anyone needing Forgejo today must wait.
- v1 designs are not validated by a second VCS implementation; interface assumptions may need revisiting when Forgejo lands.

### Neutral

- The interface affordance is cheap to maintain and signals future intent.

## Alternatives Considered

### Ship both GitHub and Forgejo in v1

Rejected. Nothing in the current workflow requires Forgejo; the work would delay everything else.

### Drop Forgejo from the spec entirely

Rejected. The interface affordance is cheap and documents the intent to be VCS-agnostic.

## Related Decisions

- [0007-pg-pr-go-cli-consolidation.md](0007-pg-pr-go-cli-consolidation.md)

See also: `docs/superpowers/specs/2026-05-19-pg-pr-design.md` §"Non-goals (v1)".
