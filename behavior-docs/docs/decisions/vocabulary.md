# Vocabulary — decision docs for the behavior-docs method

Realization decisions about the method's own element vocabulary: which kinds of element exist, which
established source each is drawn from, and how existing elements were classified when a kind was
introduced.

### `DEC-VOCAB-1` — Three extent-defining kinds, and level is the test between them <!-- uuid: adbf87bd-db89-4602-9672-b99eeaf78eef -->

_Settled 2026-07-28._

**Decision.** A behavior docs set's extent is defined by **three** kinds of element, not two, and
each is **fully defined** in the method's glossary in the method's own words while **citing** the
established source it is drawn from — the source is cited, never reproduced:

| Term defined   | Established source cited                                                                     |
| -------------- | -------------------------------------------------------------------------------------------- |
| **user story** | the Connextra role–capability–benefit template; Cohn, _User Stories Applied_; Jeffries' 3 Cs |
| **use case**   | Cockburn, _Writing Effective Use Cases_, on the form originating with Jacobson               |
| **level**      | Cockburn's three goal levels — summary, user-goal, subfunction                               |
| **journey**    | the journey- / experience-mapping practice; Kalbach, _Mapping Experiences_                   |
| **scenario**   | North's Given/When/Then in behaviour-driven development, and Gherkin                         |

`USECASE-` is introduced as a typed name (`INV-3`), and `INV-11` / `INV-13` name all three kinds, so
a use case is in extent and a set is required to include use cases.

**The level test decides the kind**, and it is the only test that does:

- **user-goal** level — what one primary actor accomplishes in one sitting ⇒ `USECASE-`;
- **subfunction** level — a step below a user goal that more than one element **includes** by
  reference ⇒ `USECASE-`. Being included by two callers is what makes it a subfunction rather than a
  goal of its own;
- **summary** level — a multi-actor arc that includes user-goal elements by reference ⇒ `JOURNEY-`.

**Scenario form.** A scenario SHOULD be written Given/When/Then — a raise from MAY, so the form is
the default rather than one option among many. The exception defers to the cited source: a use
case's **main success scenario** is a numbered step sequence with **extensions**, per Cockburn, and
is therefore not written Given/When/Then. Where any cited source prescribes a form for its kind of
element, that form governs.

**The retyping of existing elements is settled input, not a question.** When `USECASE-` was
introduced, every existing `JOURNEY-` element was re-classified by the level test alone — never by
how the element was worded, and never by which file it sat in:

- creating a new participant implementation of an interface, and adding an existing one to a
  configuration, for **each** interface kind; validating a whole configuration as its own element;
  and debugging (metrics, test events, run-scoped selectors) are **user-goal** ⇒ `USECASE-`;
- **verify** is a **subfunction**, because it is included by both create and edit — that inclusion
  by two callers is precisely the classification, not a stylistic preference;
- the workflow walkthrough that stays deliberately light on what the create/add elements already
  cover, and the bead-workflow flows of a deployment set, are **summary** level and multi-actor ⇒
  they stay `JOURNEY-`.

**Consequences.**

- A name change is not an identity change (`INV-3`): a retyped element keeps its UUID, so a consumer
  that declared the old name sees a **stale name** — a warning — and never a broken reference.
- Every **inbound reference** to a retyped element MUST be updated in the same change, in every
  document of the owning repository. References held in **other** repositories are reconciled when
  that set is next revised; until then the UUID keeps them resolvable.
- A collective noun for the three kinds is deliberately **not** introduced — that is the method's own
  `OQ-2`, which stays open — so the rules name all three explicitly.
- The method set applied the level test to its own elements when this entry landed. The map is in
  git history; the current names are the authority.
