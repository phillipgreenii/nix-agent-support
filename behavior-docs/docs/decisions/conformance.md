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

### `DEC-CONFORM-2` — The realization-gap register has one shape, and intra owns its form while impl owns its truth <!-- uuid: 78d44c97-6a13-4c9a-909d-3a90e22bb5d4 -->

_Settled 2026-08-13._

**Why this is a decision and not a per-set choice.** `INV-15` has always required a realization gap
to be "tracked against the cited IDs, never annotated inline", but it fixed **no carrier**. Two sets
then needed one independently, and the second one got it wrong: a deployment set recorded its gaps as
an open question that **quoted** `INV-2` and `INV-15` while breaking both. One rule, two independent
inventions, one of them broken, is the signal that the shape belongs to the method. `INV-23` now
fixes it; this entry carries the reasoning and settles what checks it.

**Decision — the shape.**

1. **Where it lives.** One section per set, named `## Realization gaps`. The **section name** is
   normative; the **file** is not. This is the same calibration `INV-3` already uses for
   `## External references`: the imports table is found by its heading, wherever the set puts it, so
   the method stays layout-agnostic (`INV-10`, README "layout is illustrative") while the tooling
   still has one string to look for. The register SHOULD sit with the set's other set-level sections,
   conventionally the README, because that is where a reader already goes for scope and imports.
2. **Keyed by element id.** Each row names the element the gap is against, which is literally what
   "tracked against the cited IDs" asks for, and the id resolves like any other reference (`INV-3`)
   so a dangling one is already a defect the intra evaluator sees. One element MAY carry several rows
   — a single invariant can diverge in more than one way, and merging those into one row loses which
   part converged.
3. **Never inline.** The register sits **outside every element definition**. This is the clause that
   makes the whole design coherent, because a register row **is** a statement about the current
   implementation and therefore **does** change when the code changes — which is exactly what `INV-2`
   forbids inside a behavior doc. Confining that content to one labelled set-level section is what
   buys the elements their `INV-2` purity and their `INV-4` living-ness. Read the other way round: a
   set that annotates an element inline has not "documented a gap", it has moved mutable
   implementation content into the part of the set that must not carry any.
4. **Never an `OQ-`.** An open question is **unsettled intent**; a realization gap is **settled
   intent the build has not reached**. Typing one as the other does three separate harms, which is
   why the prohibition is explicit rather than left to taste: it routes the reader to `USECASE-3`
   (decide, then delete the question) when the thing to do is build; it puts implementation-status
   prose inside an element definition, breaking 3 above; and it mints a **citable identity**
   (`INV-3`) for a gap, so when the gap closes, the deletion strands every reference to it — the
   exact dangling-citation defect this repo already carries a recorded finding for.

**Decision — what checks it, and how hard.** The register is checked, split across two of the three
concerns by the same authority test that produced the split in `DEC-CONFORM-1`:

| Concern   | What it owns about the register                                                                                               | Why it, and not the others                                                                 |
| --------- | ----------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| **intra** | **form**: the section exists and is named; no gap is recorded as an `OQ-`; no gap is annotated inline; each row's id resolves | judged with no second party present — the counterparty is a rule                           |
| **impl**  | **truth**: each row's claimed divergence is real, and a divergence the code shows has a row                                   | needs running code; a register that has drifted from the code is an implementation finding |
| **inter** | nothing                                                                                                                       | a gap is one set against its own implementation; no seam is involved                       |

**Presence is reported, not failed — for a mechanical reason, not a soft one.** The `OQ-`-misuse
check is a hard `FAIL` in `self-checks.sh`: it is precise, and no set that ships here trips it, so it
costs nothing and it catches the one mistake that actually happened. It is not a hypothetical catch —
run against the deployment set that invented the inline register, it reports
`FAIL realization gap recorded inside an open question: journeys.md:492 OQ-ZR-PHASING (INV-23)`,
while both sets the real-corpus runner reads stay clean. Missing-register **presence** is
reported as an `ADVISORY` instead, because `tests/behavior-docs-real-corpus.sh` treats **any** `FAIL`
from `self-checks.sh` as a hard failure with **no baseline escape** — it never calls its `record`
helper on that script's output, so the ratchet cannot absorb the finding the way it absorbs a
`trace-extract.sh` one. A hard presence check would therefore red the whole build for every set that
has not been retrofitted yet, and the retrofit of the sets shipped beside the tooling belongs to other
work streams (pr-pool's register is authored by bead `pg2-wr6lm.5.4`, which adopts this shape rather
than inventing one). The advisory is not the resting state: **when `self-checks.sh` reports the register section
present for every set the real-corpus runner reads, the advisory is promoted to `FAIL`.** That
condition is observable by running the runner; until then the gap is a row in the method set's own
register, keyed to `INV-23`, which is the register demonstrating its own purpose.

**Consequences.**

- A set authoring its first register follows `INV-23` rather than inventing a carrier; nothing about
  the shape is left to the set except the punctuation and any extra column it wants.
- The intra skill grows one mechanical section and one declared category; the impl skill gains the
  truth half. Neither evaluator's concern moves — this is the same three-way split, applied.
- A set that legitimately has no gap still carries an empty section, which is what makes **absence**
  mean "omitted" instead of "converged". Without that rule no evaluator could distinguish the two,
  and the check would be unwritable at any strength.
