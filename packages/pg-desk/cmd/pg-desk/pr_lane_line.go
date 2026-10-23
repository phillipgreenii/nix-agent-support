package main

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// prLaneLine is the static "pr lane" line of `pg-desk status` and `pg-desk
// doctor` (PR lane refactor, P0b): the legacy sync's mode and the lane's mode,
// both read from the configuration. The lane mode has one source, the key
// pr_lane.mode the deployment renders beside the role's own mode (the router's
// per-role JSON is not readable by pg-desk); a missing or invalid value reads
// as off. The line says only what the configuration says; it knows nothing of
// what the lane did, which is why it carries no verdict and the live lane line
// (counts, last run, remedy text) replaces it when the lane ships.
//
// A configuration that cannot be loaded reports both modes as unknown rather
// than guessing off: status and doctor still print their other sections.
func prLaneLine(cfg *config.Config) string {
	if cfg == nil {
		return "pr lane: legacy sync unknown, lane unknown (configuration unavailable)"
	}
	syncMode := cfg.Sync.Mode
	if syncMode == "" {
		syncMode = "off" // the same default the sync stage applies to an absent key
	}
	return fmt.Sprintf("pr lane: legacy sync %s, lane %s", syncMode, cfg.PRLaneMode())
}
