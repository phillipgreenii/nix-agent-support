package changesop

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// ciChanges is the changes op of the ci instance: the CI view of the change
// feed under the cursor key (consumer, ci, query). The rows are the PR rows of
// the log; feed.DeliveryForKey keeps those a CI-only consumer cares about
// (ci_changed of a member, and a PR entering or leaving the query, or removed)
// and names the change word. Each entity is the PR's current stored CI state
// with the row's own version and head_sha.
func (h *handler) ciChanges(ctx context.Context, raw json.RawMessage) (any, error) {
	m, err := args(raw)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"since", "before"} {
		if _, ok := m[k]; ok {
			return nil, invalid("changes takes no time bound; the argument %q is not supported", k)
		}
	}
	consumer, query, err := keyArgs(m)
	if err != nil {
		return nil, err
	}
	cached, err := boolArg(m, "cached")
	if err != nil {
		return nil, err
	}
	reset, err := boolArg(m, "reset")
	if err != nil {
		return nil, err
	}
	router.Annotate(ctx, "store", false, 0, 0)

	res, err := h.deps.Feed.Poll(ctx, feed.PollRequest{Kind: feed.KindCI, Consumer: consumer, Query: query, Cached: cached, Reset: reset})
	if err != nil {
		return nil, mapError(OpChanges, err)
	}
	configured, err := h.deps.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, mapError(OpChanges, err)
	}
	out := schema.CIChangesResult{
		Sources: []schema.ChangeSource{{
			Backend: status.RegistryNameCI, Status: sourceSucceeded,
			Version: res.Position, NextSeq: res.NextSeq,
		}},
		Changes:          []schema.CIChange{},
		SourcesFreshness: freshness(configured),
	}

	if reset {
		for _, ref := range res.Replay {
			e, err := h.ciCurrentEntity(ctx, ref)
			if err != nil {
				return nil, mapError(OpChanges, err)
			}
			out.Changes = append(out.Changes, schema.CIChange{
				Change: feed.ChangeAdded, Source: status.RegistryNameCI, Entity: e,
				Kinds: []string{}, Fields: json.RawMessage(`[]`),
			})
		}
		return out, nil
	}

	members := memberSet(configured, query)
	for _, row := range res.Rows {
		deliver, change := feed.DeliveryForKey(feed.KindCI, row, query, members[foldRef(row.ID)])
		if !deliver {
			continue
		}
		e, err := h.ciRowEntity(ctx, row.ID, row.Version, row.HeadSHA)
		if err != nil {
			return nil, mapError(OpChanges, err)
		}
		kinds := row.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		fields := row.Fields
		if len(fields) == 0 || string(fields) == "null" {
			fields = json.RawMessage(`[]`)
		}
		out.Changes = append(out.Changes, schema.CIChange{
			Change: change, Source: status.RegistryNameCI, Entity: e, Seq: row.Seq, Kinds: kinds, Fields: fields,
		})
	}
	return out, nil
}

// ciAck is the changes_ack op of the ci instance: it acknowledges the (consumer,
// ci, query) key only.
func (h *handler) ciAck(ctx context.Context, raw json.RawMessage) (any, error) {
	return h.ackKind(ctx, raw, feed.KindCI)
}
