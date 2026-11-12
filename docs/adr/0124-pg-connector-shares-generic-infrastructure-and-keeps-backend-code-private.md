# pg-connector shares generic infrastructure and keeps backend-specific code private

**Status**: Accepted (operator ruled 2026-11-11; supersedes the interface-only sharing rule in the unified connector design's layout, sections 4.4 and 5.2; clarifies principle 6 of ADR 0062)
**Date**: 2026-11-12
**Deciders**: Phillip Green II

## Context

The unified connector design (`docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`,
sections 4.4 and 5.2) lays the module out so that "Only pkg/schema+pkg/provider+pkg/scriptout are
importable by everyone, because that is the interface". `packages/pg-connector/cmd/pg-connector/layout_convention_test.go`
enforces that layout, and it has already been widened by hand more than once (`pkg/eventlog`,
`pkg/connectord`). Each widening needed a test edit and a justification, and the default answer for new
shared code was to copy it into every backend.

Principle 6 of ADR 0062 says credential resolution, auth checking and a backend's own store are each that
backend's own concern, and that pg-connector "ships no shared credential-resolution library of its own".
Since 2026-10-05 comments in the code have read that sentence as a ban on any shared library across
backends. It is not: it is about credentials, auth and stores.

On 2026-11-11 the operator ruled, in the pg-apps deep review planning session: "in general we want shared
code" and "if it is ambiguity to an ADR you want to clean up, then do it. if it is changing what a previous
ADR's intent is, then we need a new ADR to supercede the previous on."

The principle 6 wording is an ambiguity, so ADR 0062 is clarified in place. The interface-only layout rule
is a change of intent, so this ADR supersedes it.

## Decision

1. **Code in this module is one of three kinds.**
   - **Interface code** (`pkg/schema`, `pkg/provider` and its subpackages, `pkg/scriptout` and its
     subpackages) is the contract between the umbrella and every backend. It MAY be imported by anything.
   - **Backend-specific code** names a backend or an external system (GitHub, Jira, PagerDuty, a Slack
     workspace, an osx-bridge socket, and so on), or holds credential resolution, auth checking or a
     backend's own store logic. It MUST live in that backend's `main` package or under its own
     `cmd/<binary>/internal/`. A backend MUST NOT import, and MUST NOT execute, another backend or the
     umbrella to satisfy its own op. This keeps the isolation intent of the design's sections 4.4 and 5.2
     and of ADR 0062 principle 5.
   - **Generic infrastructure** names no backend or external system and holds no credential, auth or store
     logic of any one backend. It MAY be shared, under a root named in the layout test's allowlist. The
     first such root is `pkg/connectorkit`; `pkg/connectord` and `pkg/eventlog` already follow the same
     rule.
2. **Adding an allowlisted root is a normal change.** It needs an entry in the layout test's allowlist and
   a package comment stating that the root is generic. It does not need a new ADR.
3. **The layout test keeps rejecting a backend-private package imported by another backend**, and keeps
   rejecting any package outside the allowlist and outside a `cmd/<binary>/` boundary.
4. **Generic shared code MUST stay generic.** It MUST NOT mention a backend, an external system's type or a
   credential source. A shared package that needs such a detail takes it as a parameter from the backend.
5. **Duplication is no longer the default.** A helper that two or more backends need, and that qualifies as
   generic infrastructure, SHOULD move to a shared root rather than be copied. The per-binary fileset
   generation that lets a backend's nix build include only the shared roots it imports removes the build
   cost that used to argue for copying.

## Supersedes and clarifies

- This ADR supersedes the interface-only sharing rule in the unified connector design's layout (sections 4.4
  and 5.2). The rest of those sections, including backend isolation, stands.
- ADR 0062 principle 6 is clarified, not changed: see the dated amendment in that ADR.

## Consequences

- Duplicated generic code across backends can be consolidated into `pkg/connectorkit` or `pkg/connectord`.
- A reviewer decides "is this generic" per package by the test in item 1; the layout test checks only the
  allowlist and the cross-backend imports, not genericity. The genericity guard in `pkg/connectord` shows one
  way to enforce item 4 mechanically for a root.
- Comments in the code that say principle 6 forbids a shared library are wrong and are rewritten to state the
  rule above.
