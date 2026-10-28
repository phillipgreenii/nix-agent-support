package changes

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Baseline meta keys (pg-desk meta table). Both are keyed by the watched query
// text. The marker exists only once a query's baseline run has finished; the
// open key records the first poll that began it, so a baseline that cannot
// finish is visible as open for a long time.
const (
	// MetaBaselinePrefix prefixes change_flow.baseline.<query>: an RFC3339 UTC
	// time, written only in the poll in which the query's baseline finished.
	MetaBaselinePrefix = "change_flow.baseline."
	// MetaBaselineOpenPrefix prefixes change_flow.baseline_open.<query>: the
	// RFC3339 UTC time of the first poll that began the query's baseline,
	// written once.
	MetaBaselineOpenPrefix = "change_flow.baseline_open."
)

// BaselineState is what ReadBaseline finds for one watched query.
type BaselineState struct {
	// Done reports that the marker exists (the baseline run has finished).
	Done bool
	// At is the marker's time; zero unless Done.
	At time.Time
	// OpenSince is the time of the first poll that began the baseline; zero
	// unless HasOpenSince.
	OpenSince    time.Time
	HasOpenSince bool
}

// ReadBaseline reads query's baseline marker and open time. Both false/zero
// when neither key exists. An unparseable value reads as absent.
func ReadBaseline(st *store.Store, query string) (BaselineState, error) {
	var out BaselineState
	if t, ok, err := readMetaTime(st, MetaBaselinePrefix+query); err != nil {
		return BaselineState{}, err
	} else if ok {
		out.Done, out.At = true, t
	}
	if t, ok, err := readMetaTime(st, MetaBaselineOpenPrefix+query); err != nil {
		return BaselineState{}, err
	} else if ok {
		out.HasOpenSince, out.OpenSince = true, t
	}
	return out, nil
}

func readMetaTime(st *store.Store, key string) (time.Time, bool, error) {
	raw, found, err := st.GetMeta(key)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("changes: read %s: %w", key, err)
	}
	if !found {
		return time.Time{}, false, nil
	}
	t, perr := time.Parse(time.RFC3339Nano, raw)
	if perr != nil {
		return time.Time{}, false, nil
	}
	return t.UTC(), true, nil
}

// snapshotOnly is the hydration option of a baseline run: the snapshot is
// stored and no change_log row is appended.
func snapshotOnly(o *pipeline.EntityChangeOptions) { o.SnapshotOnly = true }

// baselineMember is one member of a baseline query, merged across the baseline
// queries that list it.
type baselineMember struct {
	id      string
	fp      string
	hasFP   bool
	queries []string
	ok      bool // marked watched (and, when needed, silently hydrated)
}

// baselineStep runs the silent baseline of every watched issue query that has
// no marker, and returns the runs hydrateListed may still use: the runs of the
// queries that are NOT in a baseline run this call. A query without a marker
// is in a baseline run: every member is marked watched and none appends a
// change_log row. A member pg-desk does not hold (or whose list_fp differs) is
// hydrated with the snapshot-only write, drawing on the same per-call budget as
// the listed hydrations; the marker is written only in the call in which the
// last unhydrated member was hydrated and the listing was complete, so a
// baseline of several polls stays silent. A member that fails or degrades is
// retried at every poll and keeps the marker unwritten. Other entity types
// have no baseline and pass through unchanged.
func (e *Engine) baselineStep(ctx context.Context, opts Options, runs []queryRun, st *pollState, bud *budget) ([]queryRun, error) {
	if opts.EntityType != issueEntityType {
		return runs, nil
	}
	var rest []queryRun
	var inBaseline []queryRun
	for _, r := range runs {
		if r.err != nil {
			rest = append(rest, r) // nothing was listed: hydrateListed skips it
			continue
		}
		state, err := ReadBaseline(e.Store, r.query)
		if err != nil {
			return nil, err
		}
		if state.Done {
			rest = append(rest, r)
			continue
		}
		inBaseline = append(inBaseline, r)
	}
	if len(inBaseline) == 0 {
		return rest, nil
	}

	nowText := e.now().UTC().Format(time.RFC3339)
	for _, r := range inBaseline {
		state, err := ReadBaseline(e.Store, r.query)
		if err != nil {
			return nil, err
		}
		if !state.HasOpenSince {
			if err := e.Store.SetMeta(MetaBaselineOpenPrefix+r.query, nowText); err != nil {
				st.side = append(st.side, fmt.Sprintf("baseline open %s: %v", r.query, err))
			}
		}
	}

	// Merge the members of every baseline query, in listing order.
	members := map[string]*baselineMember{}
	var order []string
	for _, r := range inBaseline {
		for _, ent := range r.res.Entities {
			if ent.Stale {
				continue // its fingerprint describes a cache, as in diffListed
			}
			m, ok := members[ent.ID]
			if !ok {
				m = &baselineMember{id: ent.ID}
				if fp := r.res.Fingerprints[ent.ID]; fp != "" {
					m.fp, m.hasFP = fp, true
				}
				members[ent.ID] = m
				order = append(order, ent.ID)
			}
			m.queries = appendUnique(m.queries, r.query)
		}
	}
	if st.baselineIDs == nil {
		st.baselineIDs = map[string]bool{}
	}
	for id := range members {
		st.baselineIDs[id] = true
	}

	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("changes: %w", err)
		}
		e.baselineMember(ctx, members[id], st, bud)
	}

	// Write the marker of every baseline query whose listing was complete and
	// whose members are all in.
	for _, r := range inBaseline {
		if !listComplete(r.res) {
			continue
		}
		done := true
		for _, ent := range r.res.Entities {
			if m := members[ent.ID]; m != nil && !m.ok {
				done = false
				break
			}
		}
		if !done {
			continue
		}
		if err := e.Store.SetMeta(MetaBaselinePrefix+r.query, nowText); err != nil {
			st.side = append(st.side, fmt.Sprintf("baseline marker %s: %v", r.query, err))
		}
	}
	return rest, nil
}

// baselineMember marks one member watched, first hydrating it silently when
// pg-desk does not hold it or its list_fp differs. m.ok is set only when the
// member ended up held and marked.
func (e *Engine) baselineMember(ctx context.Context, m *baselineMember, st *pollState, bud *budget) {
	row, found, err := e.Store.GetEntity(e.repo(), issueEntityType, m.id)
	if err != nil {
		st.noteQueries(m.queries, fmt.Sprintf("baseline %s: %s", m.id, firstErrLine(err.Error())))
		return
	}
	held := found && !row.Inactive
	needsHydration := !held || (m.hasFP && row.ListFP != m.fp)
	if herr, done := st.hydrated[m.id]; done { // a --reset replay already hydrated it
		if herr != nil {
			st.noteQueries(m.queries, fmt.Sprintf("hydrate %s: %s", m.id, firstErrLine(herr.Error())))
			return
		}
		needsHydration = false
	}
	if needsHydration {
		if why := bud.blocked(); why != "" {
			st.noteQueries(m.queries, why)
			return
		}
		change := gather.ChangeChanged
		if !held {
			change = gather.ChangeAdded
		}
		var listFP *string
		if m.hasFP {
			fp := m.fp
			listFP = &fp
		}
		res, herr := e.hydrateResult(ctx, issueEntityType, m.id, change, OriginPGConnector, false, listFP, snapshotOnly)
		st.hydrated[m.id] = herr
		bud.note(herr)
		if herr != nil {
			st.noteQueries(m.queries, fmt.Sprintf("hydrate %s: %s", m.id, firstErrLine(herr.Error())))
			return
		}
		if res.NotFound || !res.Written {
			st.noteQueries(m.queries, fmt.Sprintf("hydrate %s: nothing written", m.id))
			return
		}
	} else if row.Watched != nil && *row.Watched {
		m.ok = true
		return
	}
	yes := true
	if err := e.Store.SetEntityColumns(e.repo(), issueEntityType, m.id, nil, &yes); err != nil {
		st.noteQueries(m.queries, fmt.Sprintf("baseline mark %s: %s", m.id, firstErrLine(err.Error())))
		return
	}
	m.ok = true
}
