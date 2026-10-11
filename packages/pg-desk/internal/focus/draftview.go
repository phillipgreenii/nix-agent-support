package focus

import (
	"sort"
	"time"
)

// Drift is how a fresh rank now differs from a stored draft. It is a notice
// only: a fresh rank is called to COUNT drift, never to order anything.
// RowsPlacedDifferently counts draft rows that sit at a different position
// than the fresh rank gives them, among the rows both hold (a row that left
// the candidate set is reported in NoLongerCandidate, not as a displacement
// of its neighbours). NowFinished lists draft rows whose source finished
// since the draft; NoLongerCandidate lists draft rows the fresh rank neither
// holds nor covers.
type Drift struct {
	RowsPlacedDifferently int
	NowFinished           []Key
	NoLongerCandidate     []Key
}

// DraftViewRow is one draft row with its live facts. The embedded DraftRow
// is the stored one, except that Finished is also true when the live facts
// say the source has finished since.
type DraftViewRow struct {
	DraftRow
	Facts Facts
}

// DraftView is a stored draft re-rendered: the draft's rows in the draft's
// order with live facts, drift from a fresh rank, and the candidates the
// draft does not hold.
type DraftView struct {
	MadeAt        string
	Age           time.Duration
	Rows          []DraftViewRow
	Drift         Drift
	NewSinceDraft []Key
}

// ReRender re-renders draft d. madeAt is the draft's made_at and now the
// injected clock reading. Rows stay in the DRAFT's order whatever fresh says;
// facts come from live. A fresh candidate that is neither in the draft nor in
// plan is listed in NewSinceDraft (in fresh order) and is never added to the
// table. Plan rows are shown by BuildPlanView, so they are never "new".
func ReRender(d DraftDoc, madeAt, now time.Time, live LiveFacts, fresh Ranking, plan []PlanRow) DraftView {
	age := now.Sub(madeAt)
	if age < 0 {
		age = 0
	}
	v := DraftView{
		MadeAt: madeAt.UTC().Format(time.RFC3339),
		Age:    age,
		Rows:   make([]DraftViewRow, 0, len(d.Rows)),
	}

	freshPos := make(map[Key]int, len(fresh.Rows))
	freshFinished := make(map[Key]bool, len(fresh.Rows))
	for i, r := range fresh.Rows {
		freshPos[r.Key] = i
		freshFinished[r.Key] = r.Finished
	}
	inDraft := make(map[Key]bool, len(d.Rows))
	inPlan := make(map[Key]bool, len(plan))
	for _, p := range plan {
		inPlan[p.Key] = true
	}

	// common holds the draft keys the fresh rank also holds, in draft order.
	var common []Key
	for _, r := range d.Rows {
		inDraft[r.Key] = true
		f := live[r.Key]
		row := r
		if f.Finished && !row.Finished {
			row.Finished = true
		}
		v.Rows = append(v.Rows, DraftViewRow{DraftRow: row, Facts: f})

		if !r.Finished && (f.Finished || freshFinished[r.Key]) {
			v.Drift.NowFinished = append(v.Drift.NowFinished, r.Key)
		}
		if _, ok := freshPos[r.Key]; ok {
			common = append(common, r.Key)
		} else if _, covered := fresh.Covered[r.Key]; !covered {
			v.Drift.NoLongerCandidate = append(v.Drift.NoLongerCandidate, r.Key)
		}
	}

	// Rank each common key inside the common set, by draft order and by
	// fresh order; a key whose two indexes differ is placed differently.
	byFresh := append([]Key(nil), common...)
	sort.SliceStable(byFresh, func(i, j int) bool { return freshPos[byFresh[i]] < freshPos[byFresh[j]] })
	freshIdx := make(map[Key]int, len(byFresh))
	for i, k := range byFresh {
		freshIdx[k] = i
	}
	for i, k := range common {
		if freshIdx[k] != i {
			v.Drift.RowsPlacedDifferently++
		}
	}

	for _, r := range fresh.Rows {
		if !inDraft[r.Key] && !inPlan[r.Key] {
			v.NewSinceDraft = append(v.NewSinceDraft, r.Key)
		}
	}
	return v
}
