# Glossary — the behavior-docs method

Vocabulary for the method itself. Terms specific to a described system live in that
system's glossary, not here.

## Artifacts & inputs

- **Behavior doc** — a living document describing how a system _should_ behave.
- **Behavior docs set** — all the behavior docs for one **scope**; the living source of
  truth for that scope's intended behavior.
- **Decision docs** — the durable "how" input: rationale-backed realization decisions
  (architecture, language, tooling, testing, tuning, governance **authority**). An **ADR** is
  the canonical instance. Combined with behavior docs to produce a product; out of this
  method's scope. (A system's **behavioral** governance stays in its behavior docs — see
  [actors](actors.md).)
- **Two-input model** — `product = f(behavior docs, decision docs)`; the spec → design →
  plan between them is disposable.
- **Downstream artifact** — a spec, design, plan, or implementation reference derived from
  the behavior docs; disposable.

## Scope

- **Scope** — the one thing a behavior docs set describes, as **extent + floor**;
  established by its stories and journeys.
- **Extent** — what is in vs. out. A statement is in extent iff some user story or journey
  requires it.
- **Floor** — how deep the docs go before deferring downstream; located by the substitution
  test. "Implementation detail" is relative to the floor.
- **Substitution test** — generalize a term; if intended behavior is preserved you MUST
  generalize; if generalizing loses essential meaning within the extent, the specific term
  is at the floor — keep it.

## Contents of a behavior docs set

- **Actor** — a party that **acts upon** the system (has wants of it). Actor-ness is
  **scope-relative**: an entity is an actor in the set whose system it acts upon, and a
  **counterparty** where another set's system integrates with it.
- **Counterparty** — the party on the far side of an interface, seen from this set. Its **kind**
  fixes how agreement is reconciled: a **peer** (an independent product that keeps its own behavior
  docs set — cross-checked outgoing-to-incoming); an **implementer** (a set or a pluggable
  implementation that realizes _this set's_ contract — reconciled by a conformance suite); an
  **owner** (the dual of implementer: the set whose contract _this set_ implements — this set cites
  the owner's interface and reconciles by running the owner's conformance suite, a one-directional
  coupling looser than a peer cross-check); or an **actor** driving the system through the stated
  contract. Whether an integrated system is named an actor or a counterparty is scope-relative, not
  absolute.
- **Interface** — a boundary described by **what crosses it** (its field-shape) and **what must
  hold** — never how it is implemented — so the parties on each side can be confirmed to agree.
  Agreement is reconciled by a peer cross-check where the counterparty keeps its own set, otherwise
  by a conformance suite.
- **Conformance suite** — the checks an implementer runs to confirm it adheres to an interface's
  stated shape. A conformance suite reconciles agreement in place of a peer cross-check when the
  counterparty keeps no behavior docs set of its own.
- **User story** — a want from an actor's perspective, in the standard role–capability–
  benefit form: `As <actor>, I want <capability>, so that <outcome>`.
- **Journey** — an end-to-end flow through the system, told from the outside; MAY be written
  as Given/When/Then (Gherkin) scenarios.
- **Invariant** — a rule that MUST always hold.
- **Goal** — a desired, non-absolute property.
- **Concept** — a named idea invariants and goals build on; not itself a rule.
- **Open question** — an explicitly-recorded gap, in a fixed shape: the gap, its owner, a
  resolution path, and where it blocks. Preferred over guessing.
- **Precedence** — a ranking a set MAY declare for resolving a conflict between two of its own
  invariants. A newly-surfaced precedence conflict is recorded as an open question and settled by a
  decision doc, never chosen ad hoc.
- **Realization gap** — the distance between intended behavior (the docs) and what an
  implementation has yet built. A realization gap is normal — the docs may lead the build — and is
  tracked against the cited IDs, never annotated inline (the docs stay living, `INV-4`).
- **UUID** — an element's **stable identity**: a value minted once at the element's definition and
  never changed (`INV-3`), carried in an HTML comment on the definition line. The typed **name** is
  a mutable, intra-consistent label; references are name-based and cross-set matching is by UUID. A
  **removed** element leaves no tombstone — a deprecated/removed section would fight the living-doc
  rule (`INV-4`) — so when and why an element was removed lives in git history, not in the docs.
- **Reference seam** — a set together with the sets it references; the edge across which one set
  cites another's contract and vocabulary. Consistency is required along a reference seam
  (`INV-18`, `INV-20`) but not across unrelated sets.

## Examples

- **Illustrative example** — shows intent; not guaranteed byte-accurate.
- **Golden example** — asserted by a real test.
