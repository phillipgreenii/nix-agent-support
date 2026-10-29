## Correctness and security checklist

A reviewer SHOULD walk the changed code against this list. Each item is a prompt to look,
not a quota to fill.

- **Behavior**: Does the code do what the change says it does, on the normal path and on the
  edge cases (empty, null, zero, maximum, duplicate, out-of-order, concurrent)?
- **Error handling**: Are failures detected, propagated and reported? Is an error swallowed,
  or a partial result treated as complete? Does a failure leave state half-changed?
- **Resources and concurrency**: Are resources released on every path? Are shared state,
  ordering and retries safe under concurrent or repeated execution?
- **Compatibility**: Does the change break an existing caller, data format, stored data,
  public interface or configuration? Is a migration or rollout order required, and is it
  stated?
- **Secondary effects**: What else depends on the changed code (consumers, documentation,
  configuration, schedules, alerts), and was each updated or shown unaffected?
- **Input and trust boundaries**: Is every value that crosses a trust boundary validated
  before it is used in a query, a command, a path, a template or a deserializer?
- **Secrets and privacy**: Does the change introduce a credential, token or personal data
  into code, logs, errors or test fixtures?
- **Authorization**: Is the caller checked for permission at the point of the action, and
  not only at an earlier, bypassable step?
- **Destructive operations**: Can a bug or a mistaken input make an irreversible deletion
  or overwrite? Is there a guard or a way back?
- **Dependencies**: Is a new dependency justified, pinned as the project pins others, and
  from a source the project already trusts?
- **Performance**: Does the change add unbounded work, repeated work in a loop, or a
  needless blocking call on a path that is hot or user-facing?

A finding from this list MUST be a demonstrable defect, with a failing scenario that can be stated, before it is
reported as a blocker; anything weaker is reported as a lesser point or dropped.
