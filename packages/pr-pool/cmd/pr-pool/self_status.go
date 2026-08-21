package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
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
func runSelfStatus(args []string) int {
	fs := flag.NewFlagSet("self-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (from the core-issued callback)")
	token := fs.String("token", "", "auth token for the running core (from the core-issued callback)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "self-status:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "self-status: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "self-status takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandSelfStatus, err)
		return conformance.ExitError
	}
	request, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-status: read request from stdin:", err)
		return conformance.ExitError
	}
	return callCore(os.Stdout, os.Stderr, ref, core.SubcommandSelfStatus, request)
}
