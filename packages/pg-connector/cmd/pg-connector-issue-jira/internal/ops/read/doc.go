// Package read is the read ops of the Jira daemon on the wire: show and list
// served from the store with read-through freshness, the three live
// pass-through reads (children, search, list_activity), deps and auth_status.
// Every op is returned as a scriptout.OpHandler value by Handlers; registering
// them with the router is the daemon assembly's.
//
// # show
//
// The arguments are {id, fresh?, watch?}. An id that is not Jira-shaped
// (internal.IsJiraKey) answers not_found with no origin call and a blank one
// invalid_argument. The summary and detail groups are read through the two
// Ensure calls (summary.Service.Ensure, then the detail service's Ensure), so a
// read with every group inside its window is a local hit and fresh: true
// forces both groups to the origin, once each (see readGroups for the order
// that keeps the shared group service from fetching the summary twice). The answer is the stored summary issue
// plus the detail's description, direct edges and operator facts, with AsOf the
// oldest served group's fetched_at, Version the entity's stored version and the
// annotations served_from (origin when this call fetched a group, else cache),
// stale, age_seconds and groups[] (summary first, then detail), which are also
// recorded on the router's carrier.
//
// With watch: true the issue is watched as a one-off. An issue the daemon
// already knows is watched BEFORE the read (engine.WatchLate: a known issue
// gets one added row, and a change the read finds is a row of a watched issue);
// one it had never seen is watched AFTER the read stored it
// (engine.WatchBaseline: the flag only, no row). A watch argument is accepted
// on show and on nothing else.
//
// # list
//
// The arguments are {query, cursor?, ids_only?}. The query name must be one the
// daemon's OWN settings define (the request's config block is never read);
// anything else answers query_not_recognized, and a watch argument is
// invalid_argument. The stored query row is used while inside its TTL;
// otherwise the runner re-runs it (Fetch) and, when that cannot be done, the
// stored row is served stale inside the expiry. The answer is
// schema.IssueListResult from the stored query row and the stored summaries:
// Cursor is always JSON null, Truncated is true when the stored row is not
// complete, and ids_only answers PresentIDs and no entities. fresh and cursor
// are ignored. An id whose stored summary is missing, gone or tombstoned is
// left out of the answer (PresentIDs included) unless ids_only is set, which
// reads no summary.
//
// # Pass-through reads
//
// children, search, list_activity, deps and auth_status are the handlers of the
// shared dispatch tables over the governed backend, so every origin call passes
// the governor (class 1, purpose passthrough), and nothing they return is
// stored. children is never cached. deps is the old backend's existence check
// followed by an empty result; it is registered although the behavior docs'
// operation catalog does not list it, because pg-desk's hydration calls it when
// issue dependencies are read and would fail on unknown_op.
//
// # When the origin cannot answer
//
// A stored answer inside the expiry is served with stale: true when the origin
// cannot be reached or a 429 pause is in force; with nothing stored the answer
// is unavailable naming the pause or the failure. An authentication failure
// answers unauthenticated when nothing is stored, and stored content is still
// served, with its age, when there is some.
package read
