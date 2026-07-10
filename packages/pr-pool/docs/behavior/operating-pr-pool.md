# Operating pr-pool

See the [glossary](glossary.md) and [invariants](invariants.md) for shared terms and
IDs, and [contracts](contracts.md) for the extension boundaries.

## What operating pr-pool means

Using pr-pool is **deciding which queries and roles you need, and which tools fill
its contracts** — then letting it run. You:

1. point pr-pool at a **query source** (a tool satisfying `INV-QSRC-1`) and write the
   **queries** that discover the items you care about;
2. define the **roles** that handle what each query returns (a prompt + the query
   that feeds it + caps);
3. choose the **agent-runner** (a tool satisfying `INV-RUN-1`) that runs those roles;
4. set the **budget**.

pr-pool then drains: it runs each query, claims each item, dispatches the matching
role through the runner, and exits when nothing is ready. A scheduler re-invokes it
for continuous operation. What the items _mean_, and what the roles _do_, is entirely
yours — pr-pool only guarantees its own contract (isolation, budget, single-owner,
continuity; see [invariants](invariants.md)).

## User stories

- As an operator, I want to run one thing and have **all ready work advanced**, then
  have it exit.
- As an operator, I want to run it **continuously** and trust it to pause on usage
  limits and resume next window.
- As an operator, I want to **define queries and roles in config** — including
  plugging in different tools — without changing pr-pool (`INV-OP-2`).
- As an operator, I want to **see** what it is configured to do and what it is doing
  (`INV-OBS-1`).

## Design goal — keep it minimal

- **`GOAL-SIMPLE-1`** and **`GOAL-SIMPLE-2`** (in [invariants](invariants.md)) are the
  north star: pr-pool orchestrates and nothing more; every workflow and every tool
  choice lives outside it, in the deployment. If a proposed pr-pool feature encodes
  _what the work is_, it belongs in a deployment instead.

## Usage scenarios

- **Advance all ready work:** `pr-pool` (= `pr-pool drain`). Verify it works ready
  items until none remain, then exits.
- **Inspect the resolved config** (queries, roles, caps, budgets, permission
  posture): `pr-pool config --show`.
- **Run continuously:** under a scheduler/loop; verify it pauses on a usage limit and
  resumes in the next window without losing in-flight work (`INV-CONT-1`,
  `INV-CONT-3`).

## Open questions

- How are **workflows grouped/named** in config (a "workflow" being a named set of
  roles and their queries)? Today roles are a flat set.
- **Scheduling:** the continuous-run mechanism (timer vs. loop) — currently a manual
  invocation.
- How much tool-specific behavior still living in pr-pool should **migrate out** into
  config or extensions over time (`GOAL-SIMPLE-1` direction of travel)?
