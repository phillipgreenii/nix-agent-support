# Glossary — the behavior-docs method

Vocabulary for the method itself. Terms specific to a described system live in that
system's glossary, not here.

## Artifacts & inputs

- **Behavior doc** — a living document describing how a system _should_ behave.
- **Behavior docs set** — all the behavior docs for one **scope**; the living source of
  truth for that scope's intended behavior.
- **Decision docs** — the durable "how" input: rationale-backed realization decisions
  (architecture, language, tooling, testing, tuning, governance). An **ADR** is the
  canonical instance. Combined with behavior docs to produce a product; out of this
  method's scope.
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

- **Actor** — a party that interacts with the system.
- **Interface** — a boundary described by what crosses it and what must hold — never how it
  is implemented — so the parties on each side can be confirmed to agree.
- **User story** — a want from an actor's perspective, in the standard role–capability–
  benefit form: `As <actor>, I want <capability>, so that <outcome>`.
- **Journey** — an end-to-end flow through the system, told from the outside; MAY be written
  as Given/When/Then (Gherkin) scenarios.
- **Invariant** — a rule that MUST always hold.
- **Goal** — a desired, non-absolute property.
- **Concept** — a named idea invariants and goals build on; not itself a rule.
- **Open question** — an explicitly-recorded gap, in a fixed shape: the gap, its owner, a
  resolution path, and where it blocks. Preferred over guessing.

## Examples

- **Illustrative example** — shows intent; not guaranteed byte-accurate.
- **Golden example** — asserted by a real test.
