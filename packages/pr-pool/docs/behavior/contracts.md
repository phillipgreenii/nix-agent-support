# pr-pool — extension contracts

pr-pool is generic because it interacts with the outside world only through two
**contracts**. A deployment plugs a concrete tool into each; pr-pool names none. What
each contract _requires_ and _guarantees_ is behavior and lives here; **which tool
fills it, and why, is the deployment's to document** (e.g. a capability map in the
deployment's own set).

See the [glossary](glossary.md) for terms and [invariants](invariants.md) for the
IDs.

## The agent-runner contract (`INV-RUN-1`)

pr-pool does not run role logic itself; it dispatches a role to an **agent-runner**.

**pr-pool requires the runner to:**

- run a role's work as an isolated **session** — process isolation for untrusted
  content, tools scoped **least-privilege for the role**, and **no inheritance** of
  the operator's ambient credentials/secrets (`INV-SEC-1`);
- enforce the **budget** pr-pool hands it and wind down / stop on exhaustion
  (`INV-BUDGET-1`);
- expose the session as **externally identifiable and monitorable** (`INV-OBS-1`);
- report a terminal outcome pr-pool can classify by **failure class** (`INV-FAIL-1`).

**pr-pool guarantees to the runner:**

- one dispatch per **item** at a time, under a role-derived identity (`INV-CLAIM-1`);
- a bounded budget per run/role;
- that guardrails it sets (isolation, permission scope) are not weakened by a role's
  prompt/config (`INV-SEC-3`).

pr-pool does **not** guarantee anything about what the role _accomplishes_ — a role
may fail or do the wrong thing; pr-pool's contract is about safe, bounded,
observable **dispatch**, not about the work's correctness.

## The query-source contract (`INV-QSRC-1`)

pr-pool discovers work by running configured **queries** against a **query source**.

**pr-pool requires the source to:**

- answer a query with a set of **items** to consider (pr-pool treats an item as
  opaque — it does not interpret what the item _is_);
- be **read-only for discovery** — running a query never mutates the work;
- expose **durable claim/lease state** so that an item claimed in one drain is not
  re-offered to another session before its owner releases it, giving pr-pool
  cross-drain exclusivity (`INV-CLAIM-1`).

**pr-pool guarantees to the source:**

- it claims an item before dispatching and releases the claim when the item is handed
  back or sidelined (`INV-CONT-1`, `INV-CONT-2`);
- it does not require the source to understand roles, workflows, or budgets.

Durable **parking** (keeping a stuck item off _future_ ready sets, recording why) is
built on this contract by the deployment — pr-pool only sidelines within a run.

## What is deliberately NOT a contract here

Reviews, merging, PR/MR lifecycles, escalation surfaces, readiness/triage signals,
and the choice of tracker or provider are **workflow** — they are built _on top of_
these contracts by a deployment, not required _by_ pr-pool. If a concept can be
expressed as "a query returns items and a role handles them," it does not need a new
pr-pool contract.
