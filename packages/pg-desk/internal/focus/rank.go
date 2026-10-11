package focus

import (
	"cmp"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/dependency"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// DefaultCap is the cap line when RankOptions.Cap is not positive.
const DefaultCap = 6

// The three tiers, in rank order. They are what the draft and the plan store.
const (
	TierOverdue    = "overdue"
	TierStarted    = "started"
	TierNotStarted = "not_started"
)

// The deciding keys: RankedRow.DecidingKey names the first key on which a row
// and the row below it differ. KeyOverdue is the "most overdue" key of the
// overdue tier and KeyStarted its started-first key; KeyDue is the horizon
// key of the other two tiers.
const (
	KeyTier     = "tier"
	KeyOverdue  = "overdue"
	KeyStarted  = "started"
	KeyDue      = "due"
	KeyUnblocks = "unblocks"
	KeyPriority = "priority"
	KeyAge      = "age"
	KeyTiebreak = "tiebreak"
)

// Tier indexes, in rank order.
const (
	overdueIndex = iota
	startedIndex
	notStartedIndex
)

// RankOptions are the caller's choices for one rank.
//
// Date is the addressed day: its calendar date in its own location (a caller
// that parsed --date builds it in the focus time zone). The zero value means
// today, which is the day of Clock's reading in the focus time zone. Cap is
// the cap line (DefaultCap when not positive). Clock is the injected clock,
// read only when Date is zero; a nil Clock falls back to Inputs.Now.
type RankOptions struct {
	Date  time.Time
	Cap   int
	Clock interpret.Clock
}

// RankInputs counts every silent degradation of one rank, so a tracker's
// format change that quietly turns a key into "none" shows up as a number.
// Each counter is per candidate of the set ranked.
//
//   - UnparseableDue: a candidate whose own due value is present but not a
//     date or timestamp (it ranks as having no due date).
//   - UnmappedPriority: a candidate whose own priority value is in neither
//     focus.priority_map nor the P0..P4 form (it sorts after P4).
//   - AgeFallback: a candidate that ranked on the entity's first-seen time
//     because its snapshot carries no creation time (every PR, since a PR
//     snapshot has none).
//   - UnblocksUnavailable: an issue candidate whose unblocks key is 0 because
//     its stored facts carry no issue dependencies (hydration was off).
//   - StatusCategoryAbsent: a Jira issue candidate whose snapshot carries no
//     status category, so started fell back to the configured name list.
type RankInputs struct {
	UnparseableDue       int
	UnmappedPriority     int
	AgeFallback          int
	UnblocksUnavailable  int
	StatusCategoryAbsent int
}

// RankedRow is one row of the ranking, in rank order.
//
// Tier is one of TierOverdue, TierStarted and TierNotStarted. DecidingKey is
// the first key on which this row and the row below it differ (empty for the
// last row). Position is 1-based. InPlan is the cap line, for display: the
// first Cap rows that are not Finished. Due is the effective due day
// (YYYY-MM-DD; the earliest across the correlation group) or empty; Priority
// is the effective priority (P0..P4, or the raw value when unmapped; empty
// for none). Finished marks a candidate whose source is terminal; such a row
// keeps its place in the order and is not counted toward the cap.
type RankedRow struct {
	Candidate
	Tier        string
	DecidingKey string
	Position    int
	InPlan      bool
	Due         string
	Priority    string
	Finished    bool
}

// Ranking is the answer of Rank.
//
// Rows are the ranked slots after the epic slot rule and the correlation
// groups (a group's highest-ranked member is its one row). EpicsInPlay is the
// trailing block of the slot rule, not counted against Cap. Covered maps
// every key the slot rule, a correlation group or a merge link suppresses to
// the key that covers it. UnknownChildren is the slot rule's count of
// children an epic names that the store does not hold yet.
type Ranking struct {
	Rows            []RankedRow
	EpicsInPlay     []EpicEntry
	Cap             int
	Inputs          RankInputs
	Covered         map[Key]Key
	UnknownChildren map[Key]int
}

// Rank orders a candidate set. It is the daily-focus rank: strict
// lexicographic tiers (overdue; started; not started) with no weights and no
// arithmetic, inner keys inside each tier, and the kind-then-key tiebreak
// that makes the order total, so the same inputs always rank identically.
//
// Tiers and keys (D-F11):
//
//  1. Overdue (the effective due day is before the addressed day). Inside
//     it: started first, then the most overdue, then unblocks (descending),
//     then priority, then age (oldest first).
//  2. Started (not overdue). 3. Not started (not overdue). Inside both: due
//     inside the 7-day horizon (0 to 7 days after the addressed day,
//     inclusive; nearer first, and any date inside it ahead of none; a due
//     date beyond the horizon is the same class as no due date), unblocks
//     (descending), priority, age (oldest first).
//
// The final key is the candidate order (kind, then entity type and id).
//
// Started is for SEEDS only: a bead in_progress, a Jira issue whose status
// category is indeterminate (the configured jira.in_progress_statuses names
// when the snapshot carries no category), any open seed PR. A candidate
// reached only through a link is never started.
//
// Due is the candidate's own value, or for a member of a correlation group
// the earliest across the group; it is read in focus.time_zone. Priority is
// the candidate's own value, or for a member of a group the highest across
// the group; a PR with no priority anywhere in its group is P2. Unblocks is
// the count of open dependents the dependency resolver reports for a PR, and
// for an issue the count of open DIRECT dependents in the reverse-edge index
// (0 when the issue's stored facts carry no issue dependencies). Age is the
// tracker creation time, else the entity's first-seen time; a candidate with
// neither sorts after every dated one.
//
// The epic slot rule runs on the ranked order before the cap line is counted;
// then each correlation group keeps its highest-ranked remaining member as its
// one row. The cap line marks the first Cap unfinished rows InPlan, for
// display only: it never changes the order, and nothing is written.
//
// Rank is pure: it reads only its arguments and the injected clock, writes
// nothing and calls no tracker or network. It emits no OpenTelemetry or
// Prometheus signal and logs nothing; the verbs that call it own the run
// record, the stderr line and the rank_inputs metric.
func Rank(in Inputs, set CandidateSet, opts RankOptions) Ranking {
	if in.Config == nil {
		in.Config = &config.Config{}
	}
	capLine := opts.Cap
	if capLine <= 0 {
		capLine = DefaultCap
	}
	loc := in.Config.FocusTimeZone()
	now := in.Now
	if opts.Clock != nil {
		now = opts.Clock.Now()
	}
	today := addressedDay(opts.Date, now, loc)

	r := &ranker{
		in:      in,
		a:       analyze(in),
		loc:     loc,
		today:   today,
		prio:    newPriorityTable(in.Config.FocusPriorityMap()),
		inProg:  lowerSet(in.Config.InProgressStatuses()),
		reader:  memReader{in: in},
		covered: map[Key]Key{},
	}
	r.resolver = dependency.NewResolver(r.reader, in.Repo)
	r.issueDeps, _ = dependency.NewIssueDependents(r.reader, func(e store.Entity) bool {
		return classify.SourceTerminal(e.EntityType, json.RawMessage(e.Facts), in.Now, 0)
	})
	items := r.build(set.Candidates)
	r.inherit(items, Groups(in, set))
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })

	ranked := make([]Candidate, len(items))
	byKey := make(map[Key]*item, len(items))
	for i, it := range items {
		ranked[i] = it.cand
		byKey[it.cand.Key] = it
	}
	slot := ApplySlotRule(in, ranked)
	for epic, child := range slot.Suppressed {
		r.covered[epic] = child
	}

	// Group representation: among the members that kept a slot, the
	// highest-ranked one is the group's one row.
	rep := map[int]Key{}
	var rows []*item
	for _, c := range slot.Slotted {
		it := byKey[c.Key]
		if g, ok := r.groupOf[c.Key]; ok {
			if top, seen := rep[g]; seen {
				r.covered[c.Key] = top
				continue
			}
			rep[g] = c.Key
		}
		rows = append(rows, it)
	}
	// A merge link suppresses the absorbed key in favour of the kept one.
	for absorbed, kept := range in.Absorbed {
		if absorbed != kept {
			if _, dup := r.covered[absorbed]; !dup {
				r.covered[absorbed] = kept
			}
		}
	}

	out := Ranking{
		EpicsInPlay:     slot.EpicsInPlay,
		Cap:             capLine,
		Inputs:          r.counts,
		Covered:         r.covered,
		UnknownChildren: slot.UnknownChildren,
	}
	open := capLine
	for i, it := range rows {
		row := RankedRow{
			Candidate: it.cand,
			Tier:      tierName(it.tier),
			Position:  i + 1,
			Due:       it.dueText(),
			Priority:  it.prioText,
			Finished:  it.finished,
		}
		if i+1 < len(rows) {
			row.DecidingKey = decidingKey(it, rows[i+1])
		}
		if !it.finished && open > 0 {
			row.InPlan = true
			open--
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// item is one candidate with the keys the comparator reads.
type item struct {
	cand     Candidate
	tier     int // 0 overdue, 1 started, 2 not started
	started  bool
	finished bool

	hasDue    bool
	due       calendarDay // effective: own, or the group's earliest
	ownDue    calendarDay
	hasOwnDue bool
	dueIn     int // days from the addressed day; valid when hasDue
	horizon   bool

	unblocks int

	prio      int  // effective rank, 0..5
	prioKnown bool // false: no priority anywhere (sorts after P4 like unmapped)
	ownPrio   int
	rawPrio   string // the item's own stored value, trimmed
	ownKnown  bool
	prioText  string

	age      time.Time
	ageKnown bool
}

func (it *item) dueText() string {
	if !it.hasDue {
		return ""
	}
	return it.due.format()
}

func tierName(t int) string {
	switch t {
	case overdueIndex:
		return TierOverdue
	case startedIndex:
		return TierStarted
	}
	return TierNotStarted
}

// ranker holds the per-call state of one Rank.
type ranker struct {
	in      Inputs
	a       *analysis
	loc     *time.Location
	today   calendarDay
	prio    priorityTable
	inProg  map[string]bool
	reader  memReader
	counts  RankInputs
	covered map[Key]Key
	groupOf map[Key]int

	resolver  *dependency.Resolver
	issueDeps *dependency.IssueDependents // nil when the index could not be built
}

func lowerSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return m
}

// build computes the own keys of every candidate and counts the degradations.
func (r *ranker) build(cands []Candidate) []*item {
	items := make([]*item, 0, len(cands))
	for _, c := range cands {
		it := &item{cand: c}
		v := r.a.views[c.Key]
		if v == nil {
			v = &view{key: c.Key, kind: c.Kind}
		}
		e := r.in.Entities[c.Key]

		it.finished = v.merged || classify.SourceTerminal(c.Key.Type, json.RawMessage(e.Facts), r.in.Now, 0)
		it.started = r.started(c, v, e)

		if strings.TrimSpace(v.dueDate) != "" {
			if d, ok := parseDueDay(v.dueDate, r.loc); ok {
				it.ownDue, it.hasOwnDue = d, true
			} else {
				r.counts.UnparseableDue++
			}
		}
		it.due, it.hasDue = it.ownDue, it.hasOwnDue

		rank, present, unmapped := r.prio.resolve(v.priority)
		it.ownPrio, it.ownKnown = rank, present
		it.rawPrio = strings.TrimSpace(v.priority)
		if unmapped {
			r.counts.UnmappedPriority++
		}
		it.prio, it.prioKnown = rank, present

		r.age(it, v, e)
		r.unblocksOf(it, v)
		items = append(items, it)
	}
	return items
}

// started is the started definition: seeds only.
func (r *ranker) started(c Candidate, v *view, e store.Entity) bool {
	switch c.Kind {
	case KindPR:
		// An open seed PR: the seed rule already requires it open.
		return c.Seed && strings.EqualFold(strings.TrimSpace(v.state), prStateOpen)
	case KindBead:
		return c.Seed && strings.EqualFold(strings.TrimSpace(v.state), statusInProg)
	case KindJira:
		kind, hasCategory := classify.IssueStatusKind(json.RawMessage(e.Facts))
		if !hasCategory {
			r.counts.StatusCategoryAbsent++
			return c.Seed && r.inProg[strings.ToLower(strings.TrimSpace(v.state))]
		}
		return c.Seed && kind == classify.IssueStatusInProgress
	}
	return false
}

// ageLayouts are the creation-time spellings the age key reads: RFC 3339
// first (bd, GitHub, first-seen), then Jira's own offset-without-colon forms
// ("2026-09-05T10:00:00.000+0000", as pjira forwards it) with and without
// fractional seconds. They mirror the Jira connector's jiraUpdatedLayouts
// (packages/pg-connector/cmd/pg-connector-issue-jira/internal/listrange.go),
// which sits under an internal/ path and cannot be imported from here.
var ageLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05-0700",
}

// parseAge parses s under the first ageLayouts layout that accepts it.
func parseAge(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range ageLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// age reads the age key: the tracker creation time, else first-seen.
func (r *ranker) age(it *item, v *view, e store.Entity) {
	if t, ok := parseAge(v.createdAt); ok {
		it.age, it.ageKnown = t, true
		return
	}
	r.counts.AgeFallback++
	if t, ok := parseAge(e.FirstSeenAt); ok {
		it.age, it.ageKnown = t, true
	}
}

// unblocksOf reads the unblocks key.
func (r *ranker) unblocksOf(it *item, v *view) {
	switch it.cand.Key.Type {
	case entityTypePR:
		edges, err := r.resolver.DependentsOf(dependency.TypePR, it.cand.Key.ID)
		if err != nil {
			return
		}
		for _, e := range edges {
			if e.Open() {
				it.unblocks++
			}
		}
	case entityTypeIssue:
		if r.issueDeps == nil || !r.issueDeps.Available(it.cand.Key.ID) {
			r.counts.UnblocksUnavailable++
			return
		}
		it.unblocks = len(r.issueDeps.DependentsOf(it.cand.Key.ID))
	}
}

// inherit applies the correlation-group rules: every member takes the
// earliest due day across the group and the highest priority across it, and
// a PR with no priority anywhere in its group is P2. It then derives the
// tier and the displayed values of every item.
func (r *ranker) inherit(items []*item, groups []Group) {
	byKey := make(map[Key]*item, len(items))
	for _, it := range items {
		byKey[it.cand.Key] = it
	}
	r.groupOf = map[Key]int{}
	for gi, g := range groups {
		var earliest calendarDay
		hasDue := false
		best, bestKnown := 0, false
		for _, k := range g.Members {
			r.groupOf[k] = gi
			m := byKey[k]
			if m.hasOwnDue && (!hasDue || m.ownDue < earliest) {
				earliest, hasDue = m.ownDue, true
			}
			if m.ownKnown && (!bestKnown || m.ownPrio < best) {
				best, bestKnown = m.ownPrio, true
			}
		}
		for _, k := range g.Members {
			m := byKey[k]
			m.due, m.hasDue = earliest, hasDue
			m.prio, m.prioKnown = best, bestKnown
		}
	}
	for _, it := range items {
		if it.cand.Kind == KindPR && !it.prioKnown {
			it.prio, it.prioKnown = priorityDefaultPR, true
		}
		if !it.prioKnown {
			it.prio = priorityUnmapped
		}
		if it.hasDue {
			it.dueIn = int(it.due - r.today)
			it.horizon = it.dueIn >= 0 && it.dueIn <= HorizonDays
		}
		switch {
		case it.hasDue && it.dueIn < 0:
			it.tier = overdueIndex
		case it.started:
			it.tier = startedIndex
		default:
			it.tier = notStartedIndex
		}
		it.prioText = priorityText(it)
	}
}

// priorityText renders the effective priority: P0..P4 for a ranked value,
// the raw value for an unmapped one, empty for none.
func priorityText(it *item) string {
	if !it.prioKnown {
		return ""
	}
	if it.prio == priorityUnmapped {
		return it.rawPrio // "" when the value was inherited, not the item's own
	}
	return priorityLabel(it.prio, true)
}

// step is one key of the comparator: it reports -1 when a sorts first, 1 when
// b does and 0 when the key does not separate them.
type step struct {
	name string
	cmp  func(a, b *item) int
}

func cmpBool(a, b bool) int { // true first
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

func cmpInt(a, b int) int { return cmp.Compare(a, b) } // smaller first

func cmpDesc(a, b int) int { return cmpInt(b, a) } // larger first

func cmpAge(a, b *item) int {
	if a.ageKnown != b.ageKnown {
		return cmpBool(a.ageKnown, b.ageKnown)
	}
	if !a.ageKnown {
		return 0
	}
	switch {
	case a.age.Before(b.age):
		return -1
	case b.age.Before(a.age):
		return 1
	}
	return 0
}

func cmpTiebreak(a, b *item) int {
	switch {
	case candidateLess(a.cand, b.cand):
		return -1
	case candidateLess(b.cand, a.cand):
		return 1
	}
	return 0
}

var (
	unblocksStep = step{KeyUnblocks, func(a, b *item) int { return cmpDesc(a.unblocks, b.unblocks) }}
	priorityStep = step{KeyPriority, func(a, b *item) int { return cmpInt(a.prio, b.prio) }}
	ageStep      = step{KeyAge, cmpAge}
	tiebreakStep = step{KeyTiebreak, cmpTiebreak}

	// overdueSteps order the overdue tier: started first, then the most
	// overdue (the earliest effective due day), then the shared keys.
	overdueSteps = []step{
		{KeyStarted, func(a, b *item) int { return cmpBool(a.started, b.started) }},
		{KeyOverdue, func(a, b *item) int { return cmpInt(int(a.due), int(b.due)) }},
		unblocksStep, priorityStep, ageStep, tiebreakStep,
	}
	// openSteps order the started and the not-started tiers. The due key is
	// the horizon key: a date inside the horizon sorts ahead of none, nearer
	// first, and a date beyond it is the same class as none.
	openSteps = []step{
		{KeyDue, func(a, b *item) int {
			if a.horizon != b.horizon {
				return cmpBool(a.horizon, b.horizon)
			}
			if !a.horizon {
				return 0
			}
			return cmpInt(a.dueIn, b.dueIn)
		}},
		unblocksStep, priorityStep, ageStep, tiebreakStep,
	}
)

// tierSteps are the comparator steps of each tier, by tier index.
var tierSteps = [...][]step{overdueIndex: overdueSteps, startedIndex: openSteps, notStartedIndex: openSteps}

// compare is the rank comparator: it returns the sign of a versus b and the
// name of the key that decided it ("" when a and b are the same candidate).
func compare(a, b *item) (int, string) {
	if c := cmpInt(a.tier, b.tier); c != 0 {
		return c, KeyTier
	}
	for _, s := range tierSteps[a.tier] {
		if c := s.cmp(a, b); c != 0 {
			return c, s.name
		}
	}
	return 0, ""
}

// less is the strict weak order of the rank. Every step is a total preorder
// and the last one is a total order over distinct candidates, so less is
// irreflexive, asymmetric and transitive.
func less(a, b *item) bool {
	c, _ := compare(a, b)
	return c < 0
}

// decidingKey names the key that puts upper ahead of lower.
func decidingKey(upper, lower *item) string {
	_, k := compare(upper, lower)
	return k
}

// memReader is the dependency package's Reader over the already-loaded
// Inputs, so the resolver and the reverse-edge index answer from the same
// snapshot the rank reads and never touch the store.
type memReader struct{ in Inputs }

func (m memReader) RequireNewSchema() error { return nil }

func (m memReader) ListEntities() ([]store.Entity, error) {
	out := make([]store.Entity, 0, len(m.in.Entities))
	for k, e := range m.in.Entities {
		e.EntityType, e.EntityID = k.Type, k.ID
		if e.Repo == "" {
			e.Repo = m.in.Repo
		}
		out = append(out, e)
	}
	return out, nil
}

func (m memReader) ListXrefLinksFrom(_, fromType, fromID string) ([]store.XrefLink, error) {
	var out []store.XrefLink
	for _, l := range m.in.Links {
		if l.FromType == fromType && l.FromID == fromID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m memReader) ListXrefLinksTo(_, toType, toID string) ([]store.XrefLink, error) {
	var out []store.XrefLink
	for _, l := range m.in.Links {
		if l.ToType == toType && l.ToID == toID {
			out = append(out, l)
		}
	}
	return out, nil
}
