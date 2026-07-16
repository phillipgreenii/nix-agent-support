# Actors — the behavior-docs method

Who interacts with a behavior docs set. A behavior docs set MUST define all of its actors
(`INV-13`); this is the method's own list.

- **`ACTOR-1` — Author** — owns and writes the behavior docs; the authority for intended
  behavior, which derives from business need. A change of intent originates here.
- **`ACTOR-2` — Implementer** — consumes the behavior docs to produce downstream work
  (design, plan, tests, the implementation). Resolves uncertainty against the docs, cites
  the IDs of what it builds, and classifies a gap rather than guessing.

Who or what _fills_ these roles — a person or an agent, a specific reviewer or operator, and
what supervision applies — is a governance decision. It lives in the project's decision
docs, not here.
