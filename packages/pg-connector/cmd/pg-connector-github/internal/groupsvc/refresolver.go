package groupsvc

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// refResolver adapts the GitHub first-sighting resolver (entity.Resolver's
// shape) to the shared RefResolver: wire strings in, shared Resolutions out.
type refResolver struct{ r RefResolver }

var _ sharedsvc.RefResolver = refResolver{}

// Resolve parses the references (a reference that does not parse is an error:
// the shared service only passes references ParseID accepted), resolves them as
// call, and converts the resolutions back.
func (a refResolver) Resolve(ctx context.Context, call sharedsvc.CallMeta, refs []string) ([]sharedsvc.Resolution, int64, error) {
	parsed := make([]entity.Ref, len(refs))
	for i, s := range refs {
		r, err := entity.ParseRef(s)
		if err != nil {
			return nil, 0, fmt.Errorf("groupsvc: %w", err)
		}
		parsed[i] = r
	}
	res, cost, err := a.r.Resolve(ctx, ratefeed.Meta(call), parsed)
	if err != nil {
		return nil, cost, err
	}
	out := make([]sharedsvc.Resolution, len(res))
	for i, r := range res {
		out[i] = sharedsvc.Resolution{
			Requested: refString(r.Requested), NodeID: r.NodeID, Ref: refString(r.Ref),
			Found: r.Found, FromStore: r.FromStore, Renamed: r.Renamed, OldRef: refString(r.OldRef),
		}
	}
	return out, cost, nil
}

// refString is the wire form of a reference; the zero Ref is the empty string.
func refString(r entity.Ref) string {
	if r == (entity.Ref{}) {
		return ""
	}
	return r.String()
}
