---
name: behavior-docs-inter-conformance
description: The INTER third of the behavior-docs conformance family — reconcile two behavior-docs sets across a seam: verify an implementer's stated obligations align with the owner's contract, matched BY UUID via the implementer's imports table, using the interface conformance suite as the executable reconciliation (not a verbatim peer cross-check). Use when asked to check whether an implementer set (e.g. a downstream deployment implementing another set's interfaces) reconciles with the owner set, to resolve cross-set references by UUID, to find genuine divergence vs. a merely stale name, or to check that a set declares the external/consumed contracts (tools/systems it uses that have no behavior-docs of their own, e.g. git). Also reconciles the imports table in BOTH directions (cited-but-undeclared and declared-but-uncited) and flags cross-set name collisions, such as an implementer asserting an affordance name the owner does not have. The other two thirds are behavior-docs-intra-conformance (one set vs. the method's rules) and behavior-docs-impl-conformance (an implementation vs. its own set). Do NOT use for a single set in isolation, or for non-behavior-docs markdown.
---

# Behavior-docs inter-conformance

Verify the two sides of a **seam** between behavior-docs sets agree: the **owner** defines a contract
(an `INTF-*`), and an **implementer** states its own obligations and **cites** the owner. This skill is
the **inter**-evaluator — it reconciles **contract ↔ contract across sets**. It is one of three parallel
evaluators, named for the concern each reconciles (never for a version number):

| Evaluator | Reconciles                          | Skill                                                                            |
| --------- | ----------------------------------- | -------------------------------------------------------------------------------- |
| **intra** | one set ↔ the method's rules        | [`behavior-docs-intra-conformance`](../behavior-docs-intra-conformance/SKILL.md) |
| **inter** | one set ↔ another system's contract | this skill                                                                       |
| **impl**  | an implementation ↔ its own set     | [`behavior-docs-impl-conformance`](../behavior-docs-impl-conformance/SKILL.md)   |

## Arguments

- **owner** — path to the owner behavior-docs set (the set that DEFINES the contracts).
- **implementer** — path to the implementer set (the set that CITES/implements them).
- **seam** — optional: the specific `INTF-*` (or family) to focus on; default all cited.

## Core principle — identity is the UUID, not the name (1.1)

Cross-set matching is **by UUID** (`INV-3`): a set declares each external element it cites — with the
owner's UUID — in its `## External references` imports table. The **name is a mutable label**. So:

- **aligned** — the cited owner UUID resolves and the cited name matches the owner's current name.
- **stale name (WARNING, never a failure)** — the owner UUID resolves but the owner has since
  **renamed** the element; the citation's name is stale, identity is intact. This is the whole point
  of the UUID model — a rename never breaks a seam.
- **genuine divergence (FAILURE)** — the cited owner UUID resolves to **no owner definition**: the
  implementer names an obligation the owner does not define (omits/contradicts the real contract).
  This — distinguishing a real divergence from a cosmetic stale name — is this evaluator's core value.
- **external / consumed contract** — a row for a tool/system with **no behavior-docs set of its own**
  (e.g. `git`) is a **declared external contract** (`INV-8`); a set SHOULD declare the contracts it
  consumes even when the counterparty has no docs. An **undeclared** used tool is a finding.

## Step 1 — Mechanical seam resolution (deterministic)

Run **`scripts/resolve-imports.sh <owner> <implementer>`**. For every row of the implementer's
`## External references` table it prints one of `ok` / `WARN` (stale name) / `FAIL` / `external`
(declared external contract), and exits non-zero iff any row **failed to resolve**. Three conditions
are a `FAIL`: an owner UUID that resolves to no owner definition (divergence); a row carrying no
parseable owner UUID that is not marked external (**unresolvable** — including a
`[<uuid>](remote-url)` cell whose link text is not a UUID); and an owner UUID that resolves to a
definition whose **id family is unrecognized**. An unresolvable row is never a warning:
warning on it left the exit status at 0, so a table whose shape the parser did not understand
reported success while resolving nothing. Turn each `FAIL` into a reconciliation finding and each
`WARN` into a rename-the-citation note.

**Which id families a row may cite.** Ten: the eight `INV-3` enumerates for behavior elements
(`INV-`, `GOAL-`, `STORY-`, `USECASE-`, `JOURNEY-`, `INTF-`, `ACTOR-`, `OQ-`) plus the two every
`docs/decisions/README.md` defines for decision entries — `DEC-<TOPIC>-<n>` (settled) and
`IMPL-<n>` (captured, not yet decided). `GOAL-5` makes a decision entry belonging to **another**
scope declarable in this table like any other external element, so a `DEC-`/`IMPL-` row is
ordinary, not exceptional. An id outside those ten is reported as a **per-row `FAIL` naming the
token** — the script neither crashes nor guesses, and the reader decides whether the id is wrong or
the evaluator has not been taught a new family. The list lives in `resolve-imports.sh`'s `IDRE` and
MUST stay identical to the intra evaluator's `self-checks.sh` `IDRE`: the two govern the two halves
of one identity model (owner-name resolution across a seam, orphan-carrier detection within a set),
so widening one alone reinstates the same failure in the other half.

Two table shapes are accepted, detected **per row**, so a set part-way through migrating is still
checked: `| Name | Owner set-path | Owner UUID |` and
`| Name | What it is | Owner set-path | [<uuid>](remote-url) |`. The owner UUID is the last visible
cell and the owner set-path the one before it in both. The script **parses** the link; it never
**dereferences** the remote-url — confirming that the URL resolves and still carries the UUID is a
separate deferred check, and this script makes no network calls.

## Step 2 — Obligation reconciliation via the conformance suite (executable)

The seam's obligations are reconciled by the **#7 interface conformance suite**
(`packages/pr-pool/conformance` — bead pg2-hvlyj.13), **not** a verbatim peer cross-check
(`INV-INTF-2` / method `INV-18`, implementer form). The implementer runs the OWNER's message schemas
against its own `INTF-*` implementation: a schema/round-trip failure is a genuine divergence at the
message level, the executable complement to Step 1's identity check. Report suite failures alongside
the unresolved-UUID findings.

## Step 3 — External / consumed contracts (read and judge)

For each tool/system the implementer USES (git, a bead store, a CI backend), confirm it is declared
in `## External references` (Step 1 marks declared ones `external`). A used-but-undeclared contract
is a finding (the mechanical layer cannot see prose usage — read for it). This closes the `INV-8`
gap: a set declares the contract for what it depends on even when that dependency has no docs.

## Step 4 — Report

Lead with a one-line verdict (aligned / N divergences / M stale-name warnings). Then list, most-severe
first: divergences (`FAIL`), undeclared external contracts, then stale-name warnings. Cite the owner
UUID and the implementer row for each.

## Step 3b — Cross-set name collisions, scoped to the seam

Run **`scripts/name-collisions.sh <set> <set> [<set>…]`**. Two classes:

- **class 1 — ambiguous ID name (`FAIL`)** — the same ID name is DEFINED in two sets **that
  reference each other**. A bare name cited across that seam then resolves to two elements.
- **class 2 — asserted affordance (`CANDIDATE`)** — a set names a concrete affordance beside a
  citation of another set, and the cited set never uses that name. Judgment, not a failure;
  `--strict` promotes it.

**The reference edge is part of class 1's rule, not an escape from it.** `INV-3` makes a name "a
mutable, human-readable label that need only be consistent **within its own set**", and conditions
its namespacing clause on "a set that **cites another set** … so a bare name it cites never collides
with one of its own". `INV-20` states the limit outright: "Vocabulary consistency across
**unrelated** sets (no reference edge) is **not** required." Two sets that never mention each other
cannot make any citation ambiguous — a cross-set citation is qualified
`<repo> · <set-path> · <name>` and resolves by UUID regardless. So the same name in two unrelated
sets is reported as a **`note`**, not a finding; do not "fix" it by renaming a published ID. The
edge is read from each set's own `## External references` table (by owner UUID, or by name under
`INV-3`'s interim clause) — a set that cites without declaring has a different defect, already
caught as `reconcile-imports.sh`'s cited-but-undeclared `FAIL`.

A **within-set** duplicate name is not this script's job: that is the intra evaluator's
`self-checks.sh` DUAL IDENTITY check (one ID token bearing more than one UUID carrier).

## Corpus

[`corpus/inter/`](corpus/inter/) carries a fixture per seam-check type against a shared `owner/`:
`aligned/` (ok), `stale-name/` (WARN), `divergence/` (FAIL), and `external-contract/{declared,undeclared}/`.
The `test-behavior-docs-inter-conformance` bats check drives all three scripts over them under
`nix flake check`, and `test-behavior-docs-real-corpus` drives them over the REAL in-repo seam
(method set → pr-pool set) so a shipped divergence fails the build. Real-world pre-fix seams are
captured by the intra skill's `capture-prefix-snapshots.sh` (pre-fix vs. post-fix sets).

## Red flags

- Treating a stale **name** as a broken **identity** → it is a warning; the UUID resolves.
- Reconciling by pasting the owner's whole contract into the implementer → forbidden; cite + run the
  conformance suite (implementer form of `INV-18`).
- Checking a single set → that is the impl or the intra evaluator, not this one; inter needs two sets
  and a seam.
- Reading a cross-set name collision as a rename → a rename keeps the UUID; a collision is two sets
  using DIFFERENT names for one element, or the SAME name for two, and no UUID reconciles it.
- Renaming a published ID because two **unrelated** sets picked the same name → `INV-20` does not
  require distinct names with no reference edge; class 1 reports that as a `note`, and a rename
  there buys nothing and must be repeated at every future topic coincidence.
