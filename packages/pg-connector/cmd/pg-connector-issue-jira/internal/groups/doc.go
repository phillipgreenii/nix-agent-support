// Package groups is the vocabulary of an issue's field groups in the Jira
// daemon: the two stored groups, the JSON content each stores, the
// normalisation every comparison runs over, the freshness table built from the
// settings and the Describer that reads a stored summary for the shared change
// engine.
//
// # Groups
//
// An issue is a set of groups with their own freshness. Two are stored:
//
//	summary  what a listing returns (one JQL search for many issues), kept with
//	         the origin's own updated text as the revalidation token
//	detail   what a single-issue read adds: description, comments, direct edges
//	         and the two operator facts
//
// A third group, children, is never stored: a read of children is passed
// through to the origin live.
//
// # Encoding
//
// The stored summary is the JSON encoding of SummaryContent:
//
//	{"issue": <schema.Issue>, "updated": "<origin text>", "gone": <bool>}
//
// The detail group is the JSON encoding of DetailContent:
//
//	{"description": "...", "comments": [{"id","author","body","created"}],
//	 "deps": [{"id","type"}], "status_changed_at": "...", "operator_updated_at": "..."}
//
// The comment summary (comment_count and latest_comment_at) lives in
// SummaryContent.Issue, counted from the listing's comments, so a comment is a
// visible field difference in the summary and exactly one comments_changed is
// raised per comment. DetailContent carries the comment bodies for a read and is
// not compared for comments.
//
// # Comparison
//
// Normalise and NormaliseDetail give the compared form: the as_of, stale,
// version, updated_at and updated token are dropped from a summary, and the two
// operator facts of a detail are dropped (they are derived) with each comment
// body cut on a UTF-8 boundary. Fingerprint and FingerprintDetail are the sha256
// hex of the JSON encoding of the normalised content, so a change to the updated
// token alone never changes a stored fingerprint. Stored is the form that is
// persisted: Normalise with the updated token kept.
//
// This package takes plain durations and imports no config package.
package groups
