// Package feed is the core of the change feed: what a consumer polling
// `changes` receives and how its position moves, independent of any wire
// format. The ops that put it on the wire are built on it.
//
//   - A cursor key is (consumer, kind, query); kind is "pr" for the PR feed and
//     "ci" for the CI view. Calls for one key are serialised; different keys run
//     in parallel.
//   - A new key's first poll starts at the TAIL of the log, taken after the
//     query's membership baseline exists. Before the baseline no key is created:
//     the poll answers an empty result whose NextSeq is the head, and the
//     consumer's Ack of that NextSeq creates the key.
//   - Delivery is at-least-once. Poll never moves a position; only Ack does, and
//     only forward. A poll that hands out rows the key was already given and did
//     not acknowledge reports the redelivery to the Observer.
//   - Reset replays the query's current members as added and moves the key's
//     position to the head.
//   - Cached answers from the store without running the query.
//   - A key whose position fell outside retention is answered
//     *CursorExpiredError ("cursor_expired:"); after NoteMovedAside the first poll
//     of every key is answered the same way, until Reset.
//   - One consumer may not hold both the "pr" and the "ci" key of one query
//     (*KeyConflictError).
//
// Rows are filtered per key: the PR feed delivers the rows of the query's current
// members plus the membership rows that name the query (entered_query,
// left_query, removed), so a consumer sees a departure; the CI feed delivers the
// ci_changed rows of members and the same membership rows. DeliveryFor decides
// this per row and gives the change word (added, removed or changed).
//
// A change row is appended elsewhere, in the transaction of the content change
// that caused it; this package only reads the log and keeps the consumer table.
package feed
