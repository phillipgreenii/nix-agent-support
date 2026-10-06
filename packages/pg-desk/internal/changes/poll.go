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
	// hydrations, or the sweep, were left for the next tick because
	// hydration.max_per_poll ran out.
	ReasonHydrationBudget = "hydration_budget"
	// ReasonBackendDegraded marks the same when the detail backend answered
	// degraded several times in a row, so the rest was left for the next tick.
	ReasonBackendDegraded = "hydration_backend_degraded"
	// ReasonListTruncated marks a watched query whose listing a backend
	// truncated: it removes nothing and is only an add-only membership view.
	ReasonListTruncated = "list_truncated"
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
	res   gather.ListFingerprintsResult
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

// listComplete reports whether a query's listing may be trusted as the whole
// current membership: at least one backend succeeded, none is degraded
// (disabled counts as healthy) and nothing was truncated. Only a complete
// listing may create removal candidates or shrink a persisted watched set.
func listComplete(res gather.ListFingerprintsResult) bool {
	if res.Truncated || len(degradedBackendReasons(res.Sources)) > 0 {
		return false
	}
	for _, s := range res.Sources {
		if s.Status == "succeeded" {
			return true
		}
	}
	return false
}

// applyMembership folds each listed query's membership into its persisted
// watched set (meta change_flow.watchset.<type>.<query>) and removes the
// entities no configured query lists any more. A complete listing (see
// listComplete) rewrites its query's persisted set to exactly the listed ids;
// a degraded or truncated one only ever ADDS ids and never produces a removal
// candidate, because a source that failed to list an entity must not make it
// look gone. A removal candidate is an id that was in a query's persisted set,
// is absent from that query's complete listing and is held by NO other
// configured query: the other-query test reads the persisted set of EVERY
// configured query, not only the queries run this call (--query Q runs one).
// Each candidate is first CONFIRMED by exactly one `show` read through the
// hydrate path (see confirmRemoval) and then deactivated: logged closed or
// merged when the read shows it terminal, removed when it is still open or
// not found. A candidate is dropped from the persisted sets only AFTER its
// confirmation and deactivation succeeded, so a failed read or deactivation
// leaves it in the persisted set and it is found again next tick. It returns
// the failures to surface (never fatal).
func (e *Engine) applyMembership(entityType string, runs []queryRun) []string {
	return e.applyMembershipCtx(context.Background(), entityType, runs)
}

// applyMembershipCtx is applyMembership under the caller's context, which the
// confirmation reads honour.
func (e *Engine) applyMembershipCtx(ctx context.Context, entityType string, runs []queryRun) []string {
	var failures []string
	configured := e.Cfg.WatchQueries(entityType)
	persisted := make(map[string]map[string]bool, len(configured))
	next := make(map[string]map[string]bool, len(configured))
	for _, q := range configured {
		persisted[q] = loadWatchSet(e.Store, entityType, q)
		next[q] = cloneSet(persisted[q])
	}
	complete := map[string]map[string]bool{}
	dirty := map[string]bool{}
	for _, r := range runs {
		if r.err != nil {
			continue
		}
		if _, ok := next[r.query]; !ok {
			continue
		}
		listed := make(map[string]bool, len(r.res.Entities))
		for _, ent := range r.res.Entities {
			listed[ent.ID] = true
		}
		if listComplete(r.res) {
			complete[r.query] = listed
			next[r.query] = cloneSet(listed)
		} else {
			for id := range listed {
				next[r.query][id] = true
			}
		}
		if !sameSet(persisted[r.query], next[r.query]) {
			dirty[r.query] = true
		}
	}

	candidates := map[string]bool{}
	for q, listed := range complete {
		for id := range persisted[q] {
			if listed[id] {
				continue
			}
			held := false
			for other, set := range next {
				if other != q && set[id] {
					held = true
					break
				}
			}
			if !held {
				candidates[id] = true
			}
		}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// keep leaves id in every persisted set that held it so the next tick
	// finds it again.
	keep := func(id string) {
		for q := range complete {
			if persisted[q][id] {
				next[q][id] = true
			}
		}
	}
	// Confirmation reads draw on no budget: each candidate costs exactly one
	// read, they run before the hydration budget exists, and a backend that
	// answers degraded backendDownStreak times in a row ends the reads for
	// this tick (the rest are left untouched and found again next tick).
	streak := 0
	for i, id := range ids {
		if streak >= backendDownStreak {
			failures = append(failures, fmt.Sprintf("remove %s: %s", id, ReasonBackendDegraded))
			keep(id)
			continue
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, fmt.Sprintf("remove %s: %v", id, err))
			for _, rest := range ids[i:] {
				keep(rest)
			}
			break
		}
		err := e.confirmRemoval(ctx, entityType, id)
		if errors.Is(err, errDegraded) {
			streak++
		} else {
			streak = 0
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("remove %s: %v", id, firstErrLine(err.Error())))
			keep(id)
		}
	}
	for q := range dirty {
		if sameSet(persisted[q], next[q]) {
			continue
		}
		if err := saveWatchSet(e.Store, entityType, q, next[q]); err != nil {
			failures = append(failures, fmt.Sprintf("watched set %s: %v", q, err))
		}
	}
	return failures
}

// confirmRemoval confirms one removal candidate with ONE `show` read through
// the hydrate path (change kind removed, nil list fingerprint, so list_fp is
// never moved by it) and then deactivates it. An entity the store does not
// hold or that is already inactive needs no read and nothing is written.
//
//   - the read shows it terminal: the classifier has logged closed or merged
//     and the entity is deactivated with no further record;
//   - the source reports not_found: nothing was written, one removed record;
//   - the read shows it still open (it merely left a query): one removed
//     record;
//   - the read fails or degrades: nothing is written and the error is
//     returned, so the caller leaves membership untouched.
func (e *Engine) confirmRemoval(ctx context.Context, entityType, id string) error {
	row, found, err := e.Store.GetEntity(e.repo(), entityType, id)
	if err != nil {
		return err
	}
	if !found || row.Inactive {
		return nil
	}
	res, err := e.hydrateResult(ctx, entityType, id, gather.ChangeRemoved, OriginPGConnector, false, nil)
	if err != nil {
		return fmt.Errorf("confirmation read: %w", err)
	}
	removed := []string{string(classify.KindRemoved)}
	if res.NotFound {
		return e.deactivate(entityType, id, removed)
	}
	row, found, err = e.Store.GetEntity(e.repo(), entityType, id)
	if err != nil {
		return err
	}
	if !found || row.Inactive {
		return nil // terminal: the hydrate path deactivated it
	}
	if classify.SourceTerminal(entityType, json.RawMessage(row.Facts), e.now(), e.Cfg.ThreadActiveWindow()) {
		// The classifier logged closed/merged but hydrate's own deactivation
		// failed: retry it without a second record.
		return e.deactivate(entityType, id, nil)
	}
	return e.deactivate(entityType, id, removed)
}

func cloneSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for id := range in {
		out[id] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
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

// listedEntity is one entity the watched queries listed this tick, merged
// across queries, with the diff verdict against the stored row.
type listedEntity struct {
	id      string
	change  gather.ChangeKind
	fp      string
	hasFP   bool
	queries []string
	// hydratedAt orders the changed entities oldest mismatch first.
	hydratedAt time.Time
	hydrated   bool
}

// diffListed merges the successful listings of runs and keeps the entities
// whose list fingerprint differs from the stored one. Per listed entity:
// added = no row or an inactive row; changed = an active row whose list_fp
// differs from the fingerprint observed in this listing; otherwise unchanged.
// The comparison is ALWAYS list against list, never against show output. An
// entity the connector marks stale in a listing is ignored for change
// detection there (its fingerprint describes a cache, not the live entity).
// The result is ordered changed first, oldest mismatch first (never hydrated
// counts as oldest, ties by id), then added by id.
func (e *Engine) diffListed(entityType string, runs []queryRun) ([]*listedEntity, error) {
	all, err := e.Store.ListEntities()
	if err != nil {
		return nil, fmt.Errorf("changes: list entities: %w", err)
	}
	rows := map[string]store.Entity{}
	for _, ent := range all {
		if ent.EntityType == entityType {
			rows[ent.EntityID] = ent
		}
	}

	merged := map[string]*listedEntity{}
	var order []string
	for _, r := range runs {
		if r.err != nil {
			continue
		}
		for _, ent := range r.res.Entities {
			if ent.Stale {
				continue
			}
			le, ok := merged[ent.ID]
			if !ok {
				le = &listedEntity{id: ent.ID}
				if fp := r.res.Fingerprints[ent.ID]; fp != "" {
					le.fp, le.hasFP = fp, true
				}
				merged[ent.ID] = le
				order = append(order, ent.ID)
			}
			le.queries = appendUnique(le.queries, r.query)
		}
	}

	var changed, added []*listedEntity
	for _, id := range order {
		le := merged[id]
		row, found := rows[id]
		switch {
		case !found || row.Inactive:
			le.change = gather.ChangeAdded
			added = append(added, le)
		case le.hasFP && row.ListFP != le.fp:
			le.change = gather.ChangeChanged
			le.hydratedAt, le.hydrated = hydratedTime(row)
			changed = append(changed, le)
		}
	}
	sort.SliceStable(changed, func(i, j int) bool {
		a, b := changed[i], changed[j]
		if a.hydrated != b.hydrated {
			return !a.hydrated // never hydrated is oldest
		}
		if !a.hydratedAt.Equal(b.hydratedAt) {
			return a.hydratedAt.Before(b.hydratedAt)
		}
		return a.id < b.id
	})
	sort.SliceStable(added, func(i, j int) bool { return added[i].id < added[j].id })
	return append(changed, added...), nil
}

// hydrateListed hydrates, within the budget, what diffListed found different.
// Each hydration passes the fingerprint observed in THIS tick's listing so it
// is written in the same transaction as the snapshot; a hydration that fails,
// degrades or is cut by the budget writes nothing, leaving the entity
// different, so the next tick finds it again the same way. Nothing is queued
// or counted: pg-router's schedule is the only retry. It returns the ids it
// considered, so the sweep does not duplicate them.
func (e *Engine) hydrateListed(ctx context.Context, opts Options, runs []queryRun, st *pollState, bud *budget) (map[string]bool, error) {
	pending, err := e.diffListed(opts.EntityType, runs)
	if err != nil {
		return nil, err
	}
	considered := make(map[string]bool, len(pending))
	for _, p := range pending {
		considered[p.id] = true
		if herr, done := st.hydrated[p.id]; done { // a --reset replay already hydrated it
			if herr != nil {
				st.noteQueries(p.queries, fmt.Sprintf("hydrate %s: %s", p.id, firstErrLine(herr.Error())))
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("changes: %w", err)
		}
		if why := bud.blocked(); why != "" {
			st.noteQueries(p.queries, why)
			continue
		}
		var listFP *string
		if p.hasFP {
			fp := p.fp
			listFP = &fp
		}
		herr := e.hydrate(ctx, opts.EntityType, p.id, p.change, OriginPGConnector, false, listFP)
		st.hydrated[p.id] = herr
		bud.note(herr)
		if herr != nil {
			st.noteQueries(p.queries, fmt.Sprintf("hydrate %s: %s", p.id, firstErrLine(herr.Error())))
		}
	}
	return considered, nil
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
		herr := e.hydrate(ctx, entityType, id, gather.ChangeSweep, OriginSweep, true, nil)
		st.hydrated[id] = herr
		bud.note(herr)
		if herr != nil {
			st.side = append(st.side, fmt.Sprintf("sweep %s: %s", id, firstErrLine(herr.Error())))
		}
	}
	return nil
}
