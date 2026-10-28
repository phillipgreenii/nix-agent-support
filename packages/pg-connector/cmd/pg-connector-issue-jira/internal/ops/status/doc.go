// Package status is the health report of the Jira daemon: the `status` op, the
// document it answers with and the static report the client prints when the
// daemon is down.
//
// # The document
//
// Report is the one JSON document. The daemon answers the op with it, the
// client prints it with --json, and the text form (Render) is a rendering of
// the same values, so the two never disagree. It carries COUNTS ONLY: no issue
// key, title or content ever enters it. The field names are the contract.
//
//	health           "healthy", "degraded" or "down"; the exit code follows it
//	                 (0, 2, 3)
//	reasons          why health is not healthy, one line each (absent when healthy)
//	source           "daemon" (the daemon answered) or "local" (the static report)
//	version          the answering binary's stamped version
//	build_id         its build id
//	started_at       when the daemon started (daemon only)
//	uptime_seconds   seconds since started_at (daemon only)
//	state_dir        the state directory
//	socket           the socket path
//	store            {path, present, size_bytes, modified_at, age_seconds,
//	                 schema_version, identity}: the store file's own size and
//	                 age; the schema version and the identity only from a daemon
//	logs             {events, daemon, client}: the three log paths
//	supervisor       {kind, label, state, restart_hint}: kind "launchd" or
//	                 "none", the job label, the probe's answer (running,
//	                 registered, not_registered, none or unknown) and the
//	                 command that restarts the daemon
//	requests         {total, window_spent, background_spent, in_flight, cap,
//	                 window_seconds}: the request counter and the governor's
//	                 bucket (daemon only)
//	pause            {reason, until}: the origin pause in force, read from the
//	                 governor's state (absent when none)
//	rate_limit       {floor, limit, remaining, near_limit, headroom,
//	                 below_floor, reset_at, reason, last_status,
//	                 reading_age_seconds}: the origin's own rate-limit reading
//	                 (the last pjira-ratelimit stderr line) and the floor
//	                 rate_reserve_requests (daemon only). floor is always
//	                 stated; the rest only while the reading is no older than
//	                 origin.ReadingMaxAge (5 minutes), and each of limit,
//	                 remaining, near_limit and reset_at is absent when the
//	                 response did not carry it (absent is never 0). headroom is
//	                 remaining less floor and below_floor is true when it is
//	                 negative. A pjira that writes no such line, or a tenant
//	                 that sends no header, leaves only floor.
//	queue            {running, classes: [{class, name, depth,
//	                 oldest_overdue_seconds}]} (daemon only)
//	queries          [{name, last_success_at, last_success_age_seconds,
//	                 last_error, baseline_at, baseline_open_age_seconds}] per
//	                 configured query, sorted by name (daemon only)
//	auth             {state, detail, identity}: "ok" or "unauthenticated", the
//	                 failure and the acting identity (daemon only)
//	changes          {head_seq, pending_settle, settle_timeouts_last_hour,
//	                 settle_failures_last_hour, consumers: [{consumer, view,
//	                 scope, acked_seq, head_seq, lag_rows,
//	                 oldest_unacked_age_seconds, seen_at}]} (daemon only)
//	tracked          {members, other_open, closed_grace, watched}: the tracked
//	                 count by class and the watched count (daemon only)
//
// pending_settle is a count of rows, not a list: the rows name issues.
// last_success_age_seconds and oldest_unacked_age_seconds are the figures the
// consumer-side alerts read; they are also exported as metrics by the
// telemetry package.
//
// # What makes the report degraded
//
// The report is "down" when no daemon answered (Static). Otherwise it is
// "degraded" (exit 2) when ANY of these holds and "healthy" (exit 0) when none
// does:
//
//   - an origin pause is in force (a 429 holds the background classes);
//   - the origin's last rate-limit reading, while current, leaves fewer
//     requests of the burst window than the floor (rate_limit.below_floor);
//   - authentication is failing (the authentication marker is set);
//   - a configured query has a last_error;
//   - a configured query has had no whole-query success within
//     QueryStaleTTLs (3) of its query TTLs, measured from the later of its last
//     success and the daemon's start, so a restarted daemon is not degraded
//     before its first run could finish;
//   - a settle timeout or failure happened in the last hour (the settle counts
//     are in memory, so a restart resets them);
//   - a consumer is far behind the change-log head: it has ConsumerLagRows
//     (1000) or more rows pending, or its oldest unacknowledged row is
//     ConsumerOldestUnacked (24 hours) old or older.
//
// A queue that is overdue, a missing store file and a build-id difference are
// reported, not judged.
//
// # The static report
//
// Static builds the report the client prints when the daemon is down: the
// supervisor probe's answer, the socket path, the store file's size and age
// (os.Stat only; the store is never opened) and the three log paths, with
// health "down". Fallback wraps it as the client's answer to the status op.
//
// # No origin
//
// Collect reads the store and the daemon's own state and calls no origin: the
// package imports the origin package for its bucket name only and no runner
// method is called.
package status
