package focus

import "sort"

// Facts are the LIVE facts of one entity, built by the verb from the store
// and never from a draft. Finished is set from classify (as the rank does);
// Claimant is the assignee or actor when the entity is claimed; Outcome names
// how a finished row finished (for example merged, closed or done).
type Facts struct {
	Title, Due, Priority, State, Claimant       string
	Deferred, Hidden, Inactive, Finished, Stale bool
	Outcome                                     string
	// CoveredBy is set when a slot, group or merge suppresses the row.
	CoveredBy *Key
}

// LiveFacts maps a key to its live facts.
type LiveFacts map[Key]Facts

// String is the text form of a key, type then id: `pr:42`.
func (k Key) String() string { return k.Type + ":" + k.ID }

// PlanViewRow is one row of the plan view: the recorded plan row, its live
// facts and its marks. A mark is one of `finished`, `claimed by <actor>`,
// `deferred`, `no longer a candidate: hidden`, `no longer a candidate:
// inactive` and `covered by <key>`.
type PlanViewRow struct {
	PlanRow
	Facts Facts
	Marks []string
}

// PlanView is the locked plan as it reads now: the recorded order and tier,
// live facts, and the cap line.
type PlanView struct {
	Rows    []PlanViewRow
	CapLine CapLine
}

// BuildPlanView renders the locked plan. Rows are in recorded rank_position
// order (a tie falls back to the key) with their recorded tier; nothing is
// re-sorted, re-tiered or removed whatever rk now says. rk is read only for
// the key that covers a row when the live facts do not name one. The cap line
// counts only UNFINISHED rows against cap, so a finished row frees its slot
// and is listed separately; open slots are never negative.
func BuildPlanView(plan []PlanRow, live LiveFacts, rk Ranking, cap int) PlanView {
	rows := append([]PlanRow(nil), plan...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RankPosition != rows[j].RankPosition {
			return rows[i].RankPosition < rows[j].RankPosition
		}
		if rows[i].Key.Type != rows[j].Key.Type {
			return rows[i].Key.Type < rows[j].Key.Type
		}
		return rows[i].Key.ID < rows[j].Key.ID
	})
	view := PlanView{Rows: make([]PlanViewRow, 0, len(rows))}
	unfinished, finished := 0, 0
	for _, p := range rows {
		f := live[p.Key]
		marks := []string{}
		if f.Finished {
			marks = append(marks, "finished")
			finished++
		} else {
			unfinished++
		}
		if f.Claimant != "" {
			marks = append(marks, "claimed by "+f.Claimant)
		}
		if f.Deferred {
			marks = append(marks, "deferred")
		}
		switch {
		case f.Hidden:
			marks = append(marks, "no longer a candidate: hidden")
		case f.Inactive:
			marks = append(marks, "no longer a candidate: inactive")
		}
		if by := coveringKey(f, p.Key, rk); by != nil {
			marks = append(marks, "covered by "+by.String())
		}
		view.Rows = append(view.Rows, PlanViewRow{PlanRow: p, Facts: f, Marks: marks})
	}
	view.CapLine = newCapLine(capOrDefault(cap), unfinished, finished)
	return view
}

// coveringKey is the key that covers k: the live facts' CoveredBy, else the
// ranking's. A key never covers itself.
func coveringKey(f Facts, k Key, rk Ranking) *Key {
	if f.CoveredBy != nil && *f.CoveredBy != k {
		return f.CoveredBy
	}
	if by, ok := rk.Covered[k]; ok && by != k {
		return &by
	}
	return nil
}
