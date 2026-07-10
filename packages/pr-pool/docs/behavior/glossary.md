# Glossary — pr-pool

Vocabulary for pr-pool's behavior. Terms are defined at the product level and
tool-neutrally; **which tool provides a capability today lives in
[`capability-map.md`](capability-map.md)**, not here. When a term below is used in
another pr-pool doc it means exactly this.

Terms of the behavior-docs **method** itself — _behavior doc_, _invariant_, _goal_,
_downstream artifact_, _generic/base_, _per-project overlay_ — are defined once in
`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` and are not
restated here.

## The tool & how it runs

- **Orchestrator (`pr-pool`)** — the workflow-agnostic tool that discovers ready
  work, dispatches a session per configured role, and runs to empty. Deliberately
  minimal; see [`operating-pr-pool.md`](operating-pr-pool.md).
- **Drain** — one pass of the orchestrator: discover ready work, dispatch, work to
  empty, then exit. Continuous operation is a **scheduler** re-invoking the drain.
- **Workflow** — an end-to-end way work flows through the system (reviewing others'
  PRs, shepherding my PRs, working the backlog). Workflows are defined by these docs
  and _assembled_ from roles via configuration.
- **Role** — a configured worker type the orchestrator dispatches (e.g. a review
  role, a work role). Roles are named in configuration; the workflows are where
  roles are _defined_ by what they do. Which roles exist in a given deployment is
  that deployment's choice (its overlay).
- **Agent** — a session that performs a role's work (a work agent, a review agent).
  Human-less; runs autonomously under budget.
- **Session** — one running agent instance. Identifiable and monitorable; its
  per-item state is isolated; bounded by a per-role cap and the shared budget.

## Work & its records

- **Tracking object** — the generic, tool-neutral concept of _the durable record of
  one unit of work_. **Scope:** exactly one unit of work — a PR/MR, or a backlog
  item. **Lifespan:** created when the work is first detected/captured and closed
  when the work reaches a terminal state (its lifecycle and rules are in
  [`invariants.md`](invariants.md), `INV-TRACK-*`). The concept is tool-neutral; the
  tool that provides it today is in the capability map.
- **beads (`bd`)** — the specific tool that provides tracking objects today (see the
  capability map). "Tracking object" is the concept; "bead" is the concrete record
  in this tool. Docs state rules against the concept so the tool can change without
  rewriting them.
- **Claim** — an agent taking ownership of a tracking object before working it,
  under a **role-scoped identity** (never a shared default) so two agents can't work
  the same item (`INV-CLAIM-*`).
- **Park** — set a stuck tracking object aside: save/clean up, record the situation,
  mark it so it does **not** surface as ready, unclaim it, and continue other work.
- **Gate** — a condition that blocks a tracking object from becoming ready until
  satisfied (e.g. an upstream PR merging).
- **Stacked PRs** — a change split into a sequence of dependent PRs; a downstream
  PR's work is gated on its upstream PR merging.

## Readiness, triage & the human surface

- **Ready / readiness** — an item is _ready_ only when it has enough information to
  act on and its gates are satisfied. A freshly-captured item may be recorded but
  not yet ready.
- **Triage** — the _activity_ of deciding an item's readiness: improve it, set/clear
  a readiness signal, or escalate what can't be made ready. Whether a deployment
  runs a dedicated **triage role** (versus folding triage into another role) and how
  the readiness signal is set/cleared is defined by that deployment's overlay
  (`GOAL-READY-1`).
- **Escalation** — handing a decision to the human. Delivered to the human and
  surfaced in the **NEEDS ME** view; the item stays visible until resolved.
- **NEEDS ME** — the pinned section of the glance-view listing items that require
  the human (escalations, parked-and-blocked, awaiting-merge).
- **Glance-view** — the single actionable surface the human triages from: one
  compact row per item with the signals needed to decide what to act on. Distinct
  from monitoring dashboards, which are read-only.

## Reviews, authority & bounds

- **do → review → resolve** — the loop applied to substantive work: an agent does
  the thing, one or more _independent_ review agents give feedback (in the common
  **review** format), the work is resolved against that feedback.
- **Review** — a structured set of comments about a change (whole-review / per-file
  / per-block). Workflow-independent; see [`reviews.md`](reviews.md).
- **Integration style** — how a repo's changes land: **PR-driven** (open a PR; merge
  is gated by CI + approval + explicit permission) or **merge-to-main** (work in a
  worktree, rebase onto main, ff-merge back — agent-handled). Per-repo config.
- **Feedback authority hierarchy** — when feedback conflicts: **me > human > agent**
  (`INV-AUTH-1`).
- **Budget** — the bound on an agent's run (time, and optionally tokens/cost).
  Approaching it triggers wind-down; exceeding it stops work safely.
- **Capability** — a thing the system must be able to do (post a draft review, hold
  a gate, run a session). Named in these docs; mapped to a tool in the
  [capability map](capability-map.md).
