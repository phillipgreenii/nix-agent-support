// Package orchestrator is pr-pool's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Completion is bead-status-based; ccpool state is liveness only.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

type Orchestrator struct {
	CC    ccpool.Runner
	BD    beads.Runner
	Reg   roles.Registry
	Cfg   config.Config
	sleep func(time.Duration) // injectable for instant tests; nil ⇒ time.Sleep
}

func (o *Orchestrator) nap(d time.Duration) {
	if o.sleep != nil {
		o.sleep(d)
		return
	}
	time.Sleep(d)
}

// DrainOnce runs one pass: gate check, discover, drain each role up to its cap,
// teardown all pr-pool sessions. Returns nil even when individual beads fail
// (failures are recorded on the beads via OnFailure), matching the bash.
func (o *Orchestrator) DrainOnce(ctx context.Context, selfLogin string) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil // NOTE: gated exit does NOT teardown (no sessions were created)
	}
	dispatches, err := discover.Discover(ctx, o.BD, o.Reg, selfLogin)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	o.drain(ctx, o.Reg.Feedback, dispatches)
	o.drain(ctx, o.Reg.Worker, dispatches)
	o.teardownAll(ctx)
	return nil
}

func (o *Orchestrator) drain(ctx context.Context, role roles.Role, all []discover.Dispatch) {
	worked := 0
	for _, d := range all {
		if d.Role.Kind != role.Kind {
			continue
		}
		if worked >= role.Cap {
			break
		}
		if err := o.workOne(ctx, d); err != nil {
			slog.Warn("bead flagged", "role", role.Name, "bead", d.BeadID, "err", err)
		} else {
			slog.Info("bead complete", "role", role.Name, "bead", d.BeadID)
		}
		worked++
	}
}

// workOne dispatches a single bead: Ensure a fresh per-bead session, Send the
// nudge (async), then wait for completion. The session is torn down by the
// pass-level teardownAll, not here (so strays are reaped uniformly).
func (o *Orchestrator) workOne(ctx context.Context, d discover.Dispatch) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	env := map[string]string{
		"BEADS_ACTOR":    d.Role.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, name, o.Cfg.RepoRoot, env); err != nil {
		// Could not even create the session. Match the bash (work_one:
		// `ensure_session || return 1`): NO failure action here — the bead was
		// never dispatched, so we do not flag/unclaim it. A transient ccpool
		// launch hiccup must not permanently mark a worker bead `human`.
		return fmt.Errorf("ensure %s: %w", name, err)
	}
	nudge := d.Role.Nudge(d.BeadID, o.Cfg.WorktreeDir)
	if err := o.CC.Send(ctx, name, nudge, ccpool.ModeNoWait); err != nil {
		// J-dispatch-fail: feedback unclaims; worker is left for human inspection.
		if d.Role.Kind == roles.Feedback {
			_ = beads.Unclaim(ctx, o.BD, d.BeadID)
		}
		return fmt.Errorf("send %s: %w", name, err)
	}
	return o.waitDone(ctx, d, name)
}

// waitDone polls the bead status until DoneSignal fires (success) or MAX_WAIT
// elapses / the session dies (failure). On detecting death it re-reads the bead
// status once more before failing (a bead that closed in the same instant the
// session ended is a success). On failure it applies the role's OnFailure.
func (o *Orchestrator) waitDone(ctx context.Context, d discover.Dispatch, name string) error {
	deadline := time.Now().Add(o.Cfg.MaxWait)
	seenClaimed := false
	for time.Now().Before(deadline) {
		status, err := beads.Status(ctx, o.BD, d.BeadID)
		if err != nil {
			return o.fail(ctx, d, fmt.Sprintf("bead status: %v", err))
		}
		if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
			return nil
		}
		if d.Role.Kind == roles.Worker && status == "in_progress" {
			seenClaimed = true
		}
		if !o.live(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			return o.fail(ctx, d, "session exited before completing")
		}
		o.nap(o.Cfg.PollInterval)
	}
	// final status check after the deadline.
	status, _ := beads.Status(ctx, o.BD, d.BeadID)
	if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
		return nil
	}
	return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
}

func (o *Orchestrator) fail(ctx context.Context, d discover.Dispatch, reason string) error {
	_ = complete.OnFailure(ctx, o.BD, d.Role, d.BeadID)
	return fmt.Errorf("%s: %s", d.BeadID, reason)
}

// live reports whether the named session is still alive per ccpool. A session
// that is not Live, or whose store state is "failed", counts as dead. ccpool
// store states done/needs_input (a finished/paused TURN) are normal multi-turn
// operation, NOT death.
func (o *Orchestrator) live(ctx context.Context, name string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume alive; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Live && s.State != ccpool.StateFailed
		}
	}
	return false // absent ⇒ gone
}

// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior). Sessions outside the prefix are left untouched.
func (o *Orchestrator) teardownAll(ctx context.Context) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		slog.Warn("teardown list failed", "err", err)
		return
	}
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, o.Cfg.SessionPrefix) {
			if err := o.CC.Close(ctx, s.Name); err != nil {
				slog.Warn("teardown close failed", "session", s.Name, "err", err)
			}
		}
	}
}

func (o *Orchestrator) gated() bool {
	if o.Cfg.QuotaPaused != "" && fileExists(o.Cfg.QuotaPaused) {
		return true
	}
	if o.Cfg.CICDDown != "" && fileExists(o.Cfg.CICDDown) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
