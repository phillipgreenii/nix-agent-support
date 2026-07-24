# Interfaces — implementer (V3 corpus: stale-name WARNING)

The implementer cites the owner element by an OLD name (`INTF-SRC`) that the owner has since
renamed to `INTF-SOURCE`. The UUID still resolves, so identity is intact — only the NAME is stale.

## External references

| Name       | Owner set-path        | Owner UUID                           |
| ---------- | --------------------- | ------------------------------------ |
| `INTF-SRC` | `owner/docs/behavior` | 11111111-1111-4111-8111-111111111111 |

V3 expectation: `WARN` (stale name) — the owner UUID resolves but now names `INTF-SOURCE`; never a
broken identity (validates the 1.1 UUID model).
