package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/emit"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// runPushInject implements the `push-inject` INTF-CLI OPERATOR subcommand: it
// injects an arbitrary operator-supplied event into the LIVE core, performing the
// same core-side enqueue as the `ingest-event` manager callback but
// operator-initiated (interfaces.md). The event is durable via the queue and
// delivered at-least-once and deduped like any push event (INV-EVT-*) — no new
// delivery semantics.
//
// It is distinct from `ingest-event` (a manager→core callback, whose socket/token
// the core bakes in) and from `run-role` (a smoke test that tears down).
//
// The event JSON is a POSITIONAL argument, as interfaces.md specifies
// (`push-inject <json>`). Output is human-readable text by default and JSON with
// `--json`.
//
// EXIT CODES: 0 on an accepted injection, 1 on anything else — INCLUDING a usage
// error. 1-for-usage is deliberate and matches `ingest-event`: push-inject reaches
// the core through the very same `ingest-event` transport, whose coarse exit space
// reserves 2 for the common contract's pre-accept BUSY. Minting a second meaning
// for 2 at this boundary would make an operator's 2 ambiguous between "you typed
// it wrong" and "the core declined".
//
// When no core can be located it FAILS with a "no running core" diagnostic and the
// remedy. It NEVER starts one (ADR 0036 / core.ErrNoRunningCore).
func runPushInject(args []string) int {
	fs := flag.NewFlagSet("push-inject", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	asJSON := fs.Bool("json", false, "emit JSON instead of human-readable text")
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "push-inject:", err)
		return conformance.ExitError
	}
	switch {
	case fs.NArg() == 0:
		fmt.Fprintln(os.Stderr, "push-inject: missing event JSON (usage: push-inject [--json] [--socket <path>] [--token <tok>] <json>)")
		return conformance.ExitError
	case fs.NArg() > 1:
		fmt.Fprintln(os.Stderr, "push-inject: unexpected argument:", fs.Arg(1))
		fmt.Fprintln(os.Stderr, "push-inject takes ONE event JSON argument; quote it so the shell keeps it as one word")
		return conformance.ExitError
	}

	sock, tok := injectedRef(*socket, *token)
	loc := emit.Locator{
		InjectedSocket: sock,
		InjectedToken:  tok,
		// Discovery reads the record under the LOG dir only — config.LogDir resolves
		// the state directory alone, so an operator can still reach a running core
		// when the repo-local config.toml is missing or broken.
		Discover: emit.Discoverer(config.LogDir()),
	}
	// SocketEnqueuer, never QueueEnqueuer: the core owns the durable queue in
	// ANOTHER process, and only the socket reaches it. QueueEnqueuer would refuse
	// this ref (emit.ErrWrongEnqueuer) rather than enqueue into a queue that dies
	// with this CLI process.
	return pushInject(os.Stdout, os.Stderr, *asJSON, loc, emit.SocketEnqueuer{}, fs.Arg(0))
}

// pushInject runs the injection against an already-resolved locator and enqueuer,
// so the outcome rendering and the exit code are testable without the process's
// real stdout/stderr, flags or environment.
func pushInject(stdout, stderr io.Writer, asJSON bool, loc emit.Locator, enq emit.Enqueuer, eventJSON string) int {
	res, err := emit.Emit([]byte(eventJSON), loc, enq)
	if err != nil {
		reportNoCore(stderr, "push-inject", err)
		if asJSON {
			writeJSON(stdout, pushInjectReport{SchemaVersion: schemas.SchemaVersion, Accepted: false, Error: err.Error()})
		}
		return conformance.ExitError
	}
	if asJSON {
		writeJSON(stdout, pushInjectReport{
			SchemaVersion: schemas.SchemaVersion,
			Accepted:      true,
			Event: &pushInjectEvent{
				ID:        res.Event.ID,
				Type:      res.Event.Type,
				At:        instantOrEmpty(res.Event.At),
				ExpiresAt: instantOrEmpty(res.Event.ExpiresAt),
			},
			Core: &pushInjectCore{Socket: res.Core.Socket, Discovered: res.Core.Discovered},
		})
	} else {
		fmt.Fprintf(stdout, "push-inject: accepted event %q (type %q%s) by the core at %s%s\n",
			res.Event.ID, res.Event.Type, expiryNote(res.Event), res.Core.Socket, discoveredSuffix(res.Core.Discovered))
	}
	return exitOK
}

// instantOrEmpty renders an OPTIONAL event instant for the `--json` report, or ""
// so `omitempty` drops it. It echoes what the OPERATOR supplied and does not
// substitute a default: the defaults belong to the core's clock at ingest
// (INV-EVT-1), and this CLI is not that clock.
func instantOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// expiryNote spells out, in the human output, WHEN the injected event expires.
// Both `at` and `expiresAt` are optional and both DEFAULT — absent `expiresAt`
// falls back to `at`, and absent `at` to the core's own now at ingest — so an
// event carrying NEITHER is BORN EXPIRED: offered once to every matching handler,
// then dropped (INV-EVT-4). That is the intended best-effort default, but it is
// also the opposite of what "I set no expiry" usually means, so the note says
// which of the three cases this injection actually is rather than leaving the
// operator to infer it.
func expiryNote(e eventqueue.Event) string {
	switch {
	case !e.ExpiresAt.IsZero():
		return ", expires " + e.ExpiresAt.UTC().Format(time.RFC3339Nano)
	case !e.At.IsZero():
		return ", expires at its own `at` stamp " + e.At.UTC().Format(time.RFC3339Nano) + " (no expiresAt given)"
	default:
		return ", born expired: offered once to every matching handler, then dropped"
	}
}

// pushInjectReport is the `--json` output shape. It is NOT one of the INTF
// message schemas: those describe messages between managers and the core, whereas
// this is an operator-facing command report (interfaces.md only requires that
// every operator subcommand "emit JSON with --json").
//
// The auth TOKEN is deliberately absent, from both output modes. It is the only
// thing standing between a socket request and the core's queue, and an operator
// running with --json is precisely the case where the output gets piped into a log.
type pushInjectReport struct {
	SchemaVersion string `json:"schemaVersion"`
	// Accepted reports that the core TOOK the event into its durable queue. It is
	// deliberately not "enqueued": the ingest-event reply folds de-duplication into
	// its accepted count (a still-retained duplicate id IS accepted, INV-EVT-3), so
	// a fresh append and an absorbed re-emit are indistinguishable over the wire.
	Accepted bool             `json:"accepted"`
	Event    *pushInjectEvent `json:"event,omitempty"`
	Core     *pushInjectCore  `json:"core,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// pushInjectEvent echoes the injected event's identity and its OPTIONAL expiry
// instants (RFC3339). Both are `omitempty`: absent means the operator supplied
// nothing there, and the core applied the INV-EVT-1 default against its own clock
// — reporting a value this CLI invented would misattribute it.
type pushInjectEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	At        string `json:"at,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type pushInjectCore struct {
	Socket     string `json:"socket"`
	Discovered bool   `json:"discovered"`
}

// writeJSON emits one compact JSON object plus a newline, so a consumer can read
// the output as a single line.
func writeJSON(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil { // unreachable: the report holds only strings, bools and pointers to those
		fmt.Fprintf(w, "{\"schemaVersion\":%q,\"accepted\":false,\"error\":\"marshal report: %v\"}\n", schemas.SchemaVersion, err)
		return
	}
	fmt.Fprintln(w, string(data))
}

// discoveredSuffix labels how the core was located, so an operator who passed
// --socket can tell that their socket (not discovery) is what was used.
func discoveredSuffix(discovered bool) string {
	if discovered {
		return " (discovered)"
	}
	return " (injected)"
}

// injectedRef resolves an INJECTED core reference: the flags win, else the
// environment. interfaces.md allows the CLI to find the core "via an injected
// socket path (env/arg)"; this is that precedence, shared by every entry point so
// they cannot drift.
//
// PR_POOL_TOKEN is consulted ONLY when the socket also came from the environment:
// a --socket/--token pair travels together (a core-issued callback bakes in both),
// so a flag socket must not silently pick up a token meant for a different core.
func injectedRef(socket, token string) (string, string) {
	if socket == "" {
		socket = os.Getenv(envSocket)
		if socket != "" && token == "" {
			token = os.Getenv(envToken)
		}
	}
	return socket, token
}
