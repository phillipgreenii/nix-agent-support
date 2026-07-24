# pr-pool — behavior docs

pr-pool is a **dispatcher**: it routes **typed events** from pluggable **event sources** to bound
**event handlers**, through a small set of interfaces. The core knows only those interfaces — which
concrete implementation fills a **participant** (event source, event handler, monitoring sink, or
storage) is uninteresting to it. This set follows the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`).

Start here, then the [glossary](glossary.md); the rules are in [invariants](invariants.md), the
boundaries in [interfaces](interfaces.md), the actors in [actors](actors.md), and the stories,
journeys, and open questions in [journeys](journeys.md).

## The model

```mermaid
flowchart LR
    SRCa["event source (pull / push)"] -->|typed events| core
    SRCb["event source"] -->|typed events| core
    subgraph prpool["pr-pool core — dispatcher + durable queue + registry"]
      core["enqueue event → match → offer to a bound handler until accepted"]
    end
    core -->|offer until accepted| HDL["event handler → handler session (agent or non-agent)"]
    core --- MON["monitoring sink (pull / push metrics)"]
    core --- STO["storage (optional)"]
    OP["operator via CLI"] -->|configure / run / inspect| core
```

Event sources **emit** typed events; event handlers **bind** to event types (or other declared
fields) and respond to any of their bound events. Everything outside the core — event sources,
handlers, monitoring, storage — is a **participant** reached through an interface; adding another
implementation is a detail the core never sees. Handlers may be agent or non-agent.

## Scope (extent + floor)

- **Extent (in)** — matching and routing typed events to bound handlers; the participant interfaces
  and their common contract; the **durable, ordered, de-duped, TTL-bounded event queue** with
  at-least-once delivery; concurrency and per-handler capacity; the operator CLI; the metric catalog;
  **workflows** (declared wiring + validation); the daemon / run-until-idle lifecycle.
- **Extent (out)** — concrete participant **implementations** (ccpool, beads, prometheus, …) and any
  deployment-specific behavior live in `zr pr-pool-components`; governance authority and tech choices
  are decision docs; the "how" is downstream.
- **Floor** — pr-pool speaks in events, bindings, participants, handler sessions, and workflows. It
  names no concrete tool, transport, tuning constant, or file layout.
