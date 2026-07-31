# Interfaces — implementer (inter corpus: stale-name WARNING)

The implementer cites the owner element by an OLD name (`INTF-SRC`) that the owner has since
renamed to `INTF-SOURCE`. The UUID still resolves, so identity is intact — only the NAME is stale.

## External references

| Name       | What it is                        | Owner set-path        | Owner UUID                                                                                                  |
| ---------- | --------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------- |
| `INTF-SRC` | the owner's event-source contract | `owner/docs/behavior` | [11111111-1111-4111-8111-111111111111](https://example.invalid/owner/blob/main/docs/behavior/interfaces.md) |

INTER expectation: `WARN` (stale name) — the owner UUID resolves but now names `INTF-SOURCE`; never a
broken identity (validates the 1.1 UUID model).
