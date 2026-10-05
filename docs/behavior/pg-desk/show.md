# pg-desk — typed show

`pg-desk <type> show <id> [--json] [--refresh]` (`type` is `pr`, `issue` or `thread`) prints the
composite view of one stored entity: its snapshot, decorations, annotations and the entities it is
linked to (entity-change-flow design 6.7, 6.9, 9.5). It sits beside the top-level `pg-desk show
<pr>`, which keeps serving the old schema unchanged until the cutover phase removes it.

## Behavior

- `<id>` is resolved exactly as `refresh` resolves it: for `pr` `OWNER/REPO#N`, a PR URL or a bare
  number; for `issue` and `thread` verbatim.
- The view is read from the local store only, except under `--refresh`.
- An id with no stored entity fails with exit `1` and an error saying it does not resolve.
- On a store not yet cut over the verb refuses with the store's error, which says to run `pg-desk
migrate --cutover`, and exits `1`.

## JSON contract `pg-desk.view/v1`

`--json` (or `PG_DESK_OUTPUT=json`) prints one JSON object with these top-level fields, in this
order:

| Field         | Meaning                                                                                                                   |
| ------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `contract`    | Always `pg-desk.view/v1`.                                                                                                 |
| `type`, `id`  | The entity's type and canonical id.                                                                                       |
| `version`     | The entity's snapshot version.                                                                                            |
| `as_of`       | When the snapshot was taken.                                                                                              |
| `stale`       | True when the stored snapshot is flagged stale, or when `--refresh` of this entity failed or degraded.                    |
| `snapshot`    | The type's pg-connector schema value (`schema.PR`, `schema.Issue`, `schema.Thread`), as stored; `null` if none is stored. |
| `decorations` | `relationship`, `dispositions[]` of `{comment_id, computed, override}`, `urgency` (the level) and `category`.             |
| `annotations` | `hidden` as `{value, reason}`, `wip`, `suppress[]`, `force_review` and the `decider` map of maps.                         |
| `links[]`     | Every link of the entity, see below.                                                                                      |
| `links_as_of` | The newest time any of the entity's links was last confirmed; `null` when it has none.                                    |

`decorations` come from the stored interpretation and are empty (`""`, `[]`) for an entity that has
none. A disposition's `override` is the `disposition.<comment_id>` annotation, or `null`.
`force_review` is true when the annotation is set.

### Links

`links[]` lists every link of the entity, derived or external, whichever of its two entities it was
recorded from, so a link added on one entity appears in both entities' views. Each entry has:

- `type`, `id` and `relation` (how the two relate; `work` for a PR's own work items);
- `url`, optional: the linked entity's own web page from its stored snapshot, or for an
  issue-tracker key the configured `links.issue_url_template`. It is omitted, never empty, when none
  can be determined;
- `state`, `labels`, `metadata` and `assignee`, read from the linked entity's own stored snapshot.
  They are present only when the linked entity has a stored row (`state` also only when the snapshot
  has one), and `assignee` is `""` when it is unclaimed;
- `origins[]`: one `derived:<extractor>` entry per extractor that finds the link and one
  `external:<actor>` entry, with `at` and an optional `reason`, per actor that added it.

No link carries who closed a work item and no rule may depend on it: `closed_by` does not exist in
the contract.

`show` and the read-only `pg-desk links` verb share the one linked-entity read; `pg-desk links`
keeps its own output unchanged.

## Human-readable output

```text
$ pg-desk pr show acme/api#123
pr acme/api#123  Add retry to client
mine  open  ready  head=9f3c1e2  ci=failure  as_of=2026-09-29T14:03:10Z (fresh)
annotations: hidden=no  wip=no  suppress=[fix-ci]
links: bd-1 (work, open)  C1/1.5 (references)
```

The second line is `relationship  state` followed, for a PR, by `ready|draft`, `head=` and `ci=`,
then `as_of=` with `(fresh)` or `(stale)`. Each link reads `id (relation[, state])`.

## `--refresh`

`--refresh` hydrates the entity first, then each directly linked entity (one hop) that has a stored
row, each through the same hydration path as `refresh`. A linked entity that fails to hydrate is
reported on stderr and does not fail the command.

If the entity's own hydration fails or degrades, the stored view is still printed, marked
`stale: true`, and the command then exits with `refresh`'s codes:

| Exit | Meaning                                                                                 |
| ---- | --------------------------------------------------------------------------------------- |
| `0`  | Printed (and, with `--refresh`, hydrated).                                              |
| `1`  | Any other error: bad arguments, an unresolvable id, an old-schema store, a store error. |
| `2`  | The entity's hydration degraded; the stale detail is printed.                           |
| `3`  | The entity's hydration failed entirely; the stored view, if any, is printed.            |

## Invariants

- **INV-SHOW-1.** Without `--refresh` the verb MUST NOT write to the store or call the network.
- **INV-SHOW-2.** A link MUST appear in the view of both of its entities, with every origin that
  claims it.
- **INV-SHOW-3.** The contract MUST NOT carry `closed_by`.
- **INV-SHOW-4.** A failed or degraded `--refresh` of the entity MUST still print the stored view,
  marked stale, before exiting non-zero.

## Telemetry and logs

`show` emits nothing over OpenTelemetry or Prometheus and has no structured-log contract. Under
`--refresh` its hydration is counted in the store's `meta` totals exactly like a `refresh`.
