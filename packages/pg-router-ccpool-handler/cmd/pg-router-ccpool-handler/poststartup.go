package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// lifecycleRequest / lifecycleReply mirror
// packages/pg-router/schemas/handler.postStartup{,-reply}.schema.json and
// .../handler.preShutdown{,-reply}.schema.json (pg2-oju6w.15) — both hooks
// share one wire shape (no event, no deferred branch), so one pair of types
// backs both this file's runPostStartup and preshutdown.go's runPreShutdown.
type lifecycleRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
}

// runPostStartup implements the `postStartup` INTF-HANDLER subcommand
// (pg2-oju6w.15): dispatched once per process lifetime, immediately after
// the core's own bootCore succeeds, to every enabled role's registered
// handler participant. Built for symmetry with `preShutdown` (decision #2)
// — this handler has no boot-time setup of its own, so it does nothing but
// parse its flags (the same --role-config/--config pair `dispatch` takes,
// for consistency, though neither is actually read here) and reply success.
// Deliberately touches NO ccpool/beads runner at all — see
// poststartup_test.go's own no-op-contract test.
func runPostStartup(args []string) int {
	fs := flag.NewFlagSet("postStartup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("role-config", os.Getenv(envRoleConfig), "path to this process's own role config JSON (or "+envRoleConfig+")")
	_ = fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "postStartup:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "postStartup: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "postStartup takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}
	return servePostStartup(os.Stdin, os.Stdout)
}

// servePostStartup is runPostStartup's testable core, factored out so a test
// can feed it a request/capture its reply without touching the real
// os.Stdin/os.Stdout — mirrors conformance.Participant.Serve's own
// (stdin, stdout) shape. It touches NO ccpool/beads runner at all (locked in
// by poststartup_test.go's own no-op-contract test): postStartup has no
// boot-time setup to do today, built for symmetry with preShutdown
// (decision #2) alone.
func servePostStartup(stdin io.Reader, stdout io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "postStartup: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.postStartup", v); err != nil {
		writeErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	var req lifecycleRequest
	_ = json.Unmarshal(raw, &req)

	writeReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}
