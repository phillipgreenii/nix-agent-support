# Glossary

Shared vocabulary for the behavior docs. Terms are defined at the product level
and tool-neutrally; **which tool provides a capability today lives in
[`capability-map.md`](capability-map.md)**, not here. When a term below is used in
another doc it means exactly this.

- **Source-of-truth (behavior) doc** — a living document describing how the system
  _should_ behave (user stories, journeys, constraints, goals, invariants). It sits
  above throwaway spec → design → plan → code. See [`README.md`](README.md).
- **Downstream artifact** — a spec, design, plan, or implementation reference
  derived from a behavior doc; disposable once the code re-converges.
- **Invariant** — a rule that must always hold (`MUST` / `MUST NOT`). Every
  invariant has a stable **ID** (e.g. `INV-TRACK-2`) so downstream artifacts can
  cite it. Goals and defaults are _not_ invariants (see below).
- **Goal / constraint** — a desired property that is not absolute (often a `SHOULD`
  or a configurable default). Distinguished from an invariant on purpose.
- **Workflow** — an end-to-end way work flows through the system (reviewing others'
  PRs, shepherding my PRs, working the backlog). Workflows are defined by the
  behavior docs and _assembled_ from roles via configuration.
- **Role** — a configured worker type the orchestrator dispatches (e.g. a review
  role, a work role, a triage role). Roles are named in configuration; the
  workflows are where roles are _defined_ by what they do.
- **Orchestrator (`pr-pool`)** — the workflow-agnostic tool that discovers ready
  work, dispatches a session per configured role, and runs to empty. Deliberately
  minimal; see [`operating-pr-pool.md`](operating-pr-pool.md).
- **Agent** — a session that performs a role's work (a work agent, a review agent,
  a triage agent). Human-less; runs autonomously under budget.
- **Session** — one running agent instance. Identifiable and monitorable; its
  per-item state is isolated; bounded by a per-role cap and the shared budget.
- **Drain** — one pass of the orchestrator: discover ready work, dispatch, work to
  empty, then exit. Continuous operation is a **scheduler** re-invoking the drain.
- **Tracking object** — the durable record of a unit of work (a PR/MR, or a backlog
  item). Its lifecycle and rules are in `invariants.md` (`INV-TRACK-*`).
- **Claim** — an agent taking ownership of a tracking object before working it,
  under a **role-scoped identity** (never a shared default) so two agents can't
  work the same item (`INV-CLAIM-*`).
- **Park** — set a stuck tracking object aside: save/clean up, record the situation,
  mark it so it does **not** surface as ready, unclaim it, and continue other work.
- **Escalation** — handing a decision to the human. Delivered to the human and
  surfaced in the **NEEDS ME** view; the item stays visible until resolved. The
  current mechanism is a `human` label on the tracking object (see capability map).
- **NEEDS ME** — the pinned section of the glance-view listing items that require
  the human (escalations, parked-and-blocked, awaiting-merge).
- **Glance-view** — the single actionable surface the human triages from: one
  compact row per item with the signals needed to decide what to act on. Distinct
  from monitoring dashboards, which are read-only.
- **Ready / readiness** — an item is _ready_ only when it has enough information to
  act on and its gates are satisfied. A freshly-captured item may be recorded but
  not yet ready (see the triage role).
- **Triage role** — the role that decides readiness: improves an item, sets/clears
  the readiness signal, or escalates what it can't make ready.
- **do → review → resolve** — the loop applied to substantive work: an agent does
  the thing, one or more _independent_ review agents give feedback (in the common
  **review** format), the work is resolved against that feedback.
- **Review** — a structured set of comments about a change (whole-review / per-file
  / per-block). Workflow-independent; see [`reviews.md`](reviews.md).
- **Integration style** — how a repo's changes land: **PR-driven** (open a PR;
  merge is gated by CI + approval + explicit permission) or **merge-to-main** (work
  in a worktree, rebase onto main, ff-merge back — agent-handled). Per-repo config.
- **Stacked PRs** — a change split into a sequence of dependent PRs; a downstream
  PR's work is gated on its upstream PR merging.
- **Gate** — a condition that blocks a tracking object from becoming ready until
  satisfied (e.g. an upstream PR merging).
- **Budget** — the bound on an agent's run (time, and optionally tokens/cost).
  Approaching it triggers wind-down; exceeding it stops work safely.
- **Feedback authority hierarchy** — when feedback conflicts: **me > human >
  agent** (`INV-AUTH-1`).
- **Capability** — a thing the system must be able to do (post a draft review, hold
  a gate, run a session). Named in behavior docs; mapped to a tool in the
  capability map.
