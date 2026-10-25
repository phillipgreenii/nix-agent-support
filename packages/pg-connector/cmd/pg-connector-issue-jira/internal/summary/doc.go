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
//   - Service is the refresh of the summary group for the issues that are not
//     members of a configured query: its scheduler executor (one key-in search
//     per batch), the read-through Ensure that fetches an issue a caller asked
//     for and so tracks it, Rebuild of the queued tasks at start, and the
//     re-booking of the next refresh after every store. ContentFromPjira maps a
//     decoded pjira issue to the stored summary content.
//
// This package adds no store rung: the closed-at rung (entity.closed_at) came
// with the shared library and is written only through Store.SetClosedAt.
package summary
