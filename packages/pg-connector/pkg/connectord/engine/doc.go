// Package engine is the shared change engine of a connector daemon. It turns a
// stored content change into change-log rows, and it holds the result of one
// classified change and the operations over it; the kinds a result carries
// arrive as data (a registry.Spec) and the classification rules arrive through
// the Classifier interface, never as literals of this package.
//
// Exported helpers the connector's own classifier uses:
//
//   - Result, Field: one change and one changed field.
//   - EncodeFields, DecodeFields: the stored JSON form of Result.Fields.
//   - Normalize: drop unchanged fields, compact values, sort by name.
//   - OrderKinds: distinct kinds in a given canonical order.
//   - Merge: combine two results of one entity, in a given kind order.
//   - MembershipResult, RemovedResult: the results of the membership and
//     removed rows, with the kinds taken from registry.Roles.
//   - QueryFieldPrefix: the one protocol-level field name the shared code knows.
//
// # What the engine decides
//
// Its detection methods run INSIDE the store transaction that stores a
// refreshed primary content or group (the seams of the connector's persister and
// group service), so a row and the content that caused it commit together or
// not at all.
//
//   - Baselines: the first sighting of an entity and the first fetch of a group
//     add nothing (the classifier returns the zero Result for them).
//   - The settle barrier: a primary change that queues dependent groups does not
//     append at once. It is written as a pending row (kind Spec.EntityKind, id
//     the entity's key) that awaits those groups, with a deadline of detection
//     time plus the settle timeout. Further changes merge into it; a counting
//     fetch of an awaited group (Classifier.Counts) removes it from Awaited;
//     when none remain the row is appended (Complete with ResultSettled).
//   - Appending: the entity's version is bumped once, the row gets its sequence
//     number, and the pending row is deleted. A merged result that ended up
//     empty appends nothing and bumps nothing.
//   - Tombstones (DetectGone) and query membership (AppendMembership) append
//     their own rows, each with a new entity version.
//
// The timing side of settling (deadlines read off the clock, the SIGTERM append,
// the restart re-queue) belongs to the settle lifecycle; it ends a row through
// Complete (see Lifecycle: Sweep and Run end the rows whose deadline has
// passed, FlushAll ends every row, Recover re-queues the awaited groups of the
// stored rows at start). A row ended without its barrier satisfied is reported
// failed when a still-awaited group's stored row carries a last_error and
// timeout otherwise.
//
// # Transactions
//
// The engine holds no store handle. Every method that touches the store takes
// the open *store.Tx and uses only it: the store has one pooled connection, and
// a call to any *store.Store method from inside a transaction would block
// forever.
//
// # Pins this package chose where the spec is silent
//
//   - Origin: a hint recorded by NoteOrigin for the (entity, group) is used
//     once and cleared (one older than ten minutes is ignored); otherwise a
//     fetch admitted under governor.PurposeInteractive is "read" and anything
//     else is "refresh". A merged pending row keeps the origin (and origin
//     time) of the change that created it.
//   - A primary change whose own result is empty but which queues dependents
//     still creates the pending row, so the dependents merge into ONE row.
//   - Complete re-reads the pending row from the transaction and ends THAT
//     row; a pending row that is no longer stored (completed or deleted by a
//     concurrent detection) is a no-op.
//   - DetectGone is idempotent per tombstone: it records, in store_meta, the
//     tombstoned_at it appended a removed row for, and a repeat call for the
//     same tombstone appends nothing.
//   - Emission: under registry.EmitAll every tracked entity gets its rows; under
//     registry.EmitWatchedOnly DetectPrimary, DetectGroup, DetectGone and
//     AppendMembership return at once for an entity that is not watched (no
//     pending row, no version bump, no sequence number). Every appended row
//     carries the watched flag read in its own transaction (true when the same
//     write set it).
//   - Watch and WatchOne set the flag (first cause wins, never cleared). Under
//     EmitWatchedOnly a WatchLate of a known entity (one with stored primary
//     content) appends one Added-role row; a baseline watch, a watch of a
//     never-seen entity and every watch under EmitAll append none. A one-off
//     watch is applied BEFORE the refresh or read it concerns, so the change that
//     read finds is a row of a watched entity.
//   - An arrival (AppendMembership with Watch set) is ONE row: the membership kind
//     when Spec.Membership.StoreRows, joined only under EmitWatchedOnly by the
//     Added kind when the arrival newly set the flag; an empty union appends
//     nothing, and WatchBaseline sets the flag and appends nothing at all.
package engine
