package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// `pg-desk focus explain <key>` (spec section 7.5): a pure read of the live
// rows that answers "why is X among my candidates, or not in my plan" and
// "why did a bead appear". It writes nothing (no focus_period row, no draft,
// no run record, no change record) and is safe to run at any time. It is
// computed live only: there is no history to read, so the output carries no
// flip-history member. For one entity it prints candidacy and, when not a
// candidate, why (the closed list of focus.ExplainCandidate); the via of a
// linked candidate; the tier and the key that decided its position against
// each neighbor; the epic-slot reason; the position relative to the cap
// line; its plan row; its focus_selected annotation; and its bead's state
// from the bead's hold marker and its latest audit comment in the store. The
// decider's closed skip reasons are not recalled (no durable record exists):
// they are recomputed now from the same view and labelled so.
//
// Key forms: the same as select (focus.ParseKey). An unknown key prints "not
// held" and exits 0; an unmigrated store exits 1.
//
// Telemetry (D24): the verb emits no OpenTelemetry and no Prometheus signal,
// writes no focus_run row and logs nothing; its output is the whole of what
// it leaves behind. The rank is a compute-only function of stored facts: no
// language model, no network and no tracker call.

func init() {
	focusGroup().AddCommand(newFocusExplainCmd())
}

func newFocusExplainCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "explain <key> [--date YYYY-MM-DD] [--json]",
		Short: "Explain why one entity is, or is not, a candidate and where it sits",
		Long: `Explain one entity from the live store rows (there is no history to read).

The key takes the same forms as select: a PR number, a PR URL, OWNER/REPO#N, or
a tracker key. The output is a fixed list of labelled lines: whether the key is
a candidate and, if not, why (inactive, hidden, minted-bead, work-item-bead,
deferred, terminal, absorbed, not-seed-or-linked, not-in-store); for a linked
candidate its via; its tier and the key that decided its position against the
row above and the row below; the slot (ranked, suppressed by a key, or listed
in the epics-in-play block); its position against the cap line; its plan row;
its focus_selected annotation; its bead's state with the bead's hold marker and
latest audit comment; and the decider's skip reasons, recomputed now (they are
not recalled).

--date addresses the period as for the other verbs (default: today in
focus.time_zone): it selects the plan rows to read and the day the rank is
computed for. explain writes nothing and creates no period row.

Exit codes: 0 (an unknown key prints "not held" and exits 0), 1 usage error or
a store not yet migrated, 3 the store could not be read.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return focusUsageError(fmt.Sprintf("focus explain takes exactly one key, got %d", len(args)),
					"see pg-desk focus explain --help")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return runFocusExplain(cmd, args[0]) },
		// The key completes from the store's candidates (focus_complete.go).
		ValidArgsFunction: completeFocusExplainKeys,
	}
	addFocusFlags(c)
	return c
}

// explainSlot is the slot member: state is ranked, suppressed, epics_in_play
// or none; coveredBy names the covering key of a suppressed one.
type explainSlot struct {
	State     string `json:"state"`
	CoveredBy string `json:"covered_by"`
}

type explainDeciding struct {
	Above string `json:"above"`
	Below string `json:"below"`
}

type explainPlanRow struct {
	Period       string  `json:"period"`
	SelectedAt   string  `json:"selected_at"`
	RankPosition int     `json:"rank_position"`
	Tier         *string `json:"tier"`
}

type explainBead struct {
	State         string   `json:"state"`
	Marker        string   `json:"marker"`
	Audit         string   `json:"audit"`
	RecomputedNow []string `json:"recomputed_now"`
}

// explainDoc is everything explain prints; the text and --json forms are two
// renderings of it.
type explainDoc struct {
	Key       string
	Candidate bool
	Reasons   []string
	Via       *jsonVia
	Tier      string
	Deciding  explainDeciding
	Slot      explainSlot
	CapPos    *int
	Cap       int
	Plan      *explainPlanRow
	// planElsewhere is set when the plan row is in a period other than the
	// addressed one.
	PlanElsewhere bool
	Addressed     string
	Annotation    string
	Bead          explainBead
	// AuditStored is false when the bead exists but the store holds no
	// comment list for it.
	AuditStored bool
	// NotHeld is set for a key the store has no entity for.
	NotHeld bool
}

func runFocusExplain(cmd *cobra.Command, raw string) error {
	env, err := openFocusEnv(cmd, false)
	if err != nil {
		return err
	}
	defer env.Close()
	key, err := focus.ParseKey(raw, env.Cfg)
	if err != nil {
		return mapFocusErr(err, env.Period.Key)
	}
	doc, err := buildExplain(env, key)
	if err != nil {
		return err
	}
	if env.JSON {
		return printFocusJSON(cmd.OutOrStdout(), "explain", doc.payload(), "")
	}
	doc.writeText(cmd.OutOrStdout())
	return nil
}

// buildExplain computes the explanation of key from the live rows. It reads
// the store only.
func buildExplain(env *focusEnv, key focus.Key) (*explainDoc, error) {
	if len(env.Cfg.Repos) == 0 {
		return nil, focusUsageError("focus: no repository configured", "set repos in the pg-desk config")
	}
	st := env.Store
	g := &focusGather{env: env, repo: env.Cfg.Repos[0].Remote, loc: env.Cfg.FocusTimeZone()}
	in, err := focus.Load(st, env.Cfg, g.repo, env.Now)
	if err != nil {
		return nil, focusTotalError("focus: read the store: "+err.Error(), "check the pg-desk store, then re-run")
	}
	g.in = in
	g.set = focus.Candidates(in)
	g.watched = focus.LoadWatched(st, env.Cfg)
	if err := g.reloadPlan(); err != nil {
		return nil, err
	}
	cap := focus.DefaultCap
	if g.found && g.period.Cap != nil && *g.period.Cap > 0 {
		cap = *g.period.Cap
	} else if d, found, derr := st.FocusDraftGet(); derr != nil {
		return nil, focusTotalError("focus: read the stored draft: "+derr.Error(), "check the pg-desk store, then re-run")
	} else if found && isApplicableDraft(d, env.Period) && d.Cap > 0 {
		cap = d.Cap
	}

	doc := &explainDoc{Key: keyText(key), Cap: cap, Addressed: env.Period.Key, Reasons: []string{}}
	ex := focus.ExplainCandidate(in, key)
	doc.Candidate = ex.IsCandidate
	if !ex.IsCandidate {
		doc.Reasons = append(doc.Reasons, ex.Reasons...)
	}
	doc.Via = viaOf(ex.Via)
	_, stored := in.Entities[key]
	doc.NotHeld = !stored

	// Position: the rank is computed live for the addressed day.
	ranking := g.rank(cap)
	capPos := 0
	for i, r := range ranking.Rows {
		if r.InPlan {
			capPos++
		}
		if r.Key != key {
			continue
		}
		doc.Tier = r.Tier
		doc.Deciding.Below = r.DecidingKey
		if i > 0 {
			doc.Deciding.Above = ranking.Rows[i-1].DecidingKey
		}
		doc.Slot.State = "ranked"
		if r.InPlan {
			n := capPos
			doc.CapPos = &n
		}
		break
	}
	if by, ok := ranking.Covered[key]; ok && doc.Slot.State == "" {
		doc.Slot = explainSlot{State: "suppressed", CoveredBy: keyText(by)}
	}
	if doc.Slot.State == "" {
		for _, e := range ranking.EpicsInPlay {
			if e.Key == key {
				doc.Slot.State = "epics_in_play"
				break
			}
		}
	}
	if doc.Slot.State == "" {
		doc.Slot.State = "none"
	}

	// The plan row: the addressed period's, else the row another period holds.
	plan, err := explainPlanRowOf(env, g, key)
	if err != nil {
		return nil, err
	}
	doc.Plan = plan
	doc.PlanElsewhere = plan != nil && plan.Period != env.Period.Key

	// The annotation.
	doc.Annotation = "unset"
	if v, ok := in.Annotations[key][store.AnnotationFocusSelected]; ok {
		doc.Annotation = v
	}

	// The bead: its state word, hold marker and latest audit comment.
	doc.Bead.RecomputedNow = []string{}
	doc.AuditStored = true
	if doc.NotHeld {
		return doc, nil
	}
	live := focus.BuildLive(in, []focus.Key{key}, ranking, focus.LiveOptions{Watched: g.watched, SelectedAt: g.selectedAt()})
	cell := live.Beads[key]
	note := focus.ReadBeadNote(in, key)
	doc.Bead = explainBead{
		State: cell.Shown, Marker: note.Marker, Audit: note.Audit,
		RecomputedNow: []string{focus.RowOf(cell.State).SkipReason},
	}
	doc.AuditStored = !note.Present || note.AuditStored
	return doc, nil
}

// explainPlanRowOf finds the plan row of key: the addressed period's, else
// the one another period holds (the highest period key when several do).
func explainPlanRowOf(env *focusEnv, g *focusGather, key focus.Key) (*explainPlanRow, error) {
	for _, r := range g.sel {
		if r.EntityType == key.Type && r.EntityID == key.ID {
			return &explainPlanRow{Period: env.Period.Key, SelectedAt: r.SelectedAt, RankPosition: r.RankPosition, Tier: r.Tier}, nil
		}
	}
	rows, err := env.Store.FocusRowsOfEntity(g.repo, key.Type, key.ID)
	if err != nil {
		return nil, focusTotalError("focus: read plan rows: "+err.Error(), "check the pg-desk store, then re-run")
	}
	if len(rows) == 0 {
		return nil, nil
	}
	periods, err := env.Store.FocusPeriods()
	if err != nil {
		return nil, focusTotalError("focus: read periods: "+err.Error(), "check the pg-desk store, then re-run")
	}
	keyOf := map[int64]string{}
	for _, p := range periods {
		keyOf[p.ID] = p.PeriodKey
	}
	var best *explainPlanRow
	for _, r := range rows {
		pk := keyOf[r.FocusPeriodID]
		if best == nil || pk > best.Period {
			best = &explainPlanRow{Period: pk, SelectedAt: r.SelectedAt, RankPosition: r.RankPosition, Tier: r.Tier}
		}
	}
	return best, nil
}

// payload is the --json document: the members the text shows, no flip
// history, and run_id null (explain writes no run row).
func (d *explainDoc) payload() map[string]any {
	var via any
	if d.Via != nil {
		via = d.Via
	}
	var plan any
	if d.Plan != nil {
		plan = d.Plan
	}
	var capPos any
	if d.CapPos != nil {
		capPos = *d.CapPos
	}
	return map[string]any{
		"key":          d.Key,
		"candidate":    d.Candidate,
		"reasons":      d.Reasons,
		"via":          via,
		"tier":         d.Tier,
		"deciding":     d.Deciding,
		"slot":         d.Slot,
		"cap_position": capPos,
		"plan_row":     plan,
		"annotation":   d.Annotation,
		"bead":         d.Bead,
		"run_id":       nil,
	}
}

// writeText prints the fixed list of labelled lines, in order.
func (d *explainDoc) writeText(w io.Writer) {
	if d.NotHeld {
		_, _ = fmt.Fprintf(w, "%s: not held by pg-desk\nrun_id: null\n", d.Key)
		return
	}
	line := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	line("key: %s", d.Key)
	if d.Candidate {
		line("candidate: yes")
	} else {
		line("candidate: no (%s)", strings.Join(d.Reasons, ", "))
	}
	if d.Via != nil {
		more := ""
		if d.Via.More > 0 {
			more = fmt.Sprintf(", and %d more seed(s)", d.Via.More)
		}
		line("via: %s by %s%s", d.Via.Seed, d.Via.Relation, more)
	}
	if d.Tier == "" {
		line("tier and deciding keys: not ranked")
	} else {
		line("tier and deciding keys: %s; above %s / below %s", d.Tier, orNone(d.Deciding.Above), orNone(d.Deciding.Below))
	}
	switch d.Slot.State {
	case "ranked":
		line("slot: ranked")
	case "suppressed":
		line("slot: suppressed by %s", d.Slot.CoveredBy)
	case "epics_in_play":
		line("slot: epics in play")
	default:
		line("slot: none")
	}
	if d.CapPos != nil {
		line("cap line: position %d of cap %d", *d.CapPos, d.Cap)
	} else {
		line("cap line: outside the cap line (cap %d)", d.Cap)
	}
	switch {
	case d.Plan == nil:
		line("plan row: not in the plan")
	case d.PlanElsewhere:
		line("plan row: selected_at %s, rank_position %d, tier %s (in period %s, not the addressed period %s)",
			d.Plan.SelectedAt, d.Plan.RankPosition, tierText(d.Plan.Tier), d.Plan.Period, d.Addressed)
	default:
		line("plan row: selected_at %s, rank_position %d, tier %s", d.Plan.SelectedAt, d.Plan.RankPosition, tierText(d.Plan.Tier))
	}
	line("annotation: focus_selected=%s", d.Annotation)
	if d.AuditStored {
		line("bead: %s (marker %s; latest audit: %s)", d.Bead.State, orNone(d.Bead.Marker), auditText(d.Bead.Audit))
	} else {
		line("bead: %s (marker %s; audit: not stored)", d.Bead.State, orNone(d.Bead.Marker))
	}
	line("skip reasons (recomputed now): %s", strings.Join(d.Bead.RecomputedNow, ", "))
	line("run_id: null")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func tierText(t *string) string {
	if t == nil || *t == "" {
		return "none"
	}
	return *t
}

// explainAuditMax bounds the audit text on its one text line; --json carries
// the whole comment.
const explainAuditMax = 160

// auditText is the audit comment on one line, bounded.
func auditText(s string) string {
	s = focusOneLine(s)
	if s == "" {
		return "none"
	}
	if r := []rune(s); len(r) > explainAuditMax {
		return string(r[:explainAuditMax]) + "..."
	}
	return s
}
