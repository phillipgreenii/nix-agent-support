package main

import (
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// prLaneLine is the "pr lane" line of `pg-desk status` and `pg-desk doctor`
// (PR lane refactor, P0b static form, P3b3 live form).
//
// The lane mode it prints is the configured pr_lane.mode, its one source (the
// router's per-role JSON is not readable by pg-desk); a missing or invalid
// value reads as off. Everything after it is read from the persisted store
// state (the meta keys of internal/lanestate and the never-hydrated set of the
// store): the flip time, the never-hydrated count and its oldest age, the
// residual (only in apply mode: a plan-mode residual is the plan's own action
// list, not a backlog), and the time of the last recorded run. pg-desk does
// not read the run-record file, and the line MUST NOT use "time since the last
// decider apply": that is legitimately large in a quiet window and has no
// source in pg-desk.
//
// The line is information, never a failure. A configuration that cannot be
// loaded reports both modes as unknown rather than guessing off. A store that
// is absent, not at schema 2 or unreadable keeps the configured part and says
// the lane state is unavailable; it is never printed as zeros, which would
// read as a healthy lane.
//
// Format (one line; the segments after "lane <mode>" are the live part):
//
//	pr lane: legacy sync <sync>, lane <mode>, flipped <RFC3339|not yet>,
//	never-hydrated <n> (oldest <age>), residual <n> (oldest <age>)|n/a (...),
//	last run <RFC3339|never>
//
// where <age> is a Go duration (1h2m3s), and a recorded lane mode that differs
// from the configured one adds ", recorded lane mode <m>" before the residual.
func prLaneLine(cfg *config.Config, st *store.Store, now time.Time) string {
	if cfg == nil {
		return "pr lane: legacy sync unknown, lane unknown (configuration unavailable)"
	}
	syncMode := cfg.Sync.Mode
	if syncMode == "" {
		syncMode = "off" // the same default the sync stage applies to an absent key
	}
	mode := cfg.PRLaneMode()
	head := fmt.Sprintf("pr lane: legacy sync %s, lane %s", syncMode, mode)

	if st == nil {
		return head + ", lane state unavailable (no store)"
	}
	version, err := st.SchemaVersion()
	if err != nil {
		return head + fmt.Sprintf(", lane state unavailable (read schema version: %v)", err)
	}
	if version < store.NewSchemaVersion {
		return head + ", lane state unavailable (store is not at schema 2)"
	}
	snap, err := lanestate.Read(st, now)
	if err != nil {
		return head + fmt.Sprintf(", lane state unavailable (%v)", err)
	}
	unhydrated, err := st.CountPRUnhydrated(now)
	if err != nil {
		return head + fmt.Sprintf(", lane state unavailable (%v)", err)
	}

	flip := "not yet"
	if snap.FlipAt != "" {
		flip = snap.FlipAt
	}
	lastRun := "never"
	if snap.LastRunAt != "" {
		lastRun = snap.LastRunAt
	}
	line := fmt.Sprintf("%s, flipped %s, never-hydrated %d (oldest %s)", head, flip, unhydrated.Count, laneAge(unhydrated.OldestAgeSeconds))
	if snap.LaneMode != mode {
		line += ", recorded lane mode " + snap.LaneMode
	}
	switch {
	case mode != config.PRLaneModeApply:
		line += ", residual n/a (lane not in apply)"
	case snap.LaneMode != config.PRLaneModeApply:
		// A residual recorded by plan runs is a plan's own action list, not a
		// backlog the apply lane left behind.
		line += ", residual n/a (no apply run recorded yet)"
	default:
		line += fmt.Sprintf(", residual %d (oldest %s)", snap.ResidualPRs, laneAge(snap.ResidualOldestAgeSeconds))
	}
	return line + ", last run " + lastRun
}

// laneAge renders an age in seconds as a Go duration; 0 reads as "0s".
func laneAge(seconds int) string {
	return (time.Duration(seconds) * time.Second).String()
}

// composedCommandName is the PR lane's composed command (packet P3b1b, bin of
// the pg-decider package).
const composedCommandName = "pg-pr-change"

// composedCommandHint is the line an annotation verb prints after it has
// written its annotation. The verbs only write annotations and never call the
// decider, so the operator is told which command makes the lane act on it. The
// mode is the configured lane mode when it is plan or apply; with the lane off
// (or no configuration) nothing runs the decider, which the line says, and the
// plan form is the one to try first.
func composedCommandHint(cfg *config.Config, id string) string {
	mode := config.PRLaneModeOff
	if cfg != nil {
		mode = cfg.PRLaneMode()
	}
	switch mode {
	case config.PRLaneModePlan, config.PRLaneModeApply:
		return fmt.Sprintf("next: run %s %s %s to have the lane act on it", composedCommandName, mode, id)
	default:
		return fmt.Sprintf("next: the pr lane is off (pr_lane.mode), so nothing acts on it yet; when it runs: %s plan %s", composedCommandName, id)
	}
}

// printComposedCommand prints, for a PR annotation verb that has just written
// its annotation, the composed command to run (the verbs never call the
// decider). It prints nothing for another entity type, which the lane does not
// handle. The id is the PR's canonical OWNER/REPO#N, the form the composed
// command takes; a reference that cannot be resolved again (it resolved a
// moment ago, so this is a configuration reload failure) is printed as given.
// preface, when set, is printed first on its own line.
func printComposedCommand(w io.Writer, cfg *config.Config, entityType, ref, preface string) error {
	if entityType != entityTypePR {
		return nil
	}
	id := ref
	if _, resolved, err := resolvePRRef(cfg, ref); err == nil {
		id = resolved
	}
	if preface != "" {
		if _, err := fmt.Fprintln(w, preface); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, composedCommandHint(cfg, id))
	return err
}
