// Package explain is the explain op of the Jira daemon: why is one issue stale
// or silent, answered from the store and the scheduler alone (no origin call).
//
// # Request
//
// The wire argument is {"id": "ABC-1"}. A missing or blank id is
// invalid_argument. An id that is not Jira-shaped (internal.IsJiraKey) and a
// key the store holds no entity row for both answer not_found: an explanation
// of nothing is an error, not an empty report. The key is folded
// (groups.FoldRef) before it is looked up, so two spellings of one key give the
// same answer.
//
// # Result
//
// Result carries, for the one issue asked about:
//
//   - whether the issue is tracked and why (TrackedWhy is member for a member of
//     a configured query, watched for a watched issue that is no member, read
//     for an issue only a caller read), whether the keep-alive rule retains it,
//     when it closed, and whether its entity is tombstoned;
//   - whether it is watched, since when and by which recorded cause
//     (registry.ViaQuery followed by the query name, or registry.ViaDirect). The
//     flag is sticky: an issue that left every query still reports Watched until
//     eviction clears it;
//   - for each group (summary, then detail) the fetch time, the end of
//     freshness, the maximum age, the freshness verdict with its reason, the
//     last error, the queue position (-1 when not queued) and whether a task is
//     queued or running;
//   - the pending settle row, when there is one: the groups it awaits and its
//     deadline;
//   - its newest change rows (at most MaxLastChanges, newest first).
//
// The answer names the key and states only; it carries no issue content.
package explain
