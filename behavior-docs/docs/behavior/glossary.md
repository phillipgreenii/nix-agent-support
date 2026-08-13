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
  by a conformance suite. Every interface is classified on **two** axes: its counterparty's kind,
  and whether that counterparty is an essential or an optional participant (`INV-8`).
- **Essential participant** — a counterparty without which the system is nonsense; its interface
  is on the path the system exists to serve, and is touched whenever the system is used.
- **Optional participant** — a counterparty the system runs untouched without. Its interface is
  real and MUST still be defined (`INV-13`), but a deployment that never configures it is a valid
  deployment — which is also why it is touched far less often than an essential one.
- **Catalog** — the closed, enumerated set of named values a boundary carries: the metrics, event
  types or failure classes crossing it. A catalog belongs to the interface that carries it, and an
  invariant states the obligation over it rather than the list (`INV-8`).
- **Conformance suite** — the checks an implementer runs to confirm it adheres to an interface's
  stated shape. A conformance suite reconciles agreement in place of a peer cross-check when the
  counterparty keeps no behavior docs set of its own.
- **User story** — a **want**, stated from one actor's perspective, in the role–capability–
  benefit form `As <actor>, I want <capability>, so that <outcome>`. It states the want and the
  benefit, never the steps — the steps belong to a use case or a journey. Established source:
  the Connextra template, as treated in Mike Cohn's _User Stories Applied_, together with Ron
  Jeffries' card/conversation/confirmation reading of what a story is. This method defines the
  form it requires above and **cites** those sources for the rest rather than restating them.
- **Use case** — a named description of an interaction, centred on a **primary actor**'s goal,
  stating: the **primary actor**; the **scope** it is written against; its **level**; the
  **preconditions** that must hold before it starts; a **main success scenario** as a numbered
  sequence of steps; and **extensions** — the alternative and failure branches off numbered
  steps. A use case MAY **include** another by reference at a lower level instead of restating
  its steps. Established source: Alistair Cockburn's _Writing Effective Use Cases_, on the
  form that originates with Ivar Jacobson's use-case-driven analysis; this method requires the
  fields above and cites that source for the full field list and the writing guidance.
- **Level** — an element's **goal level**, one of three: **summary** — an arc spanning several
  user-goal elements, usually across more than one actor and longer than one sitting;
  **user-goal** — what a single primary actor accomplishes in one sitting, which is where most
  elements sit; **subfunction** — a step below a user goal, factored out precisely because more
  than one element **includes** it by reference. Level is the test for which kind of element to
  write: user-goal and subfunction are **use cases**, summary is a **journey**. Established
  source: Cockburn's three goal levels (the sea-level metaphor).
- **Journey** — an end-to-end flow through the system, told from the outside, at **summary**
  level and usually spanning **more than one actor**: it names the arc and **includes** the
  user-goal use cases by reference rather than restating their steps. Established source: the
  journey- / experience-mapping practice, as treated in Jim Kalbach's _Mapping Experiences_.
- **Scenario** — one concrete path through a story, use case or journey: **Given** the starting
  state, **When** the act, **Then** the observable outcome. A scenario SHOULD be written in that
  Given/When/Then form, **except** where a cited source prescribes another form for that kind of
  element — a use case's main success scenario is a numbered step sequence with extensions, not
  Given/When/Then. Established source: Dan North's introduction of Given/When/Then in
  behaviour-driven development, and the Gherkin language that encodes it.
- **Listing** — the trace an extent-defining element carries **on its own definition**: the
  invariants and goals it exercises, and the use cases or journeys it includes by reference
  (`INV-22`). A listing replaces a separate coverage section, so the trace is read and revised
  with the element it belongs to.
- **Invariant** — a rule that MUST always hold.
- **Goal** — a desired, non-absolute property.
- **Concept** — a named idea invariants and goals build on; not itself a rule.
- **Open question** — an explicitly-recorded gap in the **intended behavior itself** — something not
  yet decided — in a fixed shape: the gap, its owner, a resolution path, and where it blocks.
  Preferred over guessing. Distinct from a **realization gap**, where the intent _is_ settled and the
  implementation has not caught up: that goes in the realization-gap register, never here (`INV-23`).
- **Precedence** — a ranking a set MAY declare for resolving a conflict between two of its own
  invariants. A newly-surfaced precedence conflict is recorded as an open question and settled by a
  decision doc, never chosen ad hoc.
- **Realization gap** — the distance between intended behavior (the docs) and what an
  implementation has yet built. A realization gap is normal — the docs may lead the build — and is
  tracked against the cited IDs, never annotated inline (the docs stay living, `INV-4`). Its carrier
  is the realization-gap register (`INV-23`).
- **Realization-gap register** — the set-level section, named `## Realization gaps`, in which a set
  records its realization gaps: one row per gap, naming the element id the gap is against
  (`INV-23`). It sits outside every element definition, which is what makes it the only place a set
  describes where its implementation stands without putting _how_ (`INV-2`) or an
  implementation-status annotation (`INV-4`) into an element. A gap is never an open question: an
  open question is unsettled intent, a gap is settled intent the build has not reached. A set with
  nothing to record still carries the section, so its absence is an omission rather than a claim of
  convergence.
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
