and "the core takes no per-run status stream back from a handler at all" — with the one exception of a handler error carrying the budget-stop sentinel, which the core counts under `handler-error` with a `reason`/`role` label (operator ruling 2026-09-30, `DEC-OBS-3`) |# pg-router — behavior docs

pg-router is a **dispatcher**: it routes **typed events** from pluggable **event sources** to bound
**event handlers**, through a small set of interfaces. The core knows only those interfaces — which
concrete implementation fills a **participant** (event source, event handler, monitoring sink, or
storage) is uninteresting to it. This set follows the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`).

Start here, then the [glossary](glossary.md); the rules are in [invariants](invariants.md), the
boundaries in [interfaces](interfaces.md), the actors in [actors](actors.md), and the stories,
journeys, and open questions in [journeys](journeys.md).

## The model

The diagram below **is** this set's **interface inventory** — the five interfaces and the participant
kinds behind them. It is not a data-flow diagram and not a component diagram.

```mermaid
flowchart LR
    subgraph essential["essential path — remove any one of these and the tool is nonsense"]
      SRC["event source (pull mode or push mode)"]
      Q["durable event queue"]
      DISP["dispatcher — match a queued event to a bound handler"]
      HDL["event handler"]
      SRC ==>|"INTF-SOURCE: typed events"| Q
      Q ==>|"in order, de-duped"| DISP
      DISP ==>|"INTF-HANDLER: offer until accepted"| HDL
    end
    OP["operator"] -.->|"INTF-CLI: configure, run, inspect"| DISP
    STO["storage"] -.->|"INTF-STORE: key or value scratch"| DISP
    MON["monitoring sink"] -.->|"INTF-MON: declared subset of the metric catalog"| DISP
```

**The essential path is event source → queue → event handler**, drawn thick and boxed above. An
event source **emits** typed events; whether the core pulls them or the source pushes them is a
**mode** of the one source participant kind, not two kinds. Every event reaches a handler **through
the queue**, so the queue is a node here rather than a phrase inside a subgraph label — it is the
universal intermediary, not a realization detail. An event handler **binds** to event types — and MAY
narrow on a payload path its own binding names — and responds to any of its bound events; **one run of
one handler against one event** is a **handler session**, which is why the handler and the session are
not the same box.
A handler may be agent or non-agent, and a configured handler's operator-facing name is its **role**.

**The operator, storage and the monitoring sink are optional participants**, drawn dotted and
outside the box: the system runs untouched without them. `INTF-STORE` says a **default in-memory**
store applies when none is configured, and `INTF-MON` says a sink may be absent or several. The
**operator** is optional on the **event path** in the same sense, while being the only actor who
authors the wiring — the operator shapes what flows without standing in the flow. So the event source
and the event handler are this system's **essential participants** and those three are its
**optional participants**: the second axis the method requires of every interface, alongside its
counterparty's kind.

**The frequency asymmetry is why that split is worth drawing.** A new event source or a new event
handler is added often, and that extension is what the tool exists for; storage and monitoring are
configured once or never. Grouping all five interfaces on **kind** alone would flatten exactly that
difference away, so this set groups them on kind **and** participation.

The interfaces themselves are in [interfaces](interfaces.md), each carrying a diagram of the
interaction it intends, because the method asks a set to show intent through examples
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior · GOAL-7`). The concrete message
shapes those diagrams stand for are realization detail and live in pg-router's decision docs
(`phillipgreenii-nix-agent-support · packages/pg-router/docs/decisions · DEC-WIRE-1`); what an
implementation is actually checked against is the interface conformance suite (`INV-INTF-2`).

### The boundary principle

pg-router knows an event was **delivered**. It knows **nothing** about what happens inside an event
handler and **nothing** about how an event source derives its events. What is left is the whole of
what pg-router does: **enqueue, offer, accept-or-decline, expire, depth**.

One question generates that boundary: **does pg-router ACT on the value, or merely HAND IT OVER?** A
value pg-router acts on is part of pg-router's own configuration and contract. A value pg-router only
hands over is an **opaque token** — equally welcome, and it stays opaque. **Enforcing or
interpreting another tool's internals is what is out of scope.** A `command` handler's argv is the
shape of that grey area: it looks like another application's configuration living inside pg-router's,
but pg-router invokes it and never reads it, so it is a **pass-through** and belongs. A role's own
**configuration table** is the same shape: the core carries it to the role's adapter and never reads
inside it (`INV-ROLE-2`). **Pass-through is fine; enforcing another tool's limit is not.**

Asking that one question is what puts the following five outside pg-router's contract. They are **one
boundary, not five unrelated trims**:

1. **The handler's status callback** — what a running handler reports about its own progress is the
   handler's, so pg-router takes no such stream back.
2. **Post-acceptance failure classes** — after acceptance the handler owns persistence, resume and
   retry, so a failure past that point is not pg-router's to classify.
3. **Per-role capacity** — how many events a handler will run at once is the handler's own limit to
   keep, and pg-router declares it nowhere.
4. **Per-run `state` and `progress` fields in the operator's status reply** — the same handler
   internals reached by a second route, so the same answer applies.
5. **Tool-specific event-source query types** — how a source finds its events is the source's, so
   pg-router declares **one opaque source contract** rather than one type per tool.

Content the boundary puts outside this set is **relocated**, not discarded: it moves to pg-router's
decision area by the method's relocation procedure
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior · USECASE-5`). `GOAL-MIN-1` is this
principle's rule form; the principle is stated here, once, and nowhere else restated as a rule.

## Scope (extent + floor)

- **Extent (in)** — matching and routing typed events to bound handlers; the participant interfaces
  and their common contract; the **durable, ordered, de-duped, retention-bounded event queue** with
  at-least-once delivery; concurrency as the offer/accept model and its serialize marks — but **no
  declared per-handler ceiling**; the operator CLI; the metric catalog;
  the **wiring** (declared routing graph + validation); the daemon / run-until-idle lifecycle.
- **Extent (out)** — concrete participant **implementations** (a session runner, an issue store, a
  metrics backend, …) and any
  deployment-specific behavior live in a downstream deployment set that implements these interfaces;
  governance authority and tech choices are decision docs; the "how" is downstream.
- **Floor** — pg-router speaks in events, bindings, participants, handler sessions, and wiring. It
  names no concrete tool, transport, tuning constant, or file layout — the clause is aimed at
  **external** tools, most pointedly an external PR-management tool, which pg-router takes only as
  opaque configuration, never a literal. **There is no exception.** An earlier ruling (2026-09-14)
  let the contract surface name a fixed executor stack directly, because one of its tools was the
  core's own executor. That ruling is **superseded** (operator, 2026-10-19/20, ruling 14 of the
  role-environment design review, recorded 2026-10-20): the core has no executor of its own, since
  every role is run by the adapter it names (`INV-ROLE-1`), so this set names **no** concrete tool,
  and the contracts it states — the manager contract and the wrapper contract — carry what a
  tool-specific sentence would have.

## Realization gaps

This set's **realization-gap register** (`INV-23`): intended behavior this set's implementation has
not built yet, one row per gap, each keyed by the **element id** the gap is against. The register is
**set-level metadata and never part of an element** — which is exactly what lets it say where the
code currently stands without putting a status annotation or a _how_ into any definition. A gap is
**not** an open question: the intent below is settled and the build has not caught up, so no gap is
recorded as an `OQ-`. One element MAY carry more than one row, because merging two divergences into
one row would lose which of them converged. A row names where the work is tracked because this
project tracks it in beads; that column is this set's own choice, not the register's shape.

| Element      | Intended                                                                                                                                                                                                                                                                               | Where the implementation stands                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | Tracked by                   |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| `INV-FAIL-1` | a **post-accept** outcome (`retryable`, `resource-limit`, `critical`) is the accepting handler's own: it is surfaced **only on the handler's own surface** (its own logs/metrics) or turned into a new event, and "the core takes no per-run status stream back from a handler at all" | `roleListener` — `INTF-HANDLER`'s own boundary object from the queue's point of view — reports a `resource-limit` hit (the budget watchdog's hard stop) back to the core's own Activity Ring via a new `orchestrator.ResourceLimitObserver` hook, called inline from `Offer` before it returns (`internal/orchestrator/listener.go:236-238`), fed into `cmd/pg-router/run.go`'s `activityObserver.OnResourceLimit` (`cmd/pg-router/run.go:441`) and rendered by the `status`/`tui` surfaces as a `budget_escalation` Activity entry; the same hard stop's error text is also classified by the metrics emitter (`reason="budget-exceeded"`, `role` label; bead `pg2-irowq`) — a similar-shaped core/handler-boundary crossing to the one the (now-closed) `INV-WORKFLOW-1` work-outcome row used to record for `buildResult`, not a new exception invented here | bead `pg2-fm2gw`             |
| `INV-ROLE-1` | every role names the adapter that runs it, and the core has no kind of role                                                                                                                                                                                                            | a role without its own adapter command still reaches a handler through one shared command plus a per-role file argument, so for that role the core resolves the handler by the earlier path; the two paths coexist until no role is left on the earlier one (`DEC-ROLE-1`)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | `pg2-12gr4.6`                |
| `INV-ROLE-2` | the role object travels in the payload of every request to a role's adapter, and the core decodes the role keys it reads strictly                                                                                                                                                      | neither is built: a role's table is a file the deployment renders and passes as an argument, and the core ignores an unknown key in a role entry                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `pg2-12gr4.6`                |
| `INV-ROLE-3` | surviving the core's shutdown is a role property the core reads from its own configuration                                                                                                                                                                                             | the core reads the property from the per-role file the deployment renders (`DEC-ROLE-1`)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | `pg2-12gr4.6`                |
| `INV-ROLE-4` | a payload starts from the minimal base environment and only its wrap expands it, for roles and queries                                                                                                                                                                                 | not built: a payload inherits what its adapter's own environment holds, and a query's producer inherits the core's                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `pg2-12gr4.2`, `pg2-12gr4.4` |
| `GOAL-MIN-1` | the contract surface of this package names no concrete tool at all                                                                                                                                                                                                                     | this set, its decision docs, the README's role description and the migration guide's deployment section name no session runner; the README's list of pool-wide settings and the migration guide's worked examples still name some other tools                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | not yet tracked by a bead    |

## External references

This set follows the behavior-docs method and cites elements the method defines, and its
implementation cites elements a downstream deployment set defines. Each external element it
references is declared here with the owner's UUID, so a cross-set reference resolves by the
owner's UUID — not the mutable name (a rename never breaks the seam). The owner set-path is the
cited `<repo> · <set-path>`, and this table spans **two** owner sets: the **method** set (the
rules this set follows) and the **ZR deployment** set (the deployment that implements these
interfaces). A handler participant's own behavior set owns the decline reasons the failure-rate
alert excludes (`skipped-*` and `session-in-flight`); this set states the exclusion as a contract
(`INV-FAIL-1`) and declares no row for them. The **what it is** column is one
line, so a reader learns why the row is there without following the reference, and the UUID is
rendered as a link to the owner's remote-served definition. **The UUID is the authority; the URL
may rot** — a dead link is an inconvenience, never a broken identity. A row declares **one**
element: where the implementation cites a whole family (`INTF-ZR-*`), the family resolves through
the declared member below.

| Name             | What it is                                                                                                              | Owner set-path                                                   | Owner UUID                                                                                                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `INV-11`         | a set's extent is exactly what its stories, use cases and journeys require                                              | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [f8174e40-806c-4c42-97da-996efd7c6e23](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md)   |
| `INV-18`         | inter-consistency at every interface, reconciled by the counterparty's kind                                             | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [4c6a764b-02f5-4c85-afae-a082fe6c21cd](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md)   |
| `INV-19`         | a set MAY declare a precedence ordering over its own invariants                                                         | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [4325bdf4-2458-4606-8b37-2e5e996aa53a](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md)   |
| `INV-23`         | the realization-gap register is set-level, named `## Realization gaps`, and never an element                            | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [f3bba3e7-440f-4109-a4de-9d37daa34bcf](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md)   |
| `GOAL-7`         | a set SHOULD show intent through examples                                                                               | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [42ad1aa1-af11-4387-bf02-e0f028f80434](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/invariants.md)   |
| `USECASE-5`      | the method's procedure for relocating implementation content out of a behavior doc                                      | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` | [7d6de948-3ef5-426a-949e-2dd872f06d28](https://github.com/phillipgreenii/nix-agent-support/blob/main/behavior-docs/docs/behavior/journeys.md)     |
| `INV-FRESH-1`    | don't act on stale truth — a readiness signal derived from data past its bound MUST NOT be presented as current         | `your-private-flake · modules/zm/pg-router/docs/behavior`        | [ceac8879-4bfd-45c7-94ed-9d8c3bd11c38](https://github.com/your-org/your-private-flake/blob/main/modules/zm/pg-router/docs/behavior/invariants.md) |
| `INTF-ZR-SOURCE` | the deployment's event sources implementing `INTF-SOURCE` — the declared member the `INTF-ZR-*` family resolves through | `your-private-flake · modules/zm/pg-router/docs/behavior`        | [9dad0914-4589-4690-9814-2f5936628722](https://github.com/your-org/your-private-flake/blob/main/modules/zm/pg-router/docs/behavior/interfaces.md) |
