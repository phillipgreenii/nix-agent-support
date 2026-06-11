// Package discover turns the bead store's ready queue into role→bead dispatches.
// Feedback cycles are owned by self (the parent merge-request bead's author);
// worker beads are filtered natively by bd labels. Order is feedback-first.
package discover

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// Dispatch is a ready bead assigned to a specific role.
type Dispatch struct {
	Role   roles.Role
	BeadID string
}

// Discover returns feedback dispatches (owned by selfLogin) then worker
// dispatches, in priority order. selfLogin must be non-empty.
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry, selfLogin string) ([]Dispatch, error) {
	if selfLogin == "" {
		return nil, fmt.Errorf("discover: empty self_login (cannot resolve feedback ownership)")
	}
	var out []Dispatch
	fb, err := discoverFeedback(ctx, br, reg.Feedback, selfLogin)
	if err != nil {
		return nil, err
	}
	out = append(out, fb...)
	wk, err := discoverWorker(ctx, br, reg.Worker)
	if err != nil {
		return nil, err
	}
	out = append(out, wk...)
	return out, nil
}

func discoverFeedback(ctx context.Context, br beads.Runner, role roles.Role, selfLogin string) ([]Dispatch, error) {
	issues, err := beads.Ready(ctx, br) // bd ready --json --limit 0
	if err != nil {
		return nil, err
	}
	var out []Dispatch
	for _, iss := range issues {
		if iss.Type != "task" || !strings.HasPrefix(iss.Title, "process-feedback:") {
			continue
		}
		if iss.Parent == "" {
			continue
		}
		parent, err := beads.ShowObj(ctx, br, iss.Parent)
		if err != nil {
			return nil, err
		}
		if author, _ := parent.Metadata["author"].(string); author == selfLogin {
			out = append(out, Dispatch{Role: role, BeadID: iss.ID})
		}
	}
	return out, nil
}

func discoverWorker(ctx context.Context, br beads.Runner, role roles.Role) ([]Dispatch, error) {
	issues, err := beads.Ready(ctx, br, "--label", "worker-ready", "--exclude-label", "human")
	if err != nil {
		return nil, err
	}
	var out []Dispatch
	for _, iss := range issues {
		out = append(out, Dispatch{Role: role, BeadID: iss.ID})
	}
	return out, nil
}
