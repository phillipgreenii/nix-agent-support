# Actors — the behavior-docs method

Who **acts upon** a behavior docs set. A behavior docs set MUST define all of its actors
(`INV-13`); this is the method's own list. Actor-ness is **scope-relative**: each party below is
an actor _here_ because it acts upon this method, and the same party is a **counterparty** in a
set that integrates with it (see the [glossary](glossary.md)).

- **`ACTOR-1` — Author** <!-- uuid: 21ef684c-f0bb-4f18-87c7-23fd8040fe63 --> — owns and writes the behavior docs; the authority for intended
  behavior, which derives from business need. A change of intent originates here.
- **`ACTOR-2` — Implementer** <!-- uuid: 29f48d82-00dd-455c-ac6f-c68e42a698f4 --> — consumes the behavior docs to produce downstream work
  (design, plan, tests, the implementation). Resolves uncertainty against the docs, cites
  the IDs of what it builds, and classifies a gap rather than guessing.

## Governance: behavioral slice in a set, authority in decision docs

Who or what _fills_ these roles — a person or an agent, a specific reviewer or operator, and what
approval or supervision **authority** applies — is the **authority slice** of governance: a
realization decision that lives in the project's **decision docs**, not here (this method's own
authority is `IMPL-1` in the decision docs). Where a described system instead carries governance
as **intended behavior** — an identity it acts under, a permission posture, a guardrail that must
not be config-defeatable — that **behavioral slice** belongs **in** that system's set, as ordinary
invariants. The method's own product has no such behavioral posture (its only actors are Author
and Implementer), so all of _its_ governance is authority, routed to decision docs.
