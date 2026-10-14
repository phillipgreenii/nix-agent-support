package changesop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/list"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// stored is what the store holds of one pull request, found by wire reference.
type stored struct {
	// node is the node id; empty when the alias table does not know the reference.
	node string
	// sum and fetchedAt are the stored summary; sum is nil when there is none
	// (never fetched, evicted, or a summary of a deleted pull request).
	sum       *summary.Summary
	fetchedAt time.Time
	// version is the entity row's current version (0 when there is no row).
	version int64
}

// lookup reads the alias table, the stored summary and the entity row of ref
// (a wire reference owner/repo#n). A reference that does not parse, or that the
// alias table does not know, finds nothing; it is not an error.
func (h *handler) lookup(ctx context.Context, ref string) (stored, error) {
	parsed, err := entity.ParseRef(ref)
	if err != nil {
		return stored{}, nil
	}
	node, _, ok, err := h.deps.Aliases.NodeID(ctx, parsed)
	if err != nil || !ok {
		return stored{}, err
	}
	out := stored{node: node}
	if row, found, err := h.deps.Store.GetEntity(ctx, entity.KindPR, node); err != nil {
		return stored{}, err
	} else if found {
		out.version = row.Version
	}
	g, found, err := h.deps.Store.GetGroup(ctx, entity.KindPR, node, summary.GroupName)
	if err != nil || !found || len(g.Content) == 0 {
		return out, err
	}
	var s summary.Summary
	if err := json.Unmarshal(g.Content, &s); err != nil {
		return stored{}, scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("decode stored summary of %s: %v", ref, err))
	}
	if s.Gone {
		return out, nil
	}
	out.sum, out.fetchedAt = &s, g.FetchedAt
	return out, nil
}

// rowEntity is the entity of a delivered change row: the summary-level PR of the
// entity's current stored summary with version and head_sha replaced by the
// ROW'S OWN recorded values. When the store holds no summary for the reference
// the entity carries only id, version and head_sha.
func (h *handler) rowEntity(ctx context.Context, ref string, version int64, head string) (schema.PR, error) {
	st, err := h.lookup(ctx, ref)
	if err != nil {
		return schema.PR{}, err
	}
	pr := schema.PR{}
	if st.sum != nil {
		pr = list.SummaryEntity(*st.sum, st.fetchedAt, false)
	}
	pr.ID, pr.Version, pr.HeadSHA = ref, version, head
	return pr, nil
}

// currentEntity is the entity of a reset replay entry, as of now: the stored
// summary's head and the entity row's current version. When the store holds no
// summary the entity carries only id and version.
func (h *handler) currentEntity(ctx context.Context, ref string) (schema.PR, error) {
	st, err := h.lookup(ctx, ref)
	if err != nil {
		return schema.PR{}, err
	}
	pr := schema.PR{}
	if st.sum != nil {
		pr = list.SummaryEntity(*st.sum, st.fetchedAt, false)
	}
	pr.ID, pr.Version = ref, st.version
	return pr, nil
}
