// Package notice is the daemon half of the write-notice protocol of the Jira
// daemon.
//
// A write stays in client mode: the calling process executes it directly
// against the origin tool, so it never depends on the daemon. Around each write
// the client sends the daemon two short notices, write_begin and invalidate,
// and this package answers them without any origin call and without waiting for
// the daemon to be idle.
//
// The rule they enforce is: after write_begin no refresh of that entity, begun
// before or during the write, can store older content than the write produced,
// even when invalidate is never delivered.
//
//   - Table is the hold table: an in-memory, per-entity hold bounded by the
//     lease (write_lease). It is NOT part of the store, so a restart drops every
//     hold. Each hold has an expiry timer that, at the lease's end, bumps the
//     generations once more, releases the hold and queues one re-pull, so a
//     dropped invalidate cannot leave older content storable.
//
//   - GuardStore wraps the daemon store for the two writers of group rows (the
//     summary Persister and the shared group service): while an entity is held
//     its PutGroupIf and PutGroupIfThen store nothing and report stored false,
//     so a refresh begun after write_begin that commits during the write loses
//     and appends no change row (the change hook runs inside PutGroupIfThen only
//     when it stored).
//
//   - Handlers returns the two ops, write_begin and invalidate. write_begin
//     takes the hold FIRST and bumps the generation of both groups second, so a
//     commit that checks the hold after the bump always sees it; invalidate bumps
//     the generations again BEFORE it releases the hold, so a refresh that took
//     its token while the hold was in force loses too.
//
// Wiring order: build the Table over the raw store, build the summary and detail
// services over GuardStore (NewGuardStore(st, table)), then call Table.Bind with
// both services. A hold that expires before Bind queues nothing.
//
// Deps.Queries is the daemon's "re-run the configured queries" seam: calling it
// queues one re-run of every configured query and returns the names queued. The
// invalidate op calls it only for an issue the daemon does not know (a freshly
// created one), whose membership in a query result is the one way to learn it.
package notice
