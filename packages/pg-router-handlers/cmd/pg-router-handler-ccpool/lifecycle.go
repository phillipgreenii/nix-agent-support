package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// loadRoleAndConfig decodes the request's role strictly and loads the
// handler-wide launch config, then applies the checks that need both and the
// pool-wide budget overlay. It is the preparation dispatch and postStartup share;
// the error is the message the caller reports.
func loadRoleAndConfig(ro roleObject, cfgPath string) (roles.Role, config.Config, error) {
	role, problems := decodeRole(ro)
	if len(problems) > 0 {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %q: invalid config: %s", ro.Name, strings.Join(problems, "; "))
	}
	cfg, err := rolefile.LoadConfig(cfgPath)
	if err != nil {
		return roles.Role{}, config.Config{}, err
	}
	// The bounds that depend on the handler-wide config (poll interval, quiet
	// window maximum) can only be checked here, where both are loaded.
	if err := cfg.ValidateQuietWindowOverride(role.CCPool.WorktreeQuietWindow); err != nil {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %s: %v", role.Name, err)
	}
	if err := cfg.ValidateNeedsInputStall(role.CCPool.NeedsInputStall); err != nil {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %s: needsInputStall: %v", role.Name, err)
	}
	rolefile.OverlayBudgetThresholds(role, cfg)
	return role, cfg, nil
}

// failLifecycle writes msg to stderr and as an error reply, and returns
// conformance.ExitError.
func failLifecycle(name string, stdout io.Writer, msg string) int {
	fmt.Fprintln(os.Stderr, name+":", msg)
	wire.WriteErrorReply(stdout, msg)
	return conformance.ExitError
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
	if exit, done := parseFlags("postStartup", fs, args); done {
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
	raw, ro, exit, ok := readRequest("postStartup", "handler.postStartup", stdin, stdout)
	if !ok {
		return exit
	}
	var req wire.LifecycleRequest
	_ = json.Unmarshal(raw, &req)

	_, cfg, err := loadRoleAndConfig(ro, cfgPath)
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
	return conformance.ExitOK
}

// kickJanitorFor writes the role as a role file under the handler state
// directory (a stable per-role name, overwritten each boot, so nothing
// accumulates) and starts the janitor on it.
func kickJanitorFor(cfg config.Config, ro roleObject, cfgPath string, kick janitorKick) error {
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
	cmd := exec.Command(exe, "janitor", "--config", cfgPath, "--role-file", roleFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
