# CETA: inert-value carve-out for the git editor family (`true` and `:`)

**Status**: Accepted (resolves `pg2-agprs`)
**Date**: 2026-08-13 (operator ruling; recorded as an ADR 2026-08-17, previously held in a bd memory)
**Deciders**: Phillip Green II

## Context

The git rule screens the editor family because each member is an exec sink: git runs the value as
a program. The family has four spellings — the env vars `GIT_EDITOR` and `GIT_SEQUENCE_EDITOR`,
and their argv twins `git -c core.editor=<v>` and `git -c sequence.editor=<v>`.

Two measured facts drove the ruling:

1. **A live bypass**: the `GIT_SEQUENCE_EDITOR` argv twin was screened while the env spelling was
   not — an env-route bypass of an argv screen, violating the relation `pg2-6c85x` established
   (the env spelling is never LESS restrictive than argv).
2. **A measured prompt cost**: 65 of 97 editor-family ask rows were exactly
   `GIT_EDITOR=true git rebase --continue/--skip` (~0.43 prompts/day) — an inert value that
   deliberately disables the editor.

## Decision

The exact literal values `true` and `:` MUST be allowed, and every other value MUST be screened,
for the WHOLE family: `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `git -c core.editor=`, and
`git -c sequence.editor=`.

Constraints that MUST hold:

- **(a)** The carve-out MUST be applied to the argv spellings too, or the `pg2-6c85x` relation —
  the env spelling is never LESS restrictive than argv — breaks and its tests fail.
- **(b)** The allowlist MUST be EXACT-TOKEN and auditable, never a prefix or substring match.
  These sites become value-reading, a deliberate departure from the value-blind posture used
  everywhere else in the git rule; ADR 0050 states the general principle this instantiates
  (a config-injection relaxation is a key + value-predicate pair, never key-only).
- **(c)** A value that is a variable, a substitution, or otherwise not a literal MUST reach the
  screened verdict, never the carve-out.

## Rejected alternatives

- **Ratify as landed**: knowingly leaves the `GIT_SEQUENCE_EDITOR` env-route bypass of an argv
  screen open.
- **Screen everything and re-rule ceta's rebase arm**: the largest blast radius, and it
  invalidates a landed `pg2-a12rl` test.
