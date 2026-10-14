package changes

import (
	"context"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// removedMetaPrefix keys the store_meta record that a removed row was appended
// for an entity's current tombstone.
const removedMetaPrefix = "changes.removed."

// refKey normalises a wire reference for comparison: owner/repo is compared
// case-insensitively, the number exactly.
func refKey(ref string) string {
	i := strings.LastIndex(ref, "#")
	if i < 0 {
		return strings.ToLower(ref)
	}
	return strings.ToLower(ref[:i]) + ref[i:]
}

// DetectGone is the tombstone seam's body (summary.GoneHook and
// groupsvc.Config.OnGone). The seam's transaction has already set tombstoned_at;
// this method does not write it.
//
// When the entity has a stored summary (a baseline) it appends one removed row
// naming every configured query whose stored id list contains the entity, merged
// with the entity's pending row when there is one (a tombstone ends the barrier:
// nothing more will be fetched). With no stored summary it appends nothing and
// deletes any pending row. A repeat call for the same tombstone appends nothing.
func (e *Engine) DetectGone(ctx context.Context, tx *store.Tx, node string, purpose governor.Purpose) error {
	origin := e.originFor(node, groups.Summary, purpose)
	p, has, err := tx.GetPending(ctx, LogKindPR, node)
	if err != nil {
		return err
	}
	sum, ok, err := storedSummary(ctx, tx, node)
	if err != nil {
		return err
	}
	if !ok {
		if has {
			return tx.DeletePending(ctx, LogKindPR, node)
		}
		return nil
	}

	var tomb string
	ent, entOK, err := tx.GetEntity(ctx, LogKindPR, node)
	if err != nil {
		return err
	}
	if entOK && !ent.TombstonedAt.IsZero() {
		tomb = strconv.FormatInt(ent.TombstonedAt.UnixMilli(), 10)
	} else {
		tomb = "0"
	}
	if done, ok, err := tx.GetMeta(ctx, removedMetaPrefix+node); err != nil {
		return err
	} else if ok && done == tomb {
		if has {
			return tx.DeletePending(ctx, LogKindPR, node)
		}
		return nil
	}

	ref := sum.Ref()
	var names []string
	if ref != "" {
		queries, err := tx.ListQueries(ctx, true)
		if err != nil {
			return err
		}
		want := refKey(ref)
		for _, q := range queries {
			if !q.Configured {
				continue
			}
			for _, id := range q.IDs {
				if refKey(id) == want {
					names = append(names, q.Key)
					break
				}
			}
		}
	}

	res := classify.Removed(names)
	head, originAt := sum.HeadRefOid, originTime(sum)
	if has {
		pres, _, err := pendingResult(p)
		if err != nil {
			return err
		}
		res, origin, head, originAt = classify.Merge(pres, res), p.Origin, p.HeadSHA, p.OriginUpdatedAt
		if err := tx.DeletePending(ctx, LogKindPR, node); err != nil {
			return err
		}
	}
	if _, err := e.appendRow(ctx, tx, node, ref, head, res, origin, originAt); err != nil {
		return err
	}
	return tx.PutMeta(ctx, removedMetaPrefix+node, tomb)
}
