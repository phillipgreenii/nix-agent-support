// Package classify derives what changed in a pull request entity from the
// content a group held before and the content it holds now: the upstream change
// kinds and the field-level before and after values of a change row. It is pure
// and store-free. It decides WHAT changed; the change engine decides when a row
// is appended, held back or dropped.
//
//   - Summary classifies a refreshed summary (with the mergeability rule's
//     output and a repository rename); Group classifies the detail,
//     conversation, files, commits and runs groups from their stored bytes.
//   - Merge combines two Results the way the settle barrier does.
//   - Membership and Removed build the Results of the query membership and
//     tombstone rows.
//   - EncodeFields and DecodeFields are the stored form of Result.Fields.
//
// # Baselines
//
// The first sighting of an entity and the first fetch of a group yield the
// empty Result, even when the entity's other groups already have content. The
// pending group is never classified. A field absent on either side of a kind
// signal (an empty state, head, base or rollup) is skipped, never read as a
// change.
//
// # Signal to kind
//
//	reopened              state moved from MERGED or CLOSED to OPEN (summary)
//	closed                state moved to CLOSED (summary)
//	merged                state moved to MERGED (summary), or the merged flag
//	                      turned true (detail)
//	draft_changed         the draft flag moved (summary)
//	head_changed          the head commit moved (summary head_ref_oid)
//	base_changed          the base BRANCH NAME moved (summary base_ref_name)
//	ci_changed            the summary rollup (checks_state) moved between two
//	                      non-empty values, or a run or job field the runs
//	                      fingerprint reads moved (runs group)
//	mergeability_changed  next.MergeabilityChanged: a known value moved to a
//	                      different known one; UNKNOWN is never a change
//	review_changed        review decision or review count moved (summary), or a
//	                      review's (id, state) or the review count moved
//	                      (conversation)
//	feedback_changed      comment count moved (summary), or a comment's (id,
//	                      resolved) or the comment count moved, at the PR level
//	                      or inside a review (conversation)
//	renamed               the reference owner/repo#n moved (summary)
//	removed               built by Removed
//	entered_query         built by Membership
//	left_query            built by Membership
//
// # Fields
//
// Every change to a recorded field yields a row, with an empty kind list when no
// kind applies: every content change is delivered, and the shadow run measures
// the resulting event rate. A Field's Before and After are JSON values (a string
// is a JSON string, a missing side is the empty RawMessage); Merge compares two
// values by their compacted JSON bytes. Fields are named:
//
//	summary       state, draft, head_sha, base, checks_state, mergeable (only
//	              when mergeability_changed), review_decision, review_count,
//	              comment_count, ref (renamed: JSON strings, old then new),
//	              title, body, author, label_count, labels (sorted set),
//	              review_thread_count, head_ref (the head branch name)
//	detail        merged, review_requests (sorted set), additions, deletions,
//	              changed_files
//	conversation  comment:<id>  {"body": <text capped at bodyCap bytes>,
//	                             "hash": <hex SHA-256 of the full text>}; a
//	                             side where the comment does not exist is empty
//	              comment_resolved:<id>  JSON bool
//	              review:<id>            the review state as a JSON string
//	              review_body:<id>       a body object as for comment:<id>
//	files         files    {"count","additions","deletions","hash"}: bounded,
//	                       never the list (a PR with thousands of files is the
//	                       same four numbers)
//	commits       commits  {"count","last_sha","hash"}: bounded likewise
//	runs          run:<id>           {"attempt","workflow","head_sha","status",
//	                                  "conclusion"}; a side where the run is
//	                                  not listed is empty
//	              job:<run id>/<job name>  {"status","conclusion"}; empty where
//	                                  the job is not listed. A repeated run id
//	                                  or job name within one list gets the
//	                                  suffix #2, #3, ...
//	membership    query:<name>  entered_query: After is the query name as a JSON
//	              string; left_query and removed: Before is. One field per
//	              query, so a merge by field name never collapses two queries.
//
// A body is cut at the last character boundary at or before bodyCap bytes (a
// body of bodyCap bytes is not cut, one of bodyCap+1 is); a bodyCap of zero or
// less keeps the hash only. Two bodies are compared by the hash of their full
// text, so a change past the cap is still a change.
//
// # Fields that never alone make a row (the exclusion list)
//
// A change that moves only these yields the empty Result, so that the movement
// of a base branch does not log a row for every kept-alive PR:
//
//	summary       node_id (identity), gone (handled as no comparison), url
//	              (follows the reference), updated_at, base_ref_oid
//	detail        mergeStateStatus
//	conversation  connections (truncation report of the read); per comment:
//	              author, path, line, thread_id, review_thread_id,
//	              thread_outdated (line and outdated move with pushes); per
//	              review: author, submitted_at, commit_oid
//	runs          per run: url, provider, repo, pr_id, as_of, stale,
//	              served_from, age_seconds, groups; per job: id, url
//
// updated_at, base_ref_oid and mergeStateStatus are the three fields pg-desk's
// classifier lists under "Deliberately not signals" because they move with
// events outside the PR. TestFieldCoverage pins this list: it audits every
// exported field of the summary, detail, conversation, run and job types, so a
// field added later fails until someone classifies it.
//
// # Merging
//
// Merge unions kinds and keeps per field the earliest before and the latest
// after, dropping a field that came back. An entered_query row and a left_query
// row for one query that merge into one pending row cancel out their
// query:<name> field (the entity came and went inside one settle window) but the
// kinds stay unioned: the result then carries both kinds and no query field,
// which a consumer attributing by query reads as no delivery.
package classify
