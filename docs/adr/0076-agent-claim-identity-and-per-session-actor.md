# Agent claim identity and the per-session actor question

**Status**: Accepted
**Date**: 2026-09-29
**Deciders**: phillipg

## Context

bd resolves the actor as `--actor` > `$BEADS_ACTOR` > git `user.name` > `$USER`. An agent claiming
without an actor claims in the operator's name; if it never releases, the bead is stranded (bead
`pg2-w2jlm`). Operator ruling, 2026-09-29: beads created in the operator's name are fine; the
primary concern is claims, because a claim left in the operator's name means no agent can pick the
work up.

## Decision

1. Every agent or daemon claim MUST carry `--actor "<session-id>[-<role>]"` (or `BEADS_ACTOR`).
   This is written into `pgii-agent-rules.md` (B-5 essence) and beads-lifecycle B-5.
2. A machine `bd` wrapper MAY enforce it by refusing a non-interactive/agent claim whose resolved
   actor equals git `user.name`. The consuming repo owns that wrapper (see its own ADR).
3. Secondary question, a single per-session actor config (for example `BEADS_ACTOR` set once per
   Claude session or daemon) for consistent `created_by`: NOT adopted as a global setting. The
   session id exists only per process tree, so a value set "once" cannot be correct for
   daemons, pre-existing shells, or sibling sessions. Explicit `--actor` per invocation stays the
   contract. Callers that create beads without an actor are tolerated (operator ruling) and MAY be
   fixed opportunistically.
4. Stranded-claim detection (a periodic scan for claimed-but-ownerless beads, any name) is
   follow-up work and is not part of this decision.
