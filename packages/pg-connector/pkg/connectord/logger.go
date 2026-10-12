package connectord

// Logger is the daemon's structured-log sink for state changes: one call per
// change, a stable snake_case name for what changed and alternating key, value
// fields describing it, for example
//
//	l.Log("peer_uid_rejected", "peer_uid", 501, "daemon_uid", 502)
//
// The process shell (proc) and the socket and client code log through it so
// the library never picks a log format or destination; the daemon supplies the
// implementation (its rotated structured log). Implementations MUST be safe
// for concurrent use, MUST NOT block the caller on I/O for long, and MUST
// tolerate an odd number of field arguments (a trailing key with no value).
type Logger interface {
	Log(change string, kv ...any)
}

// LoggerFunc adapts a function to Logger.
type LoggerFunc func(change string, kv ...any)

// Log calls f.
func (f LoggerFunc) Log(change string, kv ...any) { f(change, kv...) }

// NopLogger discards every line. It is the zero-config Logger of tests and of
// callers that have not wired a log yet.
var NopLogger Logger = LoggerFunc(func(string, ...any) {})
