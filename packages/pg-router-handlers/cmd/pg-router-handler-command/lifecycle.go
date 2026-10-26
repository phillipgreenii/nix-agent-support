package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runLifecycle implements `postStartup` and `preShutdown`. The command adapter
// holds no sessions, no state to set up and nothing to sweep (a payload is a
// plain subprocess that the dispatch call itself owns until it exits), so both
// are no-op replies that satisfy the schema. The request is still read and
// checked, role object included, so a malformed call is an error and not a
// silent success.
func runLifecycle(name string, args []string) int {
	if exit, done := noArgs(name, args); done {
		return exit
	}
	return serveLifecycle(name, os.Stdin, os.Stdout)
}

// serveLifecycle is runLifecycle's testable core.
func serveLifecycle(name string, stdin io.Reader, stdout io.Writer) int {
	raw, _, exit, ok := readRequest(name, "handler."+name, stdin, stdout)
	if !ok {
		return exit
	}
	var req wire.LifecycleRequest
	_ = json.Unmarshal(raw, &req)
	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}
