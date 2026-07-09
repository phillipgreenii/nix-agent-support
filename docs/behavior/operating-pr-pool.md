# Truth: Operating pr-pool

**Status:** Living source of truth. Downstream artifacts conform to this.

## Purpose

How I drive **pr-pool** — the orchestrator that turns ready work into progress.
This is the "how do I use the tool for anything" layer. The specific workflows it
runs (reviewing PRs, shepherding my PRs, working the backlog) are defined in their
own docs and **assembled through configuration**; pr-pool itself is
workflow-agnostic.

## Model

- pr-pool performs a **drain**: it discovers ready work, dispatches a session per
  configured **role** up to each role's limit, and keeps going until there is no
  ready work left (**run to empty**).
- **Roles are configuration, not code.** The set of roles, and the prompts and
  queries they run, live in config. New roles — and other tools — can be added
  without changing pr-pool. Over time, fewer implementation details live in
  pr-pool itself and more are expressed as config.
- A drain can run **continuously**; it idles only on a usage limit or when nothing
  is ready.
- The cross-cutting invariants apply to everything pr-pool dispatches (work never
  lost, stuck→park-and-continue, usage-limit pause/resume, budget, cleanup,
  observability).

## User stories

- As an operator, I want to run one thing and have **all ready work advanced**.
- As an operator, I want to run it **continuously** and trust it to back off on
  usage limits and resume in the next window.
- As an operator, I want to **define/adjust roles via config**, without code
  changes — including plugging in other tools.
- As an operator, I want to **see** what it's configured to do and what it's doing.

## Invariants (MUST / MUST-NOT)

- A drain **MUST** run to empty (work every ready item, then stop).
- Roles **MUST** be definable via configuration; adding a role or tool **MUST NOT**
  require changing pr-pool.
- pr-pool **MUST** apply the cross-cutting invariants to every unit it dispatches.
- A safety guardrail (e.g. authorship checks) **MUST NOT** be defeatable by editing
  a role's prompt in config.

## Usage scenarios

- **Advance all ready work:** `pr-pool` (equivalent to `pr-pool drain`). Verify it
  works items until none are ready, then exits.
- **Inspect the resolved config** (roles, budgets, limits, permission posture):
  `pr-pool config --show`.
- **Run continuously:** under a scheduler/loop; verify it pauses on a usage limit
  and resumes in the next window without losing in-flight work.

## Open questions

- How are **workflows grouped/named** in config (a "workflow" = a named set of
  roles)? Today roles are a flat set.
- **Scheduling:** the continuous-run mechanism (timer vs. loop) — currently a
  manual/console invocation.
- How much of today's tool-specific behavior **migrates out of pr-pool** into
  config or other tools over time?
