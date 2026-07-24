---
name: behavior-docs-conformance
description: Review — or apply mechanical fixes to — a behavior-docs set against the behavior-docs method's invariant rules (the conformance / drift pass). Use when asked to check, review, audit, or "run the conformance pass" on a behavior docs set (a `docs/behavior` directory), to check whether it obeys the behavior-docs method, or to reconcile drift between a set and the method or a set it cites. Checks scope-explicit (extent+floor), all actors and interfaces defined, stories/journeys define the extent with every invariant traced, typed/stable IDs that resolve without collision, each named concept used at least twice beyond its glossary definition, self- and inter-consistency, cross-set citations textual and resolving, intended-behavior-only (nothing below the floor), and no per-doc status headers. Review reports ranked findings citing the method invariant each breaks; apply fixes only the mechanical/format issues and leaves intended-behavior to a human. Do NOT use to author a new set from scratch, or for non-behavior-docs markdown.
---

# Behavior-docs conformance

Run the behavior-docs method's invariant rules over a target **behavior docs set** and report what
conforms and what does not — or, in apply mode, fix only the mechanical issues. This is the method's
own **conformance / drift pass**, made repeatable.

## Arguments

- **target** — path to the behavior docs set to check (a directory, usually `.../docs/behavior`). If
  omitted, search downward from the cwd for a `docs/behavior` directory and confirm the choice.
- **mode** — `review` (default) or `apply`.

## Core principle (read first)

**Humans own the intended behavior; conformance is one-directional.** In `apply` mode you may fix
only **mechanical / format** defects (missing or malformed IDs, citation format, stray status
headers, stale vocabulary, broken cross-references). You **MUST NOT** rewrite, add, or delete
_intended behavior_ (invariants, stories, journeys, actor/interface semantics) — those are reported
for a human to decide. When docs and code disagree, the implementation is presumed at fault, not the
docs.

## Step 0 — Locate the method set and the target

1. Resolve **target** (per Arguments).
2. Find the authoritative **method set** — the self-describing behavior-docs method (its
   `invariants.md` defines the `INV-*` rules). It usually lives at a `behavior-docs/docs/behavior`
   in the same repo/workspace. If you cannot find it, ask for its path — do not check against
   remembered rules, which may be stale.
3. If the target **is** the method set, you are checking it against itself (that is expected and
   valid — it is self-describing).

## Step 1 — Load the current rules

Read the method set's `invariants.md` (and glossary) and build the checklist **from what it actually
says now** — do not hardcode a rule list, because the method evolves. Map each rule below to the
method invariant ID it enforces so findings can cite it.

## Step 2 — Mechanical checks (deterministic)

Run the bundled **`scripts/self-checks.sh <target>`** — it prints one section per check, each mapped
to the method invariant it enforces, and needs no interpretation to run. Read its output against the
table below (which documents what each section checks and the rule it maps to). Two sections are
**heuristic** — the `>=2×` named-concept pass (literal term counting) and the floor-leakage
candidates — so confirm any borderline hit by hand rather than reporting it blindly. Turn each real
hit into a finding citing the mapped invariant.

| Check                                                                         | How                                                                                                                                                                           | Method rule                         |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| Every invariant/goal/story/journey/interface/actor has a **typed, stable ID** | list `-**\`ID\`\*\*` headwords; flag any rule-bearing bullet with no ID                                                                                                       | IDs (`INV-3`)                       |
| **No ID collision** with cited sets                                           | a set that **cites another set** SHOULD use topical/namespaced IDs so a cited ID never clashes with one of its own; the **root** method (citing no set) MAY keep bare `INV-N` | IDs (`INV-3`)                       |
| Every **named concept used ≥2× beyond its glossary definition**               | for each glossary headword, count occurrences across `*.md` minus the glossary; `< 2` outside = fail                                                                          | redundancy cross-check (`INV-14`)   |
| **No per-doc status headers**                                                 | grep for `status:` / `(draft)` / `(partial)` / `forward-looking` / `TODO` / `TBD` outside prose                                                                               | living-by-default (`INV-4`)         |
| **Cross-set citations are textual, not relative links**                       | flag `](../…another-set…)`; citations use `<repo> · <set-path> · <ID>`                                                                                                        | cross-set citation (`INV-8`)        |
| **Cited IDs resolve**                                                         | every `… · <ID>` and every intra-set ID reference resolves to a definition (in the cited set, which you must read)                                                            | consistency (`INV-8/12/18`)         |
| **Floor leakage**                                                             | grep for below-floor tells — `file:line`, function/test names, tuning constants, exact CLI flag strings, tool names below the scope — as _candidates_ for the judgment check  | intended-behavior-only (`INV-2/10`) |
| **Mermaid fences balanced**                                                   | count ` ```mermaid ` opens vs closes                                                                                                                                          | (hygiene)                           |

## Step 3 — Content-level checks (read and judge)

These need reading, not grep. Judge each and cite the rule.

- **Scope explicit (extent + floor)** — the set states what is in vs. out (extent) and how deep it
  goes before deferring downstream (floor). `INV-13`.
- **All actors and all interfaces defined** — every actor a story/journey names is defined; every
  boundary the system exposes is an interface with what-crosses / what-must-hold. `INV-13`.
- **Extent traceability** — every invariant and goal is required by some story or journey (no
  orphans); stories and journeys exist and define the extent. `INV-11`.
- **Intended-behavior-only / the substitution test** — for each floor-leakage candidate from Step 2,
  apply the substitution test: try generalizing the specific term; if intended behavior is preserved
  it belongs downstream (a finding); if generalizing loses essential meaning within the declared
  extent, it is at the floor (fine). `INV-2/10`.
- **Self-consistency** — no two statements contradict; deliberate not-DRY restatements agree.
  `INV-12`.
- **Inter-consistency with cited sets** — where the set references another, they agree at the shared
  interface. If this set **implements** another's interfaces (an implementer), it should **cite** the
  owner's contract and state only its own side, reconciled by a **conformance suite** — not restate
  the whole contract. `INV-18`.
- **Gaps are explicit open questions** — known gaps are recorded as well-formed open questions
  (gap, owner, resolution path, where it blocks), not guessed. `INV-13`.

## V2 intra-evaluation — the five declared categories

This skill **is** the method's **V2 intra-evaluator**: an agent reviews ONE set against the method's
own rules (distinct from **V1**, a set's implementation vs. its own docs, and **V3**, the cross-set
inter-evaluator — see the `behavior-docs-inter-conformance` sibling skill). Steps 2–3 above cover the
general checklist; the five categories V2 is specifically accountable for — each with a FAIL and a
PASS fixture under [`corpus/v2/`](corpus/v2/) — are:

| Category                         | What FAILs                                                                                                    | Layer                                               | Rule       |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------- | --------------------------------------------------- | ---------- |
| **inline-status** (#15)          | a rule annotated with its current implementation status in prose (e.g. "unmet by the current implementation") | mechanical (self-checks "Inline status framing")    | `INV-4`    |
| **floor-leakage**                | a below-floor realization detail (`file:line`, retry counts, backoff ms)                                      | mechanical (self-checks "Floor-leakage candidates") | `INV-2/10` |
| **substitution**                 | an extent statement that fails the substitution test (names an artifact, not a behavior)                      | judgment (Step 3)                                   | `INV-2/10` |
| **extent-traceability**          | an extent-in/out claim (story/journey) no invariant traces                                                    | judgment (Step 3)                                   | `INV-11`   |
| **seam-vocab inherit-or-rename** | a borrowed (cited) term silently REDEFINED instead of inherited-or-renamed                                    | judgment (Step 3)                                   | `INV-20`   |

The mechanical categories are enforced by the bundled `self-checks.sh` (and gated by the
`test-behavior-docs-conformance-v2` bats check under `nix flake check`); the judgment categories are
the agent's, using the corpus fixtures as the reference for what a FLAG vs. CLEAN looks like.

**Real-world corpus via pre-fix snapshots.** `scripts/capture-prefix-snapshots.sh <out>` captures the
git snapshot of each behavior-docs set as it was immediately BEFORE a review-resolution edit pass —
those PRE-FIX sets are real-world FAIL fixtures (they still carry findings such as #15 inline-status
"unmet by the current implementation", the #1 owner→consumer `pr-pool-components` pointer, and the
#6-A stranded past-framing note), and the current POST-FIX sets are the PASS fixtures. The script
documents the exact revs so the fixture is reproducible.

## Step 4 — Report

Produce a **ranked findings list**, most-severe first. For each: the **method invariant ID** it
breaks, the **file:line** (or the specific IDs involved), one line on the defect, and a concrete fix.
Separate **mechanical** findings (fixable) from **judgment** findings (need a human). If the set
passes, say so per rule, so the human can trust the green.

Lead with a one-line verdict (conformant / N findings), then the list.

## Step 5 — Apply mode (only if `mode: apply`)

Apply **only** the mechanical findings from Step 2 and the clearly-mechanical ones from Step 3
(missing IDs, citation format, stray status headers, stale/renamed vocabulary, broken cross-refs,
mermaid fences). After each edit, re-run the relevant check to confirm it now passes (evidence, not
assertion). **Leave every judgment finding for the human** and list them. Never edit intended
behavior. If a fix would change what the system is supposed to do, it is not mechanical — report it.

## Red flags

- Checking against remembered rules instead of reading the method set → the method evolves; always
  reload.
- "Fixing" an intended-behavior finding in apply mode → forbidden; humans own the docs.
- Claiming a green without running the check → run it; paste/attribute the evidence.
- Treating a cross-set citation that names a term legitimately at _this_ set's floor (but below
  another's) as a contradiction → the floor is scope-relative; not a defect.
