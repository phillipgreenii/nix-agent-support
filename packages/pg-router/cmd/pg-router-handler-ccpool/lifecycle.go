package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/handlerstate"
	"github.com/phillipgreenii/pg-router/internal/handlers/rolefile"
	"github.com/phillipgreenii/pg-router/internal/handlers/wire"
	"github.com/phillipgreenii/pg-router/schemas"
)

// failLifecycle writes msg to stderr and as an error reply, and returns
// participant.ExitError.
func failLifecycle(name string, stdout io.Writer, msg string) int {
	fmt.Fprintln(os.Stderr, name+":", msg)
	wire.WriteErrorReply(stdout, msg)
	return participant.ExitError
}

// runPostStartup implements the `postStartup` INTF-HANDLER subcommand (pg2-oju6w.15):
// core calls it right after it boots, once per enabled role of this adapter
// (the routing record of pg2-12gr4.10.1 keeps postStartup a per-role fan-out).
//
// It runs NO sweep (INV-CCH-41, ADR 0117, bead pg2-fx5x0l.6): the minutes-long
// boot reconcile used to hold the role's first dispatch behind a router gate, and
// the janitor owns that hygiene now. A dispatch is protected without it: its own
// orphan unit and the capacity-pressure reconcile (admission.go) free a slot a
// stale closed-bead or orphan session holds, and the scheduled janitor's RunAtLoad
// covers boot. launchd does not rerun a job when only the DAEMON restarts, so
// postStartup kicks the janitor once, as a detached process it does not wait for;
// the janitor's single-flight lock makes a second or concurrent kick harmless.
//
// The request carries the role object; its config is decoded strictly, as a
// dispatch does. --config (or PG_ROUTER_CCPOOL_HANDLER_CONFIG) names the
// handler-wide launch config JSON. The role is never read from a file: it comes
// from stdin.
func runPostStartup(args []string) int {
	fs := flag.NewFlagSet("postStartup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
	if exit, done := rolefile.ParseFlags(helpText, "postStartup", fs, args); done {
		return exit
	}
	return servePostStartup(os.Stdin, os.Stdout, *cfgPath, kickJanitor)
}

// janitorKick starts a detached janitor run for one role; it never waits for it.
type janitorKick func(cfgPath, roleFile string) error

// servePostStartup is runPostStartup's testable core: kick is the janitor spawn
// seam, so a test keeps real processes off the machine. The reply is `ok` once
// the request and the role's config are valid; a failed kick is logged and never
// fails the call (the scheduled janitor covers it).
func servePostStartup(stdin io.Reader, stdout io.Writer, cfgPath string, kick janitorKick) int {
	raw, ro, exit, ok := rolefile.ReadRequest("postStartup", "handler.postStartup", stdin, stdout)
	if !ok {
		return exit
	}
	var req participant.LifecycleRequest
	_ = json.Unmarshal(raw, &req)

	_, cfg, err := rolefile.LoadRoleAndConfig(ro, cfgPath)
	if err != nil {
		return failLifecycle("postStartup", stdout, err.Error())
	}
	if err := kickJanitorFor(cfg, ro, cfgPath, kick); err != nil {
		slog.Warn("postStartup: janitor kick failed; the scheduled janitor still runs", "role", ro.Name, "err", err)
	}

	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return participant.ExitOK
}

// kickJanitorFor writes the role as a role file under the handler state
// directory (a stable per-role name, overwritten each boot, so nothing
// accumulates) and starts the janitor on it.
func kickJanitorFor(cfg config.Config, ro rolefile.RoleObject, cfgPath string, kick janitorKick) error {
	body, err := json.Marshal(map[string]any{"role": map[string]any{"name": ro.Name, "config": ro.Config}})
	if err != nil {
		return err
	}
	dir := handlerstate.Dir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	roleFile := filepath.Join(dir, "janitor-kick-"+ro.Name+".role.json")
	if err := os.WriteFile(roleFile, body, 0o644); err != nil {
		return err
	}
	return kick(cfgPath, roleFile)
}

// kickJanitor is the production janitorKick: it re-executes this binary as
// `janitor --config <cfg> --role-file <file>` in its own process group, with no
// stdio, and releases it without waiting.
func kickJanitor(cfgPath, roleFile string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Not routed through x/cmdrun: the janitor is detached on purpose. cmdrun.Exec
	// waits for the child and captures its pipes, and this call must return at
	// once, leave the child running in its own process group after this process
	// exits, and never kill it (no context, no timeout).
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	p, err := os.StartProcess(exe,
		[]string{exe, "janitor", "--config", cfgPath, "--role-file", roleFile},
		&os.ProcAttr{
			Env: os.Environ(), Files: []*os.File{null, null, null},
			Sys: &syscall.SysProcAttr{Setpgid: true},
		})
	if err != nil {
		return err
	}
	return p.Release()
}
