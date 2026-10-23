// Package feed is the core of the shared change feed: what a consumer polling
// for changes receives and how its position moves, independent of any wire
// format and of any connector. The registered connector's vocabulary arrives as
// a registry.Spec (its views, its roles, its reference fold) and its queries
// through the Queries interface; this package knows no kind of its own.
//
//   - A consumer holds ONE cursor, stored in the consumer table keyed by the
//     consumer name. The first poll or ack that creates it fixes its view (one of
//     Spec.Views) and its scope (the name of a configured query, or none). A
//     different view is a *KeyConflictError; a different scope is a
//     *ScopeConflictError unless the call is a Reset, which rebinds the cursor to
//     the new scope and moves it to the head. Calls for one consumer are
//     serialised; different consumers run in parallel.
//   - A scope is required exactly when the registration delivers by membership;
//     otherwise it is optional, and when given it must be a configured query.
//   - A new cursor's first poll starts at the TAIL of the log, taken after the
//     scope's membership baseline exists (for an unscoped cursor, after the
//     baseline of every configured watched query). Before the baseline no cursor
//     is created: the poll answers an empty result whose NextSeq is the head, and
//     the consumer's Ack of that NextSeq creates it.
//   - Delivery is at-least-once. Poll never moves a position; only Ack does, and
//     only forward. A poll that hands out rows the cursor was already given and
//     did not acknowledge reports the redelivery to the Observer.
//   - Reset moves the cursor to the head and answers the references to replay as
//     added: the scope's members by membership, the watched members of the scope
//     (or every watched reference, for an unscoped cursor) by watched. It never
//     appends a row.
//   - Cached answers from the store without running a query; an uncached scoped
//     poll runs its one query and an uncached unscoped poll runs every configured
//     watched query, each honouring its own freshness.
//   - A cursor whose position fell outside retention is answered
//     *CursorExpiredError ("cursor_expired:"); after NoteMovedAside the first
//     poll of every cursor is answered the same way, until Reset.
//
// Rows are filtered per cursor by Deliver, which also names the wire word
// (added, removed or changed). A change row is appended elsewhere, in the
// transaction of the content change that caused it; this package only reads the
// log and keeps the consumer table.
package feed
