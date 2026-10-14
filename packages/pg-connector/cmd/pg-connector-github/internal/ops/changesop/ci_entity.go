package changesop

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ciEntity is the CI entity of ref (whose stored state is st): the PR's CURRENT
// stored CI state, with version and head set to the values the caller passes (the ROW'S OWN for a
// delivered row, the current ones for a reset replay entry).
//
// The rollup is the stored summary's checks state folded as `pr show` folds it;
// the runs are the stored runs group's runs in the shape list_runs answers, each
// with pr_id the wire reference and as_of the group row's fetched_at. A reference
// the store holds no summary for (never fetched, evicted, or deleted) carries
// rollup "none" and no runs; a missing or empty runs group is an empty list and
// the rollup still comes from the summary. Neither is an error.
func (h *handler) ciEntity(ctx context.Context, ref string, st stored, version int64, head string) (schema.CIChangeEntity, error) {
	out := schema.CIChangeEntity{PRID: ref, Rollup: github.ChecksRollupFor(""), Runs: []schema.CIRun{}, Version: version, HeadSHA: head}
	if st.sum == nil {
		return out, nil
	}
	out.Rollup = github.ChecksRollupFor(st.sum.ChecksState)
	list, err := h.storedRuns(ctx, st.node, ref)
	if err != nil {
		return schema.CIChangeEntity{}, err
	}
	out.Runs = list
	return out, nil
}

// storedRuns reads the runs group of the PR with node id node as stored now. No
// row or no content is an empty list.
func (h *handler) storedRuns(ctx context.Context, node, ref string) ([]schema.CIRun, error) {
	g, found, err := h.deps.Store.GetGroup(ctx, entity.KindPR, node, groups.Runs)
	if err != nil {
		return nil, err
	}
	if !found || len(g.Content) == 0 {
		return []schema.CIRun{}, nil
	}
	content, err := runs.Decode(g.Content)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("decode stored runs of %s: %v", ref, err))
	}
	list := content.Runs
	if list == nil {
		list = []schema.CIRun{}
	}
	asOf := ""
	if len(list) > 0 {
		asOf = g.FetchedAt.UTC().Format(time.RFC3339)
	}
	for i := range list {
		list[i].PRID = ref
		list[i].AsOf = asOf
	}
	return list, nil
}

// ciRowEntity is the entity of a delivered ci change row: the current stored CI
// state with the ROW'S OWN version and head_sha.
func (h *handler) ciRowEntity(ctx context.Context, ref string, version int64, head string) (schema.CIChangeEntity, error) {
	st, err := h.lookup(ctx, ref)
	if err != nil {
		return schema.CIChangeEntity{}, err
	}
	return h.ciEntity(ctx, ref, st, version, head)
}

// ciCurrentEntity is the entity of a reset replay entry, as of now: the entity
// row's current version and the stored summary's head (empty when the store
// holds no summary).
func (h *handler) ciCurrentEntity(ctx context.Context, ref string) (schema.CIChangeEntity, error) {
	st, err := h.lookup(ctx, ref)
	if err != nil {
		return schema.CIChangeEntity{}, err
	}
	head := ""
	if st.sum != nil {
		head = st.sum.HeadRefOid
	}
	return h.ciEntity(ctx, ref, st, st.version, head)
}
