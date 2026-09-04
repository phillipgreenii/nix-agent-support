// Package pr declares the pr capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// [design: §3] that a Tier-2 PR backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. vcs.Provider in packages/pg-pr/pkg/provider/vcs) rather than one
// interface spanning multiple systems [design: §3].
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package pr

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the pr capability's provider interface: a Show-style read
// plus the two dedicated write ops this docket names, following the
// issue-capability-style widen-to-read+write pattern adapted to pr's own
// write set [design: §3 acceptance criteria, §6.1]. A concrete backend MAY
// additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface [design: §4.6] — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// Show returns id's current full state, including comments/
	// review-thread entries each with their own id and current
	// disposition [design: §6.1].
	Show(ctx context.Context, id string) (*schema.PR, error)

	// Categorize sets id's category to category, a plain set/overwrite
	// into a dedicated field, never a GitHub label, used only for
	// focus/filtering tooling and dashboards [design: §6.1].
	Categorize(ctx context.Context, id, category string) (*schema.CategorizeResult, error)

	// FeedbackSet sets commentID's disposition on PR id. disposition is
	// drawn from schema.ValidDispositions — a well-formed not_found
	// response (e.g. commentID no longer exists) is a valid negative
	// answer, not a broken call [design: §4.5, §6.1].
	FeedbackSet(ctx context.Context, id, commentID string, disposition schema.Disposition) (*schema.FeedbackSetResult, error)
}
