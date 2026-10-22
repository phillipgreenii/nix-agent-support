# CETA approves `bgkill NAME` for this session's own bgrun jobs

**Status**: Accepted (resolves `pg2-1kygx`)
**Date**: 2026-10-22
**Deciders**: Phillip Green II (operator ruling, 2026-10-22, recorded in the bead)

## Context

30 of 120 auto-mode classifier denials since 2026-09-20 were raw `kill <pid>` of the agent's own
bgrun jobs (test runners, builds). The classifier cannot tell an agent-started pid from anyone
else's, because tool results are stripped from its input, so an autoMode allow entry was rejected
as unsafe.

CETA does not help today. It tracks Claude Code background SHELL ids (`internal/asklog/shells.go`;
the `killshell` rule approves only the KillShell tool for a shell id recorded as agent-owned), never
OS pids, and the only Bash kill it approves is the null-signal probe (ADR 0074). A job launched
through `bgrun` is detached from the shell tracking, so its pid has no ownership record. Reading
the transcript for "pids the agent printed" was rejected: the same pids appear in `ps`-style
observations of processes the agent did not start, so "printed earlier" would approve a kill the
classifier correctly denied.

The operator's ruling (2026-10-22): a sanctioned `bgkill NAME` tool is approved, built on bgrun's
own job records, so the kill stays focused on jobs created in the background.

## Decision

1. **`bgkill NAME` is a new command in `packages/bg-tools`**, packaged like `bgrun`/`bgcheck`/
   `bgwait`. It sends SIGTERM (never SIGKILL) to the payload tree below the recorded launcher,
   deepest first, so the launcher still writes the payload's true exit code (143). It signals the
   launcher directly only when no payload exists yet, never as a fallback, so it cannot orphan a
   payload that ignores SIGTERM (exit 6 instead). It refuses (exit 5) when the recorded pid is not
   a bgrun launcher (command line lacks `bgrun`) or no longer has the start time `bgrun` recorded.

2. **`bgrun` records ownership.** `NAME.meta` gains `session:` (`$CLAUDE_CODE_SESSION_ID`, when set),
   written FIRST so no later free-text line shadows it, and `pidstart:` (the recorded pid's `ps`
   start time). Readers take the first line carrying a key.

3. **A new CETA rule, `internal/rules/bgkill`, approves exactly `bgkill NAME`** (one static,
   path-safe name; no flags, so no `--dir`/`--grace`; no env prefix; no live expansion; no
   redirection except `/dev/null` or a descriptor dup) and only when the job record proves: the
   `session:` line equals the hook's `session_id`, `NAME.exit` is absent, and the recorded pid is
   alive AND still the bgrun launcher (command line names `bgrun`; start time equals `pidstart:`
   when recorded). Everything else is `NotApplicable`: the chain continues exactly as before the
   rule existed. It never returns Ask or Reject.

4. **The job store is injected.** The live hook passes a `FileStore` over bgrun's state directory
   (`$BG_DIR`, else `$TMPDIR/pg-bg-$USER`, resolved from the hook's own environment: a record the
   hook cannot see is simply not approved); offline replay passes nil and the rule never approves,
   like `killshell`'s nil `ShellStore`.

5. **Raw `kill <pid>` of a recorded job is NOT approved.** The recorded pid is bgrun's launcher
   subshell, not the payload, so SIGTERM to it would orphan the payload; and a pid can be reused
   after a crash that leaves no exit record, so a check on a bare pid cannot be exact the way
   bgkill's own re-verification is. `pkill`, `killall`, `-u`, process groups, `kill -9`, every
   unrecorded pid and every launchd-managed service (the shared dolt server) stay exactly as gated
   as before.

## Consequences

- Only jobs launched after this change carry a `session:` line; older records are never approved.
- Ownership is a session-id match, not a proof: an agent that forges a record can only point
  bgkill at a process whose command line names `bgrun`, which is a bgrun launcher by definition
  (possibly another session's), never at dolt or an arbitrary pid.
- The hook's `session_id` and the Bash tool's `CLAUDE_CODE_SESSION_ID` must agree, including
  inside subagents. If a subagent's Bash environment carries a different id, its `bgkill` is not
  approved (it falls through to the classifier, as today), which is the safe failure.
- The rule needs `ps` at hook time (always present on macOS; `procps` is a test dependency on
  Linux).
