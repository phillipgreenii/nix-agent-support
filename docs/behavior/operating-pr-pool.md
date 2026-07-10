# Operating pr-pool

**Status:** Living source of truth. Downstream artifacts conform to this.

## Purpose

How I drive **pr-pool** — the orchestrator that turns ready work into progress.
This is the "how do I use the tool for anything" layer. The specific workflows it
runs (reviewing PRs, shepherding my PRs, working the backlog) are defined in their
own docs and **assembled through configuration**; pr-pool itself is
workflow-agnostic.

## Design goal — keep it minimal

- **`GOAL-SIMPLE-1`** — pr-pool does exactly one thing: **orchestrate** (discover
  ready work, dispatch a session per configured role, run to empty). _How_ work
  happens — the roles, prompts, queries, integration styles, budgets — is defined
  by **configuration and extensions**, not baked into pr-pool. Over time, less
  implementation detail should live in pr-pool, not more.
- **`GOAL-SIMPLE-2`** — Organization/repo-specific behavior and config live in that
  organization's/repo's own repositories, never in pr-pool. pr-pool ships generic
  defaults; a deployment supplies the specifics.

## Model

- pr-pool performs a **drain**: discover ready work, dispatch a session per
  configured role up to each role's cap, work every ready item, then **exit** (run
  to empty; `INV-OP-1`).
- **Continuous operation is a scheduler**, not the drain: a scheduler/loop
  re-invokes the drain and **idles** between runs when nothing is ready
  (`GOAL-CONT-4`). On a **usage limit**, in-flight work **pauses** and resumes in
  the next window (`INV-CONT-3`) — a pause, never an exit.
- **Roles are configuration, not code.** The set of roles, their prompts, and their
  queries live in config; new roles — and other tools — are added without changing
  pr-pool (`GOAL-SIMPLE-1`).
- Everything pr-pool dispatches is subject to the cross-cutting invariants
  (continuity, claim identity, budget, cleanup, safety).

## Sessions

- **`INV-OBS-1` (sessions facet)** — a **session** is one running agent instance.
  It is **externally identifiable and monitorable**, reused across items with
  **per-item state isolation**, and torn down when its work ends. A session is
  bounded by **both** its per-role cap **and** the shared budget (`INV-BUDGET-1`).
  This is what makes "I can monitor what's running" (`INV-OBS-1`) achievable.

## Roles ↔ the Actors the workflows define

The workflows are where roles are _defined_ (by what an actor does); pr-pool is
where they are _configured_ (as dispatchable roles with caps/prompts/queries):

| Workflow actor (defined in…)              | Configured role |
| ----------------------------------------- | --------------- |
| Review agent (reviewing-others / my PRs)  | a `review` role |
| Work agent (shepherding my PRs / backlog) | a `work` role   |
| Triage agent (working the backlog)        | a `triage` role |

Adding a workflow generally means defining its actors, then configuring the
matching roles — no pr-pool code change (`GOAL-SIMPLE-1`).

## User stories

- As an operator, I want to run one thing and have **all ready work advanced**.
- As an operator, I want to run it **continuously** and trust it to back off on
  usage limits and resume in the next window.
- As an operator, I want to **define/adjust roles via config** — including plugging
  in other tools — without code changes.
- As an operator, I want to **see** what it's configured to do and what it's doing.

## Invariants (MUST / MUST-NOT)

- **`INV-OP-1`** — A drain works every ready item, then **exits**; it does not idle.
  Idling is the scheduler's job.
- **`INV-OP-2`** — Roles **MUST** be definable via configuration; adding a role or
  tool **MUST NOT** require changing pr-pool (`GOAL-SIMPLE-1`).
- **`INV-OP-3`** — pr-pool **MUST** apply the cross-cutting invariants to every unit
  it dispatches (notably `INV-CLAIM-1` role-scoped claim identity, `INV-SEC-1`
  untrusted-content isolation, `INV-BUDGET-1`).
- Guardrails **MUST NOT** be defeatable by editing a role's prompt/config
  (`INV-SEC-3`).

## Usage scenarios

- **Advance all ready work:** `pr-pool` (= `pr-pool drain`). Verify it works ready
  items until none remain, then exits.
- **Inspect the resolved config** (roles, caps, budgets, permission posture):
  `pr-pool config --show`.
- **Run continuously:** under a scheduler/loop; verify it pauses on a usage limit
  and resumes in the next window without losing in-flight work (`INV-CONT-1`).

## Open questions

- How are **workflows grouped/named** in config (a "workflow" = a named set of
  roles)? Today roles are a flat set.
- **Scheduling:** the continuous-run mechanism (timer vs. loop) — currently a manual
  invocation.
- How much of today's tool-specific behavior **migrates out of pr-pool** into config
  or other tools over time (`GOAL-SIMPLE-1` direction of travel)?
