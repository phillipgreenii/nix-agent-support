package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// runDispatch implements the `dispatch` INTF-HANDLER subcommand for a command
// role: the request's role object names the role and its [role.config], which
// is decoded strictly; the argv is rendered, run once in its own process group
// under the payload env, and the outcome is mapped to the reply and exit code
// exactly as pg-router-ccpool-handler maps a command role today (success is
// exit 0, exit 9 is a busy decline with reason command-busy, any other failure
// an error reply carrying the stderr tail).
//
// Handler-wide settings (RepoRoot, WorktreeDir, SelfLogin for the argv
// templates, and the event log directory) come from the same launch config JSON
// the ccpool handler reads: --config, or PG_ROUTER_CCPOOL_HANDLER_CONFIG. The
// env var name is reused so one wrapper and one config file serve both
// adapters.
func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
	if exit, done := parseFlags("dispatch", fs, args); done {
		return exit
	}
	// The daemon that spawned this adapter MAY exit while the command is still
	// running, which closes the read end of our stdout pipe: the one reply write
	// then fails with EPIPE (logged by wire.WriteReply) instead of killing the
	// process by SIGPIPE. Notify, NOT signal.Ignore: an ignored disposition is
	// inherited across exec, so the payload would run with SIGPIPE ignored.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	// A TERM or INT cancels the dispatch context, which kills the payload's
	// whole process group (query.OSCommander), so a stopped adapter leaves no
	// orphan behind.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return serveDispatch(ctx, os.Stdin, os.Stdout, *cfgPath, dispatchlib.BuildDeps)
}

// serveDispatch is runDispatch's testable core: ctx is the dispatch context
// (cancel it to cancel the payload) and mkDeps builds the executor seams, so a
// test keeps bd and the event log off the machine.
func serveDispatch(ctx context.Context, stdin io.Reader, stdout io.Writer, cfgPath string, mkDeps func(config.Config, roles.Role) executor.Deps) int {
	raw, ro, exit, ok := readRequest("dispatch", "handler.dispatch", stdin, stdout)
	if !ok {
		return exit
	}
	// Check already proved raw decodes and matches handler.dispatch's schema, so
	// this second Unmarshal (into the typed shape) cannot fail.
	var req wire.DispatchRequest
	_ = json.Unmarshal(raw, &req)

	role, problems := decodeRole(ro)
	if len(problems) > 0 {
		msg := fmt.Sprintf("role %q: invalid config: %s", ro.Name, strings.Join(problems, "; "))
		fmt.Fprintln(os.Stderr, "dispatch:", msg)
		wire.WriteErrorReply(stdout, msg)
		return conformance.ExitError
	}
	cfg, err := rolefile.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		wire.WriteErrorReply(stdout, err.Error())
		return conformance.ExitError
	}

	dctx := executor.DispatchContext{Role: role, Item: dispatchlib.ItemFromPayload(req.Event.Payload), EventID: req.Event.ID}
	deps := mkDeps(cfg, role)
	// The event payload's metadata is a snapshot from enqueue time: re-read the
	// bead's CURRENT metadata so the rendered argv reflects the bead as it is at
	// dispatch time (every role gets this today, pg2-1pt7r). Best effort: a bd
	// failure is logged and the payload's metadata is used.
	dctx.Item, _ = executor.RefreshItemIssue(ctx, deps.BD, dctx.Item)

	result, err := executor.NewCommand().Dispatch(ctx, dctx, deps)
	if decline, busy := wire.BusyDeclineFor(err); busy {
		// Pre-accept busy decline (INV-CONC-1, DEC-WIRE-1 exit 9): the core
		// re-offers the event with backoff. The reason (command-busy) and kind
		// (busy) are a handler-to-alert contract and stay as they are.
		wire.DeclineAndReply(stdout, deps.Log, role, dctx.Item.ID, decline)
		return conformance.ExitBusy
	}
	if err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	// outcome is an opaque STRING on the wire; result.Fields() is the structured
	// shape the core stores uninterpreted, JSON-encoded into it.
	outcomeJSON, err := json.Marshal(result.Fields())
	if err != nil {
		wire.WriteErrorReply(stdout, "encode outcome: "+err.Error())
		return conformance.ExitError
	}
	wire.WriteReply(stdout, wire.OutcomeReply(req.ID, string(outcomeJSON), result.StartedAt))
	return conformance.ExitOK
}
