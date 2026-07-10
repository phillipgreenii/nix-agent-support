# pr-pool — invariants (the orchestrator's contract)

These are the rules **pr-pool guarantees as an orchestrator**. pr-pool does not
control what a role _does_ once dispatched (a role could still do the wrong thing);
these bound only pr-pool's own behavior and the **contracts** it holds with its
agent-runner and its query sources. The ID convention and the invariant / goal /
concept distinction are defined in the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`).

## Orchestration

- **`INV-OP-1`** — A drain works every ready item, then **exits**; it does not idle.
  Idling is the scheduler's job.
- **`INV-OP-2`** — **Queries and roles MUST be definable via configuration.** Adding
  a query, a role, or an interfacing tool **MUST NOT** require changing pr-pool
  (`GOAL-SIMPLE-1`).
- **`INV-OP-3`** — pr-pool **MUST** apply its own contract to every unit it
  dispatches: isolation (`INV-SEC-1`), budget (`INV-BUDGET-1`), single-owner
  (`INV-CLAIM-1`).

## Contracts with extensions

- **`INV-RUN-1`** (agent-runner) — pr-pool runs every role through an **agent-runner**
  that **MUST** provide process isolation for untrusted content (`INV-SEC-1`),
  least-privilege tools scoped to the role, budget enforcement, and an externally
  monitorable session (`INV-OBS-1`). pr-pool names **no** specific runner; see
  [`contracts.md`](contracts.md).
- **`INV-QSRC-1`** (query-source) — a **query source** is **read-only discovery**: it
  returns the items to consider and **MUST** expose durable claim/lease state so
  exclusivity can survive across drains (`INV-CLAIM-1`). pr-pool names **no** specific
  source; see [`contracts.md`](contracts.md).

## Budget

- **`INV-BUDGET-1`** — Work runs under a **budget** (wall-clock, optionally
  tokens/cost; per-run and/or per-role). Approaching it triggers an orderly
  wind-down (save progress, hand back); exceeding it stops the work safely. Mid-work
  exhaustion is treated like a usage limit — progress is saved and the dispatch
  **sidelined** (`INV-CONT-2`), never lost (`INV-CONT-1`).

## Continuity (what pr-pool guarantees about work it is coordinating)

- **`INV-CONT-1`** — **Work pr-pool is coordinating is never silently dropped.** On
  failure it does bounded retries (`INV-FAIL-1`); if it still can't proceed it **hands
  the item back** — releases the claim, records the failure — rather than losing it.
  _How_ a deployment then surfaces that hand-back to a human is the deployment's
  concern.
- **`INV-CONT-2`** — **A stuck dispatch does not stop the drain.** pr-pool
  **sidelines** it (removes it from this run's ready set and releases its claim) and
  continues other items. Durable **parking** — recording the situation and keeping the
  item off _future_ ready sets — is a deployment concern realized through the query
  source, not something pr-pool does itself.
- **`INV-CONT-3`** — **Usage limits pause, they don't fail.** On a provider usage
  limit the affected work pauses until the next window, then resumes.
- **`GOAL-CONT-4`** — Continuous operation is expected; pr-pool may run indefinitely,
  idling only when a usage limit is active or nothing is ready.

## Failure handling

- **`INV-FAIL-1`** — **The response depends on the failure _class_:**
  - _usage limit_ (rolling window) → pause + auto-resume next window (`INV-CONT-3`);
  - _transient_ (network, rate blip, flaky) → bounded automatic retry;
  - _non-retryable_ (authentication/permission failure, invalid request) → **stop;
    hand the item back** (`INV-CONT-1`). Retrying burns budget and never recovers.

## Concurrency

- **`INV-CLAIM-1`** — pr-pool **MUST NOT** dispatch the same discovered **item** (a
  query result) to more than one role/session at once; each in-flight item has
  exactly **one** owning session for its run, under a **role-derived identity** (never
  a shared default). Where exclusivity must survive across drains, the durable claim
  state is supplied by the query source (`INV-QSRC-1`). _(pr-pool reasons about
  "items," never about PRs or tracking objects — those are deployment concepts.)_

## Safety (untrusted content)

- **`INV-SEC-1`** — When a role processes untrusted content (e.g. a checked-out PR
  head), pr-pool **MUST** run it isolated, with tools **least-privilege for the role**
  and **no inheritance of ambient credentials/secrets** — so untrusted content cannot
  exfiltrate secrets or act under the operator's identity. Realized by the
  agent-runner (`INV-RUN-1`).
- **`INV-SEC-3`** — **Guardrails are not defeatable by config.** Isolation and
  permission-scoping (the rules above) **MUST NOT** be weakened by editing a role's
  prompt or config.

## Observability

- **`INV-OBS-1`** — pr-pool **MUST** be monitorable from outside itself. At minimum:
  queue depth, per-role session activity, budget consumption, usage-limit backoff
  state, and sidelined-item count (`INV-CONT-2`). _(Emission mechanism is
  downstream.)_

## Precedence

- **`INV-PREC-1`** — When two of pr-pool's rules conflict, the ordering is
  **safety/isolation > never-drop-work (continuity) > efficiency**. A
  newly-discovered conflict **MUST** be logged as an open question and resolved by an
  ADR, not by an agent choosing arbitrarily.

## Keep it minimal

- **`GOAL-SIMPLE-1`** — pr-pool does exactly one thing: **orchestrate** (run queries,
  dispatch the matching role per result, run to empty). _How_ work happens — the
  queries, roles, prompts, tools — is **configuration and extensions**, not baked in.
  Over time, less implementation detail should live in pr-pool, not more.
- **`GOAL-SIMPLE-2`** — Organization/repo-specific behavior, workflows, and tool
  choices live in that deployment's own repository, never in pr-pool.
