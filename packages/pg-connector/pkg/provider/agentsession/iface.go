// Package agentsession declares the agentsession capability's provider
// interface. Read-only (Show/List only, no write ops) — mirrors
// pkg/provider/thread's identical shape, since you don't create or mutate
// a session through this capability.
package agentsession

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the agentsession capability's provider interface.
type Provider interface {
	// Show returns id's current state.
	Show(ctx context.Context, id string) (*schema.AgentSession, error)

	// List returns sessions currently in scope. query is accepted for
	// interface-shape symmetry with thread/issue's List but is always nil
	// as called by this package's own dispatch table today — see
	// dispatch.go's doc comment for why.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.AgentSessionListResult, error)
}
