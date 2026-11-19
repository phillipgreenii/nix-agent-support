package main

import (
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
)

// recordSweepPass logs one housekeeping pass's wall time and appends a sweep_pass
// row to events.jsonl (role, pass, elapsed_ms, found). who names the caller that
// ran the pass: "dispatch" for the passes a dispatch runs on demand (its role's
// orphans, the capacity-pressure closed-bead reconcile) and "janitor" for the
// scheduled janitor.
// Best effort and behavior-neutral: a nil or failing event log is ignored.
func recordSweepPass(who, role, pass string, found int, elapsed time.Duration, deps executor.Deps) {
	slog.Info("sweeps done", "who", who, "role", role, "pass", pass, "elapsed", elapsed, "found", found)
	if deps.Log != nil {
		_ = deps.Log.Emit("info", "sweep_pass", "sweep pass timed", map[string]any{
			"who":        who,
			"role":       role,
			"pass":       pass,
			"elapsed_ms": elapsed.Milliseconds(),
			"found":      found,
		})
	}
}
