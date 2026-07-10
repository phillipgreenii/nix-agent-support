# Glossary — pr-pool

Vocabulary for pr-pool's behavior as an **orchestrator**. Everything here is about
discovering items and dispatching roles safely — nothing about _what the work is_.
Domain terms (PR, review, merge, backlog, gate, triage, escalation, …) are
**workflow** and belong to the deployment's own set, not here.

Terms of the behavior-docs **method** (behavior doc, invariant, goal, generic set,
per-project overlay) are defined in
`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` and not restated.

- **Orchestrator (`pr-pool`)** — the tool these docs describe: it runs queries,
  dispatches the matching role per result, and runs to empty. Deliberately minimal.
- **Query** — a configured discovery request pr-pool runs against a query source to
  find items to consider. What a query looks for is configuration, not pr-pool's
  concern.
- **Query source** — whatever answers a query with items and holds durable claim
  state. Interacts with pr-pool only through the query-source **contract**
  (`INV-QSRC-1`); pr-pool names no specific source. _(A work tracker is one possible
  query source — an illustrative example, not a dependency on any particular tool.)_
- **Item** — one result a query returns. **Opaque to pr-pool**: it may _mean_ a pull
  request, an alert, a backlog entry, or a policy hit — pr-pool neither knows nor
  cares. The meaning is the workflow's.
- **Role** — a configured worker type pr-pool dispatches to handle items a query
  returns (prompt + which query feeds it + caps). Defined entirely in configuration.
- **Agent-runner** — the extension that actually runs a role's session; interacts
  with pr-pool through the agent-runner **contract** (`INV-RUN-1`). pr-pool names no
  specific runner.
- **Agent / session** — one running instance of a role's work under the agent-runner.
  Externally identifiable and monitorable; isolated; bounded by a per-role cap and the
  budget.
- **Drain** — one pass: run the queries, dispatch roles over the results, work to
  empty, then exit.
- **Scheduler** — whatever re-invokes the drain for continuous operation; idles
  between runs when nothing is ready.
- **Claim** — pr-pool taking ownership of an item before dispatching it, under a
  **role-derived identity** (never a shared default), so the same item is never worked
  by two sessions at once (`INV-CLAIM-1`).
- **Sideline** — pr-pool removing a stuck item from the current run's ready set and
  releasing its claim, so the drain continues (`INV-CONT-2`). Distinct from durable
  **parking** (a workflow concern built on the query source).
- **Budget** — the bound on a run (wall-clock, optionally tokens/cost; per-run and/or
  per-role). Approaching it winds work down; exceeding it stops it safely.
- **Contract** — the behavior pr-pool _requires of_ and _guarantees to_ an extension
  (agent-runner, query-source). The one place pr-pool's boundary with the outside
  world is defined; see [`contracts.md`](contracts.md). Which tool fills a contract is
  the deployment's to record, not pr-pool's.
