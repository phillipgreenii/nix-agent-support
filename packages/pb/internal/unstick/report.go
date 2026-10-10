package unstick

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// prepare.json
// ---------------------------------------------------------------------------

// PrepareState is the snapshot `pb unstick prepare` writes to
// <workdir>/prepare.json and `pb unstick report` reads back. It records what
// the world looked like BEFORE the sweep so the report can diff against it.
// All slices are stored sorted; all maps are keyed by bead id.
type PrepareState struct {
	// Start and Now are UTC Z-form RFC3339. Start is the sweep start (the
	// attribution window opens here); Now is when prepare ran.
	Start string `json:"start"`
	Now   string `json:"now"`
	// Counts holds the pre-sweep counters by name: open, blocked, deferred,
	// in_progress, ready, targets, live_skip, marker_skip, review, drainable.
	// The report reads open, blocked, deferred, in_progress and ready; a
	// missing status key falls back to counting Pre.
	Counts map[string]int `json:"counts"`
	// Review lists the REVIEW bead ids (those fanned out to workers).
	Review []string `json:"review"`
	// ReadyIDs lists the ids in `bd ready` before the sweep (needed for the
	// open-not-ready split).
	ReadyIDs []string `json:"ready_ids"`
	// Pre maps id -> status for every NON-closed bead before the sweep.
	Pre map[string]string `json:"pre"`
	// Closed is the set of ids already closed before the sweep.
	Closed []string `json:"closed"`
}

// Normalize sorts and de-duplicates the slice fields and allocates nil maps.
func (p *PrepareState) Normalize() {
	p.Review = rptSortedUnique(p.Review)
	p.ReadyIDs = rptSortedUnique(p.ReadyIDs)
	p.Closed = rptSortedUnique(p.Closed)
	if p.Counts == nil {
		p.Counts = map[string]int{}
	}
	if p.Pre == nil {
		p.Pre = map[string]string{}
	}
}

// WritePrepare writes st (normalized) to path as indented JSON with a
// trailing newline. Output is deterministic.
func WritePrepare(path string, st PrepareState) error {
	st.Normalize()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode prepare state: %w", err)
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// ReadPrepare reads and validates prepare.json. A missing or unparsable
// start time is an error (the attribution window needs it).
func ReadPrepare(path string) (PrepareState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return PrepareState{}, err
	}
	var st PrepareState
	if err := json.Unmarshal(b, &st); err != nil {
		return PrepareState{}, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := ParseTime(st.Start); err != nil {
		return PrepareState{}, fmt.Errorf("%s: start: %w", path, err)
	}
	st.Normalize()
	return st, nil
}

// ---------------------------------------------------------------------------
// results/*.md grammar
// ---------------------------------------------------------------------------

// Results grammar. Each worker writes results/<batch>.md. A line is a
// "closed" claim iff, after trimming whitespace and one optional list bullet
// ("- " or "* "), it matches exactly
//
//	closed <bead-id>: <reason>
//
// (reason is the rest of the line, non-empty after trimming; the form
// "closed <bead-id>" with no reason is also accepted). Every other line is
// free text and ignored. <bead-id> is letters/digits joined by '-' or '.'.
// Prose such as "closed tc-1 because it was stale" does not match (the id
// must be followed by ": " or end of line).
var resultsClosedRE = regexp.MustCompile(`^closed ([A-Za-z0-9]+(?:[-.][A-Za-z0-9]+)*)(?::[ \t]+(.*\S))?\s*$`)

// ParseResultsClosed returns id -> reason for every "closed <id>[: reason]"
// line in text. A repeated id keeps the first reason.
func ParseResultsClosed(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		l = strings.TrimPrefix(strings.TrimPrefix(l, "- "), "* ")
		l = strings.TrimSpace(l)
		m := resultsClosedRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if _, dup := out[m[1]]; !dup {
			out[m[1]] = m[2]
		}
	}
	return out
}

// ParseFollowups splits followups.txt into OPERATOR: and FOLLOWUP: lines
// (prefix and optional list bullet stripped). Other lines are ignored. Both
// results are sorted and de-duplicated.
func ParseFollowups(text string) (operator, followup []string) {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(l, "- "), "* "))
		if rest, ok := strings.CutPrefix(l, "OPERATOR:"); ok {
			if rest = strings.TrimSpace(rest); rest != "" {
				operator = append(operator, rest)
			}
		} else if rest, ok := strings.CutPrefix(l, "FOLLOWUP:"); ok {
			if rest = strings.TrimSpace(rest); rest != "" {
				followup = append(followup, rest)
			}
		}
	}
	return rptSortedUnique(operator), rptSortedUnique(followup)
}

// ParseBatchIDs reads a batches/<name> file: one bead id per line; blank
// lines and lines starting with '#' are ignored.
func ParseBatchIDs(text string) []string {
	var ids []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		ids = append(ids, l)
	}
	return rptSortedUnique(ids)
}

// ---------------------------------------------------------------------------
// Report inputs
// ---------------------------------------------------------------------------

// ReportInput is everything BuildReport needs, already loaded from disk.
type ReportInput struct {
	Prepare PrepareState
	// PreRows is the pre-sweep export (export.jsonl); used for the
	// defer_until / labels / blockers diff. Beads absent from it are not
	// diffed.
	PreRows []Row
	// Post is the post-sweep export (export.post.jsonl).
	Post []Row
	// PostReadyIDs is the post-sweep `bd ready` id list; valid only when
	// HavePostReady is true.
	PostReadyIDs  []string
	HavePostReady bool
	// Batches maps dispatched batch name -> bead ids (batches/ files).
	Batches map[string][]string
	// Results maps batch name -> results/<name>.md text.
	Results map[string]string
	// Followups is the text of followups.txt.
	Followups string
	// Now is the report time (injected for determinism).
	Now time.Time
}

// LoadReportInput assembles a ReportInput from a sweep work directory plus
// the post-sweep rows and ready ids the caller fetched. Missing batches/
// results/ files or followups.txt are tolerated (empty).
func LoadReportInput(w Workdir, post []Row, postReady []string, havePostReady bool, now time.Time) (ReportInput, error) {
	st, err := ReadPrepare(w.Join(PrepareFile))
	if err != nil {
		return ReportInput{}, err
	}
	pre, err := ReadExportFile(w.Join(ExportFile))
	if err != nil {
		return ReportInput{}, err
	}
	in := ReportInput{
		Prepare: st, PreRows: pre, Post: post,
		PostReadyIDs: postReady, HavePostReady: havePostReady,
		Batches: map[string][]string{}, Results: map[string]string{}, Now: now,
	}
	ents, err := os.ReadDir(w.Join(BatchesDir))
	if err != nil && !os.IsNotExist(err) {
		return ReportInput{}, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(w.Join(BatchesDir, e.Name()))
		if err != nil {
			return ReportInput{}, err
		}
		in.Batches[e.Name()] = ParseBatchIDs(string(b))
	}
	ents, err = os.ReadDir(w.Join(ResultsDir))
	if err != nil && !os.IsNotExist(err) {
		return ReportInput{}, err
	}
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		b, err := os.ReadFile(w.Join(ResultsDir, e.Name()))
		if err != nil {
			return ReportInput{}, err
		}
		in.Results[strings.TrimSuffix(e.Name(), ".md")] = string(b)
	}
	if b, err := os.ReadFile(w.Join(FollowupsFile)); err == nil {
		in.Followups = string(b)
	} else if !os.IsNotExist(err) {
		return ReportInput{}, err
	}
	return in, nil
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

// Attribution values.
const (
	AttribSweep = "sweep"
	AttribPeer  = "peer"
)

// CountLine is one before/after counter with its arithmetic spelled out.
type CountLine struct {
	Name       string `json:"name"`
	Before     int    `json:"before"`
	After      *int   `json:"after,omitempty"` // nil when unavailable
	Delta      *int   `json:"delta,omitempty"`
	Arithmetic string `json:"arithmetic"`
}

// NotReadySplit is the open-not-ready population (status open|blocked and
// not in ready, plus status deferred) before and after, with who removed
// beads from it. Invariant: Before - Left + Entered == After, and
// Left == LeftSweep + LeftPeers.
type NotReadySplit struct {
	Before     int      `json:"before"`
	After      int      `json:"after"`
	Left       int      `json:"left"`
	LeftSweep  int      `json:"left_sweep"`
	LeftPeers  int      `json:"left_peers"`
	Entered    int      `json:"entered"`
	LeftIDs    []string `json:"left_ids"`
	EnteredIDs []string `json:"entered_ids"`
	Arithmetic string   `json:"arithmetic"`
}

// Change is one bead that changed in the window.
type Change struct {
	ID          string `json:"id"`
	Attribution string `json:"attribution"`
	Detail      string `json:"detail"`
}

// OutcomeGroup is the markers added in the window for one outcome.
type OutcomeGroup struct {
	Outcome string   `json:"outcome"`
	Count   int      `json:"count"`
	IDs     []string `json:"ids"`
}

// ClosedBead is a bead closed in the window.
type ClosedBead struct {
	ID          string `json:"id"`
	CloseReason string `json:"close_reason"`
	Attribution string `json:"attribution"`
}

// Report is the full output of `pb unstick report`.
type Report struct {
	Start string `json:"start"`
	Now   string `json:"now"`
	// Heuristic documents the attribution rule (there is no closer field).
	Heuristic string      `json:"heuristic"`
	Counts    []CountLine `json:"counts"`
	// OpenNotReady is nil when the post-sweep ready set was unavailable.
	OpenNotReady     *NotReadySplit `json:"open_not_ready,omitempty"`
	MarkersByOutcome []OutcomeGroup `json:"markers_by_outcome"`
	Closed           []ClosedBead   `json:"closed"`
	Undeferred       []Change       `json:"undeferred"`
	Delabelled       []Change       `json:"delabelled"`
	Retargeted       []Change       `json:"retargeted"`
	ClaimsReleased   []string       `json:"claims_released"`
	NewBeads         []string       `json:"new_beads"`
	// Peers lists every bead that changed in the window but is not
	// attributed to the sweep.
	Peers     []Change `json:"peers"`
	SweepN    int      `json:"sweep_changed"`
	PeerN     int      `json:"peer_changed"`
	Operator  []string `json:"operator"`
	Followup  []string `json:"followup"`
	ChangedBy string   `json:"changed_arithmetic"`
}

// AttributionHeuristic is the documented rule (kept as one string so the
// human and JSON renderers and the docs agree).
const AttributionHeuristic = "HEURISTIC (bd records no closer): a bead is attributed to the sweep iff it is in a " +
	"dispatched batch AND (it carries a non-unchanged sweep marker with ts >= sweep start, OR it is closed now, " +
	"was non-closed before, and is listed as 'closed <id>' in results/*.md); every other change in the window is attributed to peers"

// BuildReport computes the report. It is pure: no I/O, all output sorted.
func BuildReport(in ReportInput) Report {
	st := in.Prepare
	st.Normalize()
	start, _ := ParseTime(st.Start)

	preRow := map[string]Row{}
	for _, r := range in.PreRows {
		if _, ok := preRow[r.ID]; !ok {
			preRow[r.ID] = r
		}
	}
	postRow := map[string]Row{}
	for _, r := range in.Post {
		if _, ok := postRow[r.ID]; !ok {
			postRow[r.ID] = r
		}
	}
	closedPre := rptSet(st.Closed)
	preKnown := func(id string) bool {
		if _, ok := st.Pre[id]; ok {
			return true
		}
		return closedPre[id]
	}

	dispatched := map[string]bool{}
	for _, ids := range in.Batches {
		for _, id := range ids {
			dispatched[id] = true
		}
	}
	resultsClosed := map[string]string{}
	for _, name := range rptSortedKeys(in.Results) {
		for id, reason := range ParseResultsClosed(in.Results[name]) {
			if _, ok := resultsClosed[id]; !ok {
				resultsClosed[id] = reason
			}
		}
	}

	// Markers added in the window: valid, ts >= start, not already present
	// before the sweep (same rendered line).
	type addedMarker struct {
		id string
		m  Marker
	}
	var added []addedMarker
	attributedByMarker := map[string]bool{}
	for _, id := range rptSortedKeys(postRow) {
		row := postRow[id]
		seen := map[string]bool{}
		if pr, ok := preRow[id]; ok {
			for _, m := range pr.Markers().Valid {
				seen[m.String()] = true
			}
		}
		for _, m := range row.Markers().Valid {
			if m.Time.Before(start) || seen[m.String()] {
				continue
			}
			seen[m.String()] = true
			added = append(added, addedMarker{id, m})
			if m.Outcome != OutcomeUnchanged {
				attributedByMarker[id] = true
			}
		}
	}

	attributed := func(id string) bool {
		if !dispatched[id] {
			return false
		}
		if attributedByMarker[id] {
			return true
		}
		row, ok := postRow[id]
		if !ok || row.Status != StatusClosed || closedPre[id] {
			return false
		}
		_, listed := resultsClosed[id]
		return listed && preKnown(id)
	}
	attr := func(id string) string {
		if attributed(id) {
			return AttribSweep
		}
		return AttribPeer
	}

	rep := Report{
		Start: st.Start, Now: FormatTime(in.Now), Heuristic: AttributionHeuristic,
		MarkersByOutcome: []OutcomeGroup{}, Closed: []ClosedBead{}, Undeferred: []Change{},
		Delabelled: []Change{}, Retargeted: []Change{}, ClaimsReleased: []string{},
		NewBeads: []string{}, Peers: []Change{},
	}

	// Counts.
	post := map[string]int{}
	for _, r := range in.Post {
		post[r.Status]++
	}
	preCount := func(name string) int {
		if v, ok := st.Counts[name]; ok {
			return v
		}
		n := 0
		for _, s := range st.Pre {
			if s == name {
				n++
			}
		}
		return n
	}
	for _, name := range []string{StatusOpen, StatusBlocked, StatusDeferred, StatusInProgress} {
		rep.Counts = append(rep.Counts, rptCountLine(name, preCount(name), post[name], true))
	}
	if in.HavePostReady {
		rep.Counts = append(rep.Counts, rptCountLine("ready", st.Counts["ready"], len(rptSortedUnique(in.PostReadyIDs)), true))
	} else {
		rep.Counts = append(rep.Counts, rptCountLine("ready", st.Counts["ready"], 0, false))
	}

	// Per-bead diffs and the changed set.
	changed := map[string]bool{}
	for _, id := range rptSortedKeys(postRow) {
		row := postRow[id]
		if !preKnown(id) {
			rep.NewBeads = append(rep.NewBeads, id)
			continue
		}
		if closedPre[id] {
			continue
		}
		preStatus := st.Pre[id]
		var details []string
		if row.Status != preStatus {
			details = append(details, fmt.Sprintf("status %s -> %s", preStatus, row.Status))
		}
		if row.Status == StatusClosed {
			rep.Closed = append(rep.Closed, ClosedBead{ID: id, CloseReason: row.CloseReason, Attribution: attr(id)})
		}
		if pr, ok := preRow[id]; ok && row.Status != StatusClosed {
			if pr.DeferUntil != "" && (row.DeferUntil == "" || (row.Status != StatusDeferred && pr.Status == StatusDeferred)) {
				d := "defer_until " + pr.DeferUntil + " -> none"
				if row.DeferUntil != "" {
					d = "defer_until " + pr.DeferUntil + " -> " + row.DeferUntil + ", status " + pr.Status + " -> " + row.Status
				}
				rep.Undeferred = append(rep.Undeferred, Change{id, attr(id), d})
				details = append(details, "undeferred")
			} else if pr.Status == StatusDeferred && row.Status != StatusDeferred {
				rep.Undeferred = append(rep.Undeferred, Change{id, attr(id), "status deferred -> " + row.Status})
				details = append(details, "undeferred")
			}
			if removed := rptMinus(pr.Labels, row.Labels); len(removed) > 0 {
				rep.Delabelled = append(rep.Delabelled, Change{id, attr(id), "removed labels: " + strings.Join(removed, ", ")})
				details = append(details, "de-labelled")
			}
			pb, qb := rptBlockers(pr), rptBlockers(row)
			if rm, ad := rptMinus(pb, qb), rptMinus(qb, pb); len(rm)+len(ad) > 0 {
				var parts []string
				if len(rm) > 0 {
					parts = append(parts, "removed "+strings.Join(rm, ", "))
				}
				if len(ad) > 0 {
					parts = append(parts, "added "+strings.Join(ad, ", "))
				}
				rep.Retargeted = append(rep.Retargeted, Change{id, attr(id), "blockers: " + strings.Join(parts, "; ")})
				details = append(details, "retargeted")
			}
		}
		if len(details) > 0 {
			changed[id] = true
			if !attributed(id) {
				rep.Peers = append(rep.Peers, Change{id, AttribPeer, strings.Join(details, "; ")})
			}
		}
	}
	for id := range changed {
		if attributed(id) {
			rep.SweepN++
		} else {
			rep.PeerN++
		}
	}
	rep.ChangedBy = fmt.Sprintf("changed in window %d = sweep %d + peers %d", rep.SweepN+rep.PeerN, rep.SweepN, rep.PeerN)

	// Markers by outcome, released claims.
	byOutcome := map[string][]string{}
	for _, a := range added {
		byOutcome[a.m.Outcome] = append(byOutcome[a.m.Outcome], a.id)
		if a.m.Outcome == OutcomeReleased {
			rep.ClaimsReleased = append(rep.ClaimsReleased, a.id)
		}
	}
	for _, o := range rptSortedKeys(byOutcome) {
		ids := byOutcome[o]
		sort.Strings(ids)
		rep.MarkersByOutcome = append(rep.MarkersByOutcome, OutcomeGroup{Outcome: o, Count: len(ids), IDs: rptSortedUnique(ids)})
	}
	rep.ClaimsReleased = rptSortedUnique(rep.ClaimsReleased)

	// Open-not-ready split.
	if in.HavePostReady {
		preReady, postReady := rptSet(st.ReadyIDs), rptSet(in.PostReadyIDs)
		preNR := map[string]bool{}
		for id, s := range st.Pre {
			if rptNotReady(s, id, preReady) {
				preNR[id] = true
			}
		}
		postNR := map[string]bool{}
		for id, r := range postRow {
			if rptNotReady(r.Status, id, postReady) {
				postNR[id] = true
			}
		}
		sp := &NotReadySplit{Before: len(preNR), After: len(postNR), LeftIDs: []string{}, EnteredIDs: []string{}}
		for _, id := range rptSortedKeys(preNR) {
			if postNR[id] {
				continue
			}
			sp.LeftIDs = append(sp.LeftIDs, id)
			if attributed(id) {
				sp.LeftSweep++
			} else {
				sp.LeftPeers++
			}
		}
		for _, id := range rptSortedKeys(postNR) {
			if !preNR[id] {
				sp.EnteredIDs = append(sp.EnteredIDs, id)
			}
		}
		sp.Left, sp.Entered = len(sp.LeftIDs), len(sp.EnteredIDs)
		sp.Arithmetic = fmt.Sprintf("open-not-ready: %d - %d left (sweep %d + peers %d) + %d entered = %d",
			sp.Before, sp.Left, sp.LeftSweep, sp.LeftPeers, sp.Entered, sp.After)
		rep.OpenNotReady = sp
	}

	rep.Operator, rep.Followup = ParseFollowups(in.Followups)
	return rep
}

func rptNotReady(status, id string, ready map[string]bool) bool {
	switch status {
	case StatusDeferred:
		return true
	case StatusOpen, StatusBlocked:
		return !ready[id]
	}
	return false
}

func rptCountLine(name string, before, after int, haveAfter bool) CountLine {
	cl := CountLine{Name: name, Before: before}
	if !haveAfter {
		cl.Arithmetic = fmt.Sprintf("%s: %d -> unavailable", name, before)
		return cl
	}
	d := after - before
	cl.After, cl.Delta = &after, &d
	cl.Arithmetic = fmt.Sprintf("%s: %d -> %d (%+d = %d - %d)", name, before, after, d, after, before)
	return cl
}

// rptBlockers returns the sorted ids the row is blocked by (blocks edges
// where the row is the dependent).
func rptBlockers(r Row) []string {
	var out []string
	for _, d := range r.Dependencies {
		if d.Type == DepBlocks && d.IssueID == r.ID {
			out = append(out, d.DependsOnID)
		}
	}
	return rptSortedUnique(out)
}

// rptMinus returns the sorted elements of a that are not in b.
func rptMinus(a, b []string) []string {
	bs := rptSet(b)
	var out []string
	for _, s := range a {
		if !bs[s] {
			out = append(out, s)
		}
	}
	return rptSortedUnique(out)
}

func rptSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func rptSortedUnique(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	c := append([]string(nil), s...)
	sort.Strings(c)
	out := c[:1]
	for _, v := range c[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func rptSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Renderers
// ---------------------------------------------------------------------------

// RenderJSON renders the report as indented JSON with a trailing newline.
func (r Report) RenderJSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RenderHuman renders the report as plain text. Deterministic.
func (r Report) RenderHuman() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sweep report (start %s, report %s)\n", r.Start, r.Now)
	fmt.Fprintf(&b, "%s\n\n", r.Heuristic)

	b.WriteString("Counts (before -> after):\n")
	for _, c := range r.Counts {
		fmt.Fprintf(&b, "  %s\n", c.Arithmetic)
	}
	if r.OpenNotReady != nil {
		fmt.Fprintf(&b, "  %s\n", r.OpenNotReady.Arithmetic)
	} else {
		b.WriteString("  open-not-ready: unavailable (post-sweep ready set missing)\n")
	}
	fmt.Fprintf(&b, "  %s\n\n", r.ChangedBy)

	rptSection(&b, "Markers added by outcome", len(r.MarkersByOutcome), func() {
		for _, g := range r.MarkersByOutcome {
			fmt.Fprintf(&b, "  %s: %d (%s)\n", g.Outcome, g.Count, strings.Join(g.IDs, ", "))
		}
	})
	rptSection(&b, "Closed", len(r.Closed), func() {
		for _, c := range r.Closed {
			fmt.Fprintf(&b, "  %s [%s]: %s\n", c.ID, c.Attribution, c.CloseReason)
		}
	})
	for _, s := range []struct {
		title string
		list  []Change
	}{{"Undeferred", r.Undeferred}, {"De-labelled", r.Delabelled}, {"Retargeted", r.Retargeted}} {
		rptSection(&b, s.title, len(s.list), func() {
			for _, c := range s.list {
				fmt.Fprintf(&b, "  %s [%s]: %s\n", c.ID, c.Attribution, c.Detail)
			}
		})
	}
	rptSection(&b, "Claims released", len(r.ClaimsReleased), func() {
		fmt.Fprintf(&b, "  %s\n", strings.Join(r.ClaimsReleased, ", "))
	})
	rptSection(&b, "New beads", len(r.NewBeads), func() {
		fmt.Fprintf(&b, "  %s\n", strings.Join(r.NewBeads, ", "))
	})
	rptSection(&b, "Peers (changed, not attributed to the sweep)", len(r.Peers), func() {
		for _, c := range r.Peers {
			fmt.Fprintf(&b, "  %s: %s\n", c.ID, c.Detail)
		}
	})
	rptSection(&b, "OPERATOR", len(r.Operator), func() {
		for _, l := range r.Operator {
			fmt.Fprintf(&b, "  - %s\n", l)
		}
	})
	rptSection(&b, "FOLLOWUP", len(r.Followup), func() {
		for _, l := range r.Followup {
			fmt.Fprintf(&b, "  - %s\n", l)
		}
	})
	return b.String()
}

func rptSection(b *strings.Builder, title string, n int, body func()) {
	if n == 0 {
		fmt.Fprintf(b, "%s: none\n", title)
		return
	}
	fmt.Fprintf(b, "%s (%d):\n", title, n)
	body()
}
