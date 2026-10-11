package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// What `focus show` and `focus replan` read before they decide a mode: the
// candidate set, the coverage, the addressed period's plan and the stored
// draft, all from the store (no tracker call). The screen builder then turns
// a gather plus a mode into the rows both verbs print.
//
// Telemetry (D24): no OpenTelemetry or Prometheus signal, no logging.

// focusGather is the read-only state of one show or replan run.
type focusGather struct {
	env    *focusEnv
	repo   string
	in     focus.Inputs
	set    focus.CandidateSet
	cov    focus.CoverageReport
	loc    *time.Location
	period store.FocusPeriod
	found  bool
	closed bool
	sel    []store.FocusSelection
	plan   []focus.PlanRow
	draft  store.FocusDraft
	// hasDraft is true when a draft row is stored.
	hasDraft bool
	unclosed []string
	watched  focus.Watched
}

func gatherFocus(env *focusEnv) (*focusGather, error) {
	if len(env.Cfg.Repos) == 0 {
		return nil, focusUsageError("focus: no repository configured", "set repos in the pg-desk config")
	}
	g := &focusGather{env: env, repo: env.Cfg.Repos[0].Remote, loc: env.Cfg.FocusTimeZone()}
	in, err := focus.Load(env.Store, env.Cfg, g.repo, env.Now)
	if err != nil {
		return nil, focusTotalError("focus: read the store: "+err.Error(), "check the pg-desk store, then re-run")
	}
	g.in = in
	g.set = focus.Candidates(in)
	cov, err := focus.ComputeCoverage(env.Store, env.Cfg, env.Now, in.Entities)
	if err != nil {
		return nil, focusTotalError("focus: "+err.Error(), "check the pg-desk store, then re-run")
	}
	g.cov = cov
	g.watched = focus.LoadWatched(env.Store, env.Cfg)
	if err := g.reloadPlan(); err != nil {
		return nil, err
	}
	d, found, err := env.Store.FocusDraftGet()
	if err != nil {
		return nil, focusTotalError("focus: read the stored draft: "+err.Error(), "check the pg-desk store, then re-run")
	}
	g.draft, g.hasDraft = d, found
	return g, nil
}

// reloadPlan re-reads the addressed period, its plan rows and the unclosed
// older periods. A lock that committed since the last read shows here.
func (g *focusGather) reloadPlan() error {
	st := g.env.Store
	p, found, err := st.FocusPeriodGet(g.env.Period.Type, g.env.Period.Key)
	if err != nil {
		return focusTotalError("focus: read period: "+err.Error(), "check the pg-desk store, then re-run")
	}
	g.period, g.found = p, found
	g.closed = found && p.ClosedAt != ""
	g.sel, g.plan = nil, nil
	if found {
		rows, err := st.FocusPlanRows(p.ID)
		if err != nil {
			return focusTotalError("focus: read plan rows: "+err.Error(), "check the pg-desk store, then re-run")
		}
		for _, r := range rows {
			if r.Repo != g.repo {
				continue
			}
			g.sel = append(g.sel, r)
			g.plan = append(g.plan, focus.PlanRow{
				Key:          focus.Key{Type: r.EntityType, ID: r.EntityID},
				RankPosition: r.RankPosition, Tier: r.Tier, SelectedAt: r.SelectedAt,
			})
		}
	}
	periods, err := st.FocusPeriods()
	if err != nil {
		return focusTotalError("focus: read periods: "+err.Error(), "check the pg-desk store, then re-run")
	}
	all, err := st.FocusRowsAllPeriods()
	if err != nil {
		return focusTotalError("focus: read plan rows: "+err.Error(), "check the pg-desk store, then re-run")
	}
	held := map[int64]int{}
	for _, r := range all {
		held[r.FocusPeriodID]++
	}
	g.unclosed = nil
	for _, op := range periods {
		if op.PeriodType == g.env.Period.Type && op.PeriodKey < g.env.Period.Key && op.ClosedAt == "" && held[op.ID] > 0 {
			g.unclosed = append(g.unclosed, op.PeriodKey)
		}
	}
	sort.Strings(g.unclosed)
	return nil
}

func (g *focusGather) hasPlan() bool { return len(g.plan) > 0 }

// totalFailure is exit 3: no candidates are computable (the set is empty and
// every known listing failed).
func (g *focusGather) totalFailure() bool {
	return len(g.set.Candidates) == 0 && g.cov.AllFailed
}

// addressedDate is the addressed day as a time in the focus zone.
func (g *focusGather) addressedDate() time.Time {
	t, err := time.ParseInLocation(focusDayLayout, g.env.Period.Key, g.loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// rank computes the fresh rank under cap.
func (g *focusGather) rank(cap int) focus.Ranking {
	return focus.Rank(g.in, g.set, focus.RankOptions{Date: g.addressedDate(), Cap: cap})
}

// periodCap is the persisted cap of the addressed period, else the default.
func (g *focusGather) periodCap() int {
	if g.found && g.period.Cap != nil && *g.period.Cap > 0 {
		return *g.period.Cap
	}
	return focus.DefaultCap
}

func (g *focusGather) firstSeen(k focus.Key) string {
	return g.in.Entities[k].FirstSeenAt
}

func (g *focusGather) selectedAt() map[focus.Key]time.Time {
	out := make(map[focus.Key]time.Time, len(g.plan))
	for _, p := range g.plan {
		if t, err := time.Parse(time.RFC3339Nano, p.SelectedAt); err == nil {
			out[p.Key] = t
		}
	}
	return out
}

// listingNotices is one NOTICE line per failed, degraded or unknown candidate
// source, so a table that looks normal cannot hide a missing source.
func (g *focusGather) listingNotices() []string {
	var out []string
	for _, t := range g.cov.State.SortedTypes() {
		for _, l := range g.cov.State.PerType[t].Listings {
			if l.Status == "ok" {
				continue
			}
			word := strings.ToUpper(l.Status)
			reason := ""
			if l.Reason != "" {
				reason = " (" + l.Reason + ")"
			}
			out = append(out, fmt.Sprintf("NOTICE %s %s%s: candidates from this %s query are NOT all in this table", l.Query, word, reason, t))
		}
	}
	return out
}

// setNotices are the candidate set's notices that print as NOTICE lines.
func (g *focusGather) setNotices() []string {
	var out []string
	for _, n := range g.set.Notices {
		if n.Code == focus.NoticeEmptyIdentityList {
			out = append(out, "NOTICE focus.operator_identities is empty: no Jira issue can be a candidate by assignment")
		}
	}
	return out
}

// sourcesLine is the per-source candidates: count line; an empty source says
// why (and names the label).
func (g *focusGather) sourcesLine() string {
	c := g.set.Counts
	beads := fmt.Sprintf("bead %d", c.Bead)
	for _, n := range g.set.Notices {
		switch n.Code {
		case focus.NoticeNoBeadCandidates:
			beads += fmt.Sprintf(" (label %s matches none of %d open beads; label one or use +key)", focus.PlanableLabel, c.OpenBeads)
		case focus.NoticeNoBeadPattern:
			beads += fmt.Sprintf(" (bead_id_pattern is unset, so no issue is a bead; label %s needs it)", focus.PlanableLabel)
		}
	}
	s := fmt.Sprintf("candidates: pr %d, jira %d, %s", c.PR, c.Jira, beads)
	if c.Linked > 0 {
		s += fmt.Sprintf(", %d of them via links", c.Linked)
	}
	return s
}

// screenInput is what a verb decided: the mode, whether a draft is stored,
// and the draft or plan the screen fills PLAN from.
type screenInput struct {
	verb      string
	mode      string
	stored    bool
	draftLine string
	fresh     focus.Ranking
	cap       int
	// doc is the draft whose rows fill the candidates block; nil in plan
	// mode, where the candidates are the fresh rank less the plan.
	doc *focus.DraftDoc
	// madeAt is the draft's made_at; reRender shows the drift and the age.
	madeAt   time.Time
	reRender bool
	showAll  bool
	notices  []string
}

func (g *focusGather) buildScreen(si screenInput) *focusScreen {
	env := g.env
	s := &focusScreen{
		Verb: si.verb, Period: env.Period, PeriodClosed: g.closed, Today: env.Today,
		Scope: g.repo, Mode: si.mode, Stored: si.stored, DraftLine: si.draftLine,
		HasPlan: g.hasPlan(), Cap: si.cap, Coverage: g.cov, Sources: g.sourcesLine(),
		ShowAll: si.showAll, Unclosed: g.unclosed, Inputs: si.fresh.Inputs,
		UnknownKid: map[string]int{},
	}
	if si.doc != nil {
		s.Inputs = si.doc.Inputs
	}
	for k, n := range si.fresh.UnknownChildren {
		if n > 0 {
			s.UnknownKid[keyText(k)] = n
		}
	}

	// Which keys need live facts.
	seen := map[focus.Key]bool{}
	var keys []focus.Key
	add := func(k focus.Key) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, p := range g.plan {
		add(p.Key)
	}
	for _, r := range si.fresh.Rows {
		add(r.Key)
	}
	for _, e := range si.fresh.EpicsInPlay {
		add(e.Key)
	}
	if si.doc != nil {
		for _, r := range si.doc.Rows {
			add(r.Key)
		}
		for _, e := range si.doc.Epics {
			add(e.Key)
		}
	}
	live := focus.BuildLive(g.in, keys, si.fresh, focus.LiveOptions{Watched: g.watched, SelectedAt: g.selectedAt()})

	pv := focus.BuildPlanView(g.plan, live.Facts, si.fresh, si.cap)
	inPlan := map[focus.Key]bool{}
	for _, pr := range pv.Rows {
		inPlan[pr.Key] = true
		row := screenRow{Mark: markPlan, Rank: pr.RankPosition, InPlan: true, SelectedAt: pr.SelectedAt}
		if pr.Tier != nil {
			row.Tier = *pr.Tier
		}
		fillLive(&row, pr.Key, live)
		row.Marks = append(append([]string{}, pr.Marks...), row.Marks...)
		s.Plan = append(s.Plan, row)
		s.PlanRows = append(s.PlanRows, row)
	}
	capLine := pv.CapLine
	capLine.Cap = si.cap

	var cands []screenRow
	if si.doc != nil {
		view := focus.ReRender(*si.doc, si.madeAt, env.Now, live.Facts, si.fresh, g.plan)
		inPlanUnfinished, finished := pv.CapLine.InPlan, pv.CapLine.Finished
		for i, vr := range view.Rows {
			if inPlan[vr.Key] {
				continue
			}
			row := screenRow{
				Rank: i + 1, Tier: vr.Tier, DecidingKey: vr.DecidingKey, Kind: string(vr.Kind),
				Via: viaOf(vr.Via), New: vr.New, Plus: vr.Plus && !vr.Finished,
			}
			fillLive(&row, vr.Key, live)
			row.Marks = append(rowMarks(live.Facts[vr.Key]), row.Marks...)
			row.Finished = row.Finished || vr.Finished
			if row.Plus {
				row.Mark = markProposed
				s.Plan = append(s.Plan, row)
				inPlanUnfinished++
				continue
			}
			cands = append(cands, row)
		}
		capLine = capLineOf(si.cap, inPlanUnfinished, finished)
		for _, k := range view.NewSinceDraft {
			for _, fr := range si.fresh.Rows {
				if fr.Key != k {
					continue
				}
				row := screenRow{Rank: fr.Position, Tier: fr.Tier, DecidingKey: fr.DecidingKey, Kind: string(fr.Kind), Via: viaOf(fr.Via)}
				fillLive(&row, k, live)
				row.Marks = append(rowMarks(live.Facts[k]), row.Marks...)
				s.NewSince = append(s.NewSince, row)
			}
		}
		if si.reRender {
			d := view.Drift
			s.Drift = &jsonDrift{RowsPlacedDifferently: d.RowsPlacedDifferently, NowFinished: keyTexts(d.NowFinished), NoLongerCandidate: keyTexts(d.NoLongerCandidate)}
		}
		s.MadeAt = view.MadeAt
		for _, e := range si.doc.Epics {
			s.Epics = append(s.Epics, jsonEpic{Key: keyText(e.Key), Title: live.Facts[e.Key].Title, InPlay: e.InPlay, Total: e.Total})
		}
		s.CandsLabel = "candidates (draft order; every fact is live)"
	} else {
		for _, fr := range si.fresh.Rows {
			if inPlan[fr.Key] {
				continue
			}
			row := screenRow{Rank: fr.Position, Tier: fr.Tier, DecidingKey: fr.DecidingKey, Kind: string(fr.Kind), Via: viaOf(fr.Via), Finished: fr.Finished}
			fillLive(&row, fr.Key, live)
			row.Marks = append(rowMarks(live.Facts[fr.Key]), row.Marks...)
			cands = append(cands, row)
		}
		for _, e := range si.fresh.EpicsInPlay {
			s.Epics = append(s.Epics, jsonEpic{Key: keyText(e.Key), Title: live.Facts[e.Key].Title, InPlay: e.InPlay, Total: e.Total})
		}
		s.CandsLabel = "candidates (current rank, not frozen)"
		if n := g.aboveLastPlanRow(si.fresh, inPlan); n > 0 {
			s.Notices = append(s.Notices, fmt.Sprintf("NOTICE %d candidates now rank above the plan's last row; run replan to re-order", n))
		}
	}
	s.CapLine = capLine
	s.TotalCands = len(cands)
	s.AllCands = cands
	if !si.showAll && len(cands) > defaultShownCandidates {
		cands = cands[:defaultShownCandidates]
	}
	s.Cands = cands

	notices := append([]string{}, si.notices...)
	notices = append(notices, g.listingNotices()...)
	notices = append(notices, g.setNotices()...)
	for _, d := range g.unclosed {
		notices = append(notices, fmt.Sprintf("NOTICE period %s was never closed", d))
	}
	s.Notices = append(notices, s.Notices...)
	return s
}

func keyTexts(ks []focus.Key) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, keyText(k))
	}
	return out
}

// aboveLastPlanRow counts the candidates outside the plan that the fresh
// rank now places ahead of the plan's last-ranked row.
func (g *focusGather) aboveLastPlanRow(fresh focus.Ranking, inPlan map[focus.Key]bool) int {
	last := 0
	for _, r := range fresh.Rows {
		if inPlan[r.Key] && r.Position > last {
			last = r.Position
		}
	}
	n := 0
	for _, r := range fresh.Rows {
		if !inPlan[r.Key] && !r.Finished && r.Position < last {
			n++
		}
	}
	return n
}

// coverageResult is the exit of a printed screen: 3 for a total failure, 2
// for incomplete coverage, else nil (0).
func (g *focusGather) coverageResult() error {
	if g.totalFailure() {
		return focusTotalError(
			"focus: no candidates are computable ("+strings.Join(g.cov.State.Reasons, "; ")+")",
			"re-run pg-desk changes for the failed queries, then re-run",
		)
	}
	if g.cov.State.Incomplete {
		return focusPartialError(
			"focus: coverage incomplete ("+strings.Join(g.cov.State.Reasons, "; ")+")",
			"the table above is shown with what the store holds; re-run pg-desk changes to refresh it",
		)
	}
	return nil
}
