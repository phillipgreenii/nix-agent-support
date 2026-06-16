# ccpool tracks session facts, not work judgments

**Status**: Accepted
**Date**: 2026-06-16
**Deciders**: Phillip Green II, Claude

## Context

`pr-pool drain` was wedging on a single feedback bead (`zr-prhuj`): every run
logged `WARN bead flagged ... ccpool new ...: did not reach ready before
timeout (state=starting)` and burned ~10 minutes before timing out. Root-cause
investigation (see the session transcript and `docs/superpowers/plans/2026-06-16-ccpool-pr-pool-session-redesign.md`)
found a chain of design problems, all stemming from one conceptual error:
**ccpool's session row was being used as a proxy for the bead's lifecycle.**

Findings (all evidenced against the code + the Claude Code hook contract):

1. **The ccpool row is a session, not a bead.** Per `internal/store/store.go`,
   the row is "durable session identity plus the last turn outcome." It has no
   concept of a bead. `pr-pool` had welded the two together by using the bead id
   as the ccpool session **name** (`pr-pool-<role>-<beadid>`), which is a stable
   primary key.

2. **ccpool overclaimed `done`/`failed`.** Those states came from Claude's
   `Stop` / `StopFailure` hooks — which mean "the turn ended" and "the turn hit
   an API error," NOT "the work is complete/failed." Verified against Claude
   Code docs: an external supervisor **cannot** infer work-done/failed from
   hooks; it can only observe started / working / idle (turn-ended) / awaiting-input
   / api-errored / live. Worse, non-purge `close` **fabricated** `done` by
   reconciling a non-terminal row to `Done` with no Claude signal at all.

3. **`new` resumed a finished/phantom conversation.** Because the name was a
   stable PK and non-purge `close` kept the row, every subsequent `drain`
   resumed the prior conversation (`claude --resume <name>`). The resume never
   reached `ready` — the row referenced a Claude session whose transcript no
   longer existed on disk (`~/.claude/projects/.../<uuid>.jsonl` was gone), so
   there was nothing to resume; it sat in `starting` until the 10-minute wait
   timeout, after which `close` fabricated `done`. Then the cycle repeated.

4. **Resumability was modeled as a state, not a fact.** ccpool treated
   `done`/`failed` as terminal, but Claude resumability is determined by whether
   the session still exists on the machine — independent of how the last turn
   ended (with documented edge cases: mid-turn-interrupt, API-error, and
   corrupt transcripts can make an on-disk session unresumable).

## Decision

Re-establish a clean three-layer separation and make ccpool report **observed
session facts** only.

**Lifecycle ownership**

- **Bead lifecycle** lives in `bd` (beads). It is the durable work item and may
  span any number of runs/attempts. The done/failed **judgment** is `bd`'s
  (and `pr-pool`'s), never ccpool's.
- **Session lifecycle** lives in ccpool. A row is one Claude conversation/attempt.
- **Mapping is 1 bead → N sessions over time** (one session per attempt), never
  1:1. `pr-pool` keys sessions per-attempt and never resumes.

**ccpool data model** (drop & recreate — no production data):

- `id` — surrogate primary key (internal).
- `external_id` — the caller's handle (unique). Callers address sessions by this.
- `claude_session_id` — the Claude session UUID (unique). Used to resume.
- `name` — optional display label, **nullable and non-unique**; falls back to
  `external_id` when null.

**ccpool state vocabulary** = observed facts only, no completion judgment:
`starting`, `ready`, `working`, `needs_input`, `idle` (was `done`; Claude `Stop`),
`errored` (was `failed`; Claude `StopFailure`). There is **no terminal concept**.
`close` no longer fabricates a state.

**ccpool resumability** = "the Claude session exists on the machine," detected via
the transcript at the recorded cwd — not a state:

- tmux session alive → reuse it directly.
- tmux gone but Claude session exists on disk → resume **by `claude_session_id`
  from the recorded cwd** (attempt it; surface failure, since on-disk ≠ always
  resumable).
- Claude session gone from the machine → **remove the row**, guarded against the
  fresh-session race (don't prune a row still in `starting` that hasn't written
  a transcript yet).

**pr-pool**

- `external_id` = `<role>-<beadid>-<timestamp>` (unique per attempt) → always a
  fresh session, never resumes. Optional `name` = `<role>-<beadid>` for grouping.
- **Purge** the session on teardown (`close --purge`) — pr-pool never resumes;
  continuity lives in `bd`.
- Completion of an attempt = ccpool reaching `idle`/`errored`; pr-pool then
  judges success/failure by reading the **bead** status.
- Escalate a bead to human after repeated consecutive launch failures (defense
  in depth) instead of retrying silently forever.

## Consequences

### Positive

- The wedge is structurally impossible: per-attempt `external_id` + purge means
  every dispatch is fresh; `bd` carries cross-run continuity.
- ccpool stops asserting a judgment it cannot make; its states are honest.
- Resume-by-`claude_session_id` is exact (resume-by-name could open Claude's
  session picker), and helps any ccpool consumer, not just pr-pool.
- Phantom rows (Claude session gone) are pruned rather than resurrected.

### Negative

- Large, cross-cutting change: ccpool's schema, store ops, hooks, session
  service, cmd layer, and wait/turns all key off `external_id`/`claude_session_id`
  instead of `name`, and the state rename touches nearly every ccpool file +
  tests. Mitigated by: no production data (drop & recreate) and a single
  combined branch with full test coverage.

### Neutral

- `name` becomes an optional display label; existing `--name`-style addressing
  becomes `external_id` addressing in the CLI.
- pr-pool gains a clock/timestamp seam for deterministic tests.

## Alternatives Considered

### pr-pool-only fix (unique name + purge), leave ccpool as-is

Fixes pr-pool's wedge with the smallest change, but leaves the "resume a
finished/phantom conversation" footgun and the dishonest `done`/`failed`
semantics in ccpool for the next consumer. Rejected in favor of fixing the
root semantic at the layer that owns session identity.

### Gate resume on a "terminal" state (fresh-on-`done`/`failed`)

An earlier proposal. Rejected: resumability is a fact (does the Claude session
exist?), not a state. Modeling it as a state is what caused the confusion in the
first place.

## Related Decisions

See also: ADR 0014 (ccpool reap-all pool registry).
See also: phillipgreenii-nix-personal — pr-pool progress-markers work (bead pg2-9ati).
