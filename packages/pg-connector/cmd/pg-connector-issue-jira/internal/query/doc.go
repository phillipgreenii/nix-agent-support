// Package query is the Jira daemon's side of the shared query runner
// (pkg/connectord/query): it supplies the five small interfaces the runner
// needs and builds the runner.
//
// # What a query is
//
// A configured query is a NAME the daemon's own config defines (queries), whose
// value is one or more raw JQL expressions and a watched flag. The runner
// answers a name outside the config with an error, and never consults the
// request's own config block.
//
// # The five interfaces
//
//   - Lister (NewLister): one full JQL search per expression, with the issues'
//     changelog and comments expanded. It persists every issue the search
//     returned before it reports the members, so the entity rows and their
//     summary content exist before the runner looks at membership and marks the
//     members watched. A listing that was cut off is never reported complete.
//   - Settings (NewSettings): the queries, their watched flags and their TTLs,
//     read from the loaded config.
//   - Tracker (NewTracker): nothing to do; every listed issue already has a row
//     because the lister persisted it.
//   - Confirmer (NewConfirmer): the one fresh read that tells an issue that
//     closed from one that was merely filtered out of a query.
//   - Aliases: the summary package's NewAliases.
//
// # Watched queries
//
// The runner marks the members of a query whose declaration carries
// watched = true as watched, inside the transaction that stores its result:
// every member of a query's baseline run (its first complete run) without
// appending a row, and afterwards every member the query lists (a first
// observation or a known issue newly listed is one "added" row, a late watch).
// The shared engine appends rows only under its emission rule. The Jira daemon
// accepts no watch option on a query run (AdHocWatch is false).
package query
