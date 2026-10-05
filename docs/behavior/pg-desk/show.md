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
| `review`      | A PR's pending agent review and its escalation, see below. Present for `pr` only, absent for `issue` and `thread`.        |
| `links[]`     | Every link of the entity, see below.                                                                                      |
| `links_as_of` | The newest time any of the entity's links was last confirmed; `null` when it has none.                                    |
| `ci`          | A PR's CI runs for its head commit, see below. Present for `pr` only, last member; absent for `issue` and `thread`.       |

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

### CI runs (`pr` only)

`ci` is an additive member of `pg-desk.view/v1`: it is the last top-level member and no existing
member changes. It lists the CI runs stored for the PR, so a decider that reads only the view can
tell which builds failed on the head commit. A build id is a run's `id` plus its `attempt`.

```json
"ci": {"runs": [{"id": "900", "attempt": 2, "name": "build", "status": "completed",
                 "conclusion": "failure", "url": "https://ci.example/runs/900"}]}
```

| Field        | Meaning                                                                                                           |
| ------------ | ----------------------------------------------------------------------------------------------------------------- |
| `id`         | The run id, a string.                                                                                             |
| `attempt`    | The run's attempt number, an integer; `0` when the backend reported none. A re-run keeps `id`.                    |
| `name`       | The check name.                                                                                                   |
| `status`     | The run's lifecycle state (for example `completed`, `in_progress`), so a finished run is told from a pending one. |
| `conclusion` | The run's outcome, `""` until it completes.                                                                       |
| `url`        | The run's web URL.                                                                                                |

The names are those of pg-connector's `schema.CIRun`; pg-desk reads them from the PR's stored
`ci` facts without importing pg-connector. Runs are those of the PR's head commit: a run whose
recorded `head_sha` differs from the entity's current head is left out, and a run with no recorded
`head_sha` is kept (the stored listing is already the head commit's). Two attempts of one run
appear as two entries. When the PR has no stored CI data, `ci` is still present with `runs` as an
empty array, never `null` and never absent, so "no CI data" is told from a binary that predates the
section. The section carries the raw ingredients only; it computes no verdict (no "failing" list).

### Pending review (`pr` only)

`review` reports, for the PR, whether the acting identity has a pending (unsubmitted) review, the
commit it is anchored to, whether that is stale relative to the PR head, and the open escalation
bead for the PR. It is read from the facts the PR's hydration stored; `show` looks nothing up
itself (INV-SHOW-1), and the facts come from the `pg-connector pr review pending` record and the
open-escalation query (see [`gather.md`](gather.md), "Pending-review state").

| Field                 | Meaning                                                                                                                     |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `state`               | `none` (no pending review), `current` (anchored to the head), `stale` (anchored to an older commit) or `unknown`.           |
| `pending`             | `true` or `false`; `null` when `state` is `unknown`.                                                                        |
| `review_id`, `url`    | The pending review's id and web URL; only when there is a pending review.                                                   |
| `anchored_commit`     | The review-level commit the pending review is anchored to; only when there is a pending review.                             |
| `head_sha`            | The PR head the lookup compared it with.                                                                                    |
| `stale`               | Whether the pending review is anchored to something other than the head; `null` without a pending review or when `unknown`. |
| `error`               | Why `state` is `unknown`; absent otherwise.                                                                                 |
| `escalation.state`    | `open` (at least one open escalation bead covers the PR), `none` or `unknown`.                                              |
| `escalation.bead_ids` | The ids of the open escalation beads; empty unless `escalation.state` is `open`.                                            |
| `escalation.error`    | Why `escalation.state` is `unknown`; absent otherwise.                                                                      |

A pending review that cannot be told apart from "none" is the failure this contract exists to
prevent: a failed lookup is `state: unknown` with its reason in `error`, never `none`. So is a PR
whose stored facts predate the lookup (the reason says to run `show --refresh`). The same holds
independently for `escalation`: a failed escalation query is `unknown`, never `none`. The
escalation covering the PR is either its own per-PR bead or a systemic roll-up bead that names it.

The view is an addition to the escalation raised by `pg-router-review-escalator`, never a
substitute for it, and displaying it changes nothing (INV-SHOW-5).

## Human-readable output

```text
$ pg-desk pr show acme/api#123
pr acme/api#123  Add retry to client
mine  open  ready  head=9f3c1e2  ci=failure  as_of=2026-09-29T14:03:10Z (fresh)
annotations: hidden=no  wip=no  suppress=[fix-ci]
review: pending=yes  commit=4b1d7aa  head=9f3c1e2  stale=yes  escalation=bd-77
links: bd-1 (work, open)  C1/1.5 (references)
```

The second line is `relationship  state` followed, for a PR, by `ready|draft`, `head=` and `ci=`,
then `as_of=` with `(fresh)` or `(stale)`. Each link reads `id (relation[, state])`.

The `review:` line appears for a PR only. It reads, by state:

```text
review: pending=no  escalation=none
review: pending=yes  commit=9f3c1e2  head=9f3c1e2  stale=no  escalation=none
review: pending=yes  commit=4b1d7aa  head=9f3c1e2  stale=yes  escalation=bd-77
review: pending=unknown (<reason>)  escalation=none
review: pending=no  escalation=unknown (<reason>)
```

Commits are abbreviated to seven characters; several escalation ids are comma-separated.

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
- **INV-SHOW-5.** The `review` object MUST NOT report a failed or missing lookup as "no pending
  review" or "no escalation": it MUST be `unknown`. Displaying it MUST NOT post, delete or submit
  a review, create, update, comment on or close a bead, or send a notification; it is read from
  stored facts, and `--refresh` hydrates through the same read-only lookups.

- **INV-SHOW-6.** The `ci` section MUST be read-only stored data: it MUST NOT carry a computed
  verdict (such as a list of failing builds), and building it MUST NOT call the network or write to
  the store. For a PR it is always present (`runs` empty when there is no CI data); for `issue` and
  `thread` it is absent.

## Telemetry and logs

`show` emits nothing over OpenTelemetry or Prometheus and has no structured-log contract. Under
`--refresh` its hydration is counted in the store's `meta` totals exactly like a `refresh`.
