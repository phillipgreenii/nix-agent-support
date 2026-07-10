# Glossary — the behavior-docs method

Vocabulary for the behavior-docs method itself. Terms specific to a system being
described (pr-pool's orchestrator, roles, tracking objects, …) live in **that
system's** own glossary, not here.

- **Behavior doc** — a living document describing how a system _should_ behave
  (user stories, journeys, constraints, goals, invariants). It sits above the
  disposable spec → design → plan → code chain. See [`README.md`](README.md).
- **Behavior-doc set** — the collection of behavior docs for one **scope**, living
  at `SCOPE-ROOT/docs/behavior`.
- **Scope** — the one thing a set describes: a repository, or a single
  tool/component. Determines where the set lives.
- **Downstream artifact** — a spec, design, plan, or implementation reference
  derived from a behavior doc; disposable once the code re-converges.
- **Invariant (`INV-*`)** — a rule that must always hold (MUST / MUST NOT). Every
  invariant has a stable **ID** so downstream artifacts can cite it.
- **Goal (`GOAL-*`)** — a desired property that is not absolute (often a SHOULD or a
  configurable default). Deliberately distinguished from an invariant.
- **Concept** — a named idea invariants and goals build on; not itself a rule.
- **Open question** — an explicitly-recorded gap or undecided point. Has an owner
  and a resolution path. Preferred over guessing.
- **Generic (base) set** — a tool's org-agnostic behavior, reusable across every
  deployment; imported elsewhere by reference (cite IDs), never copied.
- **Per-project overlay** — how a specific org/repo _uses_ a tool (config,
  identities, labels, added roles/workflows). Lives in that org's own repository.
- **ADR** — Architecture Decision Record; where a decision of lasting consequence is
  recorded and from which the relevant behavior doc is referenced.
- **Drift / conformance pass** — a periodic reconciliation of each invariant and
  open question against what the code actually does.
- **Golden vs. illustrative example** — an _illustrative_ example shows intent and
  is not guaranteed byte-accurate; a _golden_ example is asserted by a real test.
- **Cross-set citation** — the `<repo-name> · <set-path> · <ID-or-section>` textual
  form used to reference another set (never a relative-path link across sets).
