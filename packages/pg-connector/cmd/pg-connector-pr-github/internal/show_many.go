package internal

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend implements pr.ShowManyReader, so the show_many op is registered and
// listed in capabilities.ops for the GitHub backend.
var _ pr.ShowManyReader = (*Backend)(nil)

// ShowMany implements pr.ShowManyReader (bead pg2-12yur): the SUMMARY-level PR
// (schema.PRListFields; no comment or review bodies) of every ref, fetched
// through GraphQL nodes(ids:) at 74 ids per document, which costs 1 point per
// document (spec section 6.6's "refresh by ids" row) where a show costs several
// calls per PR.
//
// Each returned entity is the same summary the cheap list returns for that PR,
// with as_of set to this call's time, so a caller can compare it with list
// output. A ref the origin answers with nothing, or whose node resolves to a PR
// other than the one the ref names (a rename or transfer since the caller read
// membership), is reported in Missing rather than guessed at; the caller
// confirms it with show. A request that cannot be answered is an error that
// fails every id in it, with the same rate-reserve refusal List applies: the
// reserve is judged from each document's own reading, before its results are
// used.
func (b *Backend) ShowMany(ctx context.Context, req pr.ShowManyRequest) (*schema.PRShowManyResult, error) {
	out := &schema.PRShowManyResult{Entities: []schema.PR{}, Missing: []string{}}
	seen := make(map[string]bool, len(req.Refs))
	refs := make([]pr.ShowManyRef, 0, len(req.Refs))
	nodeIDs := make([]string, 0, len(req.Refs))
	for _, ref := range req.Refs {
		if _, _, err := parsePRID(ref.ID); err != nil {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
		}
		if !github.ValidNodeID(ref.NodeID) {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("show_many: ref %q has no usable node_id", ref.ID))
		}
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		refs = append(refs, ref)
		nodeIDs = append(nodeIDs, ref.NodeID)
	}
	if len(refs) == 0 {
		return out, nil
	}

	gate := func(rl github.RateLimit) error { return enforceRateReserve(ctx, rl) }
	found, err := b.gh.SummariesByNodeIDs(ctx, nodeIDs, gate)
	if err != nil {
		return nil, classifyGHError(err)
	}
	asOf := time.Now().UTC()
	for _, ref := range refs {
		ghPR, ok := found[ref.NodeID]
		if !ok || formatPRID(ghPR.Repo, ghPR.Number) != ref.ID {
			out.Missing = append(out.Missing, ref.ID)
			continue
		}
		out.Entities = append(out.Entities, *toSchemaPR(ref.ID, &ghPR, nil, nil, asOf))
	}
	return out, nil
}
