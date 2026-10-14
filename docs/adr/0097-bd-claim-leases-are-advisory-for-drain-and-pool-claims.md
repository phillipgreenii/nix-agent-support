# A bd claim lease is advisory for drain and pool claims, and no reaper keyed on it may run

**Status**: Accepted (extends 0083 and 0089; resolves `pg2-7gtae`)
**Date**: 2026-10-14
**Deciders**: the drain orchestrator session that dispatched `pg2-7gtae`. The operator MAY overrule it.

## Context

A `bd` claim takes a lease with a time-to-live (TTL) of about 5 minutes. `bd show` prints it as a
`Lease:` line. Observed 2026-10-13 on drain worker `pgii-pool__drain-pg2` working `pg2-zk77r`: the
line read "expires in 4 mins (heartbeat just now)" at about 06:32Z and "expired (heartbeat 7 mins
ago)" at 06:39Z while the session was active throughout (it spent about 5 minutes awaiting a
subagent). Only an explicit `bd heartbeat <id>` refreshed it. The bead stayed `in_progress` and
assigned to the worker, and nothing acted on the expiry.

`bd reclaim --help` describes `bd reclaim` as the reaper to run from a supervisor on a timer: it
reverts `in_progress` beads whose lease expired (past a grace window) to `open` and clears the
assignee. Re-verified 2026-10-14 with `rg` over this repository: nothing runs `bd reclaim` and
nothing runs `bd heartbeat`. The only mentions of `bd reclaim` are comments and behavior text that
FORBID it (`packages/pg-router-ccpool-handler/docs/behavior/invariants.md` under the death-release
rule, `internal/beads/issue.go` `heldGuard`, `internal/complete/complete.go` `OnDeath`).

The router already treats a lapsed lease as not proving a session is gone. Its release of a dead
session's claim is guarded (`--if-assignee <observed holder> --if-status in_progress`) and MUST NOT
be a bare `bd reclaim` (ADR 0083's supervision lease is the router's own, separate lease on the
SESSION, not the `bd` lease on the BEAD).

Pool budgets run up to 2 hours per bead (ADR 0089) against a TTL of about 5 minutes. A reaper keyed
on lease expiry would therefore take the claim from a live worker mid-work and let a peer claim the
same bead.

Three options were open:

- (a) The worker protocol (`pb:drain-one`, from the claim onward) runs `bd heartbeat <id>` at an
  interval below the TTL, about every 2 minutes.
- (b) pg-router heartbeats the claims it dispatched for as long as the session is alive.
- (c) Leases are advisory in this pool, and the `Lease:` line is ignored.

## Decision

Option (c) is the CURRENT STANDING decision, guarded by a tripwire. Rules (RFC 2119):

1. A `bd` claim lease MUST be treated as advisory for drain and pool claims. The `Lease:` line in
   `bd show` is NOT a liveness signal for them: an agent or tool MUST NOT read "expired" as
   evidence that the holder is dead, and MUST NOT release, reassign or re-claim a bead on that
   ground. Death of a holder is established the way ADR 0083 and the `pb:unstick-beads` liveness
   procedure establish it, never from the lease.
2. Tripwire: nobody MAY enable `bd reclaim`, or any other reaper keyed on lease expiry, against
   drain or pool claims until option (a) or option (b) has first been chosen, implemented and
   recorded (an ADR that supersedes this one, or an amendment to it). This holds for a supervisor, a
   timer, a launchd job and a one-off sweep alike.
3. Choosing (a) or (b) later MUST be a separate bead; this decision does not implement either.
4. A worker MAY run a bare `bd heartbeat <id>` for its own claim. It MUST NOT rely on one, and this
   decision does not require it.

### Why not (a) now

Option (a) is not free. An orchestrator awaiting a subagent has no turn in which to heartbeat: it
ends its turn and the task notification resumes it (the `pb:drain-one` rule on async dispatch). A
background `bd heartbeat` loop needs a `Monitor`, and a `Monitor` expires (30 minutes in the recipe
observed in the 2026-10-12 drain of `pg2-p049u`) so it must be re-armed and stopped (`TaskStop`)
after `bd close`. That recipe exists only as one worker's improvisation recorded in a memory file,
not in `pb:drain-one` or the worker prompt.

### Why (b) is the likelier future choice

For a pool-dispatched session, pg-router's handler already supervises the session for its whole
life and holds the claim's identity. It is the more natural home for a heartbeat than the worker
turn, which cannot wait and heartbeat at once. If a reaper is ever wanted, option (b) is the
default candidate, and that is a separate bead (rule 3).

## Consequences

- No behavior changes today. Nothing runs `bd reclaim`, and the router's guarded release is
  unchanged.
- A drain or pool bead shows an expired `Lease:` line while a worker is live. That is expected.
- The tripwire is recorded in `claude-marketplace/pb/skills/drain-one/references/rules.md`, and
  `flake.nix`'s `test-no-full-flake-check-land-mandate` pins the wording there, so it is read by
  every drain session and cannot be dropped silently.
- Until (a) or (b) lands, a claim whose worker died stays `in_progress` under its assignee until the
  router's guarded death release or an `unstick` liveness check releases it.
