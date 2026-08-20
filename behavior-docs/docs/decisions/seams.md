# Seams — decision docs for the behavior-docs method

Realization decisions about the seam between one behavior docs set and another: how a reference is
declared so it resolves, and how a boundary is classified and described.

### `DEC-SEAM-1` — The imports row says what it is, and the UUID outranks the link <!-- uuid: 211cb725-81c6-44e6-b5c0-69677dcc8abc -->

_Settled._

**Decision.** A row of a set's `## External references` imports table carries four cells:
`` `| <name> | <what it is> | <repo> · <set-path> | [<uuid>](<remote-url>) |` ``.

**Why the "what it is" column.** A table of bare names and UUIDs told a reader nothing — faced with
two rows naming invariants of another set, the reader could not tell what either rule said or why the
row existed without opening the other repository. One line per row removes that trip. It is a summary
for the reader, never a restatement of the owner's contract, which `INV-8` keeps on the owner's side.

**Why the UUID is linked, and why the UUID still wins.** The link makes the reference followable —
and it MUST be the owner's **remote-served** URL, since a local path resolves only on the machine
that wrote it. But a URL is a location, and locations move: branches are renamed, files are relocated,
repositories are archived. Identity is the UUID (`INV-3`), so a dead link degrades a convenience and
never breaks a reference, and a checker MUST NOT report a dead link as divergence.

**The direction constraint.** A link is worth having only if the reader can open it, so it MAY point
from a **less** publicly reachable scope toward a **more** publicly reachable one and MUST NOT point
the other way. A private deployment set MAY link into the public set whose contract it implements; the
public set MUST NOT link into the private one, because the link would fail for exactly the readers the
public set exists for. Such a set declares the bare UUID instead. This is a property of the seam's
direction, not of any one repository.

**Consequences.**

- Inserting the new column shifts the owner cells one position right, so anything parsing the table
  positionally from the left breaks — and breaks **silently**, by finding no UUID and reporting
  nothing. A parser MUST therefore read the owner cells from the **right**, accept both the bare-UUID
  and the linked-UUID cell forms per row so a part-migrated table still resolves, take the identity
  from the link **text** only (a URL may itself contain a UUID), and treat a row it cannot resolve as
  a **failure** rather than a warning.
- Verifying that the URL resolves and still carries its UUID is a separate concern requiring network
  access, and is deliberately not part of resolving the seam.

### `DEC-SEAM-2` — Two axes for interfaces, and a catalog belongs to its interface <!-- uuid: e8314998-a90e-4604-961b-9ece0ef0738c -->

_Settled._

**Decision.** Every interface is classified on **two** independent axes, and an enumerated catalog is
stated on the interface that carries it (`INV-8`).

**Why two axes.** The counterparty's **kind** answers how agreement is reconciled (`INV-18`), and
that is all it answers. It cannot separate the boundaries a system exists to serve from the ones a
deployment may never touch: a system's pluggable extension points are commonly all of one kind —
implementers — so a set grouped on kind alone puts the extension point without which the tool is
nonsense in the same bucket as the optional monitoring sink. The second axis, **essential** versus
**optional participant**, is the distinction a reader needs first, and the frequency asymmetry
follows from it: an optional participant is configured rarely, or never. Grouping on kind alone
forecloses the question and is therefore forbidden.

**Why the catalog moves to the interface.** An enumerated catalog — the metrics, event types or
failure classes crossing a boundary — is part of what crosses that boundary, so it is part of the
contract and belongs where the contract is. Kept in an invariant, it turns a rule into a list that
must be edited every time the boundary gains a value, which is both the wrong place to look and the
wrong thing to version. The invariant keeps the **obligation** over the catalog — that it exists, is
complete, is honoured — which is the part that is actually invariant.

**Consequences.**

- A set with a single interface satisfies the grouping obligation vacuously; the classification
  obligation still applies to that interface.
- An invariant that currently enumerates a catalog is relocated to its interface by the method's own
  relocation procedure (`USECASE-5`), and the invariant is restated as the obligation.

### `DEC-SEAM-3` — A decision-doc citation needs an imports row only across scopes <!-- uuid: 58326fd3-1e66-4fd0-8994-4956a85e5b41 -->

_Settled 2026-08-14 (operator-confirmed; the reasoning below was shipped first and the ruling
endorsed it as-is)._

**Decision.** When a behavior doc's citation of a decision-doc entry (`GOAL-5`) names an entry
belonging to **another** scope, that entry MUST additionally be declared, by its UUID, as a row in
the citing set's `## External references` imports table — exactly like any other external element.
When the cited entry belongs to **this product's own** decision area — the sibling **input** of
the two-input model (`product = f(behavior docs, decision docs)`), never an external set — no row
is required; the typed-name citation, resolving by UUID, is enough on its own. `GOAL-5` states this
rule; `behavior-docs/docs/behavior/README.md`'s imports table demonstrates it — the table is empty
and its footnote says why: _"the decision-doc entries this set's rules cite live in this same
product's own decision area … so they are cited by typed name and need no row here (`GOAL-5`)."_

**Why a row is required across scopes: type-correctness.** The imports table feeds
`resolve-imports.sh`
(`claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/resolve-imports.sh`),
which resolves **behavior-set** elements against an owner set. A decision-doc entry is not that
kind of thing — it has no owning behavior-docs set to resolve against — so a uniform "every
citation gets a row" rule would put non-behavior-set rows in front of a resolver built for
behavior-set elements, conflating two kinds of reference in one table. Declaring the row only when
the entry is genuinely external keeps the table's rows homogeneous: each one names something the
resolver can actually resolve.

**Why no row for the product's own decisions: self-reference.** This method's own decision area
(`../decisions`) is not a set this method references — it is the other half of the same product.
Requiring a row unconditionally would force the **root** method set to declare its own sibling
decisions area as an external reference to itself, which the two-input model rules out by
definition: the decisions area is an input to the product, not a dependency of the behavior half on
another set.

**The rejected alternative.** A uniform rule — every decision-doc citation, in-product or
cross-scope, declares an imports row — was considered and declined, not dismissed. Its merit:
it is simpler to check mechanically (one rule, no judgment call at authoring time — "does this
citation cross a scope boundary?" never has to be asked). It was declined because it would have
required either widening `resolve-imports.sh` to tolerate non-behavior-set rows, or giving it a
documented skip for them, on top of the self-reference problem above; the cross-scope-only rule
needs neither.

**Consequences.**

- `resolve-imports.sh` already admits `DEC-` and `IMPL-` as resolvable id families (the single
  definition in `lib/behavior-ids.bash`), which is what lets a genuinely cross-scope row resolve
  once declared. Nothing further widens the resolver for the in-product case, because no row is
  ever declared there for it to widen against.
- A behavior doc that cites its own product's decision area declares nothing extra; a set that
  cites **another** scope's decision area declares that entry the same way it declares any other
  external element (`DEC-SEAM-1`).
