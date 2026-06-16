// Package discover turns the bead store's ready queue into role→bead dispatches.
// Feedback cycles are identified by a `mine` ownership label stamped at creation
// (pg-pr); worker beads are filtered natively by bd labels. Order is feedback-first.
package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DispatchContext is everything one dispatch needs. Today: role + bead. It is the
// explicit growth point for future fields (repo, self_login, template variables);
// keeping it a struct keeps run-role's call shape stable as it accretes fields.
type DispatchContext struct {
	Role   roles.Role
	BeadID string
}

// Validate reports every required field that is missing in a single error, so callers
// (run-role) get a complete diagnostic rather than dispatching a half-filled context.
func (d DispatchContext) Validate() error {
	var missing []string
	if d.Role.Name == "" { // every real role has a Name; Kind 0 is a valid kind, so it can't signal "unset"
		missing = append(missing, "role")
	}
	if d.BeadID == "" {
		missing = append(missing, "bead")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dispatch context missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// Discover returns feedback dispatches then worker dispatches, in priority order,
// honoring each role's Enabled flag. Both queries are pure `bd ready` label filters
// — ownership is read from the `mine` label on the cycle, not joined from its parent.
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry) ([]DispatchContext, error) {
	var out []DispatchContext
	if reg.Feedback.Enabled {
		fb, err := ForRole(ctx, br, reg.Feedback)
		if err != nil {
			return nil, err
		}
		out = append(out, fb...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Feedback.Name)
	}
	if reg.Worker.Enabled {
		wk, err := ForRole(ctx, br, reg.Worker)
		if err != nil {
			return nil, err
		}
		out = append(out, wk...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Worker.Name)
	}
	return out, nil
}

// ForRole runs ONE role's discovery query, regardless of the role's Enabled flag
// (the smoke harness must be able to query a role disabled in config). Both paths
// are self-relative by construction (label filters), so neither needs a self_login.
func ForRole(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
	switch role.Kind {
	case roles.Feedback:
		return discoverFeedback(ctx, br, role)
	case roles.Worker:
		return discoverWorker(ctx, br, role)
	default:
		return nil, fmt.Errorf("discover: unknown role kind %v", role.Kind)
	}
}

func discoverFeedback(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
	issues, err := beads.Ready(ctx, br, "--label", "mine") // self-relative: only my cycles carry `mine`
	if err != nil {
		// Propagate: a bd failure must NOT masquerade as "no ready work", or the
		// pool silently idles on infra failure. (pg2-qq9v)
		return nil, fmt.Errorf("discover feedback: bd ready: %w", err)
	}
	var out []DispatchContext
	for _, iss := range issues {
		// The `mine` label scopes to my cycles; the type/title guard confirms the
		// bead is a feedback cycle (the cycle-identity contract; no custom type).
		if iss.Type == "task" && strings.HasPrefix(iss.Title, "process-feedback:") {
			out = append(out, DispatchContext{Role: role, BeadID: iss.ID})
		}
	}
	return out, nil
}

func discoverWorker(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
	issues, err := beads.Ready(ctx, br, "--label", "worker-ready", "--exclude-label", "human")
	if err != nil {
		// Propagate rather than returning nil,nil — see discoverFeedback. (pg2-qq9v)
		return nil, fmt.Errorf("discover worker: bd ready: %w", err)
	}
	var out []DispatchContext
	for _, iss := range issues {
		out = append(out, DispatchContext{Role: role, BeadID: iss.ID})
	}
	return out, nil
}
