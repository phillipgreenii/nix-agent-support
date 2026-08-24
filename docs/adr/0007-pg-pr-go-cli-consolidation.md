# pg-pr Go CLI consolidation

**Status**: Accepted
**Date**: 2026-05-20
**Deciders**: Phillip Green II

## Context

PR-agent functionality is scattered across six sources using three languages (Python, Go, Bash) and three orchestration models (claude skills, gascity pack, workflow phases): `packages/gh-prreview`, `packages/my-code-review-support`, `packages/my-code-review-support-cli`, `your-private-flake/workflow/phases/review`, `your-private-flake/modules/zg`, and `gc/assets/imports/zr`. Duplication exists in worktree management (3x), GitHub fetch (3x), PR body generation (2x), and feedback ingestion (2x). Two competing bead schemas. Mixed-mode work (human + gascity + claude session on one PR) is fragile because each surface talks to GitHub differently and writes beads differently.

## Decision

Consolidate into a single Go CLI `pg-pr` plus a Claude plugin distributed via the existing local-marketplace mechanism (`phillipgreenii.programs.claude.plugins.local`). The CLI is the sole interface between agents and external systems (GitHub, jira, CICD) for PR work. LLM callers — human-run claude sessions, gascity background agents, manual CLI use — read and write beads and invoke `pg-pr`; they never call `gh`, the GitHub API, or jira directly for PR workflows.

## Consequences

### Positive

- Single source of truth for PR-related logic.
- Mixed-mode operation supported by a shared bead schema.
- Net reduction in LOC across the six sources after migration.
- Easier to test (Go testable; bash scripts are not).

### Negative

- Five-phase strangler migration takes weeks of calendar time.
- ZR-specific Captain's Log adapter has to live downstream in `your-private-flake` as a separate Go binary; cross-repo coordination required for protocol changes.

### Neutral

- One more Go module to maintain in this repo.

## Alternatives Considered

### Keep separate tools, paper over with bash glue

Rejected. The fundamental problem is cross-actor consistency — gascity, claude sessions, and humans each writing to GitHub differently. Bash glue cannot fix that.

### Build as a Bash CLI

Rejected. The existing Go skeleton (`packages/my-code-review-support-cli`) already exists, is more testable, and gives a clean library surface for ZR-specific extension binaries to import.

## Related Decisions

- [0008-pg-pr-extension-via-exec-scripts.md](0008-pg-pr-extension-via-exec-scripts.md)
- [0009-pg-pr-bead-schema.md](0009-pg-pr-bead-schema.md)
- [0010-pg-pr-defer-forgejo-to-v2.md](0010-pg-pr-defer-forgejo-to-v2.md)

See also: `docs/superpowers/specs/2026-05-19-pg-pr-design.md`. Supersedes the implicit decisions in `packages/gh-prreview/`, `packages/my-code-review-support/`, and `packages/my-code-review-support-cli/`.
