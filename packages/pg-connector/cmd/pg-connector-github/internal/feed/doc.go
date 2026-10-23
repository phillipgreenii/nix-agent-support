// Package feed is the GitHub daemon's adapter over the shared change feed
// (pkg/connectord/feed): it binds the GitHub registration (the views pr and ci)
// and re-exports its request, position and query types (PollRequest, AckRequest,
// Position, Queries). The rules below are the shared feed's.
//
// The feed is the core of the change feed: what a consumer polling
// `changes` receives and how its position moves, independent of any wire
// format. The ops that put it on the wire are built on it.
//
//   - A consumer holds one cursor, bound to the view ("pr" for the PR feed, "ci"
//     for the CI view) and the query of the call that created it. Another view is
//     a *KeyConflictError; another query is a *ScopeConflictError unless the call
//     is a Reset, which rebinds the cursor. Calls for one consumer are serialised;
//     different consumers run in parallel.
//   - A new cursor's first poll starts at the TAIL of the log, taken after the
//     query's membership baseline exists. Before the baseline no cursor is
//     created: the poll answers an empty result whose NextSeq is the head, and
//     the consumer's Ack of that NextSeq creates it.
//   - Delivery is at-least-once. Poll never moves a position; only Ack does, and
//     only forward. A poll that hands out rows the cursor was already given and
//     did not acknowledge reports the redelivery to the Observer.
//   - Reset replays the query's current members as added and moves the cursor to
//     the head.
//   - Cached answers from the store without running the query.
//   - A cursor whose position fell outside retention is answered
//     *CursorExpiredError ("cursor_expired:"); after NoteMovedAside the first poll
//     of every cursor is answered the same way, until Reset.
//
// Rows are filtered per cursor: the PR feed delivers the rows of the query's current
// members plus the membership rows that name the query (entered_query,
// left_query, removed), so a consumer sees a departure; the CI feed delivers the
// ci_changed rows of members and the same membership rows. DeliveryFor decides
// this per row and gives the change word (added, removed or changed).
//
// A change row is appended elsewhere, in the transaction of the content change
// that caused it; this package only reads the log and keeps the consumer table.
package feed
