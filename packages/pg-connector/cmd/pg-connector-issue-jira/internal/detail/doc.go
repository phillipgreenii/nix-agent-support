// Package detail is the detail group of an issue in the Jira daemon: the
// fetcher that reads one issue's comments and operator facts, the wiring of the
// shared group refresh service (groupsvc.Service) for the daemon, and the
// revalidation rule that makes the origin's updated text a revalidation token.
//
//   - Fetcher fetches the group with one `pjira search` per issue
//     (`key = "<KEY>"`, expanding changelog and comments). The search is used
//     because it returns the comments and status changelog that `pjira issue`
//     does not carry. The origin tool exposes no dependency concept, so the
//     stored direct edges are empty, and it emits no description, so that is
//     empty too.
//
//   - ErrorPolicy is the Jira reading of an origin failure for the shared
//     service: the scriptout error of a failure, and no pause of its own (the
//     origin runner paused the governor already).
//
//   - Service builds the shared service for entity kind issue, registers the
//     Fetcher as the only non-primary group, and exposes Revalidate, the
//     function that is installed on summary.Persister.AfterCommit: a first
//     sighting queues the baseline fetch, a changed updated token queues a
//     refresh at once, and an unchanged one extends the stored detail up to
//     (never past) detail_max_age, queuing a re-pull when it is already there.
//     A closed issue that is terminal is neither extended nor re-pulled.
//
// Nothing here appends a change row for a change to the updated token alone:
// the change engine does, from the content the Fetcher stores.
package detail
