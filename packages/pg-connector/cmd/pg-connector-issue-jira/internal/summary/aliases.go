package summary

import (
	"context"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

type aliases struct{ st *store.Store }

// NewAliases is the query runner's alias table over the daemon store. A Jira key
// is its own stable id, so Resolve answers every reference without calling the
// origin; the store's alias table is written by Put and read only by Ref.
func NewAliases(st *store.Store) query.Aliases { return aliases{st: st} }

// Put records the display reference of an id.
func (a aliases) Put(ctx context.Context, id, ref string) error {
	return a.st.PutAlias(ctx, store.AliasRow{NodeID: groups.FoldRef(id), Ref: ref, SeenAt: time.Now()})
}

// Resolve answers each reference as {ID: groups.FoldRef(ref), CanonicalRef:
// strings.ToUpper(ref)}, keyed by the reference given.
func (a aliases) Resolve(_ context.Context, refs []string) (map[string]query.Alias, error) {
	out := make(map[string]query.Alias, len(refs))
	for _, r := range refs {
		out[r] = query.Alias{ID: groups.FoldRef(r), CanonicalRef: strings.ToUpper(r)}
	}
	return out, nil
}

// Ref is the display reference recorded for the id.
func (a aliases) Ref(ctx context.Context, id string) (string, bool, error) {
	row, ok, err := a.st.AliasByNodeID(ctx, groups.FoldRef(id))
	if err != nil || !ok {
		return "", false, err
	}
	return row.Ref, true, nil
}
