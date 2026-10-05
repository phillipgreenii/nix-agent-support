# pg-decider — configuration

The decider has its own small configuration. It carries the settings its writes need and nothing
that the decider can derive from the view.

## Location and format

The configuration is one JSON file whose path is named by the environment variable
`PG_DECIDER_CONFIG`. The deployment that runs the decider renders the file.

- When `PG_DECIDER_CONFIG` is unset or empty, the decider runs with the defaults below.
- When it names a file that is missing or is not valid JSON, `apply` exits `1` with a message
  naming the path, before it writes anything.
- Unknown keys are tolerated, so a deployment MAY carry a key a later version of the decider reads.
- Only `apply` loads the file. `plan` writes nothing and reads no configuration.

```json
{
  "agent_tracker_backend": "beads",
  "beads_dir": "/path/to/beads",
  "actor": "pg-decider",
  "escalate_after": 3
}
```

## Keys

| Key                     | Type    | Default      | Effect                                                                                                                           |
| ----------------------- | ------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| `agent_tracker_backend` | string  | empty        | The tracker backend every work-item write is pinned to. When empty, no backend is passed and the connector's own default is used |
| `beads_dir`             | string  | empty        | The beads directory handed to the connector for every work-item call. When empty, none is passed                                 |
| `actor`                 | string  | `pg-decider` | The identity attributed to the decider's annotation writes; pg-desk's annotate verb requires one                                 |
| `escalate_after`        | integer | `3`          | K: the number of consecutive failing runs of a rule after which the failure is escalated to a person                             |

`escalate_after` MUST be at least `1`; an explicit smaller value makes `apply` exit `1`.

### Settings copied from pg-desk's `sync:` block

pg-desk's `sync:` block holds two settings, `sync.mode` and `sync.retry`, and neither is copied:

- `sync.mode` is replaced by the two commands: `plan` prints what would be done and `apply` does it.
- `sync.retry` (the backoff before a failed sync is retried) is replaced by the escalation after K
  consecutive failures, `escalate_after`.

The two keys the sync stage's tracker writes did consume, `agent_tracker_backend` and the first
configured repository's `beads_dir`, are carried over as the same-named keys above, with the same
meaning. No other setting is copied, because the decider reads nothing else.

## Cutover

pg-desk's own copies of these settings, its `sync:` block and its tracker-write settings, are
removed in the cutover phase, together with the `sync` stage they configure. Until then the
decider's file and pg-desk's configuration are separate, and the decider MUST NOT read pg-desk's
configuration.

## Invariants

- **INV-DECIDER-21.** The decider MUST read its configuration from the single JSON file named by
  `PG_DECIDER_CONFIG` and MUST NOT read pg-desk's configuration.
- **INV-DECIDER-22.** A configuration file that is named but unreadable, unparseable or holds an
  invalid value MUST make `apply` exit `1` before any write.
- **INV-DECIDER-23.** An absent `escalate_after` MUST mean `3`, an absent `actor` MUST mean
  `pg-decider`, and an absent `agent_tracker_backend` or `beads_dir` MUST pass nothing to the
  connector rather than an empty value.
