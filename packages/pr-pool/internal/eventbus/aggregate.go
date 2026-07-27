package eventbus

import (
	"sort"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// aggregate is a role's opt-in Aggregator (EIP, Q2): it collects correlated
// events keyed by CorrelationID until the spec's Completeness is met, then emits
// ONE aggregated event to the role's queue. Incomplete aggregates expire via TTL
// (Q4) so a missing sibling can never wedge a queue forever.
type aggregate struct {
	spec      event.CorrelationSpec
	matches   map[string]bool          // event types this aggregator collects (role Binds)
	pending   map[string][]event.Event // correlationID -> collected events
	firstSeen map[string]time.Time     // correlationID -> when its first event arrived (TTL origin)
}

// aggregateLocked routes one correlated event into roleName's aggregator. When
// the correlation id's collected set satisfies Completeness, ONE aggregated
// event (Item = the correlation target) is enqueued to the role's queue and the
// pending set for that id is cleared. Caller holds b.mu.
func (b *Bus) aggregateLocked(roleName string, agg *aggregate, e event.Event, now time.Time) {
	if !agg.matches[e.Type] {
		// A bound-but-uncorrelated type still flows to the queue directly.
		b.enqueueLocked(roleName, e)
		return
	}
	cid := e.CorrelationID
	if _, seen := agg.firstSeen[cid]; !seen {
		agg.firstSeen[cid] = now
	}
	agg.pending[cid] = append(agg.pending[cid], e)
	b.emit("info", "event_aggregate_add", "event added to aggregate", map[string]any{
		"role": roleName, "correlation": cid, "type": e.Type, "have": len(agg.pending[cid]),
	})
	if agg.spec.Completeness != nil && agg.spec.Completeness.Complete(agg.pending[cid]) {
		aggregated := event.Event{
			ID:            "aggregate:" + roleName + ":" + cid,
			Type:          "aggregate." + cid,
			Item:          item.Item{ID: cid, Type: "aggregate"},
			CorrelationID: cid,
			Source:        "aggregator:" + roleName,
			EmittedAt:     now,
		}
		b.enqueueLocked(roleName, aggregated)
		delete(agg.pending, cid)
		delete(agg.firstSeen, cid)
		b.emit("info", "event_aggregate_complete", "aggregate complete; emitted", map[string]any{
			"role": roleName, "correlation": cid, "condition": agg.spec.Completeness.Describe(),
		})
	}
}

// sweepAggregatesLocked evicts every incomplete aggregate whose oldest member is
// past TTL (Q4) — a half-collected aggregate that never completes is dropped and
// logged, so a missing sibling cannot wedge the correlator. Caller holds b.mu.
func (b *Bus) sweepAggregatesLocked(now time.Time) {
	if b.ttl <= 0 {
		return
	}
	roleNames := make([]string, 0, len(b.aggs))
	for name := range b.aggs {
		roleNames = append(roleNames, name)
	}
	sort.Strings(roleNames)
	for _, name := range roleNames {
		agg := b.aggs[name]
		cids := make([]string, 0, len(agg.firstSeen))
		for cid := range agg.firstSeen {
			cids = append(cids, cid)
		}
		sort.Strings(cids)
		for _, cid := range cids {
			if now.After(agg.firstSeen[cid].Add(b.ttl)) {
				b.emit("warn", "event_aggregate_expired", "incomplete aggregate expired (TTL)", map[string]any{
					"role": name, "correlation": cid, "have": len(agg.pending[cid]),
				})
				delete(agg.pending, cid)
				delete(agg.firstSeen, cid)
			}
		}
	}
}
