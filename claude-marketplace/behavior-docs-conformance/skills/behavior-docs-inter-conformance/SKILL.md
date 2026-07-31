---
name: behavior-docs-inter-conformance
description: Reconcile two behavior-docs sets across a seam (the V3 INTER-evaluator) — verify an implementer's stated obligations align with the owner's contract, matched BY UUID via the implementer's imports table, using the interface conformance suite as the executable reconciliation (not a verbatim peer cross-check). Use when asked to check whether an implementer set (e.g. a downstream deployment implementing another set's interfaces) reconciles with the owner set, to resolve cross-set references by UUID, to find genuine divergence vs. a merely stale name, or to check that a set declares the external/consumed contracts (tools/systems it uses that have no behavior-docs of their own, e.g. git). Distinct from V1 (a set's implementation vs. its own docs) and V2 (a single set vs. the method's rules — the behavior-docs-conformance skill). Do NOT use for a single set in isolation, or for non-behavior-docs markdown.
---

# Behavior-docs inter-conformance (V3)

Verify the two sides of a **seam** between behavior-docs sets agree: the **owner** defines a contract
(an `INTF-*`), and an **implementer** states its own obligations and **cites** the owner. V3 is the
**inter**-evaluator — it reconciles **contract ↔ contract across sets**, complementing V1 (impl ↔ its
own docs) and V2 (a set ↔ the method's rules, the [`behavior-docs-conformance`](../behavior-docs-conformance/SKILL.md)
skill).

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
  This — distinguishing a real divergence from a cosmetic stale name — is V3's core value.
- **external / consumed contract** — a row for a tool/system with **no behavior-docs set of its own**
  (e.g. `git`) is a **declared external contract** (`INV-8`); a set SHOULD declare the contracts it
  consumes even when the counterparty has no docs. An **undeclared** used tool is a finding.

## Step 1 — Mechanical seam resolution (deterministic)

Run **`scripts/resolve-imports.sh <owner> <implementer>`**. For every row of the implementer's
`## External references` table it prints one of `ok` / `WARN` (stale name) / `FAIL` / `external`
(declared external contract), and exits non-zero iff any row **failed to resolve**. Two conditions
are a `FAIL`: an owner UUID that resolves to no owner definition (divergence), and a row carrying no
parseable owner UUID that is not marked external (**unresolvable** — including a
`[<uuid>](remote-url)` cell whose link text is not a UUID). An unresolvable row is never a warning:
warning on it left the exit status at 0, so a table whose shape the parser did not understand
reported success while resolving nothing. Turn each `FAIL` into a reconciliation finding and each
`WARN` into a rename-the-citation note.

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

## Corpus

[`corpus/v3/`](corpus/v3/) carries a fixture per seam-check type against a shared `owner/`:
`aligned/` (ok), `stale-name/` (WARN), `divergence/` (FAIL), and `external-contract/{declared,undeclared}/`.
The `test-behavior-docs-conformance-v3` bats check drives `resolve-imports.sh` over them under
`nix flake check`. Real-world seams are captured by the sibling skill's
`capture-prefix-snapshots.sh` (pre-fix vs. post-fix sets).

## Red flags

- Treating a stale **name** as a broken **identity** → it is a warning; the UUID resolves.
- Reconciling by pasting the owner's whole contract into the implementer → forbidden; cite + run the
  conformance suite (implementer form of `INV-18`).
- Checking a single set → that is V1 or V2, not V3; V3 needs two sets and a seam.
