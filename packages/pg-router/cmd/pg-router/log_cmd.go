package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// This file is the operator CLI over the event-queue write-ahead log (bead
// pg2-maxn1, the verb half of pg2-8e0m6):
//
//	pg-router log compact [--dry-run] [--json] [--socket S] [--token T]
//
// WHERE IT RUNS. The running daemon owns an exclusive lock on queue.jsonl
// (eventqueue/lock.go), so a second process must never touch the file:
//
//   - against a RUNNING core (found via --socket/--token, else PG_ROUTER_SOCKET/
//     PG_ROUTER_TOKEN, else discovery under the log dir) the request goes over its
//     socket and the compaction runs inside the daemon (core's log-compact verb);
//   - with NO core running it compacts OFFLINE: a real run takes the log lock
//     itself and is refused (exit 1) if another process holds it, a dry run only
//     reads the file. A core named explicitly by --socket never falls back to the
//     offline path: the operator asked for that daemon.
//
// DRY RUN. Changes nothing - no rename, no temp file, no lock kept, no counter -
// and reports what a real run would do: bytes and records before/after, events
// kept vs dropped, gates kept, the percent of max_log_bytes after, and whether a
// real run would be refused (the log is locked by another process).
//
// A real run that finds no progress possible (the log already holds live state
// only) rewrites nothing and reports "nothing to do".
//
// EXIT CODES: 0 ok (including a dry run, or a real run, that finds nothing to
// do); 1 failure or refusal (log locked by another process, the core refused or
// could not be reached, the compaction failed); 2 usage; 9 busy (the core's
// admission control, which log-compact is not subject to, so in practice unused).

// runLog dispatches `pg-router log (compact)`.
func runLog(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "log: a subcommand is required (compact)")
		return conformance.ExitUsage
	}
	switch args[0] {
	case "compact":
		return runLogCompact(args[1:])
	case "-h", "--help", "help":
		fmt.Println(helpText)
		return exitOK
	}
	fmt.Fprintf(os.Stderr, "log: unknown subcommand %q (want compact)\n", args[0])
	return conformance.ExitUsage
}

// runLogCompact implements `log compact [--dry-run] [--json]`.
func runLogCompact(args []string) int {
	fs, socket, token := gateFlags("log compact")
	dryRun := fs.Bool("dry-run", false, "change nothing; report what a real run would do")
	asJSON := fs.Bool("json", false, "emit the result as one JSON object (the cli.log-compact-reply shape)")
	pos, code, ok := parseGateFlags(fs, "log compact", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "log compact: unexpected argument:", pos[0])
		return conformance.ExitUsage
	}
	return logCompact(os.Stdout, os.Stderr, logCompactOpts{
		dryRun: *dryRun, asJSON: *asJSON, socket: *socket, token: *token,
		logDir: config.LogDir(), limit: offlineLogLimit,
	})
}

// logCompactOpts is runLogCompact's input, so logCompact is testable without the
// process's flags, environment or real log directory.
type logCompactOpts struct {
	dryRun, asJSON bool
	socket, token  string
	// logDir holds queue.jsonl and the daemon's discovery record.
	logDir string
	// limit returns max_log_bytes for the offline path's percentages (0: unknown).
	limit func() int64
}

// offlineLogLimit resolves max_log_bytes the way the daemon would, for the percent
// an offline run reports. An unreadable config is not a reason to refuse to
// compact: the percentages are simply left out.
func offlineLogLimit() int64 {
	// config.Load narrates itself (a WARN when no config.toml exists, an INFO per
	// layer it reads); that is noise on an operator's terminal here, where only
	// the one number matters.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	cfg, err := config.Load()
	if err != nil {
		return 0
	}
	return cfg.MaxLogBytes
}

// logCompact is runLogCompact's testable body: pick the daemon or the offline
// path, run it, render the result.
func logCompact(stdout, stderr io.Writer, o logCompactOpts) int {
	socket, token := injectedRef(o.socket, o.token)
	var ref core.Ref
	haveCore := false
	if socket != "" {
		ref, haveCore = core.Ref{Socket: socket, Token: token}, true
	} else {
		r, err := core.Discover(o.logDir)
		switch {
		case err == nil:
			ref, haveCore = r, true
		case errors.Is(err, core.ErrNoRunningCore):
			// No daemon: fall through to the offline path.
		default:
			reportNoCore(stderr, "log compact", err)
			return conformance.ExitError
		}
	}

	var view core.LogCompactView
	if haveCore {
		req := map[string]any{"schemaVersion": schemas.SchemaVersion}
		if o.dryRun {
			req["dryRun"] = true
		}
		if code := callVerb(stderr, ref, core.SubcommandLogCompact, core.LogCompactReplySchema, req, &view,
			core.CallOptions{CallTimeout: core.LogCompactCallTimeout + 10*time.Second}); code != exitOK {
			return code
		}
	} else {
		v, code := logCompactOffline(stderr, o)
		if code != exitOK {
			return code
		}
		view = v
	}

	if o.asJSON {
		b, err := json.Marshal(view)
		if err != nil { // unreachable: plain scalars
			fmt.Fprintln(stderr, "log compact:", err)
			return exitGeneric
		}
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	renderLogCompact(stdout, view)
	return exitOK
}

// logCompactOffline serves the no-daemon case. It returns the view to render, or
// the exit code to report (a diagnostic already written to stderr).
func logCompactOffline(stderr io.Writer, o logCompactOpts) (core.LogCompactView, int) {
	path := filepath.Join(o.logDir, "queue.jsonl")
	res, err := eventqueue.CompactFileOffline(path, o.dryRun)
	if err != nil {
		var locked *eventqueue.ErrLogLocked
		if errors.As(err, &locked) {
			fmt.Fprintf(stderr, "log compact: %v\n", locked)
			fmt.Fprintf(stderr, "log compact: no running core answered on its socket, so a process that is not serving one holds %s; find it (for example with `lsof %s`) and stop it, then retry\n",
				textsafe.Sanitize(locked.LockPath), textsafe.Sanitize(locked.LockPath))
			return core.LogCompactView{}, conformance.ExitError
		}
		fmt.Fprintf(stderr, "log compact: %v\n", err)
		return core.LogCompactView{}, conformance.ExitError
	}
	var limit int64
	if o.limit != nil {
		limit = o.limit()
	}
	view := core.NewLogCompactView(res, limit, core.ViaOffline)
	if o.dryRun {
		// The dry run took no lock, so say whether a real run would find it held.
		if held, lerr := eventqueue.LogLocked(path); lerr == nil && held {
			view.WouldRefuse = "the log is locked by another pg-router process"
		}
	}
	return view, exitOK
}

// renderLogCompact writes the human summary of one result.
func renderLogCompact(w io.Writer, v core.LogCompactView) {
	where := "via the running daemon"
	if v.Via == core.ViaOffline {
		where = "offline, no daemon running"
	}
	size := func(n int64, pct *float64) string {
		s := fmt.Sprintf("%s (%d bytes)", humanBytes(n), n)
		if pct != nil {
			s += fmt.Sprintf(", %.1f%% of the %s limit", *pct, humanBytes(v.LimitBytes))
		}
		return s
	}
	switch {
	case v.DryRun:
		fmt.Fprintf(w, "pg-router: log compact (dry run, %s) - nothing was changed\n", where)
	case v.Compacted:
		d := ""
		if v.DurationMs != nil {
			d = fmt.Sprintf(", %s", (time.Duration(*v.DurationMs) * time.Millisecond).Round(time.Millisecond))
		}
		fmt.Fprintf(w, "pg-router: log compacted (%s%s)\n", where, d)
	default:
		fmt.Fprintf(w, "pg-router: log compact (%s) - nothing to do\n", where)
	}
	fmt.Fprintf(w, "  before: %s, %d records\n", size(v.BytesBefore, v.PercentBefore), v.RecordsBefore)
	switch {
	case v.DryRun || v.Compacted:
		fmt.Fprintf(w, "  after:  %s, %d records\n", size(v.BytesAfter, v.PercentAfter), v.RecordsAfter)
	default:
		fmt.Fprintln(w, "  the log already holds live state only: no progress is possible")
	}
	fmt.Fprintf(w, "  events: %d kept, %d dropped (evicted); gates kept: %d\n", v.EventsKept, v.EventsDropped, v.GatesKept)
	if v.Torn {
		fmt.Fprintln(w, "  warning: the log has an undecodable line; everything from it on is ignored by a replay and discarded by a compaction")
	}
	if v.DryRun {
		switch {
		case v.WouldRefuse != "":
			fmt.Fprintf(w, "  a real run would be REFUSED (exit 1): %s\n", textsafe.Sanitize(v.WouldRefuse))
		case v.NoProgress:
			fmt.Fprintln(w, "  a real run would do nothing: no progress is possible")
		default:
			fmt.Fprintf(w, "  a real run would shrink the log by %s\n", humanBytes(v.BytesBefore-v.BytesAfter))
		}
	}
}
