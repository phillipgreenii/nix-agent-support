package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// membershipRow is the row the runner hands the engine for one arrival or
// departure; it is the engine's own type.
type membershipRow = engine.Membership

// departure is a previous member the new complete listing no longer shows.
type departure struct {
	id, ref string
}

// membershipPlan is the arrivals and the departure candidates of one run.
type membershipPlan struct {
	arrivals   []Member
	departures []departure
}

// planMembership compares the previous row with the new listing by stable id.
//
// It runs BEFORE the run's alias writes (they move a renamed member's alias in
// place, after which the old reference no longer resolves) and outside any
// transaction (the alias lookups read the store). prev is the previous stored
// row; a plan is empty without a baseline (the first complete run IS the
// baseline), and arrivals are planned only when the previous listing was
// complete and departures only when the new one is.
func (r *Runner) planMembership(ctx context.Context, key Key, prev store.QueryRow, l Listing) (membershipPlan, error) {
	var plan membershipPlan
	if prev.BaselineAt.IsZero() {
		return plan, nil
	}
	newIDs := make(map[string]bool, len(l.Members))
	newRefs := make(map[string]bool, len(l.Members))
	for _, m := range l.Members {
		newIDs[m.ID] = true
		newRefs[r.fold(m.Ref)] = true
	}
	prevIDs := make(map[string]bool, len(prev.IDs))
	prevRefs := make(map[string]bool, len(prev.IDs))
	for _, id := range prev.IDs {
		prevRefs[r.fold(id)] = true
	}
	// One batched read resolves every previous member: a per-member lookup can
	// scan the whole alias table once each (pg2-6cwpf).
	var known map[string]Alias
	if len(prev.IDs) > 0 {
		var err error
		if known, err = r.cfg.Aliases.Resolve(ctx, prev.IDs); err != nil {
			return membershipPlan{}, err
		}
	}
	var gone []departure
	for _, ref := range prev.IDs {
		a, ok := known[ref]
		if !ok {
			r.log("query_membership_no_alias", "query", key.Name, "id", ref)
			continue
		}
		if a.ID == "" || prevIDs[a.ID] {
			continue
		}
		prevIDs[a.ID] = true
		gone = append(gone, departure{id: a.ID, ref: a.CanonicalRef})
	}
	if prev.Complete {
		for _, m := range l.Members {
			if prevIDs[m.ID] || prevRefs[r.fold(m.Ref)] {
				continue
			}
			plan.arrivals = append(plan.arrivals, m)
		}
	}
	if l.Complete {
		for _, d := range gone {
			if newIDs[d.id] || newRefs[r.fold(d.ref)] {
				continue
			}
			plan.departures = append(plan.departures, d)
		}
	}
	return plan, nil
}

// confirmDepartures makes the one confirming read of every departure candidate
// and returns the departure rows to append: a Gone departure appends nothing here
// (the entity's own tombstone path logs the removal). It fails, and so fails the
// run, on the first confirmation that fails, and when there is no confirmer to
// ask: a departure is never dropped and never logged unconfirmed.
func (r *Runner) confirmDepartures(ctx context.Context, key Key, deps []departure) ([]membershipRow, error) {
	var out []membershipRow
	for _, d := range deps {
		if r.cfg.Confirmer == nil {
			return nil, fmt.Errorf("query %q: departure of %s cannot be confirmed: no confirmer is configured", key.Name, d.ref)
		}
		c, err := r.cfg.Confirmer.Confirm(ctx, d.id, d.ref)
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
		out = append(out, membershipRow{
			Kind: r.cfg.Spec.Roles.LeftQuery, Query: key.Name, ID: d.id, Ref: d.ref,
			Extra: []engine.Field{{Name: r.cfg.StateFieldPrefix + key.Name, After: state}},
		})
	}
	return out, nil
}

// watchPlan says how the run marks its members watched. The zero value marks
// none.
type watchPlan struct {
	// via is the cause recorded with the flag (registry.ViaQuery or
	// registry.ViaAdHoc followed by the query name).
	via string
	// mode is engine.WatchBaseline for the baseline run of a configured watched
	// query and engine.WatchLate for every other run that marks.
	mode engine.WatchMode
}

// on reports whether the run marks its members.
func (w watchPlan) on() bool { return w.mode != 0 }

// watchPlanFor decides how the run of key marks its members. A configured key of
// a query the settings mark watched marks them as that query's own members, in
// baseline mode when the stored row has no baseline yet (prev is the row before
// this run). Any other key marks them only when the caller asked for the watch
// option (an ad-hoc run), always late: such a key has no baseline. A runner with
// no Changes marks nothing.
func (r *Runner) watchPlanFor(key Key, prev store.QueryRow, option bool) watchPlan {
	if r.cfg.Changes == nil {
		return watchPlan{}
	}
	switch {
	case !key.Adhoc() && r.cfg.Settings.Watched(key.Name):
		if prev.BaselineAt.IsZero() {
			return watchPlan{via: registry.ViaQuery + key.Name, mode: engine.WatchBaseline}
		}
		return watchPlan{via: registry.ViaQuery + key.Name, mode: engine.WatchLate}
	case option:
		return watchPlan{via: registry.ViaAdHoc + key.Name, mode: engine.WatchLate}
	}
	return watchPlan{}
}

// membershipRows is the arrivals' rows followed by the departures' rows. An
// arrival row carries the run's watch (none when the plan is off).
func (r *Runner) membershipRows(key Key, arrivals []Member, left []membershipRow, w watchPlan) []membershipRow {
	rows := make([]membershipRow, 0, len(arrivals)+len(left))
	for _, m := range arrivals {
		row := membershipRow{
			Kind: r.cfg.Spec.Roles.EnteredQuery, Query: key.Name, ID: m.ID, Ref: m.Ref,
		}
		if w.on() {
			row.Watch, row.Via = w.mode, w.via
		}
		rows = append(rows, row)
	}
	return append(rows, left...)
}

// markOthers marks, through engine.Watch, every listed member that no arrival
// row of rows already marks. It runs inside the run's transaction; the runner
// appends no row itself (a late watch of a known entity appends through the
// engine, under the engine's emission rule).
func (r *Runner) markOthers(ctx context.Context, tx *store.Tx, members []Member, rows []membershipRow, w watchPlan) error {
	marked := make(map[string]bool, len(rows))
	for _, m := range rows {
		if m.Watch != 0 {
			marked[m.ID] = true
		}
	}
	for _, m := range members {
		if marked[m.ID] {
			continue
		}
		marked[m.ID] = true
		if _, err := r.cfg.Changes.Watch(ctx, tx, engine.WatchRequest{ID: m.ID, Ref: m.Ref, Via: w.via, Mode: w.mode}); err != nil {
			return err
		}
	}
	return nil
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
