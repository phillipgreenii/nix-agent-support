package main

import (
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
// EXIT CODES follow the CALLBACK contract (0 ok / 1 error / 2 busy), not the
// operator convention where 2 means a usage error. A usage error here therefore
// exits 1, deliberately: 2 is the common contract's pre-accept BUSY signal, and a
// source that read 2 as "busy, re-offer later" when the real fault was a bad flag
// would silently drop events.
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
		return conformance.ExitError
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "ingest-event: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "ingest-event takes its request as JSON on stdin, never as arguments")
		return conformance.ExitError
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
// caller sees exactly the JSON the core produced plus its coarse exit code.
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
	if len(reply) > 0 {
		fmt.Fprintln(stdout, string(reply))
	}
	return code
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
