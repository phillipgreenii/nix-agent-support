package changes

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/revalidate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// DetectSummary is the summary seam's body (summary.StoreHook): it runs inside
// the transaction that stored st, after the rows are written.
//
// A nil prev or a first sighting is the baseline and does nothing. Otherwise the
// change is classified and the groups revalidate.Decide queues become the
// awaited set: merged into the entity's pending row when there is one (the
// deadline does not move), written as a new pending row when there is none and
// something is awaited (even when the summary's own result is empty), and
// appended at once when nothing is awaited.
func (e *Engine) DetectSummary(ctx context.Context, tx *store.Tx, prev *summary.Summary, st summary.Stored, info summary.FetchInfo) error {
	node := st.Summary.NodeID
	origin := e.originFor(node, groups.Summary, info.Purpose)
	if prev == nil || st.First || st.Summary.Gone || node == "" {
		return nil
	}
	res := classify.Summary(prev, st, e.bodyCap)
	queue := revalidate.Decide(prev, st).Queue
	now := e.now()

	p, has, err := tx.GetPending(ctx, LogKindPR, node)
	if err != nil {
		return err
	}
	if has {
		pres, aw, err := pendingResult(p)
		if err != nil {
			return err
		}
		if err := e.expire(ctx, tx, node, queue, now); err != nil {
			return err
		}
		p.HeadSHA, p.BaseSHA = st.Summary.HeadRefOid, st.Summary.BaseRefOid
		return e.putPending(ctx, tx, p, classify.Merge(pres, res), await(aw, queue, now))
	}
	if len(queue) > 0 {
		if err := e.expire(ctx, tx, node, queue, now); err != nil {
			return err
		}
		return e.putPending(ctx, tx, store.PendingChange{
			Kind: LogKindPR, ID: node,
			HeadSHA: st.Summary.HeadRefOid, BaseSHA: st.Summary.BaseRefOid,
			Origin: origin, OriginUpdatedAt: originTime(st.Summary),
			DetectedAt: now, Deadline: now.Add(e.settle),
		}, res, await(nil, queue, now))
	}
	_, err = e.appendRow(ctx, tx, node, st.Summary.Ref(), st.Summary.HeadRefOid, res, origin, originTime(st.Summary))
	return err
}
