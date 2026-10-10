# pg-desk — annotate, suppress, unsuppress, force-review

The annotation verbs write the key/value `annotation` table of the migrated store
(entity-change-flow design 6.9 and 9.6): one row per `(type, id, key)` holding `value`, `origin`,
`set_by` and `set_at`. Every write appends one `annotation_changed` change-log record in the same
transaction, so the change is never visible without its record. An annotation write does not bump
the entity's version.

```mermaid
flowchart LR
    A["annotate / suppress / unsuppress / force-review"] -->|"one transaction"| T[("annotation row")]
    A -->|"same transaction"| L[("annotation_changed record")]
    H["hide / unhide / wip / feedback set"] -->|"same writes, new schema"| T
```

## Verbs

- `pg-desk <type> annotate <id> --key K --value V [--origin O] [--actor A]` sets key `K` to `V`
  on the entity, replacing any earlier value. `<type>` is `pr`, `issue` or `thread`. `--origin`
  names the writer and defaults to `pg-desk`; it is stored on the row and on the change record.
  `--key` and one of `--value` or `--remove` are required. A reserved key MUST carry its value shape
  (below) or the verb fails and writes nothing; any other key is stored as given.
- `pg-desk <type> annotate <id> --key K --remove [--origin O] [--actor A]` deletes key `K`. `--remove`
  and `--value` are mutually exclusive: giving both, or neither, is a usage error that writes
  nothing. When the key existed it appends one `annotation_changed` record in the same transaction;
  when it did not it prints that there was nothing to do, appends no record and exits `0`. It is the
  generic removal form; any key, reserved or not, can be removed.
- `pg-desk <type> suppress <id> --kind K` sets `suppress.K` to `true`. `unsuppress <id> --kind K`
  removes `suppress.K`; unsuppressing a kind that is not suppressed prints that there was nothing
  to do, appends no record and exits `0`.
- `pg-desk pr force-review <id>` sets `force_review` to the head SHA the PR is at now. It fails
  when the PR has no recorded head commit. Setting never consumes or clears the flag.
- `pg-desk pr force-review <id> --clear [--origin O] [--actor A]` removes `force_review`. `--origin`
  names the writer and defaults to `pg-desk`; it is stored on the change record, and `--origin` is
  accepted only together with `--clear`. When the flag was set it appends one `annotation_changed`
  record; when it was not set it prints that there was nothing to do, appends no record and exits
  `0`. It is the only verb that consumes the flag. Whether and when to clear is the decider's
  choice; this verb is the mechanism.
- `<id>` is resolved as for the other typed verbs: a PR by number, `OWNER/REPO#N` or URL; an
  issue or thread id verbatim. An id with no stored entity fails and writes nothing.
- `set_by` is `--actor`, defaulting to the configured actor; these verbs fail when neither is
  available.

## Reserved keys

| Key                     | Value                                                 | Written by                             |
| ----------------------- | ----------------------------------------------------- | -------------------------------------- |
| `hidden`                | JSON `{"value": true\|false, "reason": string\|null}` | `hide`, `unhide`                       |
| `wip`                   | `true` or `false`                                     | `wip`                                  |
| `disposition.<comment>` | `will-fix`, `wont-fix` or `no-action`                 | `pr feedback set`                      |
| `suppress.<kind>`       | `true`                                                | `suppress`                             |
| `force_review`          | the head SHA it was requested at                      | `pr force-review`                      |
| `ready_to_land`         | `true` or `false`                                     | the land-ready decider, via `annotate` |
| `focus_selected`        | a period key (`YYYY-MM-DD`) or `none`                 | the focus verbs, via `annotate`        |
| `decider.<name>.<k>`    | any string                                            | deciders, via `annotate`               |

`hidden` and `suppress.<kind>` are steps 1 and 2 of every PR rule's precedence order, so their
shapes are exact. `pr feedback set --disposition open` records the explicit override `open` (the
fourth disposition the feedback verbs accept) as `disposition.<comment>` too.

## Invariants

- **INV-ANNOTATE-1.** Every successful write or removal MUST append exactly one
  `annotation_changed` record in the same transaction as the row change; a failed write MUST leave
  neither.
- **INV-ANNOTATE-2.** A reserved key MUST be written only in its documented shape; for
  `focus_selected` that is a calendar date written exactly as `YYYY-MM-DD` (a period key) or the
  word `none`, and anything else (including the empty string) MUST be rejected with nothing written.
- **INV-ANNOTATE-3.** The set path of `force-review` MUST NOT consume or clear a flag; the clear
  path (`force-review --clear`) is the only consumer of `force_review`.

## Old-schema refusal

`annotate`, `suppress`, `unsuppress` and `pr force-review` (set and `--clear`) have no old-schema
counterpart. On an old-schema store each refuses with the store's error, which says to run
`pg-desk migrate --cutover`, and exits `1`.

## Exit codes

`0` on success (including unsuppressing a kind that was not suppressed, clearing a
`force_review` that was not set and removing an annotation that was not set); `1` on any error.

## Consumption record convention

pg-desk records no consumption itself. A decider that consumes a force-review request clears the
flag with `force-review --clear` and records what it consumed with `annotate`, under the
`decider.<name>.<k>` namespace (`<name>` is the decider, here `pr-decider`):

```text
decider.pr-decider.force_review_consumed=<sha>
```

`<sha>` is the head SHA the consumed request was made at (the earlier `force_review_sha` of the
view). A request later set at that same head is then distinguishable from the consumed one by
comparing `force_review_sha` with this record; a request at a newer head is fresh. Which head counts
as fresh is the decider's rule, not pg-desk's.

## Telemetry and logs

`annotate` (set and `--remove`) emits nothing over OpenTelemetry or Prometheus and has no
structured-log contract; its only output is the one stdout line naming what it did.

## Out of scope

Every rule that reads annotations, including when a decider clears `force_review` and which head a
re-request counts as fresh against (deciders); showing annotations (`show`).
