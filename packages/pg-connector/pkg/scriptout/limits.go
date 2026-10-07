// limits.go: the shared timeout/WaitDelay/output-cap constants every exec
// path in this module (and every Tier-2 backend's own gh/git/bd child
// invocations) uses to bound a hung or unbounded child process
// [bead: pg2-332z8, items #13 and #26].
//
// Before this file, NOTHING in this module set a context deadline or an
// exec.Cmd.WaitDelay anywhere: exec.go's runInvoke (umbrella -> backend
// binary) and serve.go's serveLoop (backend binary -> its own handler, and
// transitively the handler's own gh/git/bd children, since ctx flows
// straight through) both ran with an unbounded context.Background(). A
// hung `gh`, a `bd` blocked on a wedged dolt server, or a grandchild
// process inheriting a pipe fd and holding it open, could hang the whole
// call forever — and because every fan-out in this design is deliberately
// SERIAL (see e.g. cmd/pg-connector/ci.go's fanOutCIList doc comment), one
// stalled source blocked every other source queued behind it too.
package scriptout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultExecTimeout bounds how long a single exec of another process is
// allowed to run before its context is canceled: the umbrella's own exec of
// a Tier-2 backend binary (exec.go's runInvoke), the backend binary's own
// per-request context (serve.go's serveLoop, which every handler's own
// gh/git/bd exec.CommandContext calls inherit transitively), and a value
// every backend's own gh/git/bd wrapper documents itself as using for the
// same purpose.
//
// 30s: generous for a normal gh/bd/git call (a handful of HTTPS round
// trips, or a dolt query under ordinary load) to complete comfortably,
// while still short enough that a genuinely hung call — gh stuck on an
// interactive prompt it can never receive input for, bd blocked on a
// wedged dolt server, a grandchild process holding a pipe open — fails
// within a bounded, human-noticeable time instead of hanging the calling
// process forever.
const DefaultExecTimeout = 30 * time.Second

// BackendDeadlineMargin is how much EARLIER than the umbrella's
// DefaultExecTimeout a Tier-2 backend's own per-request deadline fires
// [bead pg2-5dyz2].
//
// Before this margin both deadlines were 30s and started within
// milliseconds of each other (the umbrella arms its deadline, execs the
// backend, and the backend arms its own on startup), so the umbrella's
// SIGKILL of the backend won the race: the backend never got to report
// "deadline exceeded", wrote no event-log row, and the caller saw only a
// bare "signal: killed" (67 such source failures on 2026-10-06). With the
// backend's deadline this much earlier, the backend normally answers first
// with a structured error naming op, args and elapsed time, and the
// umbrella's kill is left as a backstop for a backend that cannot even do
// that (wedged before or after its handler).
//
// 5s: equal to DefaultWaitDelay, which is the longest a killed handler child
// can still hold the backend's pipes before Go force-closes them, so the
// backend's own error is written before the umbrella's deadline in the
// ordinary case, while still leaving the backend 25s of the old 30s budget.
const BackendDeadlineMargin = 5 * time.Second

// DefaultBackendTimeout is the deadline a Tier-2 backend's own per-request
// context carries (serve.go's serveLoop): DefaultExecTimeout minus
// BackendDeadlineMargin.
const DefaultBackendTimeout = DefaultExecTimeout - BackendDeadlineMargin

// execTimeout is the deadline runInvoke (exec.go) applies to the umbrella's
// exec of a backend. It starts at DefaultExecTimeout; tests swap it to a
// short value (the same swappable-var pattern exec.go's own execCmdFactory
// already uses) so a hang-and-get-killed test proves the mechanism without
// waiting out the real 30s production value.
var execTimeout = DefaultExecTimeout

// backendTimeout is the deadline serveLoop (serve.go) applies to the
// backend's own per-request context. It starts at DefaultBackendTimeout,
// deliberately SHORTER than execTimeout (see BackendDeadlineMargin); tests
// swap it the same way they swap execTimeout.
var backendTimeout = DefaultBackendTimeout

// MaxSummarizedArgsBytes caps how much of a request's args JSON is folded
// into a deadline error or an event-log row by SummarizeArgs: enough to
// identify a query or an id list, small enough that one pathological request
// (a long comment body) cannot bloat a log line.
const MaxSummarizedArgsBytes = 256

// SummarizeArgs renders a request's args for a diagnostic message: the raw
// JSON compacted onto one line and capped at MaxSummarizedArgsBytes on a rune
// boundary, with a marker noting how many bytes were dropped. An empty or
// null args renders as "{}" so the caller never prints a bare "args=".
func SummarizeArgs(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) == 0 || json.Compact(&buf, raw) != nil {
		buf.Reset()
		buf.WriteString(strings.TrimSpace(string(raw)))
	}
	s := buf.String()
	if s == "" || s == "null" {
		return "{}"
	}
	if len(s) <= MaxSummarizedArgsBytes {
		return s
	}
	cut := MaxSummarizedArgsBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s...[+%d bytes]", s[:cut], len(s)-cut)
}

// DefaultWaitDelay is the exec.Cmd.WaitDelay (Go 1.20+) every exec.Cmd this
// module and its sibling backends' own gh/git/bd wrappers set.
//
// WaitDelay is orthogonal to a context deadline/DefaultExecTimeout: ctx
// cancellation only asks the OS to kill the DIRECT child process (that is
// exec.CommandContext's default Cancel behavior) — it says nothing about
// how long Cmd.Wait then blocks copying that child's stdout/stderr pipes to
// completion. A grandchild process that inherited those same pipe file
// descriptors (a classic double-fork/daemonizing pattern — exactly the
// "grandchild process holding stdout open" scenario this bead's own
// description names) can keep the WRITE end of the pipe open even after
// the direct child has been killed, and without WaitDelay, Wait() then
// blocks forever waiting for an EOF that will never come, because nothing
// else caused it to be forcibly closed.
//
// WaitDelay bounds exactly that residual wait: once it elapses (timed from
// process exit, or from ctx becoming Done, whichever governs), Go forcibly
// closes the pipes so Wait can return.
//
// 5s: short, deliberately much shorter than DefaultExecTimeout, because by
// the time WaitDelay's own clock starts the command has already exited or
// been killed — this only needs to cover ordinary pipe-flush latency for a
// well-behaved child, not another full command's worth of work.
const DefaultWaitDelay = 5 * time.Second

// MaxFoldedOutputBytes caps how much of a child process's captured
// stderr/stdout is folded verbatim into an error message or a
// sources[].reason string: exec.go's runInvoke (the umbrella's own fold of
// a backend binary's stderr/stdout), and every backend's own gh/git/bd
// Run/RunStdin/Token error path that folds in that child's stderr.
//
// 64KiB: comfortably larger than any real gh/git/bd error message (those
// are ordinarily a single line to a short paragraph — bytes, not
// kilobytes), while still bounding the worst case for a runaway or
// unexpectedly verbose child to a small, fixed amount rather than the
// unbounded amount it actually produced. This module had no pre-existing
// size-cap convention to match against (a repo-wide search at authoring
// time, 2026-09-06, found none for a JSON payload or captured process
// output), so this is a fresh, round power-of-two pick rather than a reuse
// of an existing scale.
const MaxFoldedOutputBytes = 64 * 1024

// TruncateForFold trims leading/trailing whitespace from b (matching every
// existing fold call site's own bytes.TrimSpace/strings.TrimSpace
// convention, so the cap is spent on content rather than incidental
// newlines) and returns it as a string capped to at most
// MaxFoldedOutputBytes, with a trailing marker noting how many bytes were
// dropped when truncation occurred. The cut point is backed off to the
// nearest rune boundary so a multi-byte UTF-8 sequence split by the byte
// cap never produces an invalid/mangled tail.
func TruncateForFold(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= MaxFoldedOutputBytes {
		return s
	}
	cut := MaxFoldedOutputBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s... [truncated %d of %d bytes]", s[:cut], len(s)-cut, len(s))
}
