// Package discover turns each role's configured query into role→item dispatches,
// in config order, honoring each role's Enabled flag. Query errors propagate
// (pg2-qq9v): a query failure must NOT masquerade as "no ready work".
package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DispatchContext is one (role, item) dispatch. It is the explicit growth point for
// future resolved fields (worktree dir, self_login, template vars); keeping it a
// struct keeps run-role's call shape stable as it accretes fields.
type DispatchContext struct {
	Role roles.Role
	Item item.Item
}

// Validate reports every required field that is missing in a single error, so callers
// (run-role) get a complete diagnostic rather than dispatching a half-filled context.
func (d DispatchContext) Validate() error {
	var missing []string
	if d.Role.Name == "" {
		missing = append(missing, "role")
	}
	if d.Item.ID == "" {
		missing = append(missing, "item")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dispatch context missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// Discover runs each enabled role's query, in config order, honoring Enabled.
func Discover(ctx context.Context, env query.Env, rs roles.RoleSet) ([]DispatchContext, error) {
	var out []DispatchContext
	for _, role := range rs {
		if !role.Enabled {
			slog.Info("role disabled; skipping discovery", "role", role.Name)
			continue
		}
		dcs, err := ForRole(ctx, env, role)
		if err != nil {
			return nil, err
		}
		out = append(out, dcs...)
	}
	return out, nil
}

// ForRole runs ONE role's query regardless of the role's Enabled flag (the smoke
// harness must be able to query a role disabled in config).
func ForRole(ctx context.Context, env query.Env, role roles.Role) ([]DispatchContext, error) {
	items, err := role.Query.Run(ctx, env)
	if err != nil {
		// Propagate: a query failure must NOT masquerade as "no ready work", or the
		// pool silently idles on infra failure. (pg2-qq9v)
		return nil, fmt.Errorf("discover %s: %w", role.Name, err)
	}
	out := make([]DispatchContext, 0, len(items))
	for _, it := range items {
		out = append(out, DispatchContext{Role: role, Item: it})
	}
	return out, nil
}
