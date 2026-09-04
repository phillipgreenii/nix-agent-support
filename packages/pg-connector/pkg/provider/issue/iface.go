// Package issue declares the issue capability's provider interface — a
// small, capability-scoped Go interface that a Tier-2 issue backend's
// concrete provider implements. Its name and method set correspond to
// exactly the issue capability and name no backend/system (jira, beads,
// github issues, ...): the package is "issue", the interface is
// "Provider", and every method name below (Show/Create/Comment/Transition)
// is a capability-level verb, never a system-specific one. This matches
// pkg/provider/pr.Provider's identical convention.
//
// Unlike today's read-only packages/pg-pr/pkg/provider/issues.Provider
// (GetIssue only), this Provider is widened to read+write: a Show-style
// read plus three write ops (Create/Comment/Transition), so Issue can be a
// full connector rather than a mirror. This package does not import or
// reuse the pg-pr issues package — packages/pg-connector's go.mod does not
// depend on packages/pg-pr at all.
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package issue

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// IssueInput is the caller-supplied field set for Create. It is
// deliberately narrower than schema.Issue: ID, URL, and State are
// backend-assigned outputs of a successful create, never inputs a caller
// supplies.
type IssueInput struct {
	Title     string   `json:"title"`
	Priority  string   `json:"priority,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	IssueType string   `json:"issue_type,omitempty"`
}

// Provider is the issue capability's provider interface. A concrete
// backend MAY additionally implement pkg/provider.AuthChecker, asserted via
// a type-check rather than folded into this interface — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// Show returns id's current state.
	Show(ctx context.Context, id string) (*schema.Issue, error)

	// Create creates a new issue from input and returns its assigned
	// identity/state.
	Create(ctx context.Context, input IssueInput) (*schema.Issue, error)

	// Comment adds a comment with the given body to issue id.
	Comment(ctx context.Context, id, body string) error

	// Transition moves issue id to targetState. targetState is a plain
	// string, not a shared Go enum: Jira/beads/GitHub Issues do not share
	// one state vocabulary, so each backend declares its own accepted
	// values in its own capabilities response (vocabulary.state) rather
	// than this interface pinning a fixed cross-backend set. A well-formed
	// rejection of an unrecognized targetState is this method's own error
	// to report — it is not validated here.
	Transition(ctx context.Context, id, targetState string) error
}
