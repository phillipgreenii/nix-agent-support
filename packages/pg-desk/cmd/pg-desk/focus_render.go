package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// The shared rendering of `pg-desk focus show` and `pg-desk focus replan`
// (spec section 7.1, layout): one screen model, printed as text in the fixed
// section order or as the --json document. The initial draft, replan and
// plan mode share this skeleton and differ only in what fills PLAN.
//
// Telemetry (D24): rendering emits no OpenTelemetry or Prometheus signal and
// logs nothing.

// Marks of the MARK column: a row in the locked plan, a row the draft
// proposes (would be selected on ok), or neither.
const (
	markPlan     = "*"
	markProposed = "+"
)

// defaultShownCandidates is how many candidates below the cap line a table
// shows without --all ("the in-plan rows plus the next ten").
const defaultShownCandidates = 10

// Modes of the --json document.
const (
	focusModeDraft = "draft"
	focusModePlan  = "plan"
)

// screenRow is one table row with its live facts and bead column.
type screenRow struct {
	Mark         string   `json:"mark"`
	Rank         int      `json:"rank"`
	Key          string   `json:"key"`
	Type         string   `json:"type"`
	ID           string   `json:"id"`
	Kind         string   `json:"kind,omitempty"`
	Title        string   `json:"title"`
	Tier         string   `json:"tier,omitempty"`
	DecidingKey  string   `json:"deciding_key,omitempty"`
	Due          string   `json:"due,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	State        string   `json:"state,omitempty"`
	Bead         string   `json:"bead"`
	BeadState    string   `json:"bead_state"`
	Plus         bool     `json:"plus"`
	InPlan       bool     `json:"in_plan"`
	Finished     bool     `json:"finished"`
	Outcome      string   `json:"outcome,omitempty"`
	New          bool     `json:"new"`
	Stale        bool     `json:"stale"`
	Marks        []string `json:"marks"`
	Via          *jsonVia `json:"via,omitempty"`
	SelectedAt   string   `json:"selected_at,omitempty"`
	AbsorbedInto string   `json:"absorbed_into,omitempty"`
}

type jsonVia struct {
	Seed     string `json:"seed"`
	Relation string `json:"relation"`
	More     int    `json:"more,omitempty"`
}

type jsonEpic struct {
	Key    string `json:"key"`
	Title  string `json:"title,omitempty"`
	InPlay int    `json:"in_play"`
	Total  int    `json:"total"`
}

type jsonCapLine struct {
	Cap       int    `json:"cap"`
	InPlan    int    `json:"in_plan"`
	OpenSlots int    `json:"open_slots"`
	Finished  int    `json:"finished"`
	Text      string `json:"text"`
}

type jsonDrift struct {
	RowsPlacedDifferently int      `json:"rows_placed_differently"`
	NowFinished           []string `json:"now_finished"`
	NoLongerCandidate     []string `json:"no_longer_candidate"`
}

// focusScreen is everything one show or replan prints.
type focusScreen struct {
	Verb         string
	Period       focusPeriod
	PeriodClosed bool
	Today        bool
	Scope        string
	Mode         string
	Stored       bool
	// DraftLine is the draft line of the layout: DRAFT (...) or PLAN (locked ...).
	DraftLine string
	HasPlan   bool
	MadeAt    string
	Cap       int
	CapLine   focus.CapLine
	Notices   []string
	Coverage  focus.CoverageReport
	Sources   string
	Plan      []screenRow
	PlanRows  []screenRow
	Cands     []screenRow
	// AllCands are the candidates before the table truncates (the JSON keeps them all).
	AllCands []screenRow
	// TotalCands is the number of candidates before truncation.
	TotalCands int
	ShowAll    bool
	CandsLabel string
	Epics      []jsonEpic
	NewSince   []screenRow
	Drift      *jsonDrift
	Unclosed   []string
	Inputs     focus.RankInputs
	UnknownKid map[string]int
}

func capLineJSON(c focus.CapLine) jsonCapLine {
	return jsonCapLine{Cap: c.Cap, InPlan: c.InPlan, OpenSlots: c.OpenSlots, Finished: c.Finished, Text: c.String()}
}

// capLineOf builds a cap line from counts; open slots are never negative.
func capLineOf(cap, inPlan, finished int) focus.CapLine {
	open := cap - inPlan
	if open < 0 {
		open = 0
	}
	return focus.CapLine{Cap: cap, InPlan: inPlan, OpenSlots: open, Finished: finished}
}

func keyText(k focus.Key) string { return k.String() }

func viaOf(v *focus.Via) *jsonVia {
	if v == nil {
		return nil
	}
	return &jsonVia{Seed: keyText(v.SeedKey), Relation: v.Relation, More: v.More}
}

// rowMarks are the marks of a row that is not read through BuildPlanView:
// finished, claimed, deferred and no-longer-a-candidate.
func rowMarks(f focus.Facts) []string {
	marks := []string{}
	if f.Finished {
		marks = append(marks, "finished")
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
	return marks
}

// fillLive copies the live facts, the bead column and the absorbed mark into r.
func fillLive(r *screenRow, k focus.Key, live focus.Live) {
	f := live.Facts[k]
	r.Key, r.Type, r.ID = keyText(k), k.Type, k.ID
	r.Title, r.State, r.Stale = f.Title, f.State, f.Stale
	if r.Due == "" {
		r.Due = f.Due
	}
	if r.Priority == "" {
		r.Priority = f.Priority
	}
	if f.Finished {
		r.Finished, r.Outcome = true, f.Outcome
	}
	if cell, ok := live.Beads[k]; ok {
		r.Bead, r.BeadState = cell.Shown, string(cell.State)
	}
	if by, ok := live.Absorb[k]; ok {
		r.AbsorbedInto = keyText(by)
		r.Marks = append(r.Marks, "absorbed into "+keyText(by))
	}
}

// ---------------------------------------------------------------- text

func writeRows(w io.Writer, rows []screenRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MARK\tRANK\tTIER\tDECIDES\tDUE\tPRI\tBEAD\tKEY\tTITLE")
	for _, r := range rows {
		rank := "-"
		if r.Rank > 0 {
			rank = fmt.Sprint(r.Rank)
		}
		title := r.Title
		if r.Stale {
			title += " (stale)"
		}
		var marks []string
		if r.New {
			marks = append(marks, "new")
		}
		for _, m := range r.Marks {
			if m == "finished" && r.Outcome != "" {
				m = "finished (" + r.Outcome + ")"
			}
			marks = append(marks, m)
		}
		if r.Via != nil {
			via := "via " + r.Via.Seed + " (" + r.Via.Relation + ")"
			if r.Via.More > 0 {
				via += fmt.Sprintf(" +%d more", r.Via.More)
			}
			marks = append(marks, via)
		}
		if len(marks) > 0 {
			title += "  [" + strings.Join(marks, "; ") + "]"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			orBlank(r.Mark), rank, dashIfBlank(r.Tier), dashIfBlank(r.DecidingKey), dashIfBlank(r.Due), dashIfBlank(r.Priority),
			dashIfBlank(r.Bead), r.Key, title)
	}
	_ = tw.Flush()
}

func dashIfBlank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func orBlank(s string) string {
	if s == "" {
		return " "
	}
	return s
}

func ageText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// writeText prints the screen in the fixed section order: scope, notices,
// coverage, the per-source candidates line, the draft line, the PLAN table,
// the cap line, the candidates table, the epics block, new since draft and
// the footer.
func (s *focusScreen) writeText(w io.Writer) {
	closed := ""
	if s.PeriodClosed {
		closed = ", period closed"
	}
	_, _ = fmt.Fprintf(w, "scope: %s, period %s%s\n", s.Scope, s.Period.Key, closed)
	for _, n := range s.Notices {
		_, _ = fmt.Fprintln(w, n)
	}
	s.writeCoverage(w)
	_, _ = fmt.Fprintln(w, s.Sources)
	_, _ = fmt.Fprintln(w, s.DraftLine)
	if s.Drift != nil {
		d := s.Drift
		if d.RowsPlacedDifferently > 0 || len(d.NowFinished) > 0 || len(d.NoLongerCandidate) > 0 {
			_, _ = fmt.Fprintf(w, "drift: %d draft rows would sit differently under a fresh rank; finished since: %s; no longer candidates: %s\n",
				d.RowsPlacedDifferently, joinOrNone(d.NowFinished), joinOrNone(d.NoLongerCandidate))
		}
	}
	_, _ = fmt.Fprintln(w, "PLAN")
	if len(s.Plan) == 0 {
		_, _ = fmt.Fprintln(w, "  (empty)")
	} else {
		writeRows(w, s.Plan)
	}
	_, _ = fmt.Fprintln(w, s.CapLine.String())
	_, _ = fmt.Fprintln(w, s.CandsLabel)
	if len(s.Cands) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		writeRows(w, s.Cands)
	}
	if len(s.Epics) > 0 {
		_, _ = fmt.Fprintln(w, "epics with children in play")
		for _, e := range s.Epics {
			t := ""
			if e.Title != "" {
				t = "  " + e.Title
			}
			_, _ = fmt.Fprintf(w, "  %s  %d of %d open descendants in the plan%s\n", e.Key, e.InPlay, e.Total, t)
		}
	}
	if len(s.NewSince) > 0 {
		_, _ = fmt.Fprintln(w, "new since draft")
		writeRows(w, s.NewSince)
	}
	if !s.ShowAll && s.TotalCands > len(s.Cands) {
		_, _ = fmt.Fprintf(w, "%d shown of %d candidates (--all)\n", len(s.Cands), s.TotalCands)
	}
}

func joinOrNone(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

func (s *focusScreen) writeCoverage(w io.Writer) {
	c := s.Coverage.State
	if c.Incomplete {
		_, _ = fmt.Fprintf(w, "coverage: INCOMPLETE: %s\n", strings.Join(c.Reasons, "; "))
	} else {
		_, _ = fmt.Fprintln(w, "coverage: complete")
	}
	for _, t := range c.SortedTypes() {
		tc := c.PerType[t]
		oldest := tc.OldestHydratedAt
		if oldest == "" {
			oldest = "never"
		}
		var qs []string
		for _, l := range tc.Listings {
			q := l.Query + " " + l.Status
			if l.Reason != "" {
				q += " (" + l.Reason + ")"
			}
			qs = append(qs, q)
		}
		_, _ = fmt.Fprintf(w, "  %s: %d active, %d due or never hydrated, oldest hydrated %s; queries: %s\n",
			t, tc.Active, tc.DueBacklog, oldest, joinOrNone(qs))
	}
	for _, n := range s.Coverage.Notices {
		_, _ = fmt.Fprintf(w, "  NOTICE %s\n", n)
	}
	keys := make([]string, 0, len(s.UnknownKid))
	for k := range s.UnknownKid {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "  epic %s names %d children not in the store yet\n", k, s.UnknownKid[k])
	}
	in := s.Inputs
	_, _ = fmt.Fprintf(w, "  rank_inputs: unparseable_due %d, unmapped_priority %d, age_fallback %d, unblocks_unavailable %d, status_category_absent %d\n",
		in.UnparseableDue, in.UnmappedPriority, in.AgeFallback, in.UnblocksUnavailable, in.StatusCategoryAbsent)
}

// ---------------------------------------------------------------- json

// payload is the --json document of a show or replan (the draft document of
// the spec, plus the mode-independent members).
func (s *focusScreen) payload() map[string]any {
	rows := make([]screenRow, 0, len(s.Plan)+len(s.AllCands))
	rows = append(rows, s.Plan...)
	rows = append(rows, s.AllCands...)
	for i := range rows {
		if rows[i].Marks == nil {
			rows[i].Marks = []string{}
		}
	}
	planRows := s.PlanRows
	if planRows == nil {
		planRows = []screenRow{}
	}
	proposed := []string{}
	for _, r := range s.Plan {
		if r.Plus {
			proposed = append(proposed, r.Key)
		}
	}
	epics := s.Epics
	if epics == nil {
		epics = []jsonEpic{}
	}
	newSince := s.NewSince
	if newSince == nil {
		newSince = []screenRow{}
	}
	unclosed := s.Unclosed
	if unclosed == nil {
		unclosed = []string{}
	}
	notices := s.Notices
	if notices == nil {
		notices = []string{}
	}
	cov := s.Coverage.State
	if cov.Reasons == nil {
		cov.Reasons = []string{}
	}
	var madeAt any
	if s.MadeAt != "" {
		madeAt = s.MadeAt
	}
	p := map[string]any{
		"mode":             s.Mode,
		"stored":           s.Stored,
		"has_plan":         s.HasPlan,
		"plan_rows":        planRows,
		"unclosed_periods": unclosed,
		"period":           s.Period.Key,
		"period_closed":    s.PeriodClosed,
		"scope":            s.Scope,
		"made_at":          madeAt,
		"cap":              s.Cap,
		"cap_line":         capLineJSON(s.CapLine),
		"rows":             rows,
		"proposed":         proposed,
		"candidates_total": s.TotalCands,
		"candidates_shown": len(s.Cands),
		"epics":            epics,
		"new_since_draft":  newSince,
		"coverage": map[string]any{
			"incomplete": cov.Incomplete,
			"reasons":    cov.Reasons,
			"per_type":   cov.PerType,
			"notices":    nonNil(s.Coverage.Notices),
		},
		"rank_inputs":      s.Inputs,
		"notices":          notices,
		"unknown_children": s.UnknownKid,
		"sources":          s.Sources,
	}
	if s.Drift != nil {
		d := *s.Drift
		if d.NowFinished == nil {
			d.NowFinished = []string{}
		}
		if d.NoLongerCandidate == nil {
			d.NoLongerCandidate = []string{}
		}
		p["drift"] = d
	}
	return p
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
