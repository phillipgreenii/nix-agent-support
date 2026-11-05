# pg-rescue triage command role

`pg-rescue-triage` is the argv of a pg-router command role that works the deferral items
`pg-rescue-bead` files (label `pg-rescue`). It implements option A of
`docs/superpowers/specs/2026-11-05-pg-rescue-in-place-consumer-design.md`: it verifies the live
state read-only, comments the observation, and never changes the state the item points at.

## Behaviour

The role reads the item's metadata (`pg_rescue_host`, `pg_rescue_cwd`, `pg_rescue_state`,
`pg_rescue_state_kind`); the most recent `State snapshot at ...` comment, when present, replaces
`pg_rescue_state`. It claims the item, runs the verifier, comments `pg-rescue triage: <verdict>
(<action>): <reason>` with the observed values, then acts:

| Verdict                                                                               | Action                                         |
| ------------------------------------------------------------------------------------- | ---------------------------------------------- |
| `resolved` (path gone, or no operation and a clean tree)                              | close as moot                                  |
| `unreachable` (other host, unreadable path), or a `not-a-repo` item from another host | release unchanged                              |
| `drifted`                                                                             | hand over (label `human`, release)             |
| `intact`, any location class                                                          | hand over with the diagnosis (no act path yet) |
| `not-a-repo` on this host, or no usable snapshot, working directory or class          | hand over                                      |

A `canonical-clone` item is never acted on: the clone is only read. A closed item, an item without
the `pg-rescue` label, or an item a peer holds is left untouched (exit 0).

## Wiring

Declare the role with the command adapter; the item id arrives as `PG_ROUTER_ITEM_ID`:

```toml
[[role]]
name = "pg-rescue-triage"
enabled = true
binds = ["pg-rescue.item"]
handler = "/abs/path/to/pg-router-handler-command"
[role.config.command]
argv = ["/abs/path/to/pg-rescue-triage"]
```

The query that emits `pg-rescue.item` (open items labelled `pg-rescue`, not already `human`) is the
consumer's own; this repo names no tracker. Exit codes: 0 handled, 1 failure (the claim is
released), 2 usage.

## Smoke test (isolated)

Follow `.claude/rules/pg-router-wiring-testing.md`: scratch `PG_ROUTER_CONFIG` and
`PG_ROUTER_LOG_DIR`, a fake `bd` first on the role's `PATH` (set through `env PATH=...` in the argv),
then `pg-router run-role --json pg-rescue-triage '<event json>'`. Expect `bd` to be called with
`show`, `update --claim`, `comments`, `comments add` and one of `close`, `update --status open
--assignee ""` or the same plus `--add-label human`.
