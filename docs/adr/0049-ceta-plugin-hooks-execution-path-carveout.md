# CETA agent-config carve-out extends to plugin hooks via execution paths only

**Status**: Accepted (resolves `pg2-nueik`)
**Date**: 2026-08-13 (operator ruling; recorded as an ADR 2026-08-17, previously held in a bd memory)
**Deciders**: Phillip Green II

## Context

On this machine `~/.claude/settings.json` has NO `hooks` key and `~/.claude/hooks/` does not
exist, so hooks arrive ENTIRELY via plugins. The pending hooks carve-out (ADR 0041 family) was
therefore guarding an empty directory while the tree holding the actually-executing hooks stayed
writable.

Measured (corrected, read-only `?immutable=1` scan): ZERO historical write-class decisions have a
real `file_path` under `.claude/plugins/`, so a narrow rule costs nothing in prompt volume.

## Decision

ceta's agent-config carve-out MUST extend to plugin hooks, but ONLY to the paths within a plugin
that can cause EXECUTION — a plugin's `hooks/hooks.json` and the scripts it names. It MUST NOT be
implemented as a `.claude/plugins/**` subtree rule.

## Rejected alternatives

- **A subtree rule**: `cache/` and `installed_plugins.json` churn as a normal side effect of
  using Claude Code — the same collateral ADR 0041 cited when it rejected a subtree-wide
  denyWrite.
- **Declining entirely**: leaves the guard pointed at the empty directory.

## Appendix: measurement probe traps

- Filtering `tool_input_json LIKE '%/.claude/plugins/%'` OVER-matches: the blob includes the
  Write tool's CONTENT field, returning rows of files merely MENTIONING the string (23 observed).
  Filter on `json_extract(tool_input_json, '$.file_path')` instead.
- An un-scoped `json_extract` scan over ALL tools aborts with "stepping, malformed JSON" because
  at least one row's `tool_input_json` is invalid; scope to the write-class tools or guard with
  `json_valid()`.
