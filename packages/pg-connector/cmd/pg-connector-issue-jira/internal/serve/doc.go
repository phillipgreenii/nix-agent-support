// Package serve assembles the pg-connector-issue-jira daemon from the parts the
// sibling packages built: the process shell (state directory, lock, socket,
// drain), the request router and its gate, the store, the budget governor, the
// scheduler, the origin runner, the change engine, the summary and detail group
// services, the query runner, the change feed, the settle lifecycle, the write
// notice table, the logs and the telemetry, and every op of the Jira backend.
//
// # Startup order
//
//  1. The rendered config is already read (the binary loaded it).
//  2. The state directory is created (0700), the single-instance lock is taken,
//     a stale socket is removed and the socket is opened (proc.Start), so a
//     supervisor's health check connects at once.
//  3. The router's gate is closed: every op answers unavailable with a
//     "migrating:" message, and the socket server starts accepting.
//  4. The store is opened and migrated. A schema NEWER than this binary refuses
//     to start (a log line, exit 5, the file untouched); a corrupt store or a
//     failed migration is moved aside with a timestamp, logged, and the daemon
//     starts empty. While a migration runs the gate's message carries its
//     progress.
//  5. The event log, the telemetry and the governor come up; the governor's
//     pause changes and every spend go to the structured log, the event log and
//     the telemetry.
//  6. The write-notice table and its guard store, the change engine, the origin
//     runner, the summary persister, the summary and detail services, the query
//     runner, the change feed (a store moved aside in step 4 is recorded for it
//     here, so the first poll of every cursor answers cursor_expired) and the
//     settle lifecycle are built. The table is bound once both services exist
//     and the persister's AfterCommit is the detail service's Revalidate.
//  7. The ops are registered under Instance.
//  8. The scheduler's queue is rebuilt from the stored due times (the query
//     runner's Schedule FIRST, so a query row whose name left the config is
//     demoted before the summary rebuild decides who is a member, then the
//     summary service's and the detail service's Rebuild), the settle lifecycle
//     recovers the pending rows, and then the scheduler, the settle loop and
//     the sweep start.
//  9. The gate opens.
//
// A daemon that cannot reach the origin, or is unauthenticated, still starts and
// serves stored content. It reads no environment variable: every setting is the
// rendered file's.
//
// # Drain
//
// A SIGTERM (ctx ending) drains: the listener closes, in-flight requests finish,
// then the registered hooks run in order (in-flight writes finish, the scheduler
// and the background loops stop, every pending settle row is appended so no
// detected change is lost, the telemetry flushes, and the store's WAL checkpoint
// and close come last) and the lock is released.
//
// # Sweep
//
// A timer (ten minutes by default) runs the summary sweep (evictions go to the
// store.evictions metric), the query runner's expiry and the feed's retention.
// Nothing restores watched flags after a store was moved aside: the members of a
// watched query become watched again at the next run of that query, as a
// baseline run, and a one-off watch is not restored.
package serve
