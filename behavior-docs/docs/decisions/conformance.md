# Conformance — decision docs for the behavior-docs method

Realization decisions about how a behavior docs set is checked. The behavior docs state the
_obligations_ (`INV-12`, `INV-18`, `INV-21`); which tools check them, and how the checking is
divided, is a realization decision and lives here.

### `DEC-CONFORM-1` — Conformance splits three ways: intra, inter, impl <!-- uuid: e7c67c9b-e722-4901-a958-a5ffb2598ee2 -->

_Settled. Recovered from the round-1 review artifacts, where it existed only as scaffolding._

**Decision.** Conformance checking is **three** distinct concerns, each its own repeatable
prompt / command / skill, and they MUST NOT be conflated:

| Concern   | What it compares                                        | Skill                             |
| --------- | ------------------------------------------------------- | --------------------------------- |
| **intra** | one set against **the method's own rules**              | `behavior-docs-intra-conformance` |
| **inter** | one set against **other systems' contracts**            | `behavior-docs-inter-conformance` |
| **impl**  | an **implementation** against **its own** behavior docs | `behavior-docs-impl-conformance`  |

**Why three and not one.** Each has a different _authority_, a different _counterparty_, and a
different _failure meaning_ — so a single evaluator would have to pick one, and would silently not
perform the other two.

- **intra** — the authority is the **method set**. Its counterparty is a rule, not a party. A
  failure means the set is malformed: an untyped or colliding id, a missing actor or interface,
  content below the floor, a per-doc status header, a term used once. It is judged **without any
  second set present**, so it MUST be runnable on a set in isolation. Part of it mechanizes
  (`self-checks.sh`); the rest is agent judgement — the substitution test, inline-status framing,
  extent traceability, seam-vocabulary inherit-or-rename (`INV-20`) — which is precisely the class
  of finding a green mechanical run misses.
- **inter** — the authority is the **other side of the seam**, and which side that is depends on the
  counterparty's kind (method `INV-18`, `interfaces.md`'s counterparty table): with a **peer**,
  outgoing is cross-checked against the peer's incoming; with an **owner/implementer**, the
  implementer **cites** and reconciles through a **conformance suite**, never a verbatim
  cross-check. Matching is **by UUID**, through the implementer's imports table (`INV-3`). A failure
  means the two sides genuinely diverge — and the UUID model exists so a mere **rename** degrades to
  a warning instead. This is also where a **consumed external contract** — a tool or system with no
  behavior docs of its own, e.g. git — is declared and verified as far as it can be, which closes
  the `INV-8` gap for such counterparties. It is **inherently multi-set** and cannot be answered
  from one set.
- **impl** — the authority is **the set itself**, and the counterparty is running code. A failure
  is, by `INV-15`, normally a **realization gap** on the implementation, not a docs defect — the
  opposite default from the other two, where a failure indicts the docs. `INV-21` makes this the
  validity question: a product that adheres to its behavior docs MUST be treated as behaviorally
  valid.

**Consequences.**

- Three skills, one family. **intra** and **inter** exist; **impl** must be built.
- The names MUST say the concern, not a version number. The historical `V1`/`V2`/`V3` labels read as
  releases and mean evaluator concerns — `V1` = impl, `V2` = intra, `V3` = inter. Rename to
  intra / inter / impl. `V1` was the first to exist and is the one most often conflated with the
  others, which is why it was recorded separately from the outset.
- **intra** and **inter** together are the enforcement arm of the method; an interface conformance
  suite is **inter**'s executable reconciliation mechanism, not a fourth concern.
- A downstream stream MUST cite this entry rather than re-explaining the split.
