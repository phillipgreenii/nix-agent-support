# Interfaces — the behavior-docs method

An **interface** is a boundary described by **what crosses it** (its field-shape) and **what must
hold**, so the parties on each side can be confirmed to agree. The _how_ is downstream. A behavior
docs set MUST define all of its interfaces (`INV-13`), and every cross-product interaction MUST be
an explicit interface — no implicit interpretation (`INV-8`).

## Describing an interface

State three things, never how it is implemented:

- **What crosses** — the fields (a field-shape), each with a **checkable** shape. The method fixes
  no wire format or schema language; _checkable_ means an implementer can assert against it. Which
  concrete schema realizes that (a JSON Schema, a type, …) is downstream, not a method requirement.
- **What must hold** — the obligations and guarantees on each side.
- **How agreement is confirmed** — set by the **counterparty**'s kind (below).

Initiation and timing (who calls first, inline vs. deferred) need **not** be a classification axis;
a sequence diagram — or a one-word initiator note — carries them.

### Counterparty kinds → how agreement is reconciled

- **Peer** — an independent product that keeps its **own** behavior docs set. Each side defines the
  interface on its own side (`INV-8`) and the two are **cross-checked** outgoing-to-incoming
  (`INV-18`).
- **Implementer** — a set or a pluggable implementation that **realizes** _this set's_ contract,
  and which often keeps **no** set of its own. It **cites** the owner's interface, states only its
  own obligations, and reconciles agreement with a **conformance suite** rather than a peer
  cross-check (`INV-18`).
- **Owner** — the dual of implementer: the set whose contract _this set_ implements. This set
  **cites** the owner's interface (one-directional) and reconciles by running the owner's
  **conformance suite** — a looser coupling than a peer cross-check, and never a mutual one (the
  owner does not cite its implementers, `INV-3`).
- **Actor** — a human or agent that drives the system through the interface. The interface
  definition itself is the agreement; there is no second set to cross-check.

## `INTF-1` — Behavior docs → downstream

The behavior docs set is the sole authority downstream artifacts derive from.

- **Crosses the boundary (outgoing)** — intended behavior at the floor, as invariants,
  goals, actors, interfaces, stories, and journeys, each carrying a stable ID.
- **What must hold** — downstream MUST cite the ID it implements or verifies (`INV-3`), so
  the `intent → check` link survives the disposable artifact. Silence in the docs is a gap
  to surface, not license to invent.
- **Deliberately open** — the _structure_ of downstream artifacts is unspecified by this
  method; no file layout, document set, or format is prescribed, and none may be inferred.

_The behavior-docs system has no peer product and no implementer of its own contract, so `INTF-1`
needs no incoming side. A behavior docs set whose system integrates with others defines each
interface per the counterparty's kind above — peer (cross-checked, `INV-18`), implementer
(reconciled by a conformance suite, `INV-18`), or actor._
