// Package origin is the only way the Jira daemon calls Jira. It wraps the
// pjira process runner so that every execution (the summary and detail
// refreshes and the live pass-through reads) is
//
//   - admitted by the shared governor under one request-rate bucket (Bucket),
//   - bounded by a concurrency cap (a semaphore taken after admission and
//     released when the process ends, even on error),
//   - counted (the governor ticket settles with cost 1 and an outcome word),
//   - classified (rate_limited, unauthenticated, error), and
//   - answered under the 429 rules: a 429 pauses background work for the
//     stated Retry-After (a configured default when none is stated),
//     consecutive 429s double the pause with jitter, and an interactive call
//     made while a pause is in force is refused with ErrPaused.
//
// The runner has no cache: serving stored content stale during a pause is the
// callers' job. Jira exposes no quota, only a request rate, so the budget is
// internal and conservative (a small share of a tenant that other users
// share); pjira surfaces no rate-limit header, so until it does the only
// signal is the 429 status text and the stated delay is always the default
// (Options.RetryAfter is the seam for a parse that is added later).
package origin
