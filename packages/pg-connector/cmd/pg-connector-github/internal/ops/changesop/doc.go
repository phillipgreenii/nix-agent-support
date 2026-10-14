// Package changesop serves the change feed on the daemon's wire: the changes op
// (a consumer polls the rows after its acknowledged position for a query) and
// the changes_ack op (the consumer acknowledges the position it was given, after
// it has flushed its output).
//
// # Acknowledgement is explicit
//
// A changes answer does not move the key's position: the consumer flushes the
// answer and only then sends changes_ack {consumer, query, next_seq} with the
// next_seq it received, so a consumer that crashes before the flush is handed the
// same rows again (at-least-once). A poll that returned no rows still carries a
// next_seq, so a quiet query's position keeps moving. The one exception is the
// first poll of a new key and a reset, which the feed itself places at the tail.
//
// # The answer
//
// One source named with the instance's registry name (status.RegistryNamePR),
// status succeeded (a poll that fails is an error, not a degraded row), whose
// version is the key's position and whose next_seq is the log position scanned
// up to. One change per delivered row: the change word is feed.DeliveryFor's
// (entered_query is added, left_query and removed are removed, anything else is
// changed; the handler never recomputes it) and the entity is the summary-level
// PR of the entity's CURRENT stored summary (the mapping list uses) with its
// version and head_sha replaced by the ROW'S OWN recorded values, so a
// redelivered row keeps its identity after a newer change. A reset answers one
// added change per current member with seq 0, empty kinds and fields and the
// entity as of now. sources_freshness lists every configured query with the time
// of its last whole-query origin success and its last error.
//
// # Errors
//
// mapError puts every failure on the closed seven-value taxonomy: a cursor that
// is no longer served (the message begins cursor_expired:), a key conflict, an
// acknowledgement above the head of the log and every bad argument are
// invalid_argument; an unknown query is query_not_recognized; a governor refusal
// is unavailable, an authentication failure unauthenticated, anything else
// unavailable.
//
// # The CI view
//
// The ci instance serves the same two ops (changes {consumer, query, cached,
// reset} and changes_ack {consumer, query, next_seq}, stamped with
// schema.CISchemaVersion) under the cursor key (consumer, ci, query), for a
// CI-only consumer. The rows are the PR rows of the log; feed.DeliveryForKey
// keeps ci_changed rows of a member, and a PR entering or leaving the query or
// removed, and names the change word. Each entity is the PR's CURRENT stored CI
// state (schema.CIChangeEntity: pr_id, the checks rollup folded as pr show folds
// it, and the stored runs group's runs in the list_runs shape) with the ROW'S OWN
// version and head_sha; the runs are as stored now, because the log keeps no
// snapshots. The ci key and the pr key of one (consumer, query) cannot both
// exist: the feed refuses the second with invalid_argument naming the first.
// The pr feed already delivers every ci_changed row and is the path pg-router
// and pg-desk read; the ci view exists for a consumer that must not parse PR
// rows.
//
// Registration of the handlers is the assembly's.
package changesop
