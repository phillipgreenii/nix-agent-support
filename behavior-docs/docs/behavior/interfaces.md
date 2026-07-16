# Interfaces — the behavior-docs method

An **interface** is a boundary described by what crosses it and what must hold, so the
parties on each side can be confirmed to agree. The _how_ is downstream. A behavior docs
set MUST define all of its interfaces (`INV-13`), and every cross-product interaction MUST
be an explicit interface — no implicit interpretation (`INV-8`).

## `IF-1` — Behavior docs → downstream

The behavior docs set is the sole authority downstream artifacts derive from.

- **Crosses the boundary (outgoing)** — intended behavior at the floor, as invariants,
  goals, actors, interfaces, stories, and journeys, each carrying a stable ID.
- **What must hold** — downstream MUST cite the ID it implements or verifies (`INV-3`), so
  the `intent → check` link survives the disposable artifact. Silence in the docs is a gap
  to surface, not license to invent.
- **Deliberately open** — the _structure_ of downstream artifacts is unspecified by this
  method; no file layout, document set, or format is prescribed, and none may be inferred.

_The behavior-docs system interacts with no peer product, so it defines no incoming
cross-product interface. A behavior docs set whose system does (e.g. a tool that talks to a
code host) defines its outgoing and incoming interfaces per `INV-8`, cross-checked against
the peer per `INV-18`._
