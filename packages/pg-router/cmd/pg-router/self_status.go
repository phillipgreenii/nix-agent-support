package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
)

// runSelfStatus implements the `self-status` INTF-CLI callback subcommand: any
// registered participant — source, handler, monitor, or storage — pushes a
// report about its OWN health (healthy/degraded/unavailable), independent of
// any per-item outcome (interfaces.md "Self-status"). It reads the JSON request
// from STDIN, forwards it to the running core over the socket, writes the
// core's JSON reply to STDOUT, and exits with the core's coarse exit code — the
// CLI transport contract, verbatim, matching runIngestEvent.
//
// This is NOT an operator subcommand: the core hands EVERY participant this
// callback command with `--socket` and `--token` already baked in at
// registration (Service.Register / core.go), and the participant just runs it.
// Unlike `ingest-event` (a source's alone), self-status is common to every
// participant kind — interfaces.md's common manager contract, not one
// interface's own concern.
//
// EXIT CODES are the common contract's, and they are the SAME here as on every
// other subcommand: 0 ok, 2 usage, 9 the pre-accept BUSY decline the core may
// relay, 1 anything else (ADR 0042's Decision).
//
// When no core can be located it FAILS with a "no running core" diagnostic and
// exits 1. It never starts one (ADR 0036 / core.ErrNoRunningCore).
//
// The reply callCore relays here is discriminated against the protocol
// error envelope BEFORE the verb's own reply schema (register row bead
// pg2-o9r6a; Task 3.8 Binding decisions, Step 7) — callCore's own doc
// explains why that check lives at its ONE call site rather than being
// duplicated in every caller
// (see TestCallCore_SelfStatus_DiscriminatesErrorBeforeReplySchema).
func runSelfStatus(args []string) int {
	return runSelfStatusIO(args, os.Stdin, os.Stdout, os.Stderr)
}

// runSelfStatusIO is runSelfStatus with its streams injected, so a test can
// drive the real subcommand against a real core without touching the process's
// stdio.
func runSelfStatusIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("self-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (from the core-issued callback)")
	token := fs.String("token", "", "auth token for the running core (from the core-issued callback)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stdout, helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(stderr, "self-status:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "self-status: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(stderr, "self-status takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(stderr, core.SubcommandSelfStatus, err)
		return conformance.ExitError
	}
	request, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "self-status: read request from stdin:", err)
		return conformance.ExitError
	}
	if len(bytes.TrimSpace(request)) == 0 {
		// An empty stdin is not a request: the core's schema wants an object
		// (it would answer "expected object, got null"). The fix is here, in
		// the CLI refusing to forward nothing, not in the schema accepting null:
		// a self-status push is only meaningful for a named participant.
		fmt.Fprintln(stderr, "self-status: no request on stdin; this is a participant callback and needs a JSON object on stdin,")
		fmt.Fprintln(stderr, `  e.g. {"schemaVersion":"1","id":"<tracking id>","participantId":"<id>","self":"healthy|degraded|unavailable"}`)
		return conformance.ExitUsage
	}
	return callCore(stdout, stderr, ref, core.SubcommandSelfStatus, request)
}
