package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
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
// The old binary's postStartup was a no-op because its dispatch carried the
// reconcile; sessions outlive daemon restarts (ADR 0085), so this adapter runs
// the post-startup reconcile here: the same sweeps a dispatch of the role runs
// before its executor (runSweeps), so an orphan or a leaked worktree the
// previous daemon left behind is reclaimed at boot instead of waiting for the
// role's next event.
//
// The request carries the role object; its config is decoded strictly, as a
// dispatch does, because the sweeps need the role's pool, isolation and budgets.
// --config (or PG_ROUTER_CCPOOL_HANDLER_CONFIG) names the handler-wide launch
// config JSON; the old --role-config is not read.
func runPostStartup(args []string) int {
	fs := flag.NewFlagSet("postStartup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
	if exit, done := parseFlags("postStartup", fs, args); done {
		return exit
	}
	return servePostStartup(context.Background(), os.Stdin, os.Stdout, *cfgPath, dispatchlib.BuildDeps)
}

// servePostStartup is runPostStartup's testable core: ctx cancels the sweeps and
// mkDeps builds the executor seams, so a test keeps bd and ccpool off the
// machine. A sweep never fails the call (every pass is best effort), so the reply
// is `ok` once the request and the role's config are valid.
func servePostStartup(ctx context.Context, stdin io.Reader, stdout io.Writer, cfgPath string, mkDeps func(config.Config, roles.Role) executor.Deps) int {
	raw, ro, exit, ok := readRequest("postStartup", "handler.postStartup", stdin, stdout)
	if !ok {
		return exit
	}
	var req wire.LifecycleRequest
	_ = json.Unmarshal(raw, &req)

	role, cfg, err := loadRoleAndConfig(ro, cfgPath)
	if err != nil {
		return failLifecycle("postStartup", stdout, err.Error())
	}
	runSweeps(ctx, "postStartup", role, cfg, mkDeps(cfg, role))

	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}
