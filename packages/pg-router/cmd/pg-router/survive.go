package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pg-router/internal/config"
)

// handlerRoleConfigPath is the per-role file under cfg.HandlerCommandDir that
// handlerCommandFor hands a handler as `--role-config`.
func handlerRoleConfigPath(dir, roleName string) string {
	return filepath.Join(dir, roleName+".json")
}

// roleSurvivalFlag is the ONE key of a role's rendered config file this binary
// reads: a role whose handler supervises its own work independently of this
// daemon declares `"survivesShutdown": true` (the deployment module renders it
// for ccpool-backed roles). The rest of the file is the handler's private
// business and is never interpreted here (GOAL-MIN-1).
type roleSurvivalFlag struct {
	SurvivesShutdown bool `json:"survivesShutdown"`
}

// roleSurvivesShutdown reports whether role's dispatch MUST be left running,
// rather than cancelled, when this daemon shuts down (bead pg2-dtigc, ADR
// 0085). Absent config, no HandlerCommandDir, a missing role file, or an
// unreadable/ill-formed one all mean false: the dispatch is cancelled after
// the drain exactly as before this flag existed. A file that exists but cannot
// be decoded is logged, since silently losing the flag would silently restore
// the old kill-on-restart behavior.
func roleSurvivesShutdown(cfg config.Config, roleName string) bool {
	if cfg.HandlerCommandDir == "" {
		return false
	}
	path := handlerRoleConfigPath(cfg.HandlerCommandDir, roleName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("role config unreadable; its dispatch will be cancelled at shutdown", "role", roleName, "path", path, "err", err)
		}
		return false
	}
	var f roleSurvivalFlag
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("role config undecodable; its dispatch will be cancelled at shutdown", "role", roleName, "path", path, "err", err)
		return false
	}
	return f.SurvivesShutdown
}

// survivingRoles resolves, once, which ENABLED roles survive a shutdown, as a
// set keyed by role name (the eventqueue listener id).
func survivingRoles(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	for _, r := range cfg.Roles {
		if r.Enabled && roleSurvivesShutdown(cfg, r.Name) {
			out[r.Name] = true
		}
	}
	return out
}
