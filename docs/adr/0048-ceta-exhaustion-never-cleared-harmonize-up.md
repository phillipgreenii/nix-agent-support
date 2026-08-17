# CETA never clears the exhaustion half of a captured substitution (harmonize up)

**Status**: Accepted (resolves `pg2-gwp57`)
**Date**: 2026-08-13 (operator ruling; recorded as an ADR 2026-08-17, previously held in a bd memory)
**Deciders**: Phillip Green II

## Context

Exhaustion means "ceta has no model for this command" (vocabulary: ADR 0043/0044). ceta models NO
interpreter, so `X=$(bash -c "rm -rf /")`, `X=$(python3 -c ...)`, `X=$(ssh host ...)`, and
`X=$(curl evil)` are exhaustions EXACTLY like `X=$(seq 1 3)` — the exhaustion half IS arbitrary
code execution, and it is not separable by provenance (ADR 0044).

At ruling time the two positions diverged: env-value position gave a decisive Ask while
command-position substitutions could reach `NoOpinion` — and ADR 0043 states `NoOpinion` is
auto-approved in auto mode, so `echo $(bash -c "rm -rf /")` was PERMITTED. A live hole, not a
hypothetical.

## Decision

ceta MUST NOT clear the EXHAUSTION half of a captured substitution. Consequences:

1. The COMMAND-position substitution floor MUST be RAISED so
   `echo $(bash -c "rm -rf /")` stops reaching `NoOpinion`.
2. env-value position keeps its decisive Ask; the two positions converge UPWARD, never downward.
3. Relieving prompt volume by widening the static safe-substitution allowlist for an exhaustion
   body is FORBIDDEN by this ruling; `pg2-xl79d`'s exhaustion cohort (`seq`, `[`) is therefore
   not clearable.
4. A REFUSAL body MUST NOT be cleared either (`pg2-2ke04`/`pg2-2u5jf`).

## Rejected alternatives

- **Harmonize DOWN**: would relieve 214 of 1647 env-vars asks but EXTENDS the auto-mode hole into
  env-value position.
- **Ratify the asymmetry**: freezes the command-position hole and grows the allowlist per
  basename forever.
