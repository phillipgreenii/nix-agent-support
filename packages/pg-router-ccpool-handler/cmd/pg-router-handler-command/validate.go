package main

import (
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runValidate implements the optional `validate` INTF-HANDLER subcommand: the
// adapter's ONE validation entry point, run by its nix module on every rendered
// role config during the build and called by pg-router doctor and at daemon
// startup. It reads the request's role object and decodes the role's config
// with the very decoder dispatch uses, so validate and dispatch cannot
// disagree. It runs no payload and touches no tracker.
//
// Exit 0 with a reply means "ran" (an empty problems list means valid, and
// problems are listed, never an exit code). A request without a usable role is
// an error reply with conformance.ExitUsage.
func runValidate(args []string) int {
	if exit, done := noArgs("validate", args); done {
		return exit
	}
	return serveValidate(os.Stdin, os.Stdout)
}

// serveValidate is runValidate's testable core.
func serveValidate(stdin io.Reader, stdout io.Writer) int {
	_, ro, exit, ok := readRequest("validate", "handler.validate", stdin, stdout)
	if !ok {
		return exit
	}
	_, problems := decodeRole(ro)
	if problems == nil {
		problems = []string{}
	}
	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"problems":      problems,
		"warnings":      []string{},
	})
	return conformance.ExitOK
}
