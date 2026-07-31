# Interfaces — implementer (inter corpus: declared-but-uncited WARN)

This implementer declares a row for a contract it never mentions outside the imports table: either
the citation was removed and the row outlived it, or the row was speculative.

- **`INTF-ZR-SOURCE`** — implements the owner's event source. Cites
  `owner · owner/docs/behavior · INTF-SOURCE`.

## External references

| Name           | What it is                        | Owner set-path        | Owner UUID                                                                                                  |
| -------------- | --------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------- |
| `INTF-SOURCE`  | the owner's event-source contract | `owner/docs/behavior` | [11111111-1111-4111-8111-111111111111](https://example.invalid/owner/blob/main/docs/behavior/interfaces.md) |
| `INTF-HANDLER` | the owner's handler contract      | `owner/docs/behavior` | [22222222-2222-4222-8222-222222222222](https://example.invalid/owner/blob/main/docs/behavior/interfaces.md) |

INTER expectation: `reconcile-imports.sh` reports `INTF-HANDLER` as declared-but-uncited — a WARN by
default (a stale row misleads a reader but breaks no identity) and a FAIL under `--strict`.
