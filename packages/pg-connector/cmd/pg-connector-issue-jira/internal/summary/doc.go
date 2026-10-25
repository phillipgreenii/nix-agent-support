// Package summary is the persistence and retention of the primary (summary)
// group of an issue in the Jira daemon.
//
//   - Persister stores one refreshed summary, its entity row and its change
//     detection in ONE store transaction through groupkit.CommitThen, with the
//     generation token the caller took (Persister.Begin) BEFORE its origin call.
//     After the commit it stamps the entity's closed_at when the issue is seen to
//     reach status category done, and clears it on a reopen.
//
//   - KeepAlive is the daemon's retention rule: an issue is tracked, and so
//     refreshed in the background, until it has been closed for TerminalTTL.
//
//   - Sweep evicts an issue that has been closed for the expiry (7 days); the
//     row, its groups and its alias go, and with them the watched flag.
//
//   - Classes counts the tracked and watched entities for the metrics.
//
//   - NewAliases is the query runner's alias table: a Jira key is its own stable
//     id, so a reference resolves without an origin call.
//
// This package adds no store rung: the closed-at rung (entity.closed_at) came
// with the shared library and is written only through Store.SetClosedAt.
package summary
