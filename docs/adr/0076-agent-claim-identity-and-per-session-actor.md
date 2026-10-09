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

## Addendum 2026-10-02: callers, and the unwrapped `bd` build (bead `pg2-lhi3b`)

Decision 1 required every claim to carry an actor; this addendum records how the callers in this
repo now meet it, and what covers a `bd` build the machine wrapper (decision 2) does not wrap.

**Caller changes.** Every `bd` claim site listed in `pg2-lhi3b` now passes an explicit identity:

| Site                                                                       | How it carries an actor                                                                                                                                                                                                                                                                                                                  |
| -------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `packages/pg-connector/cmd/pg-connector-issue-beads/internal/runner.go`    | `CLIRunner` prefixes `--actor <id>` to every bd call, `<id>` = `CLIRunner.Actor`, else `$PG_CONNECTOR_ISSUE_BEADS_ACTOR`, else `$BEADS_ACTOR`. A claim-shaped call (`--claim`, `--status in_progress`, non-empty `--assignee`, before any `--` terminator) with no identity is refused with `ErrActorNotConfigured` without spawning bd. |
| `packages/pg-router-ccpool-handler/internal/beads/runner.go`               | `CLIRunner.Actor` (set from the dispatching role's ccpool actor via `NewCLIRunnerForRepo(dir, actor)`) is prefixed as `--actor`; a claim with no `Actor`, no `--actor` and no `$BEADS_ACTOR` is refused with `ErrClaimWithoutActor`.                                                                                                     |
| `claude-marketplace/plan-decompose/scripts/create-packet.sh` (`pg2-0pnww`) | New `--actor <id>`, forwarded to both `bd create` and `bd defer`; `plan-decomposer` and `phase-decomposer` are instructed to pass their session id.                                                                                                                                                                                      |
| `claude-marketplace/session-wrapup/skills/wrap-up-session/SKILL.md`        | The skill only closes and creates (it never claims); `bd close` now shows `--actor`, and a rule states any claim here MUST pass `--actor`. Creates are deliberately unchanged (operator ruling).                                                                                                                                         |

No default or constant identity is invented: the connector's identity comes from the launching
daemon or session exporting `PG_CONNECTOR_ISSUE_BEADS_ACTOR` (or `BEADS_ACTOR`); the handler's comes
from the role's own `ccpool.actor`, which `loadRole` now requires to be non-empty.

**Decision: the unwrapped `bd` build is covered by a documented mitigation, not by a new wrapper.**
The machine claim guard lives in the private consuming repo and wraps only the `bd` that repo builds;
this repo is standalone and public, so it cannot depend on that guard, and making every `bd` it
resolves (`llm-agentsPkgs.beads or llm-agents.packages.<system>.beads`) guarded would need a design
decision about where a generic guard lives. Instead the unwrapped build is covered in depth:

1. A pg-router-dispatched session always starts with `BEADS_ACTOR=<role ccpool actor>`
   (`internal/executor/ccpool.go`; asserted by `TestDispatch_exportsRoleActorAsBeadsActor`), and
   `loadRole` rejects an empty actor, so even an unwrapped `bd` that is handed no `--actor` resolves
   to the role identity and never to git `user.name`.
2. The two Go runners above refuse an identity-less claim themselves, independent of which `bd`
   binary is on PATH.
3. The machine wrapper stays the backstop for the build it wraps.

Residual, accepted: a process that runs an unwrapped `bd` claim with no `--actor` and no
`BEADS_ACTOR` (not dispatched by pg-router, not one of the runners above) still resolves to git
`user.name`. Closing that needs a generic guard shipped by this repo; file it separately if it
shows up in practice.

## Addendum 2026-10-08: stranded-claim liveness rules (bead `pg2-oob3u`)

Decision 4 left stranded-claim detection as follow-up work. It is now the `stranded` pass of
`packages/beads-exporter`. This addendum records the liveness rules; the exporter's README holds
the mechanics.

**What counts as a claim.** A not-closed bead with a non-empty assignee whose stored status is
`open`, `in_progress` or `hooked`.

**Live owner.** A claim is live when any of these holds, with "within the window" meaning within
`staleClaimHours` of now (hours, configurable, never minutes: an idle session is not a dead one):

1. a lower-case UUID inside the assignee names a session transcript
   (`<claudeDir>/projects/<slug>/<uuid>.jsonl`) written within the window;
2. a transcript written within the window (a session's own or a subagent's, never a statusline
   sidecar) uses the assignee as a claim value on a line the session itself wrote: the argument
   of `--actor`, the value after `BEADS_ACTOR=`, or an `"assignee":` JSON value, in raw or
   JSON-escaped form;
3. the assignee is a configured operator name and the claim itself is younger than the window.
   Rule 2 is skipped for operator names because the name appears in nearly every transcript.

A bare mention of the assignee is not a claim value and does not make a claim live. That includes
everything a tool printed: a transcript line that is a tool result is skipped whole, so a session
that listed beads as JSON (or grepped another transcript) does not revive the claims in the output. A value is recognised only when it is made of
letters, digits and `._-:@/+`; an assignee outside that set can be live by rule 1 or 3 only.

**Report only.** The pass exports counts and the oldest claim time as metrics, logs one line per
claim with no live owner (the bead's own fields, never transcript text), and a Grafana alert fires
after 30 minutes. Nothing releases or clears a claim, in line with the decision that detection is
alert/report only.

**Rejected.** A process or session-registry check (an actor id is not a session id), and a
bare-string match of the assignee against transcript text (any mention, including the alert's own
release command, would make a claim look live).
