package focus

import (
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Coverage: whether the candidate sources the rank read were complete. It
// reads only the store: the persisted per-query listing status (the
// change_flow.listing keys), the active count and due backlog of the watched
// types (the shared changes.ActiveCount and changes.DueBacklog helpers) and
// the persisted watch-set membership. No tracker is called.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; the verbs print the report and the doctor and metrics
// packets read the same persisted facts.

// StatusUnknown is the Listing status of a configured watch query whose
// listing status was never persisted (it has not run, or its meta key is
// unreadable). It counts as incomplete: a source that may be missing is shown
// as incomplete, never as complete.
const StatusUnknown = "unknown"

// StatusNotWatched is the Listing status of a configured watch query of a
// daemonOwnedTypes type while no change flow is wired for that type (no
// consumer of the type is registered, so nothing has ever listed it). It is
// explicit rather than unknown, and it does not make coverage incomplete: the
// query is simply not watched yet. Once a flow is wired a query that still
// has no recorded status is StatusUnknown and incomplete again.
const StatusNotWatched = "not-watched"

// notWatchedReason is the Reason of a StatusNotWatched listing.
const notWatchedReason = "no change flow wired for this type"

// unknownReason is the Reason of a StatusUnknown listing.
const unknownReason = "no listing status recorded"

// daemonOwnedTypes are the entity types whose change flow is owned by the
// daemon cutover (the pr feed) rather than a role that runs
// `pg-desk <type> changes` today. For these, no registered consumer means
// "not yet watched", not "watched but unrecorded". issue is absent on
// purpose: its flow is wired, so a missing status there is a real gap.
var daemonOwnedTypes = map[string]bool{entityTypePR: true}

// The listing statuses the persisted keys carry (the changes package's
// StatusOK, StatusDegraded and StatusFailed).
const (
	listingOK       = "ok"
	listingDegraded = "degraded"
	listingFailed   = "failed"
)

// CoverageTypes are the entity types whose coverage focus reports, in order.
var CoverageTypes = []string{entityTypePR, entityTypeIssue}

// CoverageReport is the coverage of one run. State is what a stored draft
// carries; Notices are the routine conditions (a small hydration backlog)
// that do not make coverage incomplete; AllFailed is true when at least one
// listing is known and EVERY known listing failed, which with an empty
// candidate set is a total failure (exit 3).
type CoverageReport struct {
	State     CoverageState
	Notices   []string
	AllFailed bool
}

// ComputeCoverage reads the coverage of the watched types at now. entities
// are the stored entity rows of the configured repository (Inputs.Entities).
// Exit 2 (incomplete) is State.Incomplete: any listing degraded, failed or
// unknown, or a due backlog above focus.coverage_backlog_max of its type. A
// backlog within the bound is a Notice only.
func ComputeCoverage(st *store.Store, cfg *config.Config, now time.Time, entities map[Key]store.Entity) (CoverageReport, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	rows := make([]store.Entity, 0, len(entities))
	for _, e := range entities {
		rows = append(rows, e)
	}
	rep := CoverageReport{State: CoverageState{PerType: map[string]TypeCoverage{}}}
	known, failed := 0, 0
	for _, t := range CoverageTypes {
		cov := TypeCoverage{
			Active:     changes.ActiveCount(rows, t),
			DueBacklog: changes.DueBacklog(rows, t, now, cfg.SweepMaxAge()),
		}
		cov.OldestHydratedAt = oldestHydrated(rows, t)

		listings, err := QueryListings(st, cfg, t)
		if err != nil {
			return CoverageReport{}, err
		}
		for _, l := range listings {
			cov.Listings = append(cov.Listings, l)
			switch l.Status {
			case StatusNotWatched:
				continue
			case StatusUnknown:
				rep.State.Reasons = append(rep.State.Reasons, fmt.Sprintf("%s %s listing status unknown", t, l.Query))
				continue
			}
			known++
			switch l.Status {
			case listingOK:
			case listingFailed:
				failed++
				rep.State.Reasons = append(rep.State.Reasons, listingReason(t, l))
			default:
				rep.State.Reasons = append(rep.State.Reasons, listingReason(t, l))
			}
		}
		if max := cfg.FocusCoverageBacklogMax(cov.Active); cov.DueBacklog > max {
			rep.State.Reasons = append(rep.State.Reasons,
				fmt.Sprintf("%s hydration backlog %d is above focus.coverage_backlog_max %d", t, cov.DueBacklog, max))
		} else if cov.DueBacklog > 0 {
			rep.Notices = append(rep.Notices,
				fmt.Sprintf("hydration backlog: %d of %d active %s entities are due (within focus.coverage_backlog_max %d)", cov.DueBacklog, cov.Active, t, max))
		}
		rep.State.PerType[t] = cov
	}
	rep.State.Incomplete = len(rep.State.Reasons) > 0
	rep.AllFailed = known > 0 && failed == known
	return rep, nil
}

func listingReason(t string, ls Listing) string {
	s := fmt.Sprintf("%s %s listing %s", t, ls.Query, ls.Status)
	if ls.Reason != "" {
		s += " (" + ls.Reason + ")"
	}
	return s
}

// QueryListings is the listing status of every configured watch query of
// entityType, in configured order, read from the persisted statuses only.
// It is the one resolution behind both `focus show` coverage and `doctor`, so
// the two agree on the same query. A query with no persisted status is
// StatusNotWatched when entityType is daemon-owned and no change flow is
// wired for it (changes.FlowWired), otherwise StatusUnknown. An unreadable
// consumer table counts as wired: a source that may be missing is shown as
// incomplete, never as quietly not watched.
func QueryListings(st *store.Store, cfg *config.Config, entityType string) ([]Listing, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	persisted, err := changes.ReadListingStatuses(st, entityType)
	if err != nil {
		return nil, fmt.Errorf("focus: read listing statuses: %w", err)
	}
	byQuery := map[string]changes.ListingStatus{}
	for _, ls := range persisted {
		byQuery[ls.Query] = ls
	}
	wired := true
	if daemonOwnedTypes[entityType] {
		if w, werr := changes.FlowWired(st, entityType); werr == nil {
			wired = w
		}
	}
	var out []Listing
	for _, q := range cfg.WatchQueries(entityType) {
		if ls, ok := byQuery[q]; ok {
			out = append(out, Listing{Query: q, Status: ls.Status, Reason: ls.Reason})
		} else if !wired {
			out = append(out, Listing{Query: q, Status: StatusNotWatched, Reason: notWatchedReason})
		} else {
			out = append(out, Listing{Query: q, Status: StatusUnknown, Reason: unknownReason})
		}
	}
	return out, nil
}

// oldestHydrated is the earliest hydrated_at among the active entities of
// entityType that have one, as the stored RFC 3339 text; "" when none has.
func oldestHydrated(rows []store.Entity, entityType string) string {
	var oldest time.Time
	var text string
	for _, e := range rows {
		if e.EntityType != entityType || e.Inactive || e.HydratedAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.HydratedAt)
		if err != nil {
			continue
		}
		if text == "" || t.Before(oldest) {
			oldest, text = t, e.HydratedAt
		}
	}
	return text
}

// WatchSet is the persisted watch-set membership of one entity type. Known
// is false when no configured query of the type has a persisted watch set,
// in which case nothing is reported unwatched (the set was never recorded).
type WatchSet struct {
	IDs   map[string]bool
	Known bool
}

// Watched maps an entity type to its watch set.
type Watched map[string]WatchSet

// LoadWatched reads the watch sets of the coverage types.
func LoadWatched(st *store.Store, cfg *config.Config) Watched {
	w := Watched{}
	for _, t := range CoverageTypes {
		ids, known := changes.WatchedIDs(st, cfg.WatchQueries(t), t)
		w[t] = WatchSet{IDs: ids, Known: known}
	}
	return w
}

// Unwatched reports whether k is outside every watch query of its type: the
// type's watch sets are known and none holds the id.
func (w Watched) Unwatched(k Key) bool {
	ws, ok := w[k.Type]
	return ok && ws.Known && !ws.IDs[k.ID]
}

// SortedTypes returns the coverage types of c in report order.
func (c CoverageState) SortedTypes() []string {
	out := make([]string, 0, len(c.PerType))
	for t := range c.PerType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
