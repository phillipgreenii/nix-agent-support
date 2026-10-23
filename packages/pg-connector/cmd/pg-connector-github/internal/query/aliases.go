package query

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

// IdentityAliases is the GitHub implementation of the shared query.Aliases: it
// records and resolves the members' aliases (node id to owner/repo#n) over
// entity.Identity.
type IdentityAliases struct {
	id  *entity.Identity
	log func(event string, kv ...any)
}

var _ shared.Aliases = (*IdentityAliases)(nil)

// NewAliases returns the Aliases over id. log reports a reference that cannot
// be parsed (nil discards).
func NewAliases(id *entity.Identity, log func(event string, kv ...any)) *IdentityAliases {
	return &IdentityAliases{id: id, log: log}
}

// Put records that id is ref.
func (a *IdentityAliases) Put(ctx context.Context, id, ref string) error {
	r, err := entity.ParseRef(ref)
	if err != nil {
		return err
	}
	_, err = a.id.Put(ctx, id, r)
	return err
}

// Resolve answers the alias of each reference in ONE batched read of the alias
// table (the lookup folds the reference's case, which cannot use the table's
// index, so a lookup per reference would scan the whole table once each,
// pg2-6cwpf). The result is keyed by the reference as given. A reference that
// does not parse is reported (query_membership_unparsable_id) and answered with
// an empty Alias, so the runner skips it without a second report; a reference no
// alias names is absent.
func (a *IdentityAliases) Resolve(ctx context.Context, refs []string) (map[string]shared.Alias, error) {
	out := make(map[string]shared.Alias, len(refs))
	byKey := make(map[string][]string, len(refs))
	var parsed []entity.Ref
	for _, s := range refs {
		ref, err := entity.ParseRef(s)
		if err != nil {
			if a.log != nil {
				a.log("query_membership_unparsable_id", "id", s, "error", err.Error())
			}
			out[s] = shared.Alias{}
			continue
		}
		if len(byKey[ref.Key()]) == 0 {
			parsed = append(parsed, ref)
		}
		byKey[ref.Key()] = append(byKey[ref.Key()], s)
	}
	if len(parsed) == 0 {
		return out, nil
	}
	known, err := a.id.NodeIDs(ctx, parsed)
	if err != nil {
		return nil, err
	}
	for key, k := range known {
		for _, s := range byKey[key] {
			out[s] = shared.Alias{ID: k.NodeID, CanonicalRef: k.Canonical.String()}
		}
	}
	return out, nil
}
