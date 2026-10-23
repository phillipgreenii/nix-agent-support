// Package feedop serves the shared change feed on a daemon's wire: the changes
// op (a consumer polls the rows after its acknowledged position for a query) and
// the changes_ack op (the consumer acknowledges the position it was given, after
// it has flushed its output). It is built on pkg/connectord/feed and is the same
// code for every connector: the connector's entity JSON comes from a Renderer,
// its wire errors from a list of unauthenticated sentinels, and the rest from a
// registry.Spec carried by the feed.
//
// # Acknowledgement is explicit
//
// A changes answer does not move the cursor's position: the consumer flushes the
// answer and only then sends changes_ack {consumer, query, next_seq} with the
// next_seq it received, so a consumer that crashes before the flush is handed the
// same rows again (at-least-once). A poll that returned no rows still carries a
// next_seq, so a quiet query's position keeps moving. The one exception is the
// first poll of a new cursor and a reset, which the feed itself places at the
// tail.
//
// # The answer
//
// One source named with Deps.Source, status succeeded (a poll that fails is an
// error, not a degraded row), whose version is the cursor's position and whose
// next_seq is the log position scanned up to. One change per delivered row: the
// change word is feed.Deliver's (the handler never recomputes it) and the entity
// is what Renderer.Row returns for the row. A reset answers one added change per
// current member with seq 0, empty kinds and fields and the entity of
// Renderer.Replay. sources_freshness lists every configured query with the time
// of its last whole-query origin success and its last error.
//
// # Errors
//
// MapError puts every failure on the closed seven-value wire taxonomy: a cursor
// that is no longer served (the message begins cursor_expired:), a cursor
// conflict, an acknowledgement above the head of the log and every bad argument
// are invalid_argument; an unknown query is query_not_recognized; a governor
// refusal is unavailable, an error matching one of Deps.AuthErrors
// unauthenticated, anything else unavailable.
//
// Registration of the handlers is the assembly's.
package feedop
