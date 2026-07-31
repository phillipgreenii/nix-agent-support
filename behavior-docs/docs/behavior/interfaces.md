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
- **Its catalog, in full, where the boundary carries one** — the closed set of named values that
  cross it (metrics, event types, failure classes). The catalog belongs **here**, to the interface
  that carries it; an invariant states the obligation over it, never the list (`INV-8`).

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

### Participation → what the system is for

Kind answers _how agreement is reconciled_. It does **not** answer _what the system is for_, and it
cannot: several interfaces of a system routinely share one kind — four pluggable extension points are
all **implementers** — while differing entirely in whether the system means anything without them.
So every interface is classified on a **second, independent axis** (`INV-8`):

- **Essential participant** — the system is **nonsense** without it. It sits on the path the system
  exists to serve, and is touched every time the system is used.
- **Optional participant** — the system runs **untouched** without it. Its interface is real and MUST
  still be defined (`INV-13`); a deployment that never configures it is a valid deployment. The
  **frequency asymmetry** follows from that rather than being a third axis: an optional participant is
  configured rarely, or never.

|                 | Essential participant                                 | Optional participant                                    |
| --------------- | ----------------------------------------------------- | ------------------------------------------------------- |
| **Peer**        | an integration the system's purpose depends on        | an integration a deployment MAY leave unwired           |
| **Implementer** | an extension point the system is meaningless without  | an extension point a deployment MAY leave unimplemented |
| **Owner**       | a contract this set's purpose depends on implementing | a contract this set MAY implement and still be itself   |
| **Actor**       | the party the system exists to serve                  | a party that MAY never appear in a valid deployment     |

A set MUST group its interfaces so **both** axes are readable, and MUST NOT group on kind alone —
grouping on kind alone flattens the essential/optional distinction away, which is the distinction a
reader needs first.

## `INTF-1` — Behavior docs → downstream <!-- uuid: f8e47ad0-bf96-43d1-b174-baa959a2b6a2 -->

The behavior docs set is the sole authority downstream artifacts derive from.

- **Counterparty** — kind: **actor** (`ACTOR-2`, the Implementer, drives this boundary; there is no
  second set to cross-check). Participation: **essential** — a behavior docs set nothing derives from
  serves no purpose, which is the north-star.
- **Crosses the boundary (outgoing)** — intended behavior at the floor, as invariants,
  goals, actors, interfaces, stories, use cases, and journeys, each carrying a stable ID.
- **Catalog** — the element kinds above **are** this boundary's catalog: the closed set of typed
  names it carries (`INV-3`). There is no second, separate list.
- **What must hold** — downstream MUST cite the ID it implements or verifies (`INV-3`), so
  the `intent → check` link survives the disposable artifact. Silence in the docs is a gap
  to surface, not license to invent.
- **Deliberately open** — the _structure_ of downstream artifacts is unspecified by this
  method; no file layout, document set, or format is prescribed, and none may be inferred.

_The behavior-docs system has no peer product and no implementer of its own contract, so `INTF-1`
needs no incoming side. A behavior docs set whose system integrates with others defines each
interface on both axes above — the counterparty's kind (peer, cross-checked, `INV-18`; implementer,
reconciled by a conformance suite, `INV-18`; owner; or actor) **and** its participation (essential
or optional). With a single interface this set has nothing to group, so the grouping obligation of
`INV-8` is satisfied vacuously here and bites in any set with several._
