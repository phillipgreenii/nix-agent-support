package main

import (
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/handlers/rolefile"
	"github.com/phillipgreenii/pg-router/internal/handlers/wire"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runPreShutdown implements the `preShutdown` INTF-HANDLER subcommand. It
// answers at once and touches nothing: no ccpool call, no bd call, no git
// call, no launch config read, no spawned process.
//
// Core calls it at daemon shutdown, once per distinct handler. It used to run
// a global sweep that listed every session, looked up beads and closed
// sessions and worktrees, which was the largest fixed cost of a stop with
// nothing in flight and ate the launchd ExitTimeOut budget. Nothing is lost by
// dropping it: the same passes (the purge_pending retry, the closed-bead
// reconcile, the default-pool stale pass, the orphan reconcile) run on the
// scheduled janitor (INV-CCH-41) and on dispatch, and a session spared at
// shutdown is already outlived by the daemon. A detached janitor run from here
// was rejected: the janitor is single-flight and scheduled, so a spawn would
// only race the next scheduled run, and a child that outlives the daemon is
// the shape the shutdown budget exists to avoid.
//
// The request still carries the role object like every other request and is
// rejected without a usable one; the role is not decoded.
func runPreShutdown(args []string) int {
	fs := flag.NewFlagSet("preShutdown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// --config is still accepted (the core passes the handler's launch flags to
	// every subcommand) but is not read.
	_ = fs.String("config", os.Getenv(rolefile.EnvConfig), "unused: preShutdown reads no launch config")
	if exit, done := rolefile.ParseFlags(helpText, "preShutdown", fs, args); done {
		return exit
	}
	return servePreShutdown(os.Stdin, os.Stdout)
}

// servePreShutdown is runPreShutdown's testable core. A request that fails the
// schema or has no usable role gets an error reply; any other request gets an
// ok reply with no side effect.
func servePreShutdown(stdin io.Reader, stdout io.Writer) int {
	raw, _, exit, ok := rolefile.ReadRequest("preShutdown", "handler.preShutdown", stdin, stdout)
	if !ok {
		return exit
	}
	var req wire.LifecycleRequest
	_ = json.Unmarshal(raw, &req)
	slog.Info("preShutdown: nothing to do; session cleanup runs on the janitor")
	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}
