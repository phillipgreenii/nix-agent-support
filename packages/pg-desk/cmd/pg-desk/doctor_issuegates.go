package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	// doctorBaselineOpenGateSeconds is the age (one day, INV-ICD-21 item 2)
	// past which a baseline that is still not stored fails doctor.
	doctorBaselineOpenGateSeconds = 86400
	// doctorCursorExpiredWindow is how long a recorded cursor_expired: answer
	// keeps failing doctor.
	doctorCursorExpiredWindow = 24 * time.Hour
)

// The gate names: the lower-case name printed before the verdict and appended
// to the failure list.
const (
	gateNameDispatch        = "dispatch check"
	gateNameTwoBackends     = "issue served by two backends"
	gateNameNoWatchedQuery  = "backend without watched query"
	gateNameCursorExpired   = "feed cursor expired"
	gateNameFailedIngestAge = "failed ingest age"
	gateNameBaselineOpen    = "baseline open"
)

// doctorIssueCapabilityer and doctorIssueBaselineAges are the doctor's reads
// of the owning backends; the default is the gatherer through pg-connector.
var (
	doctorIssueCapabilityer = func(cfg *config.Config) changes.Capabilityer { return gather.NewGatherer(cfg, nil) }
	doctorIssueBaselineAges = func(ctx context.Context, cfg *config.Config, backend string) (map[string]int64, error) {
		return gather.NewGatherer(cfg, nil).BaselineOpenAges(ctx, "issue", backend)
	}
)

// memoCaps asks each backend once and remembers the answer or the error, so
// the gates that need a backend's capabilities share one call.
type memoCaps struct {
	g    changes.Capabilityer
	ans  map[string]gather.CapabilitiesAnswer
	errs map[string]error
}

func (m *memoCaps) Capabilities(ctx context.Context, entityType, backend string) (gather.CapabilitiesAnswer, error) {
	if err, ok := m.errs[backend]; ok {
		return gather.CapabilitiesAnswer{}, err
	}
	if a, ok := m.ans[backend]; ok {
		return a, nil
	}
	a, err := m.g.Capabilities(ctx, entityType, backend)
	if err != nil {
		m.errs[backend] = err
		return gather.CapabilitiesAnswer{}, err
	}
	m.ans[backend] = a
	return a, nil
}

// gateReport collects one gate's verdict and prints it.
type gateReport struct {
	w        io.Writer
	failures *[]string
}

func (r gateReport) ok(name string, detail string) {
	if detail != "" {
		fmt.Fprintf(r.w, "    %s: ok (%s)\n", name, detail)
		return
	}
	fmt.Fprintf(r.w, "    %s: ok\n", name)
}

func (r gateReport) fail(name string, what []string) {
	fmt.Fprintf(r.w, "    %s: FAIL (%s)\n", name, strings.Join(what, "; "))
	*r.failures = append(*r.failures, name)
}

// unreachable reports a gate whose daemon read answered unavailable: a
// degraded run, never a pass.
func (r gateReport) unreachable(name string, errs []string) {
	fmt.Fprintf(r.w, "    %s: UNREACHABLE (%s)\n", name, strings.Join(errs, "; "))
	*r.failures = append(*r.failures, name)
}

// doctorIssueGates runs the issue change-flow gates of the owning and the
// non-owning (beads) backends and prints one line per gate under an "issue change-flow
// gates:" heading, appending the name of every failing or unreachable gate to
// *failures:
//
//   - dispatch check: changes.CheckDispatch live against the capabilities
//     answer (an unavailable daemon is UNREACHABLE, not a mismatch);
//   - issue served by two backends: changes.ResolveIssueBackend over every
//     issue entity with a NULL backend column fails on ErrAmbiguousBackend;
//   - backend without watched query: an owning instance whose capabilities
//     report owns_changes true and no watched query, while active issue
//     entities carry that instance in their backend column; the same for a
//     non-owning instance (an entity is attributed by its backend column, else
//     by changes.ResolveIssueBackend), and an active issue entity attributable
//     to no backend while no non-owning backend declares a watched query;
//   - feed cursor expired: change_flow.feed_cursor_expired.<instance> younger
//     than 24 hours;
//   - failed ingest age: an entry whose first failure is older than
//     hydration.ingest_failed_max_age;
//   - baseline open: a query whose baseline has been open for more than one
//     day (issue health), naming the instance's failed-ingest entities; for a
//     non-owning (beads) query, the pg-desk baseline marker and the open time
//     (changes.ReadBaseline), naming the query's members that keep it open.
//
// Reports (doctorIssueReports) follow the gates and never fail.
func doctorIssueGates(ctx context.Context, w io.Writer, st *store.Store, cfg *config.Config, now time.Time, failures *[]string) {
	fmt.Fprintln(w, "  issue change-flow gates:")
	if cfg == nil {
		fmt.Fprintln(w, "    skipped (config unavailable)")
		return
	}
	rep := gateReport{w: w, failures: failures}
	owning := cfg.OwningInstances("issue")
	caps := &memoCaps{g: doctorIssueCapabilityer(cfg), ans: map[string]gather.CapabilitiesAnswer{}, errs: map[string]error{}}
	entities, lerr := st.ListEntities()

	gateDispatchCheck(ctx, rep, cfg, caps, owning)
	gateTwoBackendsServe(rep, cfg, st, entities, lerr)
	gateNoWatchedQueryBackend(ctx, rep, cfg, st, caps, owning, entities, lerr)
	gateFeedCursorExpired(rep, st, owning, now)
	gateFailedIngestAgeCheck(rep, cfg, st, now)
	gateBaselineOpenAge(ctx, rep, cfg, st, owning, now)
	doctorIssueReports(w, st, cfg, entities, lerr)
}

func gateDispatchCheck(ctx context.Context, rep gateReport, cfg *config.Config, caps *memoCaps, owning []string) {
	mismatches := changes.CheckDispatch(ctx, cfg, caps)
	var what, unreach []string
	for _, m := range mismatches {
		if m.Kind == changes.MismatchInstanceUnanswered && errors.Is(caps.errs[m.Instance], gather.ErrUnavailable) {
			unreach = append(unreach, fmt.Sprintf("backend %s: %v", m.Instance, caps.errs[m.Instance]))
			continue
		}
		what = append(what, m.String())
	}
	switch {
	case len(what) > 0:
		rep.fail(gateNameDispatch, what)
	case len(unreach) > 0:
		rep.unreachable(gateNameDispatch, unreach)
	case len(owning) == 0 && len(cfg.NonOwningInstances("issue")) == 0:
		rep.ok(gateNameDispatch, "no backend configured")
	default:
		rep.ok(gateNameDispatch, "")
	}
}

func gateTwoBackendsServe(rep gateReport, cfg *config.Config, st *store.Store, entities []store.Entity, lerr error) {
	if lerr != nil {
		rep.fail(gateNameTwoBackends, []string{fmt.Sprintf("list entities: %v", lerr)})
		return
	}
	var what []string
	for _, e := range entities {
		if e.EntityType != "issue" || e.Backend != "" {
			continue
		}
		if _, _, err := changes.ResolveIssueBackend(cfg, st, e.EntityID); err != nil {
			if errors.Is(err, changes.ErrAmbiguousBackend) {
				what = append(what, "issue "+e.EntityID+": "+err.Error())
			} else {
				what = append(what, fmt.Sprintf("issue %s: %v", e.EntityID, err))
			}
		}
	}
	if len(what) > 0 {
		rep.fail(gateNameTwoBackends, what)
		return
	}
	rep.ok(gateNameTwoBackends, "")
}

// nonOwningAttribution counts the ACTIVE issue entities served by a non-owning
// backend per instance (an entity is attributed by its backend column when set,
// else by changes.ResolveIssueBackend) and the active entities attributable to
// no backend at all. An entity of an owning instance, and an id served by two
// backends (the two-backends gate's finding), is counted by neither.
func nonOwningAttribution(cfg *config.Config, st *store.Store, entities []store.Entity) (byInst map[string]int, unattributed int) {
	owning := map[string]bool{}
	for _, inst := range cfg.OwningInstances("issue") {
		owning[inst] = true
	}
	byInst = map[string]int{}
	for _, e := range entities {
		if e.EntityType != "issue" || e.Inactive {
			continue
		}
		inst := e.Backend
		if inst == "" {
			var isOwning bool
			var err error
			inst, isOwning, err = changes.ResolveIssueBackend(cfg, st, e.EntityID)
			if err != nil || isOwning {
				continue
			}
		}
		switch {
		case owning[inst]:
		case inst == "":
			unattributed++
		default:
			byInst[inst]++
		}
	}
	return byInst, unattributed
}

func gateNoWatchedQueryBackend(ctx context.Context, rep gateReport, cfg *config.Config, st *store.Store, caps *memoCaps, owning []string, entities []store.Entity, lerr error) {
	if lerr != nil {
		rep.fail(gateNameNoWatchedQuery, []string{fmt.Sprintf("list entities: %v", lerr)})
		return
	}
	active := map[string]int{}
	for _, e := range entities {
		if e.EntityType == "issue" && !e.Inactive && e.Backend != "" {
			active[e.Backend]++
		}
	}
	nonOwningActive, unattributed := nonOwningAttribution(cfg, st, entities)
	nonOwning := map[string]bool{}
	for _, inst := range cfg.NonOwningInstances("issue") {
		nonOwning[inst] = true
	}
	for inst := range nonOwningActive {
		nonOwning[inst] = true
	}
	nonOwningInsts := make([]string, 0, len(nonOwning))
	for inst := range nonOwning {
		nonOwningInsts = append(nonOwningInsts, inst)
	}
	sort.Strings(nonOwningInsts)
	if len(owning) == 0 && len(nonOwningInsts) == 0 && unattributed == 0 {
		rep.ok(gateNameNoWatchedQuery, "no backend configured")
		return
	}

	var what, unreach []string
	for _, inst := range owning {
		ans, err := caps.Capabilities(ctx, "issue", inst)
		if err != nil {
			if errors.Is(err, gather.ErrUnavailable) {
				unreach = append(unreach, fmt.Sprintf("backend %s: %v", inst, err))
			}
			// Any other failure is the dispatch check's finding.
			continue
		}
		b, ok := ans.Backend(inst)
		if !ok || !b.OwnsChanges() {
			continue
		}
		if !capsDeclareWatched(b) && active[inst] > 0 {
			what = append(what, fmt.Sprintf("backend %s holds %d active issue entities and declares no watched query", inst, active[inst]))
		}
	}

	// A non-owning backend is read the same way; "declared" is whether ANY
	// non-owning backend that answered declares a watched query, and "unknown"
	// is a backend whose answer is the dispatch check's finding, so the
	// unattributed-entity rule is not concluded over it.
	declared, unknown := false, false
	for _, inst := range nonOwningInsts {
		ans, err := caps.Capabilities(ctx, "issue", inst)
		if err != nil {
			unknown = true
			if errors.Is(err, gather.ErrUnavailable) {
				unreach = append(unreach, fmt.Sprintf("backend %s: %v", inst, err))
			}
			continue
		}
		b, ok := ans.Backend(inst)
		if !ok {
			unknown = true
			continue
		}
		if capsDeclareWatched(b) {
			declared = true
			continue
		}
		if nonOwningActive[inst] > 0 {
			what = append(what, fmt.Sprintf("backend %s holds %d active issue entities and declares no watched query", inst, nonOwningActive[inst]))
		}
	}
	if unattributed > 0 && !declared && !unknown {
		what = append(what, fmt.Sprintf("%d active issue entities are attributable to no backend and no non-owning backend declares a watched query", unattributed))
	}
	switch {
	case len(what) > 0:
		rep.fail(gateNameNoWatchedQuery, what)
	case len(unreach) > 0:
		rep.unreachable(gateNameNoWatchedQuery, unreach)
	default:
		rep.ok(gateNameNoWatchedQuery, "")
	}
}

func capsDeclareWatched(b gather.CapBackend) bool {
	for _, q := range b.Queries {
		if q.Watched {
			return true
		}
	}
	return false
}

func gateFeedCursorExpired(rep gateReport, st *store.Store, owning []string, now time.Time) {
	var what []string
	for _, inst := range owning {
		v, found, err := st.GetMeta(changes.MetaFeedCursorExpiredPrefix + inst)
		if err != nil {
			what = append(what, fmt.Sprintf("backend %s: read %s: %v", inst, changes.MetaFeedCursorExpiredPrefix+inst, err))
			continue
		}
		if !found {
			continue
		}
		at, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			what = append(what, fmt.Sprintf("backend %s: unreadable cursor_expired time %q", inst, v))
			continue
		}
		if now.Sub(at) < doctorCursorExpiredWindow {
			what = append(what, fmt.Sprintf("backend %s: feed key expired at %s; run the dropped-key runbook (pg-desk issue refresh <id> of every watched entity of the backend)", inst, v))
		}
	}
	if len(what) > 0 {
		rep.fail(gateNameCursorExpired, what)
		return
	}
	rep.ok(gateNameCursorExpired, "")
}

func gateFailedIngestAgeCheck(rep gateReport, cfg *config.Config, st *store.Store, now time.Time) {
	bound := cfg.IngestFailedMaxAge()
	fails, err := st.ListIngestFailures("")
	if err != nil {
		rep.fail(gateNameFailedIngestAge, []string{fmt.Sprintf("list failed ingests: %v", err)})
		return
	}
	var what []string
	for _, f := range fails {
		at, perr := time.Parse(time.RFC3339Nano, f.FirstFailedAt)
		if perr != nil {
			what = append(what, fmt.Sprintf("backend %s entity %s: unreadable first_failed_at %q", f.Instance, f.EntityID, f.FirstFailedAt))
			continue
		}
		if age := now.Sub(at); age > bound {
			what = append(what, fmt.Sprintf("backend %s entity %s: failing since %s (%s, bound %s): %s",
				f.Instance, f.EntityID, f.FirstFailedAt, age.Round(time.Second), bound, f.LastError))
		}
	}
	if len(what) > 0 {
		rep.fail(gateNameFailedIngestAge, what)
		return
	}
	rep.ok(gateNameFailedIngestAge, fmt.Sprintf("%d entries, bound %s", len(fails), bound))
}

func gateBaselineOpenAge(ctx context.Context, rep gateReport, cfg *config.Config, st *store.Store, owning []string, now time.Time) {
	beadsQueries := cfg.NonOwningWatchQueries("issue")
	if len(owning) == 0 && len(beadsQueries) == 0 {
		rep.ok(gateNameBaselineOpen, "no backend configured")
		return
	}
	var what, unreach []string
	what = append(what, beadsBaselineOpen(st, cfg, beadsQueries, now)...)
	for _, inst := range owning {
		ages, err := doctorIssueBaselineAges(ctx, cfg, inst)
		if err != nil {
			if errors.Is(err, gather.ErrUnavailable) {
				unreach = append(unreach, fmt.Sprintf("backend %s: %v", inst, err))
			} else {
				what = append(what, fmt.Sprintf("backend %s: read health: %v", inst, err))
			}
			continue
		}
		queries := make([]string, 0, len(ages))
		for q := range ages {
			queries = append(queries, q)
		}
		sort.Strings(queries)
		for _, q := range queries {
			if ages[q] <= doctorBaselineOpenGateSeconds {
				continue
			}
			what = append(what, fmt.Sprintf("backend %s query %s: baseline open for %ds (limit %ds); failed-ingest entities: %s",
				inst, q, ages[q], doctorBaselineOpenGateSeconds, failedIngestEntities(st, inst)))
		}
	}
	switch {
	case len(what) > 0:
		rep.fail(gateNameBaselineOpen, what)
	case len(unreach) > 0:
		rep.unreachable(gateNameBaselineOpen, unreach)
	default:
		rep.ok(gateNameBaselineOpen, "")
	}
}

// beadsBaselineOpen reads the pg-desk baseline marker and open time of every
// non-owning watched query: a query with no marker whose baseline began more
// than a day ago fails, naming the query and the members that keep it open.
func beadsBaselineOpen(st *store.Store, cfg *config.Config, queries []string, now time.Time) []string {
	var what []string
	for _, q := range queries {
		state, err := changes.ReadBaseline(st, q)
		if err != nil {
			what = append(what, fmt.Sprintf("query %s: read baseline: %v", q, err))
			continue
		}
		if state.Done || !state.HasOpenSince {
			continue
		}
		age := int64(now.Sub(state.OpenSince).Seconds())
		if age <= doctorBaselineOpenGateSeconds {
			continue
		}
		inst := ""
		if b, ok := cfg.WatchQueryBackend("issue", q); ok {
			inst = b.Instance
		}
		what = append(what, fmt.Sprintf("backend %s query %s: baseline open for %ds (limit %ds); members keeping it open: %s",
			inst, q, age, doctorBaselineOpenGateSeconds, baselineOpenMembers(st, q)))
	}
	return what
}

// baselineOpenMembers names the members of the query's persisted watch set
// that are absent from the entity table, inactive or not watched, sorted, or
// "none".
func baselineOpenMembers(st *store.Store, query string) string {
	ids := changes.WatchSetIDs(st, "issue", query)
	if len(ids) == 0 {
		return "none"
	}
	entities, err := st.ListEntities()
	if err != nil {
		return fmt.Sprintf("unreadable (%v)", err)
	}
	byID := map[string]store.Entity{}
	for _, e := range entities {
		if e.EntityType == "issue" {
			byID[e.EntityID] = e
		}
	}
	var out []string
	for _, id := range ids {
		e, ok := byID[id]
		switch {
		case !ok:
			out = append(out, id+" (absent)")
		case e.Inactive:
			out = append(out, id+" (inactive)")
		case e.Watched == nil || !*e.Watched:
			out = append(out, id+" (not watched)")
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// failedIngestEntities names the distinct entities of instance's failed-ingest
// entries, sorted, or "none" (the members that keep a baseline from finishing).
func failedIngestEntities(st *store.Store, instance string) string {
	fails, err := st.ListIngestFailures(instance)
	if err != nil {
		return fmt.Sprintf("unreadable (%v)", err)
	}
	seen := map[string]bool{}
	var ids []string
	for _, f := range fails {
		if !seen[f.EntityID] {
			seen[f.EntityID] = true
			ids = append(ids, f.EntityID)
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// doctorIssueReports prints the three read-only issue change-flow reports
// under an "issue change-flow reports:" heading. A report line is information
// only: it never touches the failure list.
//
//   - watched entities per backend: the ACTIVE issue entities whose watched
//     mark is true, per backend (an entity is attributed by its backend column,
//     else by changes.ResolveIssueBackend; none when neither names one);
//   - members per watched query: the size of the query's persisted watch set
//     (an owning backend keeps no per-query set, so its line is the count of
//     watched active entities of the instance);
//   - an entity nothing watches: the ACTIVE issue entities that no watched
//     query's persisted set holds and whose watched mark is not true.
func doctorIssueReports(w io.Writer, st *store.Store, cfg *config.Config, entities []store.Entity, lerr error) {
	fmt.Fprintln(w, "  issue change-flow reports:")
	if lerr != nil {
		fmt.Fprintf(w, "    unavailable (list entities: %v)\n", lerr)
		return
	}
	held := map[string]bool{}
	for _, q := range cfg.WatchQueries("issue") {
		for _, id := range changes.WatchSetIDs(st, "issue", q) {
			held[id] = true
		}
	}
	watchedPer := map[string]int{}
	nothingWatches := 0
	for _, e := range entities {
		if e.EntityType != "issue" || e.Inactive {
			continue
		}
		isWatched := e.Watched != nil && *e.Watched
		if isWatched {
			inst := e.Backend
			if inst == "" {
				if resolved, _, err := changes.ResolveIssueBackend(cfg, st, e.EntityID); err == nil {
					inst = resolved
				}
			}
			if inst == "" {
				inst = "none"
			}
			watchedPer[inst]++
		} else if !held[e.EntityID] {
			nothingWatches++
		}
	}

	fmt.Fprintln(w, "    watched active issue entities per backend:")
	if len(watchedPer) == 0 {
		fmt.Fprintln(w, "      none")
	}
	backends := make([]string, 0, len(watchedPer))
	for b := range watchedPer {
		backends = append(backends, b)
	}
	sort.Strings(backends)
	for _, b := range backends {
		fmt.Fprintf(w, "      %s: %d\n", b, watchedPer[b])
	}

	fmt.Fprintln(w, "    members per watched query:")
	queries := cfg.WatchQueries("issue")
	if len(queries) == 0 {
		fmt.Fprintln(w, "      none")
	}
	for _, q := range queries {
		b, ok := cfg.WatchQueryBackend("issue", q)
		switch {
		case ok && b.OwnsChanges:
			fmt.Fprintf(w, "      %s: %d (owning backend %s: watched active entities of the backend)\n", q, watchedPer[b.Instance], b.Instance)
		default:
			fmt.Fprintf(w, "      %s: %d\n", q, len(changes.WatchSetIDs(st, "issue", q)))
		}
	}

	fmt.Fprintf(w, "    active issue entities nothing watches: %d (an entity nothing watches)\n", nothingWatches)
}
