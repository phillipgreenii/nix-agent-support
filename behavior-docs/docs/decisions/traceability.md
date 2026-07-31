# Traceability — decision docs for the behavior-docs method

Realization decisions about how a set shows that every rule it states is actually required by its
extent.

### `DEC-TRACE-1` — Traceability is a per-element listing, not a coverage section <!-- uuid: 5b389780-f102-44dd-9d7f-c4ce579240d2 -->

_Settled._

**Decision.** The trace from an invariant or goal back to the element that requires it is carried
**on the definition of that element**, as a **listing** (`INV-22`). A set MUST NOT discharge it with
a separate "Coverage (traceability)" section.

**Why the listing and not the section.** A coverage section is a second copy of the graph, kept in a
place nobody edits when they change an element. In practice it read as a wall of names with no
sentence around them, conveying nothing to the reader it was written for, while the information it
held was already implicit in the elements themselves. A listing sits where the reader already is: it
is read with the element and revised with it, and an element whose listing is empty is visibly a
problem rather than an omission somewhere else in the file.

**The ordering constraint is part of the decision, not an implementation note.** Removing a coverage
section can silently destroy the **only** trace an invariant or goal had. So the listings land
**first**, per element, and only then may the section go — and the check is per set, mechanically,
never assumed: a set whose section is already redundant loses nothing, and a set with one
element traced solely by the section loses that element's place in the extent (`INV-11`) the moment
it is deleted. Where both cannot land in one pass, the section stays.

**Consequences.**

- `INV-22` is the obligation; the punctuation of a listing is illustrative (`GOAL-7`), like every
  other layout choice this method leaves open (`INV-10`).
- The listing is also what a mechanical extractor walks: stories, use cases and journeys in, referenced
  IDs out, diffed against what the set defines, reporting both untraced elements and dangling
  references. The obligation is stated so that extractor has something well-defined to read.
- A dangling name in a listing is a defect, not a warning: `INV-22` requires every listed name to
  resolve in this set or in the declared external references (`INV-3`).
