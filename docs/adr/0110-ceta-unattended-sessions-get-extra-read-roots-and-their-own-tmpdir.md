# CETA: unattended sessions get extra read roots and their own TMPDIR

**Status**: Accepted (implements the operator ruling on `pg2-r9kv7` W3 and W6; work item
`pg2-b1lir`)
**Date**: 2026-10-27
**Deciders**: the operator (Phillip), ruling 2026-10-27 on `pg2-r9kv7`: "W3: pool-only known read
roots ... excludes `~/.claude/projects` and secrets", "W6: `mkdir -p` / `mktemp -d` / `tar -xf`
into `$TMPDIR` and `/tmp/<bead>-*` only".

## Context

A pool session (a ccpool worker in permission mode `dontAsk`) is denied any command that BOTH the
`--allowed-tools` prefix list and CETA abstain on, and 1,606 such denials were surveyed on
`pg2-b1lir`. About 280 were read-only probes of a path CETA's zone ladder does not know: the bgrun job
directory under `$TMPDIR`, `/var/log`, `/Library/LaunchDaemons` and `/usr/local/libexec/pg-launchd`.
CETA's extra roots (`CETA_EXTRA_READONLY_ROOTS`, `CETA_EXTRA_READWRITE_ROOTS`) are baked into the
hook wrapper for EVERY session, so adding these to them would also widen an interactive session,
which the ruling did not approve ("pool-only").

## Decision

1. A second, separate list, `CETA_UNATTENDED_EXTRA_READONLY_ROOTS`, holds the pool-only read-only
   roots. `patheval.PathEvaluator.ExtendForUnattended` applies it, and `main.go` calls it only for a
   session that `HookInput.IsUnattended()` calls unattended (permission mode `dontAsk`, or
   `CCPOOL_AUTONOMOUS=1`). Pool-only is therefore a property of this binary, not of how a launcher set
   an environment: a role's `sessionEnv` cannot set `CETA_EXTRA_READONLY_ROOTS` anyway, because the
   wrapper `--set`s it.
2. The same call grants the session's own `$TMPDIR` read-write (the bgrun state, a `mktemp -d`
   directory), but only when it is a directory strictly inside `/private/var/folders`, `/private/tmp`
   or `/tmp`. `$TMPDIR` is the session's environment, not the command's, so a command cannot move it.
3. A configured root is ignored when it is at or above `$HOME` or when it is, contains or sits under
   `~/.claude`. The home-manager option asserts the same, so a bad list fails the build before it can
   reach a session.
4. `mktemp` and `tar -x` (and `sysctl -n`, `pmset -g`, `uuidgen`, ruling items W4 and W6) are approved
   for every session, but a write is approved only into a path under a temp root
   (`internal/temproot`) that the path evaluator also allows writing. "`/tmp/<bead>-*`" is read as
   "under `/tmp`": `/tmp` is already writable to `mkdir`, `touch` and a redirect for every session, so a
   narrower rule for the new commands would protect nothing.

## Consequences

- The extra roots are checked after every built-in zone and after the secrets and `denyRead`
  guards, so they can make an unknown path known but can never unblock a credential path.
- `~/.claude/projects` is NOT made readable by this change, and is not made unreadable either:
  CETA's built-in zone ladder already treats `~/.claude` (plans and projects included) as readable to
  a Bash read command in every session. The ruling's exclusion is honoured in that no root here adds
  it; closing the existing zone would be a separate, narrowing decision.
- The set of pool-only roots is pinned by `checks.<system>.pg-router-pool-grants` in the consuming
  machine flake and by `test-ceta-unattended-roots` here.
