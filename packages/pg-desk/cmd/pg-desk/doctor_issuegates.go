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

// doctorIssueGates runs the issue change-flow gates of the owning backends
// (Jira values only) and prints one line per gate under an "issue change-flow
// gates:" heading, appending the name of every failing or unreachable gate to
// *failures:
//
//   - dispatch check: changes.CheckDispatch live against the capabilities
//     answer (an unavailable daemon is UNREACHABLE, not a mismatch);
//   - issue served by two backends: changes.ResolveIssueBackend over every
//     issue entity with a NULL backend column fails on ErrAmbiguousBackend;
//   - backend without watched query: an owning instance whose capabilities
//     report owns_changes true and no watched query, while active issue
//     entities carry that instance in their backend column;
//   - feed cursor expired: change_flow.feed_cursor_expired.<instance> younger
//     than 24 hours;
//   - failed ingest age: an entry whose first failure is older than
//     hydration.ingest_failed_max_age;
//   - baseline open: a query whose baseline has been open for more than one
//     day (issue health), naming the instance's failed-ingest entities.
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
	gateNoWatchedQueryBackend(ctx, rep, caps, owning, entities, lerr)
	gateFeedCursorExpired(rep, st, owning, now)
	gateFailedIngestAgeCheck(rep, cfg, st, now)
	gateBaselineOpenAge(ctx, rep, cfg, st, owning)
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
	case len(owning) == 0:
		rep.ok(gateNameDispatch, "no owning backend configured")
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

func gateNoWatchedQueryBackend(ctx context.Context, rep gateReport, caps *memoCaps, owning []string, entities []store.Entity, lerr error) {
	if len(owning) == 0 {
		rep.ok(gateNameNoWatchedQuery, "no owning backend configured")
		return
	}
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
		watched := false
		for _, q := range b.Queries {
			watched = watched || q.Watched
		}
		if !watched && active[inst] > 0 {
			what = append(what, fmt.Sprintf("backend %s holds %d active issue entities and declares no watched query", inst, active[inst]))
		}
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

func gateBaselineOpenAge(ctx context.Context, rep gateReport, cfg *config.Config, st *store.Store, owning []string) {
	if len(owning) == 0 {
		rep.ok(gateNameBaselineOpen, "no owning backend configured")
		return
	}
	var what, unreach []string
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
