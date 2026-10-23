package query

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
)

// IdentityAliases is the GitHub implementation of the shared query.Aliases; it
// lives in the entity leaf, which the group refresh service shares.
type IdentityAliases = entity.IdentityAliases

// NewAliases returns the Aliases over id. log reports a reference that cannot
// be parsed (nil discards).
func NewAliases(id *entity.Identity, log func(event string, kv ...any)) *IdentityAliases {
	return entity.NewAliases(id, log)
}
