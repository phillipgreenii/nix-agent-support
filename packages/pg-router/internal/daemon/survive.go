package daemon

import (
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// RoleSurvives is the survival decision for one role (bead pg2-dtigc, ADR 0085):
// whether its dispatch MUST be left running, rather than cancelled, when this
// daemon shuts down. It is the generic survives_shutdown key core decoded from
// the role's own [[role]] TOML entry (default false); core never reads anything
// the handler owns (GOAL-MIN-1).
func RoleSurvives(r roles.Role) bool {
	return r.SurvivesShutdown
}

// SurvivingRoles resolves, once, which ENABLED roles survive a shutdown, as a
// set keyed by role name (the eventqueue listener id).
func SurvivingRoles(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	for _, r := range cfg.Roles {
		if r.Enabled && RoleSurvives(r) {
			out[r.Name] = true
		}
	}
	return out
}
