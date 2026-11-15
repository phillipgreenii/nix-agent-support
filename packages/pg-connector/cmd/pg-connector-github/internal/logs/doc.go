// Package logs is pg-connector-github's own three JSONL logs, all in the
// phillipgreenii JSONL standard (time / level / msg required) and all strictly
// best effort: a failed write never changes an answer.
//
// # The three files
//
// Every file lives directly in the state directory the rendered config names
// (config.Config.StateDir); no path is read from the environment, and every
// constructor takes the directory explicitly.
//
//	file                (config constant)            Loki service_name              writer / rotation
//	events.jsonl        (config.EventLogFileName)    pg-connector-github            daemon, 5 MiB
//	daemon.log.jsonl    (config.DaemonLogFileName)   pg-connector-github            daemon, 5 MiB
//	client.log.jsonl    (config.ClientLogFileName)   pg-connector-github-client     client, 1 MiB under an flock
//
// The names are distinct, so no file collides with another. Phase 4a registers
// the logSources entries from this table. A glob of the form
// <state dir>/*.jsonl matches all three LIVE files and never an archive:
// rotation renames a full log to <file>.1 (the daemon logs, through x/jsonllogger
// RotatingFile) and the client log keeps a <file>.lock beside it
// (pkg/eventlog.Append); neither ends in ".jsonl". The client log is the
// one file the client writes, so it MUST get its own logSources entry whose
// service_name is pg-connector-github-client; a state-directory glob that
// also carries it would label its rows pg-connector-github.
//
// # Event log
//
// events.jsonl has two row kinds. kind=request is one row per request, written
// by the middleware EventLog.Middleware installs through the router's Use
// hook. kind=origin is one row per origin call, written by EventLog.OnSpend,
// which the governor calls for every recorded spend, so background work tied to
// no request is still accounted.
//
// The two daemon logs are slog JSONL written through x/jsonllogger: each typed
// row (RequestRow, OriginRow, stateLine) is mapped to a slog record in sink.go,
// which keeps its field names and the standard time layout, and the file is a
// jsonllogger.RotatingFile capped at DaemonMaxBytes, so a row that would take
// the file past the cap starts a fresh file and the old one becomes <file>.1.
// The client log stays on pkg/eventlog: many short-lived client processes
// append to it, and its rotation is under a cross-process flock, which
// RotatingFile (a per-process mutex) does not give.
//
// # Structured log
//
// daemon.log.jsonl is one line per state change. StructuredLog implements
// connectord.Logger, so the process shell, the socket server, the governor and
// the identity tracker log into it unchanged, and it adds one typed method per
// state change the daemon owns (start, migration, lock, viewer change, budget
// pause and resume, store move-aside or refusal).
//
// # Client log
//
// client.log.jsonl is appended by client mode when it answers unavailable
// because the daemon cannot be reached: one kind=request row with outcome
// daemon_unreachable, so caller-visible outages can be counted while the
// daemon (which would otherwise log them) is down. The client rotates it
// itself, at 1 MiB, under an flock on <file>.lock so concurrent clients rotate
// once.
package logs
