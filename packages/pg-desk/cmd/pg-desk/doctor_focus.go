package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The `focus:` block of `pg-desk doctor` and `pg-desk status`
// [docs/behavior/pg-desk/operator-commands.md, spec 8.2]. Every line states
// whether it is a gate (a tripped gate makes doctor exit non-zero) or a
// report (never a failure), the count and a one-line remedy. All figures are
// the shared focus.ComputeHealth of the stored rows (the same function behind
// the /metrics families and the alert rules), so doctor, status and the
// alerts cannot disagree.
//
// Telemetry (D24): the block emits no OpenTelemetry or Prometheus signal; it
// logs only the ordinary CLI report text. It reads only the store (and, for
// the lag line, the --router-config file) and writes nothing.

// The two line kinds.
const (
	focusLineGate   = "gate"
	focusLineReport = "report"
)

// The gate names doctor appends to its failure list.
const (
	focusGateUnminted      = "focus unminted selections"
	focusGateRowNoAnnot    = "focus row_without_annotation"
	focusGateSelectedHeld  = "focus selected_bead_held"
	focusGateViewContract  = "focus view contract"
	focusViewContractField = "annotations.focus_selected"
)

// focusLine is one line of the block.
type focusLine struct {
	// Name labels the line.
	Name string
	// Kind is focusLineGate or focusLineReport.
	Kind string
	// Count is the line's count; -1 when the line has none (a status word).
	Count int
	// Detail is the text after the count: ages, per-type figures, or the
	// reason a figure is not available.
	Detail string
	// Remedy is the one-line remedy.
	Remedy string
	// Tripped is true only for a gate whose condition holds.
	Tripped bool
	// GateName is the failure name doctor records for a tripped gate.
	GateName string
}

// String renders the line.
func (l focusLine) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  [%s] %s:", l.Kind, l.Name)
	switch {
	case l.Count >= 0 && l.Detail != "":
		fmt.Fprintf(&b, " %d (%s)", l.Count, l.Detail)
	case l.Count >= 0:
		fmt.Fprintf(&b, " %d", l.Count)
	case l.Detail != "":
		fmt.Fprintf(&b, " %s", l.Detail)
	}
	if l.Tripped {
		b.WriteString(" FAIL")
	}
	if l.Remedy != "" {
		fmt.Fprintf(&b, "; remedy: %s", l.Remedy)
	}
	return b.String()
}

// focusViewJSON is the seam through which doctor builds the composite view of
// one stored entity in-process with the builder `show` uses (buildView) and
// marshals it to the pg-desk.view/v1 JSON. Tests replace it to plant a view
// without the member.
var focusViewJSON = func(cfg *config.Config, st *store.Store, repo, entityType, id string) ([]byte, error) {
	vd, err := buildView(cfg, st, repo, entityType, id, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(vd.view)
}

// focusViewContractLine probes the view contract on a known entity: the first
// row of the newest plan, else any one stored entity. A gate: it trips on a
// view whose annotations object has no focus_selected member. With no entity
// in the store the line is n/a and never trips.
func focusViewContractLine(cfg *config.Config, st *store.Store) (focusLine, error) {
	line := focusLine{
		Name: "focus view contract", Kind: focusLineGate, Count: -1, GateName: focusGateViewContract,
		Remedy: "upgrade pg-desk so `show` emits annotations.focus_selected, then re-run doctor",
	}
	repo := ""
	if len(cfg.Repos) > 0 {
		repo = cfg.Repos[0].Remote
	}
	typ, id, found, err := focusProbeEntity(st, repo)
	if err != nil {
		return line, err
	}
	if !found {
		line.Detail = "n/a (no entity in the store)"
		return line, nil
	}
	raw, err := focusViewJSON(cfg, st, repo, typ, id)
	if err != nil {
		return line, fmt.Errorf("build the view of %s %s: %w", typ, id, err)
	}
	var v struct {
		Annotations map[string]json.RawMessage `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return line, fmt.Errorf("decode the view of %s %s: %w", typ, id, err)
	}
	if _, ok := v.Annotations["focus_selected"]; ok {
		line.Detail = "ok"
		return line, nil
	}
	line.Detail = "missing " + focusViewContractField
	line.Tripped = true
	return line, nil
}

// focusProbeEntity picks the entity the view-contract probe runs on: the first
// row of the newest period that holds rows, else the first stored entity.
func focusProbeEntity(st *store.Store, repo string) (typ, id string, found bool, err error) {
	periods, err := st.FocusPeriods()
	if err != nil {
		return "", "", false, err
	}
	sort.Slice(periods, func(i, j int) bool {
		if periods[i].PeriodKey != periods[j].PeriodKey {
			return periods[i].PeriodKey > periods[j].PeriodKey
		}
		return periods[i].ID > periods[j].ID
	})
	for _, p := range periods {
		rows, err := st.FocusPlanRows(p.ID)
		if err != nil {
			return "", "", false, err
		}
		for _, r := range rows {
			if repo == "" || r.Repo == repo {
				return r.EntityType, r.EntityID, true, nil
			}
		}
	}
	entities, err := st.ListEntities()
	if err != nil {
		return "", "", false, err
	}
	for _, e := range entities {
		if repo == "" || e.Repo == repo {
			return e.EntityType, e.EntityID, true, nil
		}
	}
	return "", "", false, nil
}

// focusLagLine reports the reconcile consumer's lag (the figure behind
// pg_desk_consumer_lag) for the focus role's consumer: per bound type, the
// consumer of the router query named desk-<type>-changes. Without
// --router-config no consumer name is known and the line says n/a.
func focusLagLine(rc *routerConfig, flow changes.Flow) focusLine {
	line := focusLine{
		Name: "reconcile consumer lag", Kind: focusLineReport, Count: -1,
		Remedy: "check the focus role's router timer and that pg-desk serve or the consumer's reader is running",
	}
	if rc == nil {
		line.Detail = "n/a (no --router-config)"
		return line
	}
	var parts []string
	for _, t := range []string{entityTypeIssue, entityTypePR} {
		name := "desk-" + t + "-changes"
		for _, q := range rc.Queries {
			if q.Name != name {
				continue
			}
			for _, consumer := range consumerFlagValues(q.Argv) {
				for _, c := range flow.Consumers {
					if c.Type == t && c.Name == consumer {
						parts = append(parts, fmt.Sprintf("%s/%s lag=%d", t, consumer, c.Lag))
					}
				}
			}
		}
	}
	if len(parts) == 0 {
		line.Detail = "n/a (no registered consumer for a desk-<type>-changes source in the router config)"
		return line
	}
	line.Detail = strings.Join(parts, ", ")
	return line
}

// focusLines computes the lines of the block from the shared health model.
// Gates come first, then reports. h MUST be migrated (the caller prints
// `focus: unmigrated` otherwise).
func focusLines(h focus.Health, cfg *config.Config, now time.Time, contract focusLine, lag focusLine) []focusLine {
	gateAge := cfg.FocusPendingGateAge()
	var lines []focusLine

	// ---- gates
	unminted := h.Pending[focus.PendingUnminted]
	oldest, haveOldest := h.OldestPendingAge[focus.PendingUnminted]
	un := focusLine{
		Name: "unminted selections", Kind: focusLineGate, Count: unminted, GateName: focusGateUnminted,
		Remedy: "bind the focus decider role to issue.changed and pr.changed and set it to apply (a missing or plan-mode role mints nothing)",
	}
	if haveOldest {
		un.Detail = fmt.Sprintf("oldest %s, gate at %s", oldest.Round(time.Second), gateAge)
		un.Tripped = oldest > gateAge
	} else {
		un.Detail = fmt.Sprintf("gate at %s", gateAge)
	}
	lines = append(lines, un)

	rwa := h.Divergence[focus.DivergenceRowWithoutAnnotation]
	lines = append(lines, focusLine{
		Name: "row_without_annotation divergence", Kind: focusLineGate, Count: rwa, GateName: focusGateRowNoAnnot,
		Tripped: rwa > 0, Remedy: "pg-desk focus select --repair",
	})
	held := h.Divergence[focus.DivergenceSelectedBeadHeld]
	lines = append(lines, focusLine{
		Name: "selected_bead_held divergence", Kind: focusLineGate, Count: held, GateName: focusGateSelectedHeld,
		Tripped: held > 0,
		Remedy:  "a selected item's bead is held though its source is not finished: let the decider release it, or deselect the item",
	})
	lines = append(lines, contract)

	// ---- reports
	var backlog []string
	backlogTotal := 0
	for _, t := range focus.CoverageTypes {
		c, ok := h.Coverage.PerType[t]
		if !ok {
			continue
		}
		if max := cfg.FocusCoverageBacklogMax(c.Active); c.DueBacklog > max {
			backlogTotal += c.DueBacklog
			backlog = append(backlog, fmt.Sprintf("%s %d due of %d active, bound %d, oldest hydrated %s", t, c.DueBacklog, c.Active, max, orDash(c.OldestHydratedAt)))
		}
	}
	lines = append(lines, focusLine{
		Name: "coverage backlog", Kind: focusLineReport, Count: backlogTotal, Detail: strings.Join(backlog, "; "),
		Remedy: "run pg-desk sweep, or raise focus.coverage_backlog_max",
	})
	var failed []string
	for _, l := range h.Listings {
		if l.Status == "degraded" || l.Status == "failed" {
			failed = append(failed, fmt.Sprintf("%s %q %s", l.Type, l.Query, l.Status))
		}
	}
	lines = append(lines, focusLine{
		Name: "degraded or failed listings", Kind: focusLineReport, Count: len(failed), Detail: strings.Join(failed, "; "),
		Remedy: "run pg-desk changes for the type and check pg-connector config validate",
	})
	lines = append(
		lines,
		focusLine{
			Name: "unwatched selections", Kind: focusLineReport, Count: h.UnwatchedSelected,
			Remedy: "add the source to a watch query, or deselect it",
		},
		focusLine{
			Name: "removed from the plan, bead still in play", Kind: focusLineReport, Count: h.RemovedWithBeadInPlay,
			Remedy: "a dropped item's bead can still be worked: close it, or re-select the item",
		},
		focusLine{
			Name: "held bead whose item is selected again", Kind: focusLineReport, Count: h.HeldButReselected,
			Remedy: "wait one decider poll for the release, or check the focus role is bound and in apply mode",
		},
		focusLine{
			Name: "bead minted and never claimed", Kind: focusLineReport, Count: h.MintedNeverClaimed,
			Detail: fmt.Sprintf("open and unassigned older than %s", gateAge),
			Remedy: "start a worker on the focus bead queue",
		},
		lag,
		focusLine{
			Name: "audit comments written today", Kind: focusLineReport, Count: -1,
			Detail: "not available (the stored issue snapshot carries no comments)",
			Remedy: "read the audit comments on the beads themselves to spot a re-hold loop",
		},
		focusLine{
			Name: "escalated focus rules", Kind: focusLineReport, Count: h.EscalatedFocusRules,
			Remedy: "resolve the human escalation bead and clear the escalated.focus annotation",
		},
	)
	idList := focusLine{
		Name: "operator identity list", Kind: focusLineReport, Count: 0, Detail: "configured",
		Remedy: "none needed",
	}
	if h.IdentityListEmpty {
		idList.Count = 1
		idList.Detail = "empty: no Jira issue is a candidate"
		idList.Remedy = "set focus.operator_identities in the config"
	}
	lines = append(lines, idList)
	noBeadCand := 0
	if h.OpenBeads > 0 && h.Candidates["bead"] == 0 {
		noBeadCand = h.OpenBeads
	}
	lines = append(lines, focusLine{
		Name: "open beads but zero bead candidates", Kind: focusLineReport, Count: noBeadCand,
		Detail: fmt.Sprintf("%d open beads, %d bead candidates", h.OpenBeads, h.Candidates["bead"]),
		Remedy: "label a bead pg-focus-planable to make it a candidate",
	})
	stale := 0
	draftDetail := "no stored draft"
	if h.DraftPeriodKey != "" {
		today := now.In(cfg.FocusTimeZone()).Format(focusDayLayout)
		draftDetail = "stored draft for " + h.DraftPeriodKey
		if h.DraftPeriodKey < today {
			stale = 1
			draftDetail += ", older than the current period " + today
		}
	}
	lines = append(lines, focusLine{
		Name: "stored draft older than its period", Kind: focusLineReport, Count: stale, Detail: draftDetail,
		Remedy: "pg-desk focus replan",
	})
	lines = append(lines, focusLine{
		Name: "jira status category", Kind: focusLineReport, Count: h.JiraCategoryAbsent,
		Detail: "active Jira issues lacking one; see the jira status category line of change_flow",
		Remedy: "upgrade pjira or check the status's category in Jira",
	})
	return lines
}

// printFocusBlock prints the `focus:` block of one store. It prints
// `focus: unmigrated` and skips on a store without the focus tables. Every
// tripped gate's name is appended to *failures when failures is non-nil.
func printFocusBlock(ctx context.Context, w io.Writer, st *store.Store, cfg *config.Config, rc *routerConfig, failures *[]string) {
	version, err := st.SchemaVersion()
	if err != nil {
		fmt.Fprintf(w, "focus: FAIL (read schema version: %v)\n", err)
		appendFailure(failures, "focus store")
		return
	}
	if version < store.NewSchemaVersion {
		fmt.Fprintln(w, "focus: unmigrated")
		return
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	now := focusNow()
	h, err := focus.ComputeHealth(st, cfg, now)
	if err != nil {
		fmt.Fprintf(w, "focus: FAIL (compute health: %v)\n", err)
		appendFailure(failures, "focus health")
		return
	}
	if !h.Migrated() {
		// A migrated schema with no repository configured has no focus data.
		fmt.Fprintln(w, "focus: unmigrated")
		return
	}
	flow, err := changes.Observe(st, changesNow(), cfg.SweepMaxAge())
	if err != nil {
		fmt.Fprintf(w, "focus: FAIL (observe change flow: %v)\n", err)
		appendFailure(failures, "focus health")
		return
	}
	contract, err := focusViewContractLine(cfg, st)
	if err != nil {
		fmt.Fprintf(w, "focus: FAIL (view contract probe: %v)\n", err)
		appendFailure(failures, focusGateViewContract)
		return
	}
	fmt.Fprintln(w, "focus:")
	for _, l := range focusLines(h, cfg, now, contract, focusLagLine(rc, flow)) {
		fmt.Fprintln(w, l.String())
		if l.Tripped {
			appendFailure(failures, l.GateName)
		}
	}
}

func appendFailure(failures *[]string, name string) {
	if failures != nil {
		*failures = append(*failures, name)
	}
}

// doctorFocus runs the focus checks of doctor and prints the `focus:` block,
// appending the name of every tripped gate to *failures. The store is opened
// raw so an unmigrated store is reported, never refused.
func doctorFocus(ctx context.Context, w io.Writer, cfg *config.Config, rc *routerConfig, failures *[]string) {
	st, err := deskStoreOpenRaw()
	if err != nil {
		fmt.Fprintf(w, "focus: FAIL (open store: %v)\n", err)
		*failures = append(*failures, "focus store")
		return
	}
	defer func() { _ = st.Close() }()
	printFocusBlock(ctx, w, st, cfg, rc, failures)
}
