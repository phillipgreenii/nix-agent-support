package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/corecall"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runRegister implements the `register` subcommand (interfaces.md's
// "Lifecycle"; DEC-WIRE-1's illustrative register message): it sends
// {schemaVersion, id, kind: "handler", self} to the core over the injected
// socket at process start, and relays the core's cli.register-reply (or
// protocol-level cli.error) plus its coarse exit code.
func runRegister(args []string) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "path to pg-router's core socket (or "+corecall.EnvSocket+")")
	token := fs.String("token", "", "auth token for pg-router's core (or "+corecall.EnvToken+")")
	id := fs.String("id", "", "this participant's own chosen registration id (required)")
	self := fs.String("self", "healthy", "this participant's own health: healthy|degraded|unavailable")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "register:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "register: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}
	if *id == "" {
		fmt.Fprintln(os.Stderr, "register: --id is required")
		return conformance.ExitUsage
	}
	ref, err := corecall.ResolveRef(*socket, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "register:", err)
		return conformance.ExitError
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            *id,
		"kind":          "handler",
		"self":          *self,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "register: build request:", err)
		return conformance.ExitError
	}
	return corecall.Relay(os.Stdout, os.Stderr, ref, "register", payload)
}

// runSelfStatus implements the `self-status` INTF-CLI callback subcommand,
// common to every participant kind (interfaces.md's "Self-status"). It
// reads the cli.self-status request as JSON on stdin — the core hands EVERY
// registered participant this command with --socket/--token already baked
// in, so this process never composes the request itself, mirroring
// cmd/pg-router/self_status.go's own reference implementation exactly.
func runSelfStatus(args []string) int {
	fs := flag.NewFlagSet("self-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "path to pg-router's core socket (or "+corecall.EnvSocket+")")
	token := fs.String("token", "", "auth token for pg-router's core (or "+corecall.EnvToken+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "self-status:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "self-status: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "self-status takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}
	ref, err := corecall.ResolveRef(*socket, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-status:", err)
		return conformance.ExitError
	}
	request, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-status: read request from stdin:", err)
		return conformance.ExitError
	}
	return corecall.Relay(os.Stdout, os.Stderr, ref, "self-status", request)
}
