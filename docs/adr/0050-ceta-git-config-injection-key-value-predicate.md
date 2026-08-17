# CETA git rule: config-injection relaxations are (key, value-predicate) pairs, never key-only

**Status**: Accepted (constraint verified `pg2-arfw6`)
**Date**: 2026-07-28 (verification; recorded as an ADR 2026-08-17, previously held in a bd memory)
**Deciders**: Phillip Green II

## Context

Verified 2026-07-28 on git 2.54.0 (bead `pg2-arfw6`): `core.fsmonitor` is NOT an inert
boolean-only git config key. Per `git help config`: "Otherwise, this variable contains the
pathname of the fsmonitor hook command." Empirically:

- `git -c core.fsmonitor=/path/to/script status` EXECUTES the script.
- Config keys are CASE-INSENSITIVE, so `git -c CORE.FSMONITOR=<script> status` executes too.
- Bare `-c core.fsmonitor` (no `=`) assigns boolean true and cannot name a program.

So a key that looks like a harmless toggle is an exec sink for every non-boolean value.

## Decision

Any relaxation of the git rule's `hasGitConfigInjection` RCE guard MUST be a
(key, VALUE-PREDICATE) pair allowlist, never a key-only allowlist:

- `core.fsmonitor` is safe ONLY when the value is a git boolean literal
  (`true`/`false`/`1`/`0`/`yes`/`no`/`on`/`off`). A key-only allowlist containing it reopens the
  RCE hole the guard exists to close.
- `--config-env=KEY=ENVVAR` can NEVER be cleared by any value predicate — the value comes from
  the environment, not the command text.

ADR 0047's editor-family carve-out (exact-token value allowlist on `core.editor` /
`sequence.editor`) is an instance of this principle.
