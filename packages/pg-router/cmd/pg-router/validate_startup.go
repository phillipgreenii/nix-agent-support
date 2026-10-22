package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// startupValidateTimeout bounds the whole startup validate step (docket
// pg2-12gr4.8): the roles' validate calls run concurrently, so the step waits at
// most this long. Seconds, not minutes: a handler that hangs must not hold up
// startup. A package var so tests can lower it.
var startupValidateTimeout = 10 * time.Second

// validateRolesAtStartup asks each ENABLED role's handler to run its optional
// validate subcommand (design section 5, ruling 31), after bootCore has
// registered the roles and before postStartupAll. It runs on both startup paths
// (the long-running daemon and drain-and-exit).
//
// Curated readings, recorded here rather than in the behavior docs:
//   - A role whose handler reports a non-empty problem list stays REGISTERED and
//     is marked UNAVAILABLE with the problems as its reason (Registry.
//     SetUnavailable). roleListener.Offer then declines it with
//     DeclineUnavailable, its unexpired events are kept, and its postStartup
//     still runs (sessions outlive restarts). There is no alert for it.
//   - "Not supported" (the usage exit, or a client that cannot validate at all)
//     is not a failure: the role stays available.
//   - A Validate error (another exit, an unparsable reply, a timeout) means
//     validation could not run, which is not a verdict: it is logged at WARN and
//     the role stays available, so a transient hiccup does not silently park a
//     role until restart.
//   - Warnings never fail validation; they are logged.
func validateRolesAtStartup(ctx context.Context, o *orchestrator.Orchestrator, cfg config.Config) {
	validator, ok := o.Handler.(wireclient.Validator)
	if !ok || o.Registry == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, startupValidateTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, r := range cfg.Roles {
		if !r.Enabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := validator.Validate(ctx, r)
			switch {
			case err != nil:
				slog.Warn("role validate could not run; the role stays available", "role", r.Name, "err", err)
				return
			case !res.Supported:
				slog.Debug("role handler has no validate subcommand; not supported", "role", r.Name)
				return
			}
			for _, w := range res.Warnings {
				slog.Warn("role validate warning", "role", r.Name, "warning", w)
			}
			if len(res.Problems) == 0 {
				return
			}
			reason := strings.Join(res.Problems, "; ")
			if err := o.Registry.SetUnavailable(r.Name, reason); err != nil {
				slog.Warn("could not mark role unavailable", "role", r.Name, "err", err)
				return
			}
			slog.Warn("role validation failed; the role is registered but unavailable", "role", r.Name, "reason", reason, "problems", len(res.Problems))
		}()
	}
	wg.Wait()
}
