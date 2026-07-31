# Interfaces — implementer (inter corpus: external-contract DECLARED)

This set consumes an external tool (`git`) that has no behavior-docs set of its own, and DECLARES
the consumed contract in its imports table (INV-8, external-contract convention).

## External references

| Name  | What it is                                   | Owner set-path | Owner UUID   |
| ----- | -------------------------------------------- | -------------- | ------------ |
| `git` | the version-control tool this set commits to | `(external)`   | `(external)` |

INTER expectation: `external` (declared external contract) — no owner set to resolve; recorded as
declared, and what can be checked (consistency) is checked.
