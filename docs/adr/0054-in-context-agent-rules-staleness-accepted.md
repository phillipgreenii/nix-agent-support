# A long-running agent session's in-context CLAUDE.md/skills/commands go stale after an apply, and this is accepted with no mechanism shipped

**Status**: Accepted
**Date**: 2026-08-18
**Deciders**: Phillip Green II

## Context

`~/.claude/CLAUDE.md` (and every marketplace-shipped skill/command) is a home-manager symlink into
the nix store. An apply rewrites the symlink target, but the harness loads CLAUDE.md — and any
skill or command text — into a session's context ONCE, at session start (or at invocation, for a
command). A session that is already running does not see the rewrite.

Observed live, not hypothesised, in bead `pg2-80xds`:

- A drain session started 2026-08-14 00:24 kept an in-context `CLAUDE.md` copy that read F-1..F-8
  with no `Superseding Rulings` section and no F-9, for its ENTIRE remaining life (hours, dozens of
  beads), even though the on-disk file had been rewritten at 00:41 to add exactly those sections
  (rules landed at commit `5248f60b8b63079892b3c245756fb02641706602`, bead `pg2-xx1y5`).
- A second, independent instance widened the finding to COMMANDS, not just `CLAUDE.md`: a running
  `/drain-beads` session's in-context copy of the command definition had no
  `CLOSE-WITH-ABSORPTION-TRACE` section, while the on-disk copy (refreshed by the same apply) had
  it at four places. A missing command section is a missing CAPABILITY, not a missing preference —
  a stronger cost than a missing background rule.
- A mid-session re-read cannot simply replace the stale copy either: agent context is append-only,
  so re-reading `CLAUDE.md` mid-session leaves BOTH versions present, with the old rules still
  sitting there as apparently-valid instructions (e.g. two conflicting F-3 tables, one saying an
  absent path is decisive and one saying it is ambiguous) — a real design problem, not merely "read
  it again."

### Why this matters more than it first looks

The affected sessions are exactly the LONG-LIVED ones — `/drain-beads` loops, `/unblock-human-beads`
sweeps, polecats, background workers. Those are also the sessions the rules are most written for,
and the ones most likely to be running when an apply lands (an apply is often triggered specifically
to ship a rule at them). A rule can therefore be "live on the machine" and still not bind the agent
it was written to bind, with no signal in either direction: the agent cannot tell its copy is stale,
and the operator cannot tell the agent did not get it.

This is NOT a defect in any particular rule change, and NOT the same thing as marketplace/skill
staleness that an apply fixes — the installed, on-disk copies ARE refreshed correctly (`pg2-7hvwn`
proved this for the triggering change, with all its mechanical checks passing). The gap is between
the refreshed FILE and the already-loaded CONTEXT.

**The forward-only class is the worst-affected.** Guidance whose whole value is that it starts
applying promptly — the mistake-acknowledgment marker rules (M-1..M-3 in `~/.claude/CLAUDE.md`,
which generate data forward-only and cannot be backfilled) are the standing example — suffers most
from this gap. A session that misses them emits unmarked transcripts while believing it is
compliant, and that data can never be recovered after the fact.

## Decision

**Accept the behaviour as a known property. Ship NO mechanism to detect or correct it.**

A long-running agent session's in-context copy of `CLAUDE.md`, and of any skill or command text
loaded at invocation, may silently diverge from the on-disk (post-apply) version for the remainder
of that session's life. No code, hook, or instruction change closes this gap. An agent or operator
who notices the symptom should recognize it as this accepted property, not as a bug to fix.

### Alternatives considered and REJECTED

**Option 2 — an apply post-hook records that an apply happened; long-running commands re-read
`CLAUDE.md`/their own definition at a natural boundary (e.g. `/drain-beads`'s per-bead loop).**
Rejected. Even scoped to a natural boundary, this still hits the append-only-context problem above:
a re-read does not replace the stale copy, it ADDS a second, potentially-contradictory one, which
then needs an explicit precedence rule ("the copy read later supersedes the one loaded at session
start") — itself a real design question, not the cheap fix it first appears to be.

**Option 3 — compare the start-time `readlink` target of the governing home-manager derivation
against the current one, and re-read on change.** Rejected, for the same append-only-context reason
as option 2, plus it only detects that SOMETHING changed, not what — the agent would still need the
precedence rule above to act correctly on the detection. (For the record: the symlink target is
`/nix/store/<hash>-home-manager-files/.claude/CLAUDE.md`, a SINGLE derivation covering every
home-manager-managed agent-facing file, so this comparison would have caught skill/command drift
too, not just `CLAUDE.md`, with no producer-side plumbing. That generality did not change the
decision.)

Both were presented to the operator with their trade-offs and explicitly declined, including in
reduced form — no re-read at a loop boundary, no readlink comparison, no apply-generation counter,
no staleness warning. **This is an executed decision. A later reader who finds this gap again should
find this ADR, not re-open the design.**

### Rationale

- Applies are infrequent, and sessions eventually end — the exposure window for any single session
  is bounded, and the next fresh session starts with a fully current copy. The gap is self-clearing,
  never accumulating.
- Both live alternatives (2 and 3) require solving the append-only-context precedence problem to be
  correct, which is a nontrivial redesign of how an agent's context handles superseding instructions
  — not a proportionate cost for a bounded, self-clearing exposure.
- The cost is knowingly concentrated on the forward-only class (M-1..M-3-style rules): that cost is
  accepted rather than mitigated. A rule author who needs a forward-only signal to start binding
  promptly across already-running sessions must not rely on this mechanism to guarantee it.

## Consequences

### Positive

- No new mechanism, no new failure mode, no new per-session token cost to detect or narrate a
  no-op — this ADR's own existence (a doc under `docs/`, not the always-on rules file) is the only
  artifact, and it costs nothing to a session that never reads it.
- The design space is closed: a future contributor hitting this gap can cite this ADR instead of
  re-deriving and re-presenting the same three options.

### Negative

- A long-running session (drain loops, unblock sweeps, polecats, background workers) can execute for
  its entire remaining life against rules, skills, and commands that no longer match what is
  installed on disk, with no signal to either the agent or the operator.
- Forward-only, time-sensitive guidance (the M-1..M-3 marker family is the standing example) can be
  silently skipped by any session that started before the guidance landed, and that skipped
  compliance window is unrecoverable — the guidance generates data going forward only.
- A missing COMMAND section (not just a missing background rule) is possible, as demonstrated by the
  `/drain-beads` `CLOSE-WITH-ABSORPTION-TRACE` case — an agent can be missing an entire operating
  procedure it believes it has, not merely an outdated preference.

### Neutral

- This ADR does not change how home-manager, the apply pipeline, or the harness's context loading
  work. It records that a known interaction between them is accepted, not remediated.

## Related Decisions

- Bead `pg2-80xds` — the bead this ADR resolves; carries the full observation history (two
  independent live instances) and the operator ruling this ADR now records durably.
- Bead `pg2-7hvwn` — the change whose post-apply verification first surfaced the `CLAUDE.md`
  instance of this gap.
- Bead `pg2-zazgy` — surfaced the second, command-level instance (`CLOSE-WITH-ABSORPTION-TRACE`
  missing from a running session's copy of `/drain-beads`).
- Bead `pg2-xx1y5` — landed the M-1..M-3 mistake-acknowledgment marker rules, the standing example
  of the forward-only class this gap costs most.
