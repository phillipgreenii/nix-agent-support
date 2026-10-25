// Package classify holds the pure, store-free rules that turn the difference
// between two stored contents of one issue into the change kinds and changed
// fields of a change row.
//
// # Kinds
//
// Kinds lists the change kinds in the canonical order every kind list uses. The
// membership words entered_query and left_query are information about a query
// and are never the kind of a stored row; KindAdded, the one first-observation
// kind, is produced by the change-engine registration and is not listed by
// Kinds. There is no "opened" kind.
//
// # Summary
//
// Summary compares two stored summaries after normalising them (as_of, stale,
// version, updated_at and the updated token never take part in a comparison).
// Every difference in a compared field is recorded as a field of the result,
// under the schema.Issue JSON key, so no real difference is lost; a difference
// no specific kind covers is the catch-all kind fields_changed, which names its
// fields. A change to the updated token alone yields an empty Result but still
// queues the detail group for a re-pull.
//
// # Detail
//
// Detail compares two stored details: a description difference is
// description_changed and a difference in the direct edges is deps_changed.
// Comment bodies, the two operator facts and the comment summary are not
// compared there.
//
// # Baselines
//
// The first sighting and the first fetch of a group are baselines and add
// nothing: the engine never calls these functions for them, so both arguments
// of every function here are the non-nil content of a previous and a refreshed
// read.
package classify
