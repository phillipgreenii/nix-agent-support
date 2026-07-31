# Interfaces — owner set (inter corpus)

The owner defines the contracts; identity is the UUID carrier, the name is a mutable label.

- **`INTF-SOURCE`** — typed events into the core. <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
- **`INTF-HANDLER`** — events out to a handler; status back. <!-- uuid: 22222222-2222-4222-8222-222222222222 -->

The owner's operator affordance for the push path is `source-push <json>`.

Every fixture in this corpus uses the **D5 four-column** imports shape
(`| <name> | <what it is> | <repo> · <set-path> | [<uuid>](<remote-url>) |`), which is the
current normative shape a reader should copy. The OLD three-column shape is still accepted by
`resolve-imports.sh` — per-row, so a table mid-migration resolves — and stays covered by the
inline fixtures in `tests/behavior-docs-inter-conformance.bats`, including a table that mixes
both shapes. The remote-url is deliberately unreachable: the script PARSES the link and never
DEREFERENCES it, so a fixture must not depend on a live URL.
