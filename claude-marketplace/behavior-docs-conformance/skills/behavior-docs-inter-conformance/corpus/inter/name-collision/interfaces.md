# Interfaces — implementer (inter corpus: asserted affordance the owner does not have)

This implementer names a concrete affordance on the same line as its citation of the owner's
contract, and the owner has no such name. The CITATION is perfectly valid — the UUID resolves and
the name matches — so every UUID-matching check reports this fixture as aligned.

- **`INTF-ZR-SOURCE`** — implements the owner's event source. **The push path:** `owner-emit <json>`
  is an `INTF-SOURCE` subcommand that submits one operator-supplied event.

## External references

| Name          | What it is                        | Owner set-path        | Owner UUID                                                                                                  |
| ------------- | --------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------- |
| `INTF-SOURCE` | the owner's event-source contract | `owner/docs/behavior` | [11111111-1111-4111-8111-111111111111](https://example.invalid/owner/blob/main/docs/behavior/interfaces.md) |

INTER expectation: `name-collisions.sh` reports `owner-emit` as a class-2 candidate — the owner set
calls that affordance `source-push` and has no `owner-emit`. This is the shape of the shipped
`push-inject` vs. `pr-pool-emit` defect.
