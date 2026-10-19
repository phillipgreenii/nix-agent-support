package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Changes is the part of the change engine the runner uses to record a
// membership change; *changes.Engine satisfies it. The call runs inside the
// transaction that stores the run's result, so the row and the result are
// visible together or not at all.
type Changes interface {
	AppendMembership(ctx context.Context, tx *store.Tx, m changes.Membership) error
}

var _ Changes = (*changes.Engine)(nil)

// Confirmation is what one confirming entity read found: the PR's stored state
// (GitHub's spelling: OPEN, MERGED or CLOSED) or Gone when the read found the PR
// gone (the entity is tombstoned).
type Confirmation struct {
	State string
	Gone  bool
}

// Confirmer makes the one entity read that tells a PR that closed or merged
// apart from one that was merely filtered out of a query. node is the entity key
// and ref the wire reference. Any failure, a governor refusal included, is an
// error: the departure is then retried by the next run.
type Confirmer interface {
	Confirm(ctx context.Context, node, ref string) (Confirmation, error)
}

// StateFieldPrefix starts the name of the extra field a left_query row carries:
// the field "query_state:<query>" whose After is the confirming read's State (a
// JSON string such as "MERGED"), so a consumer can tell a PR that closed or
// merged from one that was filtered out of the query.
const StateFieldPrefix = "query_state:"

// departure is a previous member the new complete listing no longer shows.
type departure struct {
	node, ref string
}

// membershipPlan is the arrivals and the departure candidates of one run.
type membershipPlan struct {
	arrivals   []member
	departures []departure
}

// log reports an event the run survives.
func (r *Runner) log(event string, kv ...any) {
	if r.cfg.Log != nil {
		r.cfg.Log(event, kv...)
	}
}

// refFold is a wire reference with owner/repo folded to lower case and the
// number exact, the form two references of one PR compare equal in.
func refFold(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}

// planMembership compares the previous row with the new listing by node id.
//
// It runs BEFORE the run's alias writes (they move a renamed PR's alias in
// place, after which the old reference no longer resolves) and outside any
// transaction (the alias lookups read the store). prev is the previous stored
// row; a plan is empty without a baseline (the first complete run IS the
// baseline), and arrivals are planned only when the previous listing was
// complete and departures only when the new one is.
func (r *Runner) planMembership(ctx context.Context, key Key, prev store.QueryRow, l listing) (membershipPlan, error) {
	var plan membershipPlan
	if prev.BaselineAt.IsZero() {
		return plan, nil
	}
	newNodes := make(map[string]bool, len(l.members))
	newRefs := make(map[string]bool, len(l.members))
	for _, m := range l.members {
		newNodes[m.nodeID] = true
		newRefs[refFold(m.ref.String())] = true
	}
	prevNodes := make(map[string]bool, len(prev.IDs))
	prevRefs := make(map[string]bool, len(prev.IDs))
	// One batched read resolves every previous member: the alias lookup folds
	// the reference's case, which cannot use the table's index, so a lookup per
	// previous member would scan the whole alias table once each (pg2-6cwpf).
	type parsed struct {
		id  string
		ref entity.Ref
	}
	var ids []parsed
	var refs []entity.Ref
	for _, id := range prev.IDs {
		prevRefs[refFold(id)] = true
		ref, err := entity.ParseRef(id)
		if err != nil {
			r.log("query_membership_unparsable_id", "query", key.Name, "id", id, "error", err.Error())
			continue
		}
		ids = append(ids, parsed{id: id, ref: ref})
		refs = append(refs, ref)
	}
	var known map[string]entity.KnownAlias
	if len(refs) > 0 {
		var err error
		if known, err = r.cfg.Identity.NodeIDs(ctx, refs); err != nil {
			return membershipPlan{}, err
		}
	}
	var gone []departure
	for _, p := range ids {
		a, ok := known[p.ref.Key()]
		if !ok {
			r.log("query_membership_no_alias", "query", key.Name, "id", p.id)
			continue
		}
		if prevNodes[a.NodeID] {
			continue
		}
		prevNodes[a.NodeID] = true
		gone = append(gone, departure{node: a.NodeID, ref: a.Canonical.String()})
	}
	if prev.Complete {
		for _, m := range l.members {
			if prevNodes[m.nodeID] || prevRefs[refFold(m.ref.String())] {
				continue
			}
			plan.arrivals = append(plan.arrivals, m)
		}
	}
	if l.complete {
		for _, d := range gone {
			if newNodes[d.node] || newRefs[refFold(d.ref)] {
				continue
			}
			plan.departures = append(plan.departures, d)
		}
	}
	return plan, nil
}

// confirmDepartures makes the one confirming read of every departure candidate
// and returns the left_query rows to append: a Gone departure appends nothing
// here (the entity's own tombstone path logs removed). It fails, and so fails
// the run, on the first confirmation that fails, and when there is no confirmer
// to ask: a departure is never dropped and never logged unconfirmed.
func (r *Runner) confirmDepartures(ctx context.Context, key Key, deps []departure) ([]changes.Membership, error) {
	var out []changes.Membership
	for _, d := range deps {
		if r.cfg.Confirm == nil {
			return nil, fmt.Errorf("query %q: departure of %s cannot be confirmed: no confirmer is configured", key.Name, d.ref)
		}
		c, err := r.cfg.Confirm.Confirm(ctx, d.node, d.ref)
		if err != nil {
			return nil, fmt.Errorf("query %q: confirm departure of %s: %w", key.Name, d.ref, err)
		}
		if c.Gone {
			continue
		}
		state, err := json.Marshal(c.State)
		if err != nil {
			return nil, err
		}
		out = append(out, changes.Membership{
			Kind: classify.KindLeftQuery, Query: key.Name, Node: d.node, Ref: d.ref,
			Extra: []classify.Field{{Name: StateFieldPrefix + key.Name, After: state}},
		})
	}
	return out, nil
}

// membershipRows is the arrivals' entered_query rows followed by the departures'
// left_query rows.
func membershipRows(key Key, arrivals []member, left []changes.Membership) []changes.Membership {
	rows := make([]changes.Membership, 0, len(arrivals)+len(left))
	for _, m := range arrivals {
		rows = append(rows, changes.Membership{
			Kind: classify.KindEnteredQuery, Query: key.Name, Node: m.nodeID, Ref: m.ref.String(),
		})
	}
	return append(rows, left...)
}

// baselineFor is the row's baseline: the previous one when it has one (never
// cleared), else now when this run is a complete one, else none.
func baselineFor(prev store.QueryRow, complete bool, now time.Time) time.Time {
	switch {
	case !prev.BaselineAt.IsZero():
		return prev.BaselineAt
	case complete:
		return now
	}
	return time.Time{}
}
