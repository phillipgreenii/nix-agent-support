# pg-desk — typed refresh

`pg-desk <type> refresh <id>` (`type` is `pr`, `issue` or `thread`) hydrates exactly one entity
through the entity-change pipeline: it reads the entity from pg-connector, classifies the change
against the previous snapshot, writes the snapshot and appends one `change_log` record, all in one
transaction (entity-change-flow design 6.9, 9.12). For `issue` the appended record is subject to the
delivery gate: the gate withholds the row of an entity that is not watched, and the rest of the write
still happens (see [`store-schema.md`](store-schema.md), "The delivery gate"). It is the operator's targeted counterpart of the
list-and-diff `changes` verb, which pg-desk still runs for the types whose backend does not own its
changes. The old top-level `run` verb is unchanged and stays until the cutover
phase deletes it.

`refresh` is the only verb that classifies a change. For `pr` and `ci` the daemon-backed backend owns
change detection and supplies the upstream change kinds, and pg-desk's own PR change flow, which
was the classifier's other caller, is retired (see [`changes.md`](changes.md), "Who owns change
detection"). `refresh` stays as the operator's targeted, local classify-and-record verb; it does not
ask the backend what changed, and it does not use the backend's feed or its cursors.

## Behavior

- `<id>` is the entity's canonical id, resolved exactly as `history` resolves it: for `pr`
  `OWNER/REPO#N`, a PR URL or a bare number; for `issue` and `thread` verbatim.
- On success it prints `refreshed <type> <id>: <kinds> (version N)` where `<kinds>` is the list of
  kinds appended on the entity's own record, or `no change`. A first observation of a `pr` or `thread`
  entity yields only `reconcile`. For `issue`, a first observation of a WATCHED entity is kind `added`
  (the one first-observation kind), and a refresh of an `issue` entity that is not watched hydrates it
  but the delivery gate withholds the row, so no row is appended and no event can follow.
- The appended record's origin is `pg-desk`.
- `refresh` takes one option, `--watch`, for `issue` only: it marks that one entity watched as part of
  the same hydration, so its first row passes the gate. It is documented in
  [`operator-commands.md`](operator-commands.md), "issue refresh --watch", and is a one-off, not a
  query.
- For `issue`, `refresh` reads the entity's stored `backend` and pins that backend on its read, so an
  id served by a backend that owns its changes never costs a call to another backend first. When the
  stored `backend` is NULL it resolves the backend from the configured watched queries; an id served by
  watched queries of two different backends is a `doctor` gate (see
  [`operator-commands.md`](operator-commands.md), "doctor").
- Every call, whatever its outcome, is counted in the store's `meta` hydration totals and
  per-entity degraded set, exactly like a hydration made by `changes`.

## Change kind passed to the pipeline

A refresh passes the pipeline the change kind `changed`. The two constraints that decide it:

- It MUST NOT be `removed`. On a `removed` read the pipeline treats a not-found entity as an
  expected outcome (it reports "not found", writes nothing and returns no error), which would make
  a refresh of a genuinely absent entity exit quietly instead of failing loudly.
- It SHOULD NOT be `sweep`. A sweep skips re-fetching the rest of a PR's facts when its head commit
  is unchanged since the last read, which defeats an explicit operator request to re-read.

That leaves `added` and `changed`, which the pipeline treats identically. `changed` is chosen
because a refresh targets an entity the operator already knows about; `added` means a newly watched
entity.

## Failure semantics and exit codes

| Exit | Meaning                                                                                                  |
| ---- | -------------------------------------------------------------------------------------------------------- |
| `0`  | The entity was hydrated.                                                                                 |
| `1`  | Any other error: bad arguments, an unresolvable reference, an old-schema store, a store or config error. |
| `2`  | Hydration degraded for this entity: part of the detail read failed. The stale detail is kept.            |
| `3`  | Hydration failed entirely, including an entity that is not found. The previous snapshot is kept.         |

- A failed or degraded refresh writes nothing: the previous snapshot stands, `hydrated_at` is
  unchanged, and the entity stays active and due, so a later refresh or sweep retries it (for `issue`,
  which has no sweep, a later refresh or poll does).
- An entity that does not exist at the source fails loudly with exit `3` and an error that names the
  entity id and says "not found". This differs deliberately from `changes`, where the same absence
  (reported as a removal) makes the entity inactive; only an explicit removal is a membership
  decision, and a targeted refresh never makes one.

## Old-schema refusal

`refresh` has no old-schema counterpart, so on a store not yet cut over it refuses with the store's
error, which says to run `pg-desk migrate --cutover`, and exits `1`.

## Invariants

- **INV-REFRESH-1.** <!-- uuid: 3f84f75b-bbe3-4b6a-8b65-4319e31971b1 --> A refresh MUST NOT write to the entity or the change log when the read failed,
  was degraded, or found the entity absent.
- **INV-REFRESH-2.** <!-- uuid: b9258358-e299-4bcd-baea-ad4cea63e571 --> A refresh of an absent entity MUST fail with exit `3` and MUST NOT make the
  entity inactive.
- **INV-REFRESH-3.** <!-- uuid: 55825ba6-51ec-4be3-b4bf-457d29b3cd0c --> The only binary this verb execs is `pg-connector`, through the gather layer.
- **INV-REFRESH-4.** <!-- uuid: 180bb785-8c1e-4706-a78d-3e13cc58cc99 --> `refresh` MUST remain the only verb that classifies a change once pg-desk's own PR
  change flow is retired, and it MUST NOT depend on the backend's change feed.
- **INV-REFRESH-5.** <!-- uuid: f852d70b-3fec-4335-af4e-3e2ca4675e2a --> A refresh of an `issue` entity that is not watched MUST hydrate it and MUST append no row,
  because the delivery gate withholds it.
- **INV-REFRESH-6.** <!-- uuid: 15c3f393-3f1f-4720-970e-a1dbb05f09e2 --> A first observation of a watched `issue` entity MUST be recorded as kind `added`, never as
  `reconcile`.
- **INV-REFRESH-7.** <!-- uuid: f504e2ba-921f-4271-9e1f-ac6654df44a5 --> `refresh --watch` MUST mark exactly the one named `issue` entity watched in its hydration write,
  and a refresh of an `issue` MUST pin the backend it reads from, taking it from the entity's stored
  `backend` or, when that is NULL, from the configured watched queries.

## Telemetry and logs

`refresh` emits nothing over OpenTelemetry or Prometheus and has no structured-log contract. Its
hydration is counted in the store's `meta` totals that `status`, `doctor` and `/metrics` read; on
failure it prints ordinary CLI error text.
