// Package feed is the core of the shared change feed: what a consumer polling
// for changes receives and how its position moves, independent of any wire
// format and of any connector. The registered connector's vocabulary arrives as
// a registry.Spec (its views, its roles, its reference fold) and its queries
// through the Queries interface; this package knows no kind of its own.
//
//   - A cursor is (consumer, view, scope): the view is one of Spec.Views, the
//     scope is the name of a configured query. The cursor is stored in the
//     consumer table (view in its kind column, scope in its query column). Calls
//     for one cursor are serialised; different cursors run in parallel.
//   - A new cursor's first poll starts at the TAIL of the log, taken after the
//     scope's membership baseline exists. Before the baseline no cursor is
//     created: the poll answers an empty result whose NextSeq is the head, and
//     the consumer's Ack of that NextSeq creates it.
//   - Delivery is at-least-once. Poll never moves a position; only Ack does, and
//     only forward. A poll that hands out rows the cursor was already given and
//     did not acknowledge reports the redelivery to the Observer.
//   - Reset replays the scope's current members as added and moves the cursor to
//     the head.
//   - Cached answers from the store without running the query.
//   - A cursor whose position fell outside retention is answered
//     *CursorExpiredError ("cursor_expired:"); after NoteMovedAside the first
//     poll of every cursor is answered the same way, until Reset.
//   - One consumer may not hold the cursors of two views for one scope
//     (*KeyConflictError).
//
// Rows are filtered per cursor by Deliver, which also names the wire word
// (added, removed or changed). A change row is appended elsewhere, in the
// transaction of the content change that caused it; this package only reads the
// log and keeps the consumer table.
//
// Only the legacy delivery rule exists in this package: a scope is required, a
// member's rows are delivered, and a non-member's row only when a membership
// kind names the scope.
package feed
