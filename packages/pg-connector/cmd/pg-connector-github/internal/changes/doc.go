// Package changes is the change engine's core: it turns a stored content change
// into change-log rows. Its detection methods run INSIDE the store transaction
// that stores a refreshed summary or group (the seams of summary.Persister and
// groupsvc.Service), so a row and the content that caused it commit together or
// not at all.
//
// # What the engine decides
//
//   - Baselines: the first sighting of an entity and the first fetch of a group
//     add nothing (the classifier returns the zero Result for them).
//   - The settle barrier: a summary change that queues dependent groups does not
//     append at once. It is written as a pending row (kind LogKindPR, id the
//     entity's node id) that awaits those groups, with a deadline of detection
//     time plus the settle timeout. Further changes merge into it; a counting
//     fetch of an awaited group removes it from Awaited; when none remain the
//     row is appended (Complete with ResultSettled).
//   - Appending: the entity's version is bumped once, the row gets its sequence
//     number, and the pending row is deleted. A merged result that ended up
//     empty appends nothing and bumps nothing.
//   - Tombstones (DetectGone) and query membership (AppendMembership) append
//     their own rows, each with a new entity version.
//
// The timing side of settling (deadlines read off the clock, the SIGTERM append,
// the restart re-queue) belongs to the settle lifecycle; it ends a row through
// Complete.
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
//   - A summary change whose own result is empty but which queues dependents
//     still creates the pending row, so the dependents merge into ONE row.
//   - Complete re-reads the pending row from the transaction and ends THAT
//     row; a pending row that is no longer stored (completed or deleted by a
//     concurrent detection) is a no-op.
//   - DetectGone is idempotent per tombstone: it records, in store_meta, the
//     tombstoned_at it appended a removed row for, and a repeat call for the
//     same tombstone appends nothing.
package changes
