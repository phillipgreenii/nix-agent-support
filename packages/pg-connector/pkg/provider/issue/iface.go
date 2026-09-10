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
// supplies. Description was added by bead pg2-akfw5 (review finding A-33: Create previously
// had no way to set one at all); Assignee/Deps stay out of scope for
// that bead's fix — it names only "give Create a description parameter."
// Metadata and Parent were added by bead pg2-2j5ac.28.3 (this bead's own
// Contract: "IssueInput (Create's input) gains Metadata map[string]string
// and Parent string").
type IssueInput struct {
	Title       string            `json:"title"`
	Priority    string            `json:"priority,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	IssueType   string            `json:"issue_type,omitempty"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Parent      string            `json:"parent,omitempty"`
}

// IssueUpdateFields is Update's caller-supplied field set — every field
// optional, applied together in ONE call (bead pg2-2j5ac.28.3's own
// Contract: "--metadata, --add-label, --remove-label, --priority, --title,
// --description, all optional, applied in ONE call"). Metadata here is a
// MERGE (set/overwrite the named keys), never a full replace — mirroring
// bd's own --set-metadata semantics, the backing this method's beads
// implementation uses.
type IssueUpdateFields struct {
	Metadata     map[string]string `json:"metadata,omitempty"`
	AddLabels    []string          `json:"add_labels,omitempty"`
	RemoveLabels []string          `json:"remove_labels,omitempty"`
	Priority     string            `json:"priority,omitempty"`
	Title        string            `json:"title,omitempty"`
	Description  string            `json:"description,omitempty"`
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

	// List runs query (already resolved from the request's own
	// config.queries block — dispatch.go's "list" handler does that
	// resolution centrally, reporting query_not_recognized itself before
	// ever calling List) and returns the matching issues. query MAY carry
	// more than one expression (design: "run each, union results
	// deduplicated by id, report truncated if any member truncated") —
	// List, not the dispatch table, does that fan-in. idsOnly, when true,
	// means the caller only wants IssueListResult.PresentIDs populated; a
	// Provider MAY still choose to populate Entities anyway (harmless,
	// just wasted work) but need not. Mirrors pkg/provider/pr.Provider's
	// identical List addition (bead pg2-2j5ac.28.1).
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.IssueListResult, error)

	// Update applies fields' optional members to issue id in ONE call and
	// returns its resulting state (bead pg2-2j5ac.28.3's own Contract). A
	// backend maps each populated field onto its own tracker-native write
	// (Jira maps to fields; beads maps to `bd update`) [freedom boundary:
	// which Jira fields --metadata maps to, and the exact bd update flag
	// combination, are the implementer's own call within each backend's
	// existing field-mapping conventions]. A backend that genuinely cannot
	// perform this write against its underlying tool (no update op exists
	// at all) reports that as its own well-formed error, per
	// pkg/scriptout's closed error taxonomy — it must not silently no-op.
	Update(ctx context.Context, id string, fields IssueUpdateFields) (*schema.Issue, error)

	// Close closes issue id, recording reason where its tracker has a
	// place to keep one. Jira maps this to a resolving transition; beads
	// maps it to `bd close --reason` (bead pg2-2j5ac.28.3's own Contract).
	// Reopening a closed issue is Transition(ctx, id, "open") followed by
	// Update — there is deliberately no dedicated reopen op (binding
	// decision), and this method exists ONLY as a resolving write: no
	// gate op of any kind is ever added through it (D13).
	Close(ctx context.Context, id, reason string) error

	// Deps returns id's recursive UPWARD (transitively blocked-by)
	// dependency set — this is what "waiting-on-me" tooling needs, and is
	// distinct from Show's own one-level, all-edge-type schema.Issue.Deps
	// field (bead pg2-2j5ac.28.3's own Contract and binding decision: "do
	// not conflate the two or repurpose Show's field for this"). With
	// full=false the result carries only ids; with full=true it also
	// carries the full schema.Issue entity for each. A backend with no
	// dependency concept of its own (e.g. issue-jira, which has no
	// dependency/link query available) answers an empty result, never an
	// error.
	Deps(ctx context.Context, id string, full bool) (*schema.IssueDepsResult, error)
}
