package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Source reasons the hydration budget adds [design 8.5].
const (
	// ReasonHydrationBudget marks a watched query whose added/changed
	// hydrations, or the sweep, were deferred because hydration.max_per_poll
	// ran out.
	ReasonHydrationBudget = "hydration_budget"
	// ReasonBackendDegraded marks the same when the detail backend answered
	// degraded several times in a row, so the rest was deferred.
	ReasonBackendDegraded = "hydration_backend_degraded"
)

// backendDownStreak is the number of consecutive degraded hydrations after
// which the backend counts as degraded/rate-limited and the rest of the poll's
// hydrations are deferred instead of retried inline. One degraded entity must
// not stop the others (it would otherwise starve the sweep forever, since a
// degraded read leaves hydrated_at untouched).
const backendDownStreak = 3

// casAttempts bounds the lost-race retries of the membership and
// source-terminal deactivation writes.
const casAttempts = 5

// errDegraded marks a hydration whose read was degraded (not an error).
var errDegraded = errors.New("degraded")

// queryRun is one watched query's listing outcome.
type queryRun struct {
	query string
	res   gather.ListChangesResult
	err   error
}

// pollState accumulates one call's hydration outcomes and the extra source
// notes the budget and failures produce.
type pollState struct {
	hydrated map[string]error    // entity id -> hydration failure (nil = ok)
	notes    map[string][]string // watched query -> extra degraded reasons
	all      []string            // reasons that apply to every watched-query row
	side     []string            // failures with no watched query to carry them
}

func (s *pollState) noteQueries(queries []string, reason string) {
	if len(queries) == 0 {
		s.all = appendUnique(s.all, reason)
		return
	}
	for _, q := range queries {
		s.notes[q] = appendUnique(s.notes[q], reason)
	}
}

// budget is hydration.max_per_poll for one call plus the degraded streak.
type budget struct {
	left   int
	streak int
}

// blocked returns why no further hydration may start, or "".
func (b *budget) blocked() string {
	switch {
	case b.left <= 0:
		return ReasonHydrationBudget
	case b.streak >= backendDownStreak:
		return ReasonBackendDegraded
	}
	return ""
}

func (b *budget) note(herr error) {
	b.left--
	if errors.Is(herr, errDegraded) {
		b.streak++
	} else {
		b.streak = 0
	}
}

func appendUnique(dst []string, vals ...string) []string {
	for _, v := range vals {
		dup := false
		for _, d := range dst {
			if d == v {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, v)
		}
	}
	return dst
}

func (e *Engine) repo() string {
	if e.Cfg == nil || len(e.Cfg.Repos) == 0 {
		return ""
	}
	return e.Cfg.Repos[0].Remote
}

// applyMembership folds each successfully listed query's added/changed/removed
// entries into its persisted watched set, then deactivates (as `removed`)
// every entity a query reported removed that NO configured query still
// returns. A query with a degraded backend contributes no removals: a source
// that failed to list an entity must not make it look removed [design 9.2,
// 10]. It returns the failures to surface (never fatal).
func (e *Engine) applyMembership(entityType string, runs []queryRun) []string {
	var failures []string
	configured := e.Cfg.WatchQueries(entityType)
	sets := make(map[string]map[string]bool, len(configured))
	for _, q := range configured {
		sets[q] = loadWatchSet(e.Store, entityType, q)
	}
	dirty := map[string]bool{}
	candidates := map[string]bool{}
	for _, r := range runs {
		set, ok := sets[r.query]
		if r.err != nil || !ok {
			continue
		}
		degraded := len(degradedBackendReasons(r.res.Sources)) > 0
		for _, c := range r.res.Changes {
			switch c.Change {
			case gather.ChangeAdded, gather.ChangeChanged:
				if !set[c.EntityID] {
					set[c.EntityID] = true
					dirty[r.query] = true
				}
			case gather.ChangeRemoved:
				if degraded {
					continue
				}
				if set[c.EntityID] {
					delete(set, c.EntityID)
					dirty[r.query] = true
				}
				candidates[c.EntityID] = true
			}
		}
	}
	for q := range dirty {
		if err := saveWatchSet(e.Store, entityType, q, sets[q]); err != nil {
			failures = append(failures, fmt.Sprintf("watched set %s: %v", q, err))
		}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		stillWatched := false
		for _, set := range sets {
			if set[id] {
				stillWatched = true
				break
			}
		}
		if stillWatched {
			continue
		}
		if err := e.deactivate(entityType, id, []string{string(classify.KindRemoved)}); err != nil {
			failures = append(failures, fmt.Sprintf("remove %s: %v", id, err))
		}
	}
	return failures
}

// deactivate sets an entity inactive through a compare-and-set write that
// keeps its snapshot and hydrated_at, appending a change_log row only when
// kinds is non-empty. An absent or already inactive entity is left alone.
func (e *Engine) deactivate(entityType, id string, kinds []string) error {
	return e.deactivateIf(entityType, id, kinds, nil)
}

// deactivateIf is deactivate that first asks guard (when non-nil) whether the
// freshly read row still warrants deactivation.
func (e *Engine) deactivateIf(entityType, id string, kinds []string, guard func(store.Entity) bool) error {
	at := e.now().UTC().Format(time.RFC3339)
	for attempt := 0; attempt < casAttempts; attempt++ {
		row, found, err := e.Store.GetEntity(e.repo(), entityType, id)
		if err != nil {
			return err
		}
		if !found || row.Inactive || (guard != nil && !guard(row)) {
			return nil
		}
		_, err = e.Store.WriteEntityStateWithLog(row, row.Version, row.HydratedAt, false, kinds, OriginPGConnector, at)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("still losing the version race after %d attempts: %w", casAttempts, store.ErrVersionConflict)
}

// deactivateIfTerminal sets a just-hydrated entity inactive when it is
// terminal in its source system (closed/merged PR, issue in a terminal state,
// thread quiet for the active window), so the sweep stops re-hydrating it.
// The classifier already logged closed/merged, so no record is appended.
func (e *Engine) deactivateIfTerminal(entityType, id string) error {
	window := e.Cfg.ThreadActiveWindow()
	now := e.now()
	return e.deactivateIf(entityType, id, nil, func(row store.Entity) bool {
		return classify.SourceTerminal(entityType, json.RawMessage(row.Facts), now, window)
	})
}

// pendingHydration is one added/changed entity waiting to be hydrated this
// call, with the watched queries that reported it (none for an entity carried
// over from an earlier poll).
type pendingHydration struct {
	deferredHydration
	queries []string
}

// hydratePending hydrates, within the budget and oldest-deferral first, what
// the queries reported added/changed plus the entities earlier polls deferred.
// What cannot be hydrated now (budget, degraded backend) or failed is queued
// for the next poll, because pg-connector will not report it again. It
// returns the ids it considered, so the sweep does not duplicate them.
func (e *Engine) hydratePending(ctx context.Context, opts Options, runs []queryRun, st *pollState, bud *budget) (map[string]bool, error) {
	pending := map[string]*pendingHydration{}
	var order []string
	add := func(id string, change gather.ChangeKind, attempts int, query string) {
		p, ok := pending[id]
		if !ok {
			p = &pendingHydration{deferredHydration: deferredHydration{ID: id, Change: change, Attempts: attempts}}
			pending[id] = p
			order = append(order, id)
		}
		if query != "" {
			p.queries = appendUnique(p.queries, query)
		}
	}
	queue := loadDeferred(e.Store, opts.EntityType)
	for _, d := range queue {
		add(d.ID, d.Change, d.Attempts, "")
	}
	for _, r := range runs {
		if r.err != nil {
			continue
		}
		for _, c := range r.res.Changes {
			if c.Change == gather.ChangeAdded || c.Change == gather.ChangeChanged {
				add(c.EntityID, c.Change, 0, r.query)
			}
		}
	}

	var next []deferredHydration
	for _, id := range order {
		p := pending[id]
		if herr, done := st.hydrated[id]; done { // a --reset replay already hydrated it
			if herr != nil {
				st.noteQueries(p.queries, fmt.Sprintf("hydrate %s: %s", id, firstErrLine(herr.Error())))
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("changes: %w", err)
		}
		if why := bud.blocked(); why != "" {
			next = append(next, p.deferredHydration)
			st.noteQueries(p.queries, why)
			continue
		}
		herr := e.hydrate(ctx, opts.EntityType, id, p.Change, OriginPGConnector, false)
		st.hydrated[id] = herr
		bud.note(herr)
		if herr == nil {
			continue
		}
		msg := fmt.Sprintf("hydrate %s: %s", id, firstErrLine(herr.Error()))
		if len(p.queries) == 0 {
			st.side = append(st.side, msg)
		} else {
			st.noteQueries(p.queries, msg)
		}
		p.Attempts++
		if p.Attempts >= maxDeferredAttempts {
			e.warnf("changes: giving up retrying %s %s after %d polls\n", opts.EntityType, id, p.Attempts)
			continue
		}
		next = append(next, p.deferredHydration)
	}
	if len(queue) > 0 || len(next) > 0 {
		if err := saveDeferred(e.Store, opts.EntityType, next); err != nil {
			e.warnf("changes: persist deferred hydrations: %v\n", err)
		}
	}
	return idSet(order), nil
}

func idSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// sweep re-hydrates up to sweep.max_per_poll ACTIVE entities of the type whose
// hydrated_at is older than sweep.max_age, oldest first, as reconcile (origin
// sweep) [design 8.4]. It draws on the same budget as the added/changed
// hydrations and runs after them, so a tight budget starves it first; when
// the budget or a degraded backend stops it short, every watched-query row is
// marked degraded because a sweep entity belongs to no single query.
func (e *Engine) sweep(ctx context.Context, entityType string, st *pollState, bud *budget, exclude map[string]bool) error {
	all, err := e.Store.ListEntities()
	if err != nil {
		return fmt.Errorf("changes: list entities: %w", err)
	}
	skip := map[string]bool{}
	for id := range exclude {
		skip[id] = true
	}
	for id := range st.hydrated {
		skip[id] = true
	}
	ids := selectSweep(all, entityType, e.now(), e.Cfg.SweepMaxAge(), e.Cfg.SweepMaxPerPoll(), skip)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("changes: %w", err)
		}
		if why := bud.blocked(); why != "" {
			st.noteQueries(nil, why)
			return nil
		}
		herr := e.hydrate(ctx, entityType, id, gather.ChangeSweep, OriginSweep, true)
		st.hydrated[id] = herr
		bud.note(herr)
		if herr != nil {
			st.side = append(st.side, fmt.Sprintf("sweep %s: %s", id, firstErrLine(herr.Error())))
		}
	}
	return nil
}
