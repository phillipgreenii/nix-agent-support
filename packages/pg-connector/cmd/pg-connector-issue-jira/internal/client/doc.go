// Package client is the client mode of pg-connector-issue-jira: the thin
// scriptout backend the umbrella execs as
//
//	pg-connector-issue-jira --config <file> --instance <name>
//
// It reads one scriptout request on stdin, writes one response on stdout and
// exits with the code scriptout.ExitCodeForCode gives the response's error
// code. The loop is this package's own rather than scriptout.ServeLoop
// because ServeLoop answers unknown_op for an op missing from its table, where
// this client forwards it.
//
// # Routing
//
//   - capabilities is answered from compiled-in values plus the loaded config,
//     never by the daemon. The umbrella's cacheEnabled fails OPEN when
//     capabilities fails, so a forwarded call to a down daemon would silently
//     re-enable the umbrella cache and hide the outage. The answer carries
//     owns_changes, cache_opt_out, the state and priority vocabularies, the
//     activity kinds and queries: an object from each configured query name to
//     {"watched": <bool>}.
//   - The five writes (create, comment, transition, update, close) run in THIS
//     process, directly against the origin tool, under the caller's own
//     environment: the acting identity is the caller's, and a writer must keep
//     working while the daemon is down (INV-ICD-3). The client sends the daemon
//     the two write notices around each write (write_begin before,
//     invalidate after), each bounded by write_notice_timeout. A notice never
//     fails a write and never delays it by more than its delivery timeout; when
//     write_begin is not delivered the write still runs and invalidate is
//     skipped.
//   - Every other op, listed or not, is forwarded over the socket with the
//     call's own budget less a margin; the daemon answers unknown_op for an op
//     nobody registered. With the daemon down a forwarded op answers
//     unavailable, naming the socket path, the supervisor and its restart hint,
//     and a daemon_unreachable row is appended to the client log. The client
//     never reads the store and never calls the origin to answer a read.
//   - The status op is the one forwarded op with a local answer: the op catalog
//     (interfaces.md, "status") says it reports even when the daemon is down, so
//     with the daemon unreachable the client answers it with status.Fallback
//     (the static report, health down, source local) as an ordinary result,
//     exit 0, and appends no daemon_unreachable row. A daemon that answers with
//     an error (migrating, version mismatch) is still relayed as that error.
//
// # Callers that treat unavailable as terminal
//
// A read answers unavailable while the daemon is down or migrating
// ("migrating: ..."). The closing note of the packet that added this package
// lists the callers found.
package client
