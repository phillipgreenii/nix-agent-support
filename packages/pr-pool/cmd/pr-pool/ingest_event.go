package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
)

// Environment names for an INJECTED core reference. interfaces.md allows the CLI
// to find the core "via an injected socket path (env/arg)"; the flags win, these
// are the env form.
const (
	envSocket = "PR_POOL_SOCKET"
	envToken  = "PR_POOL_TOKEN"
)

// runIngestEvent implements the `ingest-event` INTF-CLI callback subcommand: a
// push source (or a pull source's deferred query reply) hands the core one or more
// events. It reads the JSON request from STDIN, forwards it to the running core
// over the socket, writes the core's JSON reply to STDOUT, and exits with the
// core's coarse exit code — the CLI transport contract, verbatim.
//
// This is NOT an operator subcommand: the core hands a participant one callback
// command with `--socket` and `--token` already baked in, and the participant just
// runs it (interfaces.md "Callback"). `push-inject` is the operator-facing front
// door to the same core-side enqueue.
//
// EXIT CODES are the common contract's, and they are the SAME here as on every
// operator subcommand: 0 ok, 2 usage, 9 the pre-accept BUSY decline the core may
// relay, 1 anything else (ADR 0042's Decision). Usage no longer collides with
// BUSY, so a source can distinguish "your invocation was wrong" from "re-offer
// later" — reading one as the other is what would silently drop events.
//
// When no core can be located it FAILS with a "no running core" diagnostic and
// exits 1. It never starts one (ADR 0036 / core.ErrNoRunningCore).
func runIngestEvent(args []string) int {
	fs := flag.NewFlagSet("ingest-event", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (from the core-issued callback)")
	token := fs.String("token", "", "auth token for the running core (from the core-issued callback)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "ingest-event:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "ingest-event: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "ingest-event takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandIngestEvent, err)
		return conformance.ExitError
	}
	request, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingest-event: read request from stdin:", err)
		return conformance.ExitError
	}
	return callCore(os.Stdout, os.Stderr, ref, core.SubcommandIngestEvent, request)
}

// locateCore resolves the running core the way interfaces.md's "Locating the core"
// describes: an INJECTED socket (flag, then env — injectedRef) wins; otherwise
// discover the running socket service under the log dir. Never auto-starts
// (ADR 0036).
func locateCore(socket, token string) (core.Ref, error) {
	socket, token = injectedRef(socket, token)
	if socket != "" {
		return core.Ref{Socket: socket, Token: token}, nil
	}
	return core.Discover(config.LogDir())
}

// callCore forwards one request to the core and relays the reply verbatim, so the
// caller sees exactly the JSON the core produced plus its coarse exit code — the
// manager-callback wire contract (interfaces.md), which this function never
// alters.
//
// It ALSO discriminates the reply (register row bead pg2-o9r6a; Task 3.8
// Binding decisions, Step 7): before this, ingest-event and self-status
// relayed whatever bytes came back with NO validation against the protocol
// error envelope at all, so a caller reading only the exit code could not
// tell a genuine protocol-level refusal (bad token, core not `started`) from
// the verb's own outcome schema. discriminateReply's warning is
// OBSERVABILITY only — it changes neither the relayed stdout body nor the
// exit code, since a manager callback's contract is exactly those two
// things.
func callCore(stdout, stderr io.Writer, ref core.Ref, subcommand string, request []byte) int {
	client, err := core.Dial(ref)
	if err != nil {
		reportNoCore(stderr, subcommand, err)
		return conformance.ExitError
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(subcommand, request)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", subcommand, err)
		return conformance.ExitError
	}
	if diagErr := discriminateReply(reply, replySchemaFor(subcommand), nil); diagErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", subcommand, diagErr)
	}
	if len(reply) > 0 {
		fmt.Fprintln(stdout, string(reply))
	}
	return code
}

// replySchemaFor maps a manager-callback subcommand to the reply schema
// discriminateReply validates a NON-error reply against.
func replySchemaFor(subcommand string) string {
	switch subcommand {
	case core.SubcommandIngestEvent:
		return core.IngestReplySchema
	case core.SubcommandSelfStatus:
		return core.SelfStatusReplySchema
	case core.SubcommandStatus:
		return core.StatusReplySchema
	default:
		return ""
	}
}

// discriminateReply checks reply against the protocol-level error envelope
// (cli.error) BEFORE validating it against the verb's own reply schema —
// register row bead pg2-o9r6a's actual content (Task 3.8 Binding decisions,
// Step 7): creating the cli.error schema alone does not close the gap, since
// every CLI-facing client that reads a core reply must apply this ordering
// itself, not merely have the schema artifact available.
//
// A body-less reply (the legal busy shape, exit 9) is not discriminated at
// all — there is nothing to check. When out is non-nil and reply matches
// neither shape as an error, reply is decoded into it (the caller's typed
// reply value); a nil out is for a caller that only wants the validation
// (callCore's raw relay never decodes the reply itself).
func discriminateReply(reply []byte, replySchema string, out any) error {
	if len(reply) == 0 {
		return nil
	}
	if conformance.CheckBytes(core.ErrorReplySchema, reply) == nil {
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(reply, &errBody); err == nil {
			return fmt.Errorf("core refused: %s", errBody.Error)
		}
	}
	if replySchema == "" {
		return nil
	}
	if err := conformance.CheckBytes(replySchema, reply); err != nil {
		return fmt.Errorf("core reply is not a valid %s: %w", replySchema, err)
	}
	if out != nil {
		if err := json.Unmarshal(reply, out); err != nil {
			return fmt.Errorf("decode %s reply: %w", replySchema, err)
		}
	}
	return nil
}

// reportNoCore renders a failure from any subcommand that has to reach the core,
// prefixed with that subcommand's name. A "no running core" is reported with the
// remedy (start one) precisely because the CLI will not start one itself — that is
// what keeps "is a core running?" answerable from the exit code (ADR 0036).
//
// It is shared by the callback front door (`ingest-event`) and the operator front
// door (`push-inject`) so the one diagnostic an operator acts on cannot drift
// between them.
func reportNoCore(stderr io.Writer, subcommand string, err error) {
	fmt.Fprintf(stderr, "%s: %v\n", subcommand, err)
	if errors.Is(err, core.ErrNoRunningCore) {
		fmt.Fprintf(stderr, "%s: start the core's socket service first, or pass --socket/--token to name a running core\n", subcommand)
	}
}
