package focus

import (
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// The final set of `focus select` (spec section 7.2 step 4): from the draft,
// the current plan and a parsed reply, WHAT would be inserted and deleted and
// WHY. ComputeFinalSet is a pure function: it performs no store write, takes
// no store transaction, assigns no rank_position and never recomputes the
// rank (the order is the draft's, D-F22); the lock packet applies the result.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is pure.

// Where an inserted row came from.
const (
	SourceDraft   = "draft"
	SourceForce   = "force"
	SourceHandAdd = "handadd"
	// SourcePull is the source of a row the pull verb adds; ComputeFinalSet
	// never produces it, the vocabulary is shared with the store's rows.
	SourcePull = "pull"
)

// Why a row is deleted (the cause vocabulary as stored; the display words
// belong to the echo output).
const (
	CauseOperator = "operator"
	CauseCap      = "cap"
	CauseDropped  = "dropped"
	// CauseNewPeriod is the cause of a row a first lock of a later period
	// replaces. ComputeFinalSet reads one period's plan and never emits it;
	// the lock packet does.
	CauseNewPeriod = "new_period"
)

// Why a key was skipped (a routine skip is exit 0, never an error).
const (
	SkipFinished          = "finished"
	SkipNoLongerCandidate = "no-longer-candidate"
	SkipAlreadySelected   = "already-selected"
	SkipNotACandidate     = "not-a-candidate"
)

// Skip is a key the reply named that has nothing to apply to.
type Skip struct {
	Key    Key
	Reason string
}

// Merge is a --merge pair: Absorbed is dropped from consideration and an
// external link records it under Kept.
type Merge struct{ Kept, Absorbed Key }

// Insertion is a row the lock would add. Tier is the draft's tier of the row
// and is nil for a key the draft does not hold.
type Insertion struct {
	Key    Key
	Source string
	Tier   *string
}

// Deletion is a row the lock would delete, and why. A deletion with cause
// operator is made in EVERY period's plan.
type Deletion struct {
	Key   Key
	Cause string
}

// FinalSetInput is everything ComputeFinalSet reads.
type FinalSetInput struct {
	// Draft is the draft the operator saw (or the one the caller computed
	// in-process); nil only for a draft-free preview of an empty period.
	Draft *DraftDoc
	// Plan is the period's current rows, before this reply.
	Plan  []PlanRow
	Reply Reply
	// Candidates is the LIVE candidate set: a +key of a candidate that
	// arrived after the draft is judged against it and is a force-pull.
	Candidates CandidateSet
	// Facts are the live facts of every key the draft, the plan and the
	// reply name; a key with no entry is read as live and unfinished.
	Facts LiveFacts
	// Merges are the pairs of --merge flags of this run.
	Merges []Merge
	// PersistedMerges are the merges already recorded (the external links);
	// a +<absorbedKey> of one reverses it.
	PersistedMerges []Merge
	// PeriodHasRows is true when the addressed period has any plan row: false
	// is a first lock.
	PeriodHasRows bool
	// PeriodCap is the persisted period cap, if any. DefaultCap is the cap
	// when none is persisted (the package default when not positive).
	PeriodCap  *int
	DefaultCap int
	// DraftCap is the draft's own cap (its --cap), if any.
	DraftCap *int
	// OpenChild returns an open child of an epic key, so +<epicKey> is a
	// usage error naming it. Nil means no key is an epic.
	OpenChild func(Key) (Key, bool)
	// AnyPeriodRows holds the keys that hold a plan row in ANY period: a
	// -key naming no candidate and no row anywhere is a usage error.
	AnyPeriodRows map[Key]bool
	// Config resolves the raw keys of the reply (see ParseKey).
	Config *config.Config
}

// FinalSet is the result.
//
//   - Insert lists the rows to add: the draft's proposals in draft order, then
//     the force-pulled and hand-added keys in reply order.
//   - Delete lists the rows to remove with their cause: operator strikes in
//     reply order, then dropped rows in plan order, then cap removals highest
//     rank_position first.
//   - Forced and HandAdded name the force-pulled keys and the hand-added keys
//     (a subset of Insert).
//   - Skipped are the routine skips, each key once.
//   - EffectiveCap is the cap the plan holds after the lock: at a first lock
//     the initial-plan size plus one per force-pulled or hand-added key; over
//     a plan the larger of the cap in force and the unfinished rows the plan
//     now holds, so open slots never go negative.
//   - PersistCap is the period cap to persist, or nil to leave it unchanged.
//   - Absorbed are the merges to record; Unmerge are the recorded merges a
//     +<absorbedKey> reverses.
type FinalSet struct {
	Insert       []Insertion
	Delete       []Deletion
	Forced       []Key
	HandAdded    []Key
	Skipped      []Skip
	EffectiveCap int
	PersistCap   *int
	Absorbed     []Merge
	Unmerge      []Merge
}

// finalCtx holds the lookups of one ComputeFinalSet call.
type finalCtx struct {
	in        FinalSetInput
	plan      map[Key]PlanRow
	draftRow  map[Key]DraftRow
	cand      map[Key]bool
	absorbed  map[Key]bool // keys currently absorbed, after this reply's merge changes
	struck    map[Key]bool
	skipped   map[Key]bool
	out       FinalSet
	firstLock bool
}

func (c *finalCtx) skip(k Key, reason string) {
	if c.skipped[k] {
		return
	}
	c.skipped[k] = true
	c.out.Skipped = append(c.out.Skipped, Skip{Key: k, Reason: reason})
}

// finished reads FINISHED from the live facts, before any activity test (a
// closed entity is also inactive); a key with no live facts falls back to the
// mark the draft carried.
func (c *finalCtx) finished(k Key, mark bool) bool {
	if f, ok := c.in.Facts[k]; ok {
		return f.Finished
	}
	return mark
}

// lost reports a key that left candidacy for a NON-terminal reason: hidden,
// or inactive without a terminal state. Finished is tested first.
func (c *finalCtx) lost(k Key) bool {
	f, ok := c.in.Facts[k]
	return ok && !f.Finished && (f.Hidden || f.Inactive)
}

func positiveOr(p *int) (int, bool) {
	if p != nil && *p > 0 {
		return *p, true
	}
	return 0, false
}

// ComputeFinalSet implements step 4 of the select procedure. Every error is a
// usage error (ErrUsage) and comes with a zero FinalSet: nothing is decided
// when anything is wrong.
func ComputeFinalSet(in FinalSetInput) (FinalSet, error) {
	c := &finalCtx{
		in:       in,
		plan:     map[Key]PlanRow{},
		draftRow: map[Key]DraftRow{},
		cand:     map[Key]bool{},
		absorbed: map[Key]bool{},
		struck:   map[Key]bool{},
		skipped:  map[Key]bool{},
	}
	c.firstLock = !in.PeriodHasRows && len(in.Plan) == 0
	for _, p := range in.Plan {
		c.plan[p.Key] = p
	}
	var draftRows []DraftRow
	if in.Draft != nil {
		draftRows = in.Draft.Rows
	}
	for _, r := range draftRows {
		c.draftRow[r.Key] = r
	}
	for _, cd := range in.Candidates.Candidates {
		c.cand[cd.Key] = true
	}

	plus, minus, err := c.resolveReply()
	if err != nil {
		return FinalSet{}, err
	}
	if err := c.applyMerges(plus); err != nil {
		return FinalSet{}, err
	}

	// Cap roles. defaultCap is the cap when nothing is persisted; draftCap is
	// the initial-plan size of the draft; persisted is the period cap in force.
	defaultCap := DefaultCap
	if v, ok := positiveOr(&in.DefaultCap); ok {
		defaultCap = v
	}
	draftCap := defaultCap
	if v, ok := positiveOr(in.PeriodCap); ok {
		draftCap = v
	}
	if v, ok := positiveOr(in.DraftCap); ok {
		draftCap = v
	}
	persisted := defaultCap
	if v, ok := positiveOr(in.PeriodCap); ok {
		persisted = v
	}
	var replyCap *int
	if in.Reply.Cap != nil && *in.Reply.Cap > 0 {
		v := *in.Reply.Cap
		replyCap = &v
	}
	switch {
	case replyCap != nil:
		v := *replyCap
		c.out.PersistCap = &v
	case c.firstLock:
		v := draftCap
		c.out.PersistCap = &v
	}

	if err := c.applyMinus(minus); err != nil {
		return FinalSet{}, err
	}

	// Plan rows that lost candidacy for a non-terminal reason are dropped.
	planOrder := c.planInOrder()
	for _, p := range planOrder {
		if c.lost(p.Key) && !c.struck[p.Key] {
			c.out.Delete = append(c.out.Delete, Deletion{Key: p.Key, Cause: CauseDropped})
			c.struck[p.Key] = true
		}
	}

	window := c.proposalWindow(draftRows, replyCap)
	inWindow := map[Key]bool{}
	for _, k := range window {
		inWindow[k] = true
	}

	// The draft's proposals.
	for _, k := range window {
		if _, ok := c.plan[k]; ok || c.struck[k] || c.absorbed[k] {
			continue
		}
		row := c.draftRow[k]
		if c.finished(k, row.Finished) {
			c.skip(k, SkipFinished)
			continue
		}
		if c.lost(k) {
			c.skip(k, SkipNoLongerCandidate)
			continue
		}
		tier := row.Tier
		c.out.Insert = append(c.out.Insert, Insertion{Key: k, Source: SourceDraft, Tier: &tier})
	}

	if err := c.applyPlus(plus, inWindow); err != nil {
		return FinalSet{}, err
	}

	// Cap shrink: only a reply cap below the persisted cap, only over rows
	// already in the plan, unfinished rows with the highest positions.
	if replyCap != nil && *replyCap < persisted && !c.firstLock {
		var keep []PlanRow
		for _, p := range planOrder {
			if c.finished(p.Key, false) || c.struck[p.Key] {
				continue
			}
			keep = append(keep, p)
		}
		for i := *replyCap; i < len(keep); i++ {
			c.struck[keep[i].Key] = true
		}
		for i := len(keep) - 1; i >= *replyCap; i-- {
			c.out.Delete = append(c.out.Delete, Deletion{Key: keep[i].Key, Cause: CauseCap})
		}
	}

	c.out.EffectiveCap = c.effectiveCap(replyCap, draftCap, persisted, planOrder)
	return c.out, nil
}

// planInOrder is the plan in recorded rank_position order.
func (c *finalCtx) planInOrder() []PlanRow {
	rows := append([]PlanRow(nil), c.in.Plan...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RankPosition != rows[j].RankPosition {
			return rows[i].RankPosition < rows[j].RankPosition
		}
		return keyLess(rows[i].Key, rows[j].Key)
	})
	return rows
}

// resolveReply resolves the raw keys of the reply to canonical keys, drops
// repeats and rejects a key on both sides.
func (c *finalCtx) resolveReply() (plus, minus []Key, err error) {
	seen := map[Key]bool{}
	for _, raw := range c.in.Reply.Plus {
		k, err := ParseKey(raw, c.in.Config)
		if err != nil {
			return nil, nil, err
		}
		if !seen[k] {
			seen[k] = true
			plus = append(plus, k)
		}
	}
	seenMinus := map[Key]bool{}
	for _, raw := range c.in.Reply.Minus {
		k, err := ParseKey(raw, c.in.Config)
		if err != nil {
			return nil, nil, err
		}
		if seen[k] {
			return nil, nil, usageErr("drop one of them", "conflicting tokens for %s: -%s and +%s", k, raw, raw)
		}
		if !seenMinus[k] {
			seenMinus[k] = true
			minus = append(minus, k)
		}
	}
	return plus, minus, nil
}

// applyMerges validates the --merge pairs and settles the merge reversals of
// the +keys: the set of absorbed keys, the merges to record and the recorded
// merges to remove.
func (c *finalCtx) applyMerges(plus []Key) error {
	plusSet := map[Key]bool{}
	for _, k := range plus {
		plusSet[k] = true
	}
	byAbsorbed := map[Key]Key{}
	for _, m := range c.in.Merges {
		if m.Kept == m.Absorbed {
			return usageErr("merge two different keys", "cannot merge %s into itself", m.Kept)
		}
		if prev, ok := byAbsorbed[m.Absorbed]; ok {
			if prev != m.Kept {
				return usageErr("merge each key into one key", "%s is merged into both %s and %s", m.Absorbed, prev, m.Kept)
			}
			continue
		}
		byAbsorbed[m.Absorbed] = m.Kept
		// +<absorbedKey> of a merge of this run cancels it.
		if plusSet[m.Absorbed] {
			continue
		}
		c.absorbed[m.Absorbed] = true
		c.out.Absorbed = append(c.out.Absorbed, m)
	}
	for _, m := range c.in.PersistedMerges {
		if plusSet[m.Absorbed] {
			c.out.Unmerge = append(c.out.Unmerge, m)
			continue
		}
		c.absorbed[m.Absorbed] = true
	}
	return nil
}

// applyMinus applies the -keys. Every key must be known: a candidate, a key
// of the draft, a row of any period or an absorbed key.
func (c *finalCtx) applyMinus(minus []Key) error {
	for _, k := range minus {
		if _, ok := c.plan[k]; ok {
			switch {
			case c.finished(k, false):
				c.skip(k, SkipFinished)
			case c.lost(k):
				c.skip(k, SkipNoLongerCandidate)
			default:
				c.struck[k] = true
				c.out.Delete = append(c.out.Delete, Deletion{Key: k, Cause: CauseOperator})
			}
			continue
		}
		switch row, ok := c.draftRow[k]; {
		case c.in.AnyPeriodRows[k]:
			c.struck[k] = true
			c.out.Delete = append(c.out.Delete, Deletion{Key: k, Cause: CauseOperator})
		case ok:
			switch {
			case c.finished(k, row.Finished):
				c.skip(k, SkipFinished)
			case c.lost(k):
				c.skip(k, SkipNoLongerCandidate)
			default:
				// A proposal never selected has no row to delete: it is left
				// out of the final set and nothing backfills its slot.
				c.struck[k] = true
			}
		case c.cand[k]:
			c.struck[k] = true
		case c.absorbed[k]:
			c.skip(k, SkipNotACandidate)
		default:
			return usageErr("check the key", "-%s names no candidate and no row in any period", k)
		}
	}
	return nil
}

// proposalWindow is the ordered list of draft keys the lock proposes. At a
// first lock it is the rows marked + or, with a reply cap, the first N
// unfinished rows of the SAME draft order. Over a plan only an initial draft
// proposes anything (a replan draft marks none, and a reply cap never makes
// it propose), and its window is built the same way.
func (c *finalCtx) proposalWindow(rows []DraftRow, replyCap *int) []Key {
	if !c.firstLock && !c.initialDraft(rows) {
		return nil
	}
	var out []Key
	if replyCap == nil {
		for _, r := range rows {
			if r.Plus {
				out = append(out, r.Key)
			}
		}
		return out
	}
	taken := 0
	for _, r := range rows {
		if taken >= *replyCap {
			break
		}
		out = append(out, r.Key)
		if !c.finished(r.Key, r.Finished) {
			taken++
		}
	}
	return out
}

// initialDraft tells an initial draft from a replan draft: only an initial
// draft marks a row + or holds a row that is already in the plan.
func (c *finalCtx) initialDraft(rows []DraftRow) bool {
	for _, r := range rows {
		if _, ok := c.plan[r.Key]; ok || r.Plus {
			return true
		}
	}
	return false
}

// applyPlus applies the +keys, in reply order. inWindow holds the keys the
// proposals already cover.
func (c *finalCtx) applyPlus(plus []Key, inWindow map[Key]bool) error {
	for _, k := range plus {
		if _, ok := c.plan[k]; ok {
			switch {
			case c.finished(k, false):
				c.skip(k, SkipFinished)
			case c.lost(k):
				c.skip(k, SkipNoLongerCandidate)
			default:
				c.skip(k, SkipAlreadySelected)
			}
			continue
		}
		if c.in.OpenChild != nil {
			if child, ok := c.in.OpenChild(k); ok {
				return usageErr("pull the open child instead",
					"+%s is an epic with an open child %s", k, child)
			}
		}
		if row, ok := c.draftRow[k]; ok {
			switch {
			case c.finished(k, row.Finished):
				c.skip(k, SkipFinished)
			case c.lost(k):
				c.skip(k, SkipNoLongerCandidate)
			case inWindow[k]:
				// Already proposed by the draft: nothing to force.
			default:
				tier := row.Tier
				c.force(k, &tier)
			}
			continue
		}
		if c.cand[k] {
			switch {
			case c.finished(k, false):
				c.skip(k, SkipFinished)
			case c.lost(k):
				c.skip(k, SkipNoLongerCandidate)
			default:
				c.force(k, nil)
			}
			continue
		}
		if f, ok := c.in.Facts[k]; ok {
			if f.Finished {
				return usageErr("name an open item", "+%s is closed or finished", k)
			}
			if f.Hidden {
				return usageErr("unhide it first", "+%s is hidden", k)
			}
		}
		c.out.HandAdded = append(c.out.HandAdded, k)
		c.out.Insert = append(c.out.Insert, Insertion{Key: k, Source: SourceHandAdd})
	}
	return nil
}

func (c *finalCtx) force(k Key, tier *string) {
	c.out.Forced = append(c.out.Forced, k)
	c.out.Insert = append(c.out.Insert, Insertion{Key: k, Source: SourceForce, Tier: tier})
}

// effectiveCap is documented on FinalSet.
func (c *finalCtx) effectiveCap(replyCap *int, draftCap, persisted int, plan []PlanRow) int {
	if c.firstLock {
		base := draftCap
		if replyCap != nil {
			base = *replyCap
		}
		return base + len(c.out.Forced) + len(c.out.HandAdded)
	}
	base := persisted
	if replyCap != nil {
		base = *replyCap
	}
	unfinished := len(c.out.Insert)
	for _, p := range plan {
		if !c.finished(p.Key, false) && !c.struck[p.Key] {
			unfinished++
		}
	}
	if unfinished > base {
		return unfinished
	}
	return base
}
