# Interfaces — implementer (inter corpus: cited-but-undeclared FAIL)

This implementer cites TWO of the owner's contracts in prose but declares only one, so the second
citation resolves for a human and for nothing mechanical.

- **`INTF-ZR-SOURCE`** — implements the owner's event source. Cites
  `owner · owner/docs/behavior · INTF-SOURCE`.
- **`INTF-ZR-HANDLER`** — implements the owner's handler contract, `INTF-HANDLER`, whose obligations
  it restates only on its own side.

## External references

| Name          | What it is                        | Owner set-path        | Owner UUID                                                                                                  |
| ------------- | --------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------- |
| `INTF-SOURCE` | the owner's event-source contract | `owner/docs/behavior` | [11111111-1111-4111-8111-111111111111](https://example.invalid/owner/blob/main/docs/behavior/interfaces.md) |

INTER expectation: `reconcile-imports.sh` reports `INTF-HANDLER` as cited-but-undeclared (a FAIL).
`resolve-imports.sh` reports this fixture as clean — the row it DOES declare resolves — which is
exactly why the one-directional check is not enough.
