// Package feed is the Jira daemon's change feed on the wire: the shared feed
// bound to the Jira registration and its configured queries (New), the queries
// adapter it reads (NewQueries), the renderer of a delivered row's entity
// (NewRenderer) and the two ops changes and changes_ack (Handlers), which are
// the shared feedop handlers for the view "issue".
//
// # Delivery
//
// A row reaches a consumer by the stored watched flag, not by query
// membership: the closed row of a watched issue is delivered after the issue
// left every query, and an unwatched issue's row is never delivered (it is
// never appended). A consumer holds ONE cursor, and the query argument only
// filters. Every answer carries the store identity, a poll that returns no row
// still carries next_seq, and a position outside retention answers
// invalid_argument whose message starts cursor_expired:. The membership words
// entered_query and left_query are not stored rows, so a membership report can
// never bump an issue's version.
//
// # Entity
//
// The entity of a delivered row is the summary-level issue as the store holds
// it now, with Version set to the row's OWN version and AsOf set to the stored
// summary's fetched time. An issue with no stored summary renders as a minimal
// entity carrying only its id, with empty AsOf and Stale true.
//
// Registration of the ops is the assembly's.
package feed
