package focus

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The focus health model: ONE pure function of the stored rows that the
// /metrics families, the alert rules and the doctor and status focus block all
// read, so they cannot disagree. It reads only the store (no tracker call, no
// network, no clock but the injected now) and writes nothing.
//
// "The current plan" is the rows of the NEWEST period that holds rows, never
// the local day. A focus bead is an issue entity whose own metadata carries
// source_id.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; internal/metrics registers the families that expose Health at
// scrape time (docs/behavior/pg-desk/serve.md).

// The closed label vocabularies of the focus families. NO series carries an
// entity key or a bead id.
var (
	// HealthBeadStates are the state label of pg_desk_focus_beads.
	HealthBeadStates = []string{"open", "claimed", "held", "deferred_other", "blocked", "closed"}
	// HealthAgedBeadStates are the state label of
	// pg_desk_focus_oldest_bead_age_seconds.
	HealthAgedBeadStates = []string{"open", "held"}
	// HealthPendingReasons are the reason label of pg_desk_focus_pending_beads.
	HealthPendingReasons = []string{"unminted", "hidden", "suppressed", "no_bead_by_design", "unwatched", "source_terminal"}
	// HealthDivergenceKinds are the kind label of pg_desk_focus_divergence.
	HealthDivergenceKinds = []string{
		DivergenceRowWithoutAnnotation, DivergenceAnnotationWithoutRow, DivergenceStalePeriod,
		DivergenceSelectedBeadHeld, DivergenceRemovedBeadOpenUnclaimed, DivergenceRemovedBeadClaimed,
		DivergenceTerminalSourceBeadOpen,
	}
	// HealthPlanRowStates are the state label of pg_desk_focus_plan_rows.
	HealthPlanRowStates = []string{"in_plan", "finished", "open_slots"}
	// HealthCandidateSources are the source label of pg_desk_focus_candidates.
	HealthCandidateSources = []string{"pr", "jira", "bead"}
	// HealthRankInputs are the input label of
	// pg_desk_focus_rank_degraded_inputs.
	HealthRankInputs = []string{"unparseable_due", "unmapped_priority", "age_fallback", "unblocks_unavailable", "status_category_absent"}
	// HealthRunOutcomes are the outcome label of pg_desk_focus_runs.
	HealthRunOutcomes = []string{"ok", "partial", "total", "period_closed", "usage", "empty"}
	// HealthListingStatuses are the status label of
	// pg_desk_watch_listing_status.
	HealthListingStatuses = []string{"ok", "degraded", "failed"}
)

// The seven divergence kinds.
const (
	DivergenceRowWithoutAnnotation      = "row_without_annotation"
	DivergenceAnnotationWithoutRow      = "annotation_without_row"
	DivergenceStalePeriod               = "stale_period"
	DivergenceSelectedBeadHeld          = "selected_bead_held"
	DivergenceRemovedBeadOpenUnclaimed  = "removed_bead_open_unclaimed"
	DivergenceRemovedBeadClaimed        = "removed_bead_claimed"
	DivergenceTerminalSourceBeadOpen    = "terminal_source_bead_open"
	annotationNoneWord                  = store.FocusAnnotationNone
	healthEscalatedFocusAnnotationStart = "escalated.focus"
)

// The pending reasons.
const (
	PendingUnminted        = "unminted"
	PendingHidden          = "hidden"
	PendingSuppressed      = "suppressed"
	PendingNoBeadByDesign  = "no_bead_by_design"
	PendingUnwatched       = "unwatched"
	PendingSourceTerminal  = "source_terminal"
	planRowInPlan          = "in_plan"
	planRowFinished        = "finished"
	planRowOpenSlots       = "open_slots"
	beadStateOpen          = "open"
	beadStateHeld          = "held"
	beadStateClaimed       = "claimed"
	beadStateDeferredOther = "deferred_other"
	beadStateBlocked       = "blocked"
	beadStateClosed        = "closed"
)

// TypedListing is one watch query's persisted listing status with the entity
// type it lists.
type TypedListing struct {
	Type string
	Listing
}

// Health is the focus health of one store at one instant.
type Health struct {
	// Selected is the number of rows of the newest period that holds rows.
	Selected int
	// PlanRows counts in_plan (unfinished rows), finished and open_slots of
	// that plan against its cap.
	PlanRows map[string]int
	// Beads counts every focus bead of the store by state (the metric state
	// label: open, claimed, held, deferred_other, blocked, closed).
	Beads map[string]int
	// OldestBeadAge is the age of the oldest focus bead in state open and in
	// state held; a state with no bead has no entry. A bead's age is now
	// minus its tracker creation time, else its first-seen time.
	OldestBeadAge map[string]time.Duration
	// Pending counts the rows of the current plan by the reason they have no
	// bead to wait for; every reason has an entry.
	Pending map[string]int
	// OldestPendingAge is the oldest now-minus-selected_at per pending reason;
	// a reason with no row has no entry.
	OldestPendingAge map[string]time.Duration
	// Divergence counts the seven divergence kinds; every kind has an entry.
	Divergence map[string]int
	// UnwatchedSelected counts selected rows whose entity is in no
	// change_flow.watchset.<type>.<query> set.
	UnwatchedSelected int
	// Runs counts focus_run rows by [verb, outcome].
	Runs map[[2]string]int
	// RunAge is the time since the last non-dry-run select; nil when none.
	RunAge *time.Duration
	// RankInputs counts the silent degradations of one rank.
	RankInputs RankInputs
	// Listings are the persisted per-query listing statuses, by entity type.
	Listings []TypedListing
	// Candidates counts the candidate set by source (pr, jira, bead).
	Candidates map[string]int
	// LinkedCandidates counts the candidates in the set only through a link.
	LinkedCandidates int
	// DraftAge is the time since the stored draft's made_at; nil when no
	// draft is stored.
	DraftAge *time.Duration
	// LastLockDraftAge and LastLockDriftRows are the draft age and the drift
	// rows recorded by the last select run; nil when absent.
	LastLockDraftAge  *time.Duration
	LastLockDriftRows *int
	// JiraCategoryAbsent counts active Jira issues whose stored facts lack a
	// status category.
	JiraCategoryAbsent int
	// Coverage is the per-type coverage of the candidate sources. A zero
	// Health (Coverage.PerType empty) means "not migrated: emit nothing".
	Coverage CoverageState
	// EscalatedFocusRules counts source entities carrying an annotation key
	// that starts with escalated.focus.
	EscalatedFocusRules int
	// MintedNeverClaimed counts focus beads that are open and unassigned and
	// older than focus.pending_gate_age.
	MintedNeverClaimed int
	// RemovedWithBeadInPlay counts entities whose annotation is none and
	// whose bead is open or claimed.
	RemovedWithBeadInPlay int
	// HeldButReselected counts held beads whose (unfinished) source has a
	// real period key.
	HeldButReselected int
	// AuditCommentsToday is ALWAYS 0: not available, because the stored issue
	// snapshot carries no comments, so there is no stored audit text.
	AuditCommentsToday int

	// OpenBeads is the number of open beads in the store (the candidate
	// set's count), for the doctor's open-beads-but-no-bead-candidates report.
	OpenBeads int
	// DraftPeriodKey is the period key of the stored draft ("" when none).
	DraftPeriodKey string
	// IdentityListEmpty is true when focus.operator_identities is empty.
	IdentityListEmpty bool
}

// Migrated reports whether h came from a store that holds the focus tables.
// The zero Health is not migrated.
func (h Health) Migrated() bool { return len(h.Coverage.PerType) > 0 }

// ComputeHealth classifies the current plan, its sources and the store's
// focus beads from the stored rows only. A store that is not migrated for the
// focus tables, or a configuration with no repository, yields the zero Health
// and no error (the metrics then emit nothing and the scrape still succeeds).
// Any other read failure is an error, never a quiet all-clear.
func ComputeHealth(st *store.Store, cfg *config.Config, now time.Time) (Health, error) {
	if st == nil {
		return Health{}, fmt.Errorf("focus: health: no store")
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := st.RequireNewSchema(); err != nil {
		return Health{}, nil //nolint:nilerr // an unmigrated store has no focus series by design
	}
	if len(cfg.Repos) == 0 {
		return Health{}, nil
	}
	repo := cfg.Repos[0].Remote
	in, err := Load(st, cfg, repo, now)
	if err != nil {
		return Health{}, err
	}
	set := Candidates(in)
	cov, err := ComputeCoverage(st, cfg, now, in.Entities)
	if err != nil {
		return Health{}, err
	}
	watched := LoadWatched(st, cfg)

	h := Health{
		PlanRows:         zeroCounts(HealthPlanRowStates),
		Beads:            zeroCounts(HealthBeadStates),
		OldestBeadAge:    map[string]time.Duration{},
		Pending:          zeroCounts(HealthPendingReasons),
		OldestPendingAge: map[string]time.Duration{},
		Divergence:       zeroCounts(HealthDivergenceKinds),
		Runs:             map[[2]string]int{},
		Candidates:       map[string]int{"pr": set.Counts.PR, "jira": set.Counts.Jira, "bead": set.Counts.Bead},
		LinkedCandidates: set.Counts.Linked,
		Coverage:         cov.State,
		OpenBeads:        set.Counts.OpenBeads,
	}
	h.IdentityListEmpty = len(cfg.FocusOperatorIdentities()) == 0
	for _, t := range CoverageTypes {
		if c, ok := cov.State.PerType[t]; ok {
			for _, l := range c.Listings {
				h.Listings = append(h.Listings, TypedListing{Type: t, Listing: l})
			}
		}
	}

	// ---- the plan: periods, rows, newest row per item
	periods, err := st.FocusPeriods()
	if err != nil {
		return Health{}, err
	}
	allRows, err := st.FocusRowsAllPeriods()
	if err != nil {
		return Health{}, err
	}
	periodOf := make(map[int64]store.FocusPeriod, len(periods))
	for _, p := range periods {
		periodOf[p.ID] = p
	}
	holds := map[int64]int{}
	newestRow := map[Key]store.FocusSelection{}
	for _, r := range allRows {
		if r.Repo != repo {
			continue
		}
		holds[r.FocusPeriodID]++
		k := Key{Type: r.EntityType, ID: r.EntityID}
		if cur, ok := newestRow[k]; !ok || rowNewer(r, cur, periodOf) {
			newestRow[k] = r
		}
	}
	var current *store.FocusPeriod
	for _, p := range periods {
		if holds[p.ID] == 0 {
			continue
		}
		p := p
		if current == nil || periodNewer(p, *current) {
			current = &p
		}
	}
	var plan []store.FocusSelection
	capLine := DefaultCap
	if current != nil {
		rows, err := st.FocusPlanRows(current.ID)
		if err != nil {
			return Health{}, err
		}
		for _, r := range rows {
			if r.Repo == repo {
				plan = append(plan, r)
			}
		}
		if current.Cap != nil && *current.Cap > 0 {
			capLine = *current.Cap
		}
	}
	h.Selected = len(plan)

	// ---- the draft
	d, haveDraft, err := st.FocusDraftGet()
	if err != nil {
		return Health{}, err
	}
	if haveDraft {
		h.DraftPeriodKey = d.PeriodKey
		if t, ok := parseAge(d.MadeAt); ok {
			age := nonNegative(now.Sub(t))
			h.DraftAge = &age
		} else {
			zero := time.Duration(0)
			h.DraftAge = &zero
		}
	}

	// ---- runs
	stats, err := st.FocusRunStats()
	if err != nil {
		return Health{}, err
	}
	for k, n := range stats.ByVerbOutcome {
		h.Runs[k] = n
	}
	if t, ok := parseAge(stats.LastSelectAt); ok {
		age := nonNegative(now.Sub(t))
		h.RunAge = &age
	}
	if stats.LastLockDraftAgeSeconds != nil {
		age := time.Duration(*stats.LastLockDraftAgeSeconds) * time.Second
		h.LastLockDraftAge = &age
	}
	if stats.LastLockDriftRows != nil {
		n := *stats.LastLockDriftRows
		h.LastLockDriftRows = &n
	}

	// ---- the rank (computed at most once per call)
	rk := Rank(in, set, RankOptions{Cap: capLine})
	h.RankInputs = rk.Inputs

	// ---- focus beads
	beads := focusBeads(in)
	gateAge := cfg.FocusPendingGateAge()
	for _, b := range beads {
		h.Beads[b.metricState]++
		if b.metricState == beadStateOpen || b.metricState == beadStateHeld {
			if b.age > h.OldestBeadAge[b.metricState] || !hasAge(h.OldestBeadAge, b.metricState) {
				h.OldestBeadAge[b.metricState] = b.age
			}
		}
		if b.metricState == beadStateOpen && strings.TrimSpace(b.assignee) == "" && b.age > gateAge {
			h.MintedNeverClaimed++
		}
	}

	// ---- live facts and the bead column of every key the model reads
	keySet := map[Key]bool{}
	for _, r := range plan {
		keySet[Key{Type: r.EntityType, ID: r.EntityID}] = true
	}
	for k := range newestRow {
		keySet[k] = true
	}
	for _, b := range beads {
		if b.source != (Key{}) {
			keySet[b.source] = true
		}
	}
	keys := make([]Key, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	selectedAt := make(map[Key]time.Time, len(plan))
	for _, r := range plan {
		if t, ok := parseAge(r.SelectedAt); ok {
			selectedAt[Key{Type: r.EntityType, ID: r.EntityID}] = t
		}
	}
	live := BuildLive(in, keys, rk, LiveOptions{Watched: watched, SelectedAt: selectedAt})

	// ---- plan rows and pending reasons (rows of the current plan)
	planRows := make([]PlanRow, 0, len(plan))
	for _, r := range plan {
		k := Key{Type: r.EntityType, ID: r.EntityID}
		planRows = append(planRows, PlanRow{Key: k, RankPosition: r.RankPosition, Tier: r.Tier, SelectedAt: r.SelectedAt})
		if watched.Unwatched(k) {
			h.UnwatchedSelected++
		}
		reason := pendingReason(in, live, k)
		if reason == "" {
			continue
		}
		h.Pending[reason]++
		var age time.Duration
		if t, ok := selectedAt[k]; ok {
			age = nonNegative(now.Sub(t))
		}
		if !hasAge(h.OldestPendingAge, reason) || age > h.OldestPendingAge[reason] {
			h.OldestPendingAge[reason] = age
		}
	}
	cl := BuildPlanView(planRows, live.Facts, rk, capLine).CapLine
	h.PlanRows[planRowInPlan], h.PlanRows[planRowFinished], h.PlanRows[planRowOpenSlots] = cl.InPlan, cl.Finished, cl.OpenSlots

	// ---- divergence: the row-based kinds, over each item's newest row
	annotation := func(k Key) string { return in.Annotations[k][store.AnnotationFocusSelected] }
	for k, r := range newestRow {
		want := periodOf[r.FocusPeriodID].PeriodKey
		if annotation(k) != want {
			h.Divergence[DivergenceRowWithoutAnnotation]++
		}
		cell, f := live.Beads[k], live.Facts[k]
		switch cell.State {
		case StateHeld:
			if !f.Finished {
				h.Divergence[DivergenceSelectedBeadHeld]++
			}
		case StateSourceDone:
			h.Divergence[DivergenceTerminalSourceBeadOpen]++
		}
	}
	for k, anns := range in.Annotations {
		v := anns[store.AnnotationFocusSelected]
		if v != "" && v != annotationNoneWord {
			if _, hasRow := newestRow[k]; !hasRow {
				h.Divergence[DivergenceAnnotationWithoutRow]++
			}
		}
	}
	// stale_period: a period that STILL HOLDS rows, has no closed_at, and is
	// older than the newest period that holds rows or older than yesterday.
	yesterday := now.In(cfg.FocusTimeZone()).AddDate(0, 0, -1).Format("2006-01-02")
	for _, p := range periods {
		if holds[p.ID] == 0 || p.ClosedAt != "" {
			continue
		}
		olderThanNewest := current != nil && p.PeriodType == current.PeriodType && p.PeriodKey < current.PeriodKey
		olderThanYesterday := p.PeriodType == store.FocusPeriodDay && p.PeriodKey < yesterday
		if olderThanNewest || olderThanYesterday {
			h.Divergence[DivergenceStalePeriod]++
		}
	}
	// removed_bead_*: over the annotation and the link, not over a row.
	seenSource := map[Key]bool{}
	for _, b := range beads {
		if b.source == (Key{}) || seenSource[b.source] {
			continue
		}
		seenSource[b.source] = true
		a := annotation(b.source)
		if a != "" && a != annotationNoneWord {
			if live.Beads[b.source].State == StateHeld && !live.Facts[b.source].Finished {
				h.HeldButReselected++
			}
			continue
		}
		switch live.Beads[b.source].State {
		case StateOpen, StateOpenChildren, StateOpenMarkerStale, StateSourceDone:
			h.Divergence[DivergenceRemovedBeadOpenUnclaimed]++
			h.RemovedWithBeadInPlay++
		case StateClaimed:
			h.Divergence[DivergenceRemovedBeadClaimed]++
			h.RemovedWithBeadInPlay++
		}
	}

	// ---- reports
	for _, anns := range in.Annotations {
		for key := range anns {
			if strings.HasPrefix(key, healthEscalatedFocusAnnotationStart) {
				h.EscalatedFocusRules++
				break
			}
		}
	}
	h.JiraCategoryAbsent = jiraCategoryAbsent(in)
	return h, nil
}

// pendingReason names why a row of the current plan has no bead to wait for,
// or "" when it is not pending (it has a bead, is not live, or has no
// annotation). The order makes the reasons disjoint: a source that is itself
// a bead (an epic or a bd task) has no bead by design; a terminal source is
// finished (its bead, if any, is held by the terminal-source hold, so it is
// never unminted); hidden and suppressed come next; an unwatched source
// cannot be minted by the decider; the rest is unminted when the annotation
// is set and the source is live.
func pendingReason(in Inputs, live Live, k Key) string {
	cell, f := live.Beads[k], live.Facts[k]
	switch {
	case cell.State == StateNotApplicable:
		return PendingNoBeadByDesign
	case f.Finished:
		return PendingSourceTerminal
	}
	switch cell.State {
	case StatePendingHidden:
		return PendingHidden
	case StatePendingSuppress:
		return PendingSuppressed
	case StatePending:
	default:
		return "" // a bead exists (or the decider escalated): not pending
	}
	if strings.HasSuffix(cell.Shown, unwatchedWord) {
		return PendingUnwatched
	}
	if f.Inactive {
		return ""
	}
	if a := in.Annotations[k][store.AnnotationFocusSelected]; a == "" || a == annotationNoneWord {
		return ""
	}
	return PendingUnminted
}

// healthBead is one focus bead as the health model reads it.
type healthBead struct {
	metricState string
	assignee    string
	age         time.Duration
	source      Key
}

// focusBeads lists the focus beads of in: issue entities whose own metadata
// carries source_id, other than an escalation (human) bead. Their metric
// state is ClassifyBead's, mapped to the metric label.
func focusBeads(in Inputs) []healthBead {
	sourceOf := map[string]Key{}
	for _, l := range in.Links {
		if l.Relation == relationSource && l.FromType == entityTypeIssue {
			sourceOf[l.FromID] = Key{Type: l.ToType, ID: l.ToID}
		}
	}
	keys := make([]Key, 0)
	for k := range in.Entities {
		if k.Type == entityTypeIssue {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keyLess(keys[i], keys[j]) })
	var out []healthBead
	for _, k := range keys {
		e := in.Entities[k]
		var v view
		v.key = k
		decodeFacts(e.Facts, &v)
		if _, ok := v.metadata[MetaSourceID]; !ok {
			continue
		}
		if hasLabel(v.labels, humanLabel) {
			continue
		}
		link := LinkedBead{
			Present: true, Status: v.state, Assignee: v.assignee,
			FocusHold: metaString(v.metadata, metaFocusHold),
		}
		b := healthBead{metricState: metricStateOf(ClassifyBead(link, SourceFlags{})), assignee: v.assignee}
		if src, ok := sourceOf[k.ID]; ok {
			b.source = src
		} else if t, id := metaString(v.metadata, "source_type"), metaString(v.metadata, MetaSourceID); t != "" && id != "" {
			b.source = Key{Type: t, ID: id}
		}
		created := v.createdAt
		t, ok := parseAge(created)
		if !ok {
			t, ok = parseAge(e.FirstSeenAt)
		}
		if ok {
			b.age = nonNegative(in.Now.Sub(t))
		}
		out = append(out, b)
	}
	return out
}

// metricStateOf maps a bead's table row to the metric state label: the open
// rows (plain, with children, marker stale, source done) are open; deferred
// by someone else is deferred_other; the status words (blocked, pinned,
// hooked) are blocked.
func metricStateOf(s BeadState) string {
	switch s {
	case StateClaimed:
		return beadStateClaimed
	case StateHeld:
		return beadStateHeld
	case StateDeferredOther:
		return beadStateDeferredOther
	case StateStatusWord:
		return beadStateBlocked
	case StateClosed:
		return beadStateClosed
	}
	return beadStateOpen
}

// jiraCategoryAbsent counts active Jira issues (an issue entity that is not a
// bead of either kind) whose stored facts carry no status category.
func jiraCategoryAbsent(in Inputs) int {
	a := analyze(in)
	n := 0
	for k, v := range a.views {
		if v.kind != KindJira {
			continue
		}
		e := in.Entities[k]
		if e.Inactive {
			continue
		}
		if _, ok := v.metadata[MetaSourceID]; ok {
			continue
		}
		if _, ok := v.metadata[MetaDedupKey]; ok {
			continue
		}
		if _, has := classify.IssueStatusKind(json.RawMessage(e.Facts)); !has {
			n++
		}
	}
	return n
}

func zeroCounts(names []string) map[string]int {
	m := make(map[string]int, len(names))
	for _, n := range names {
		m[n] = 0
	}
	return m
}

func hasAge(m map[string]time.Duration, k string) bool {
	_, ok := m[k]
	return ok
}

func nonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// periodNewer reports whether a is a newer period than b: the greater key,
// then the greater id.
func periodNewer(a, b store.FocusPeriod) bool {
	if a.PeriodKey != b.PeriodKey {
		return a.PeriodKey > b.PeriodKey
	}
	return a.ID > b.ID
}

// rowNewer reports whether row a is newer than row b: the newer period, then
// the greater id.
func rowNewer(a, b store.FocusSelection, periods map[int64]store.FocusPeriod) bool {
	pa, pb := periods[a.FocusPeriodID], periods[b.FocusPeriodID]
	if pa.PeriodKey != pb.PeriodKey {
		return pa.PeriodKey > pb.PeriodKey
	}
	if a.FocusPeriodID != b.FocusPeriodID {
		return a.FocusPeriodID > b.FocusPeriodID
	}
	return a.ID > b.ID
}
