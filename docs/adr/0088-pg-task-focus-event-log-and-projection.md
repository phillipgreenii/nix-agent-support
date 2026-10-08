# pg-task-focus keeps an append-only event log, derives its state by replay, and has one writer

**Status**: Accepted (operator rulings, 2026-10-07 and 2026-10-08; resolves `pg2-t7me1.1`)
**Date**: 2026-10-08
**Deciders**: phillipg

This ADR records the decisions behind the `pg-task-focus` core library: how its state is stored,
how a change is validated and written, which zone database it trusts, and which component is allowed
to write. The behavior these decisions produce is specified in
`docs/behavior/pg-task-focus/` of `phillipgreenii-nix-agent-support` (this repository); this ADR
carries only the reasons. `pg-task-focus` is a local, single-user daemon that keeps its operator on a
written routine (recurring day, week and sprint checklists, and timed work cycles), operated through
a web UI, a CLI and a menu-bar plugin. The core library is its first of five sub-projects and has no
HTTP, UI or CLI. The design discussion it came from is an ephemeral working document and, per this
repository's citation conventions, is not a citation target; everything a later reader needs is here
and in the behavior set.

The loopback-only, no-authentication posture of the daemon is a decision that belongs to the daemon,
not the library. It is left to the amendment that sub-project 2 (the daemon and its API) makes to this
ADR.

## Context

The routine is a record the operator wants to trust: a task is "done at 09:05" or "skipped, because
R", a cycle ran for 187 minutes, a day rolled over with these tasks missed. Four properties follow.

- The record must survive a crash and a power loss without losing what the operator was told
  succeeded and without leaving half-written state.
- Mistakes are inevitable (a forgotten lunch break, a rollover done a day late, a cycle started with
  the wrong type) and must be fixable without destroying what was originally recorded.
- Time is the dominant source of defects in a scheduler. Due times are wall-clock times in a zone,
  clocks skip and repeat hours, and a host's zone files differ from machine to machine.
- Several clients (a web UI, a CLI, a menu-bar plugin, a connector backend) will act on the same
  record, so any of them may retry a request whose answer was lost.

## Decision

1. **Event sourcing with correction events instead of edits.** The state of record is one
   append-only file of newline-delimited JSON events. No component rewrites or deletes a line; the
   only truncations are crash recovery and the rollback of a failed append, each back to the end of
   the last committed record. A mistake is fixed by appending a correction or a retraction that names
   the earlier event, and the corrected view is derived from the log. Every correction is undoable,
   including an undo. This is chosen over in-place edits because an edit destroys what was originally
   recorded, which the operator wants kept beside the fix, and because an append is the one write
   that a crash can leave in only two states (committed, or a torn tail that recovery trims).
   Multi-event changes are batches closed by a commit marker, so a crash leaves a whole change or
   none of it.
2. **The projection is never persisted.** Every task, period and cycle state, every elapsed and
   remaining time, is a function of the log and an injected clock, computed by replaying it. Nothing
   ticks into the log, so a restart or a sleeping laptop cannot corrupt a timer, and an event keeps
   the snapshot of the configuration it needs (a task's title, group, link and due rule; a cycle's
   title and planned minutes), so replay never consults the configuration and a configuration edit
   never rewrites history. The cost is a replay per start and a replay per validation, which is
   accepted for a single-user log and measured by a benchmark rather than assumed; an incremental
   path is a later optimization that could change only internals, never an event, a code or an
   interface.
3. **Validation by candidate replay, with one specific error per condition.** Every change, and every
   correction, retraction and client-supplied instant, is validated by replaying the whole log with
   the change applied before anything is appended, and the validated result is what is adopted as the
   new state, so there is no second implementation of the rules to keep equal to the first. A refusal
   carries a code named for its own problem, a plain-sentence message naming the entity, the instants
   and the stored events involved, and structured details; there is no generic "invalid timeline" code
   (operator ruling, 2026-10-08: "the error should be expressive of the problem . invalid_timeline is
   not helpful to understanding the problem."). The same condition gets the same code whether it is
   found from the present state or by replay.
4. **The zone database is the standard library's embedded `time/tzdata`, and the host's zone files
   win when present.** The Go standard library tries the host's zone directories first and uses the
   embedded copy only when the host has none, so the embedded copy keeps a machine with no usable zone
   files working, and no zone database is committed to the repository (operator ruling, 2026-10-08:
   "we can use libraries which handle timezones, no need to pull in a zip."). A zone name is valid
   when the standard library resolves it in its exact letter case (operator ruling, 2026-10-08: "what
   is in the standard library is fine."); the empty string and `Local` are rejected because they are
   escapes to an implicit zone, and `ET`, `PST` and bare offsets are rejected because the library does
   not know them. `EST` and `MST` are accepted but are fixed offsets with no daylight saving, which the
   docs say, recommending region names. There is no default zone anywhere. Because the host's files
   win, tests pin only transitions that are stable across zone database releases and never assert
   which source answered, and the nonexistent and repeated civil times of a transition are resolved by
   explicit rules the standard library does not guarantee.
5. **One `Engine` is the only writer.** The `Engine` owns the write path under one mutex: plan the
   events for a command, validate the candidate by replay, append and make durable through the event
   store, adopt the validated candidate, publish the new state version, and answer. The event store is
   the only component that touches the file, behind an exclusive advisory lock on the data directory
   taken at startup; a second instance refuses to start, and the offline check reads the log without
   the lock. The projection can therefore never be ahead of the log. The `internal/` packages are not
   importable by other modules on purpose: a connector backend talks to the daemon's HTTP API as a
   process-boundary adapter (see `phillipgreenii-nix-agent-support` ADR 0062's Decision), never by
   linking the library.
6. **Failure handling is rollback, then read-only mode, and read-only mode is loud.** A failed write
   or sync is rolled back to the end of the last committed record; if the rollback or the sync of the
   append cannot be trusted, the store enters read-only mode until the process restarts and has run
   crash recovery. A refusal then means the outcome is unknown, and a client retries with the same
   request `id`; the idempotency lookup precedes the read-only gate, so a retry of a request whose
   events are durable returns its original result. The operator accepted restart-only recovery on the
   condition that the mode is obvious in every client, so the library keeps the cause and the instant
   and exposes them, and each client shows the same sentence (operator ruling, 2026-10-08).

## Consequences

### Positive

- A crash loses nothing acknowledged and never leaves half a change, and the one place a human might
  need to look, the recovery sidecar, keeps the discarded bytes.
- History is honest: a correction sits beside the original, an undo of an undo restores the first
  state, and a configuration edit cannot rewrite what happened.
- Timers are exact by construction, because elapsed time is a sum over recorded segments.
- Every refusal says what is wrong in terms the operator can act on.
- No zone database is committed and no zone file is required on the host.
- A retry after an unknown outcome is safe, including across a restart and while read-only.

### Negative

- Every validation replays the whole log, an O(n) cost per change that the benchmark must keep
  acceptable; and a log is never compacted, so it only grows.
- The host's zone files win over the embedded copy, so two machines with different zone databases can
  resolve a rare transition differently, and the embedded database ages with the Go toolchain.
- Read-only mode is cleared only by a restart, so a transient disk error costs the operator a restart.
- After an adoption failure the in-memory model is behind the log until the restart, so a client that
  reads state in that window sees a state without a change that is durable.
- The correction and retraction rules (identity fields, batch membership, dependents) are more to
  specify and test than in-place edits would be.

### Neutral

- The library adds two third-party modules, a JSON Schema 2020-12 validator (to check events and the
  configuration against checked-in schemas) and `pgregory.net/rapid` for property tests, in test code
  only, as operator-accepted dependency choices of 2026-10-08.
- The daemon, the CLI, the web UI, the connector backend and the menu-bar plugin consume the library
  in later sub-projects; the daemon's loopback and no-authentication decision is restated by
  sub-project 2's amendment to this ADR.

See also: `phillipgreenii-nix-agent-support` ADR 0062 (pg-connector's Tier-1 and Tier-2 split, the
process-boundary adapter the connector backend follows).
