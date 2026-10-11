package focus

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DraftContract is the contract member of the stored draft document.
const DraftContract = "pg-desk.focus.draft/v1"

// DraftMode is the mode member of a stored draft: a draft is always `draft`;
// the `plan` mode of the verbs' JSON is produced from a PlanView, never
// stored.
const DraftMode = "draft"

// ErrDraftUnreadable reports a stored draft body that cannot be used: it is
// not decodable, or its contract member is missing or is not exactly
// DraftContract. A caller treats it as an ABSENT draft, never as a failure.
var ErrDraftUnreadable = errors.New("focus: stored draft unreadable")

// PlanRow is one row of the locked plan: a store.FocusSelection reduced to
// what the draft and plan views read. Tier is nil when none was recorded.
type PlanRow struct {
	Key          Key     `json:"key"`
	RankPosition int     `json:"rank_position"`
	Tier         *string `json:"tier,omitempty"`
	SelectedAt   string  `json:"selected_at"`
}

// Listing is the status of one watch query the coverage state was built from.
type Listing struct {
	Query  string `json:"query"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// TypeCoverage is the coverage of one entity type.
type TypeCoverage struct {
	Active           int       `json:"active"`
	DueBacklog       int       `json:"due_backlog"`
	OldestHydratedAt string    `json:"oldest_hydrated_at,omitempty"`
	Listings         []Listing `json:"listings,omitempty"`
}

// CoverageState is what the store held when a draft was computed: per entity
// type counts, and whether any listing was incomplete (with the reasons). The
// coverage computation belongs to the verbs; the draft only carries it.
type CoverageState struct {
	PerType    map[string]TypeCoverage `json:"per_type,omitempty"`
	Incomplete bool                    `json:"incomplete"`
	Reasons    []string                `json:"reasons,omitempty"`
}

// DraftRow is one ordered row of a draft. It stores candidates, proposals and
// marks only: never a fact (title, due date, state), which is read live at
// render. Plus proposes the row for the plan; Finished and New are the marks
// of the moment the draft was computed.
type DraftRow struct {
	Key         Key    `json:"key"`
	Kind        Kind   `json:"kind"`
	Tier        string `json:"tier"`
	DecidingKey string `json:"deciding_key,omitempty"`
	Plus        bool   `json:"plus"`
	Finished    bool   `json:"finished"`
	New         bool   `json:"new"`
	Via         *Via   `json:"via,omitempty"`
}

// DraftDoc is the stored draft: a Memento the tool keeps singly and that ends
// at lock. It holds the order, tiers, proposals, marks, the trailing epics
// block and the coverage state and rank inputs it was computed under. It
// stores NO plan rows (the PLAN block is read live from the plan) and NO
// facts.
type DraftDoc struct {
	Contract string        `json:"contract"`
	Mode     string        `json:"mode"`
	Rows     []DraftRow    `json:"rows"`
	Epics    []EpicEntry   `json:"epics"`
	Coverage CoverageState `json:"coverage"`
	Inputs   RankInputs    `json:"rank_inputs"`
}

// capOrDefault is DefaultCap when cap is not positive, as Rank treats it.
func capOrDefault(cap int) int {
	if cap <= 0 {
		return DefaultCap
	}
	return cap
}

func draftRowOf(r RankedRow) DraftRow {
	row := DraftRow{
		Key: r.Key, Kind: r.Kind, Tier: r.Tier, DecidingKey: r.DecidingKey,
		Finished: r.Finished,
	}
	if r.Via != nil {
		v := *r.Via
		row.Via = &v
	}
	return row
}

func newDraftDoc(rk Ranking, cov CoverageState) DraftDoc {
	return DraftDoc{
		Contract: DraftContract,
		Mode:     DraftMode,
		Rows:     []DraftRow{},
		Epics:    append([]EpicEntry{}, rk.EpicsInPlay...),
		Coverage: cov,
		Inputs:   rk.Inputs,
	}
}

// NewInitialDraft computes the draft of a period with no plan: every ranked
// row in rank order, the top cap UNFINISHED rows proposed (`+`). A finished
// row keeps its place, is never proposed and does not count toward cap. A cap
// that is not positive is DefaultCap.
func NewInitialDraft(rk Ranking, cap int, cov CoverageState) DraftDoc {
	cap = capOrDefault(cap)
	d := newDraftDoc(rk, cov)
	proposed := 0
	for _, r := range rk.Rows {
		row := draftRowOf(r)
		if !row.Finished && proposed < cap {
			row.Plus = true
			proposed++
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

// NewReplanDraft computes the draft over a locked plan. The plan rows stay
// out of the document; every other candidate is ordered afresh by rk and
// proposed for nothing (no row is `+`). A candidate that arrived since the
// plan was locked (its first-seen time, from firstSeen, is later than the
// earliest plan row's selected_at) carries New. With an empty plan the first
// plan is the degenerate replan: the result is the initial draft.
func NewReplanDraft(plan []PlanRow, rk Ranking, cov CoverageState, cap int, firstSeen func(Key) string) DraftDoc {
	if len(plan) == 0 {
		return NewInitialDraft(rk, cap, cov)
	}
	inPlan := make(map[Key]bool, len(plan))
	earliest := plan[0].SelectedAt
	for _, p := range plan {
		inPlan[p.Key] = true
		if timeBefore(p.SelectedAt, earliest) {
			earliest = p.SelectedAt
		}
	}
	d := newDraftDoc(rk, cov)
	for _, r := range rk.Rows {
		if inPlan[r.Key] {
			continue
		}
		row := draftRowOf(r)
		if firstSeen != nil {
			if fs := firstSeen(r.Key); fs != "" && timeBefore(earliest, fs) {
				row.New = true
			}
		}
		d.Rows = append(d.Rows, row)
	}
	return d
}

// timeBefore reports whether a is strictly earlier than b. Both are RFC 3339
// timestamps; a value that does not parse compares as text, which orders
// equal-format UTC stamps correctly.
func timeBefore(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a < b
	}
	return ta.Before(tb)
}

// EncodeDraft encodes d as the body of the stored draft row: indented JSON
// with deterministic member and map-key order, and empty (never null) lists.
// An empty Contract or Mode is filled in; any other value is an error.
func EncodeDraft(d DraftDoc) ([]byte, error) {
	if d.Contract == "" {
		d.Contract = DraftContract
	}
	if d.Contract != DraftContract {
		return nil, fmt.Errorf("focus: encode draft: contract %q, want %q", d.Contract, DraftContract)
	}
	if d.Mode == "" {
		d.Mode = DraftMode
	}
	if d.Mode != DraftMode {
		return nil, fmt.Errorf("focus: encode draft: mode %q, want %q", d.Mode, DraftMode)
	}
	if d.Rows == nil {
		d.Rows = []DraftRow{}
	}
	if d.Epics == nil {
		d.Epics = []EpicEntry{}
	}
	return json.MarshalIndent(d, "", "  ")
}

// DecodeDraft decodes a stored draft body. A body that is not valid JSON, or
// whose contract member is missing or not exactly DraftContract, is
// ErrDraftUnreadable: callers treat it as ABSENT.
func DecodeDraft(body []byte) (DraftDoc, error) {
	var d DraftDoc
	if err := json.Unmarshal(body, &d); err != nil {
		return DraftDoc{}, fmt.Errorf("%w: %v", ErrDraftUnreadable, err)
	}
	if d.Contract != DraftContract {
		return DraftDoc{}, fmt.Errorf("%w: contract %q", ErrDraftUnreadable, d.Contract)
	}
	return d, nil
}

// ProposedCapLine is the cap line of an initial draft's proposal: the rows
// marked `+` are the in-plan count against cap. A draft proposes no finished
// row, so the finished count is 0; the cap line of a plan with finished rows
// comes from BuildPlanView. A cap that is not positive is DefaultCap.
func (d DraftDoc) ProposedCapLine(cap int) CapLine {
	cap = capOrDefault(cap)
	n := 0
	for _, r := range d.Rows {
		if r.Plus {
			n++
		}
	}
	return newCapLine(cap, n, 0)
}
