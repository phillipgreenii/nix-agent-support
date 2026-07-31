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
