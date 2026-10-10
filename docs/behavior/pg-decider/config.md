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
- `apply` loads the file for every key. `plan` writes nothing and reads the file only for
  `area_labels`, so the plan it prints shows the labels `apply` would write; a file that is named
  but unusable makes `plan` exit `1`.

```json
{
  "agent_tracker_backend": "beads",
  "beads_dir": "/path/to/beads",
  "actor": "pg-decider",
  "escalate_after": 3,
  "bead_id_pattern": "^bd-[a-z0-9]+$",
  "focus_beads_query": "focus-beads",
  "focus_priority_map": {
    "Highest": "P0",
    "High": "P1",
    "Medium": "P2",
    "Low": "P3",
    "Lowest": "P4"
  },
  "area_labels": [
    {
      "pattern": "^[a-z]+\\(widgets/api\\)",
      "labels": ["widgets-api", "widgets"]
    },
    { "pattern": "(?i)PROJ-[0-9]+", "field": "branch", "labels": ["proj"] }
  ]
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

### Focus keys

These keys serve the `focus-item` work kind (see [`work-items.md`](work-items.md), "The focus
bead"). Each is optional.

| Key                  | Type   | Default                                                     | Effect                                                                                                                                                                                                                              |
| -------------------- | ------ | ----------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `bead_id_pattern`    | string | empty                                                       | A regular expression (Go RE2, searched unanchored; supply an anchored pattern for an exact match) with the same name and meaning as pg-desk's key: an issue whose id matches is a bead, every other issue is not. Empty means unset |
| `focus_beads_query`  | string | empty                                                       | The name of the pg-connector named query that lists focus beads in EVERY status, closed included. The focus rule's dedup lookup reads it; it MUST NOT be a feed name the work-bead feeds share                                      |
| `focus_priority_map` | object | `Highest:P0`, `High:P1`, `Medium:P2`, `Low:P3`, `Lowest:P4` | Tracker priority name to `P0` to `P4`: the priority of a focus bead minted for that source. A configured map REPLACES the default whole. An unmapped name, and a PR source (which has no priority), map to `P2`                     |

`focus_beads_query` is read only by the dedup lookup of a focus bead's `create`. With the key unset,
a focus-item `create` fails closed: `apply` counts it `failed` and writes nothing, and never falls back
to the work-beads query, which would not list a closed or an untitled focus bead and so could mint a
duplicate. The deployment supplies a query that lists the label `focus-item` in every status, closed
included, under a name no work-bead feed shares (widening a feed's query would make it dispatch
non-PR beads as errors).

`bead_id_pattern` MUST compile and MUST NOT be blank when present, and every `focus_priority_map`
value MUST be one of `P0`, `P1`, `P2`, `P3`, `P4`; otherwise `apply` exits `1` before any write
(`INV-DECIDER-22`). The deployment supplies the pattern and the query name; the decider names no
project or tracker of its own.

The focus rule's condition has a durable home: the section "The `focus.item` rule" of
[`work-items.md`](work-items.md), which [`README.md`](README.md) names. This doc owns only the keys.

### Area labels

| Key           | Type  | Default | Effect                                                                       |
| ------------- | ----- | ------- | ---------------------------------------------------------------------------- |
| `area_labels` | array | empty   | Rules that derive area labels for a PR's anchor and its children (see below) |

Each rule is an object with a `pattern` (a Go RE2 regular expression, searched unanchored), an
optional `field` (`title`, the default, or `branch`) naming the PR field the pattern is searched
in, and `labels` (at least one non-blank label). A PR's area label set is the union of the labels
of every rule whose pattern matches. Which labels exist is deployment configuration; the decider
names none. A rule with an empty or uncompilable pattern, an unknown `field` or no labels makes the
file invalid.

The area label set is applied as follows:

- The anchor is created with the set.
- A `review-pr` or `process-feedback` item is created with the set plus every label named by some
  rule that its anchor already carries, so a label an operator put on the anchor flows to its
  children.
- An existing anchor, `review-pr` or `process-feedback` item gains the labels it is missing on its
  next write of any kind (the writes the rules already call for); no write is created only to add a
  label.
- A label is only ever added, never removed: labels added by hand, and an area label later removed
  by hand, are never fought over.

With no `area_labels`, the decider adds nothing and its plans are unchanged.

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
- **INV-DECIDER-24.** The decider MUST add the labels of `area_labels` rules only to the anchor and
  to `review-pr` and `process-feedback` items, MUST NOT remove a label for this reason, MUST NOT
  create a write whose only purpose is to add one, and with no `area_labels` configured MUST emit
  exactly the plan it would emit without the key.
