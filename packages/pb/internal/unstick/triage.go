package unstick

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ExcludedLabelsCSV is the label set /pb:drain-beads refuses to claim. It is
// drift-tested against the claim query in drain-beads.md. A ready bead
// carrying any of these labels is not DRAINABLE.
const ExcludedLabelsCSV = "human,human-focus-required,refactor-campaign"

// ExcludedLabels returns ExcludedLabelsCSV split into labels.
func ExcludedLabels() []string { return strings.Split(ExcludedLabelsCSV, ",") }

// Triage tuning, from unstick-beads.md.
const (
	// LiveInProgressWindow is how recently an in_progress bead must have been
	// updated to seed LIVE.
	LiveInProgressWindow = 24 * time.Hour
	// MarkerQuietWindow is how far past the marker timestamp updated_at may
	// be while still counting as "only the marker touched it".
	MarkerQuietWindow = 15 * time.Minute

	issueTypeEpic = "epic"
	issueTypeGate = "gate"
	labelHuman    = "human"
	// labelFocusItem marks a focus bead held by the decider. A sweep must
	// never review, undefer, re-date, re-edge or close one (a closed focus
	// bead is never reopened), so it is excluded from TARGETS in EVERY
	// status. Mirrors unstick-beads.md SKIP-focus-item.
	labelFocusItem = "focus-item"
)

// Review reasons recorded in TriageResult.ReviewReasons.
const (
	ReasonNoMarker         = "no-marker"
	ReasonUnknownRef       = "unknown-reference"
	ReasonFullSweep        = "full"
	ReasonNeighbourClosed  = "neighbour-closed-after-marker"
	ReasonNeighbourChanged = "neighbour-changed-after-marker"
	ReasonDeferElapsed     = "defer-until-elapsed"
	ReasonDeferUnparsable  = "defer-until-unparsable"
	ReasonDeferredNoDate   = "deferred-without-future-defer-until"
	ReasonUpdatedAfter     = "updated-after-marker"
	ReasonNewerComment     = "comment-after-marker"
	ReasonRecheckDue       = "recheck-due"
)

// TriageOptions narrows and tunes a triage run.
type TriageOptions struct {
	// Full ignores sweep markers (no marker skip).
	Full bool
	// Label, when set, keeps only targets carrying it. Applied AFTER LIVE and
	// DRAINABLE are computed over the whole workspace.
	Label string
	// IDPrefix, when set, keeps only targets whose id has this prefix. Applied
	// like Label.
	IDPrefix string
}

// ClaimCandidate is an in_progress or assigned-but-not-ready bead whose claim
// may be dead. Heartbeat and lease data are not part of the export row.
type ClaimCandidate struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Assignee  string `json:"assignee"`
	UpdatedAt string `json:"updated_at"`
}

// MalformedBead lists the marker-looking lines of one bead that failed the
// strict grammar (they count as no marker).
type MalformedBead struct {
	ID      string            `json:"id"`
	Markers []MalformedMarker `json:"-"`
	Lines   []string          `json:"lines"`
}

// TriageCounts are the headline numbers of a triage run.
type TriageCounts struct {
	Open       int `json:"open"`
	Blocked    int `json:"blocked"`
	Deferred   int `json:"deferred"`
	InProgress int `json:"in_progress"`
	Ready      int `json:"ready"`
	Targets    int `json:"targets"`
	LiveSkip   int `json:"live_skip"`
	MarkerSkip int `json:"marker_skip"`
	Review     int `json:"review"`
	Drainable  int `json:"drainable"`
	// FocusExcluded counts focus-item beads removed from TARGETS.
	FocusExcluded int `json:"focus_excluded"`
}

// TriageResult is the deterministic outcome of Triage. Every id list is sorted
// by id.
type TriageResult struct {
	// Targets is the (possibly narrowed) set of open-but-not-ready beads;
	// Targets = Live + MarkerSkip + Review.
	Targets    []string
	Live       []string
	MarkerSkip []string
	Review     []string
	// FocusExcluded lists unclaimed focus-item beads (any status) removed
	// from the target set. They belong to none of Live, MarkerSkip or Review
	// and are sent to no worker.
	FocusExcluded []string
	// ReviewReasons maps each REVIEW id to why it was not skipped.
	ReviewReasons map[string]string
	// Drainable is ready minus excluded labels, epics and templates
	// (whole workspace, never narrowed).
	Drainable []string
	// InProgress lists all in_progress ids.
	InProgress []string
	// AssignedOpen lists non-ready open/blocked/deferred beads with an
	// assignee (candidates, not targets).
	AssignedOpen []string
	// ClaimCandidates is InProgress plus AssignedOpen, with details.
	ClaimCandidates []ClaimCandidate
	// MalformedMarkers lists beads (any non-closed status) carrying a
	// malformed marker line.
	MalformedMarkers []MalformedBead
	Counts           TriageCounts
}

// Arithmetic renders the partition line "targets N = live a + marker b +
// review c".
func (r TriageResult) Arithmetic() string {
	return fmt.Sprintf("targets %d = live %d + marker %d + review %d",
		len(r.Targets), len(r.Live), len(r.MarkerSkip), len(r.Review))
}

// CheckPartition verifies that Live, MarkerSkip and Review partition Targets
// exactly (sum and membership). A violation is an internal error.
func (r TriageResult) CheckPartition() error {
	if len(r.Live)+len(r.MarkerSkip)+len(r.Review) != len(r.Targets) {
		return fmt.Errorf("triage partition broken: %s", r.Arithmetic())
	}
	seen := map[string]int{}
	for _, l := range [][]string{r.Live, r.MarkerSkip, r.Review} {
		for _, id := range l {
			seen[id]++
		}
	}
	for _, id := range r.Targets {
		if seen[id] != 1 {
			return fmt.Errorf("triage partition broken: %s appears %d times", id, seen[id])
		}
	}
	return nil
}

type triager struct {
	g       *Graph
	ready   map[string]ReadyRow
	now     time.Time
	opts    TriageOptions
	ancBad  map[string]bool // memo: id has a disqualifying ancestor
	liveSet map[string]bool
}

// Triage classifies the open-but-not-ready beads of an export. rows is the
// whole workspace export, ready the `bd ready` result, now the injected clock.
// The result is a pure function of its inputs (adjacency maps are built once,
// all output is id-sorted).
func Triage(rows []Row, ready []ReadyRow, opts TriageOptions, now time.Time) TriageResult {
	t := &triager{
		g:       NewGraph(rows),
		ready:   map[string]ReadyRow{},
		now:     now.UTC(),
		opts:    opts,
		ancBad:  map[string]bool{},
		liveSet: map[string]bool{},
	}
	for _, r := range ready {
		t.ready[r.ID] = r
	}
	res := TriageResult{ReviewReasons: map[string]string{}}
	res.Counts.Ready = len(ready)

	// Sorted unique rows by id (the first duplicate wins, as in Graph).
	var all []Row
	for _, id := range t.g.IDs() {
		r, _ := t.g.Row(id)
		all = append(all, r)
	}

	var candidateTargets []string // all targets, before narrowing
	claim := map[string]ClaimCandidate{}
	for _, r := range all {
		switch r.Status {
		case StatusOpen:
			res.Counts.Open++
		case StatusBlocked:
			res.Counts.Blocked++
		case StatusDeferred:
			res.Counts.Deferred++
		case StatusInProgress:
			res.Counts.InProgress++
			res.InProgress = append(res.InProgress, r.ID)
			claim[r.ID] = ClaimCandidate{ID: r.ID, Status: r.Status, Assignee: r.Assignee, UpdatedAt: r.UpdatedAt}
		}
		_, isReady := t.ready[r.ID]
		isTarget := r.Status == StatusDeferred ||
			((r.Status == StatusOpen || r.Status == StatusBlocked) && !isReady)
		if isTarget {
			if r.Assignee != "" {
				res.AssignedOpen = append(res.AssignedOpen, r.ID)
				claim[r.ID] = ClaimCandidate{ID: r.ID, Status: r.Status, Assignee: r.Assignee, UpdatedAt: r.UpdatedAt}
			} else {
				candidateTargets = append(candidateTargets, r.ID)
			}
		}
		if r.Status != StatusClosed {
			if m := r.Markers(); len(m.Malformed) > 0 {
				mb := MalformedBead{ID: r.ID, Markers: m.Malformed}
				for _, x := range m.Malformed {
					mb.Lines = append(mb.Lines, x.Line)
				}
				res.MalformedMarkers = append(res.MalformedMarkers, mb)
			}
		}
	}
	for _, id := range sortedKeys(claim) {
		res.ClaimCandidates = append(res.ClaimCandidates, claim[id])
	}

	res.Drainable = t.drainable()
	res.Counts.Drainable = len(res.Drainable)
	t.computeLive(res.Drainable, candidateTargets)

	for _, id := range candidateTargets {
		row, _ := t.g.Row(id)
		// SKIP-focus-item comes FIRST: before narrowing, LIVE, markers and
		// every override.
		if row.HasLabel(labelFocusItem) {
			res.FocusExcluded = append(res.FocusExcluded, id)
			continue
		}
		if opts.Label != "" && !row.HasLabel(opts.Label) {
			continue
		}
		if opts.IDPrefix != "" && !strings.HasPrefix(id, opts.IDPrefix) {
			continue
		}
		res.Targets = append(res.Targets, id)
		switch {
		case t.liveSet[id]:
			res.Live = append(res.Live, id)
		default:
			if skip, why := t.markerSkip(row); skip {
				res.MarkerSkip = append(res.MarkerSkip, id)
			} else {
				res.Review = append(res.Review, id)
				res.ReviewReasons[id] = why
			}
		}
	}
	res.Counts.Targets = len(res.Targets)
	res.Counts.LiveSkip = len(res.Live)
	res.Counts.MarkerSkip = len(res.MarkerSkip)
	res.Counts.Review = len(res.Review)
	res.Counts.FocusExcluded = len(res.FocusExcluded)
	return res
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// drainable is ready minus excluded labels, epics and templates.
func (t *triager) drainable() []string {
	var out []string
	for id, r := range t.ready {
		if r.IsTemplate || r.IssueType == issueTypeEpic || hasAnyLabel(r.Labels, ExcludedLabels()) {
			continue
		}
		// bd ready un-defers beads whose defer_until elapsed, so an earlier
		// export can still say deferred for a ready row; such a bead is not
		// drainable and must reach the marker/override path.
		if er, ok := t.g.Row(id); ok && er.Status != StatusOpen && er.Status != StatusInProgress {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func hasAnyLabel(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// computeLive fills t.liveSet with the least fixpoint described in
// unstick-beads.md Stage 3 (with the no-open-blocker deviation).
func (t *triager) computeLive(drainable, targets []string) {
	for _, id := range drainable {
		if r, ok := t.g.Row(id); ok && r.IssueType == issueTypeGate {
			continue // a gate is never LIVE
		}
		t.liveSet[id] = true
	}
	for _, id := range t.g.IDs() {
		r, _ := t.g.Row(id)
		if r.Status != StatusInProgress || r.IssueType == issueTypeGate {
			continue
		}
		if u, err := ParseTime(r.UpdatedAt); err == nil && t.now.Sub(u) < LiveInProgressWindow {
			t.liveSet[id] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, id := range targets {
			if t.liveSet[id] {
				continue
			}
			if t.canJoinLive(id) {
				t.liveSet[id] = true
				changed = true
			}
		}
	}
}

func (t *triager) canJoinLive(id string) bool {
	r, _ := t.g.Row(id)
	if r.IssueType == issueTypeGate || r.HasLabel(labelHuman) || r.DeferUntil != "" || r.Status == StatusDeferred {
		return false
	}
	if p, ok := t.g.Parent(id); ok && !t.g.Has(p) {
		return false // unknown parent
	}
	var open []string
	for _, b := range t.g.Blockers(id) {
		br, ok := t.g.Row(b)
		if !ok {
			return false // unknown id => REVIEW
		}
		if br.Status != StatusClosed {
			open = append(open, b)
		}
	}
	if len(open) == 0 {
		return false // anomalous: non-ready with no open blocker => REVIEW
	}
	for _, b := range open {
		if !t.liveSet[b] {
			return false
		}
	}
	if t.badAncestor(id) {
		return false
	}
	desc := map[string]bool{}
	for _, d := range t.g.Descendants(id) {
		desc[d] = true
	}
	for _, b := range open {
		if desc[b] || t.badAncestor(b) {
			return false
		}
	}
	return true
}

// badAncestor reports whether id has a parent-child ancestor that is deferred,
// blocked, human-labelled, has defer_until, or is absent from the export.
func (t *triager) badAncestor(id string) bool {
	if v, ok := t.ancBad[id]; ok {
		return v
	}
	bad := false
	for _, a := range t.g.Ancestors(id) {
		ar, ok := t.g.Row(a)
		if !ok || ar.Status == StatusDeferred || ar.Status == StatusBlocked ||
			ar.HasLabel(labelHuman) || ar.DeferUntil != "" {
			bad = true
			break
		}
	}
	t.ancBad[id] = bad
	return bad
}

// parseDefer reads a defer_until value: RFC3339 or a bare date.
func parseDefer(s string) (time.Time, error) {
	if tm, err := ParseTime(s); err == nil {
		return tm, nil
	}
	d, err := time.Parse(dateFmt, s)
	if err != nil {
		return time.Time{}, err
	}
	return d.UTC(), nil
}

// markerSkip decides SKIP-marker-valid for one non-LIVE target. When it
// returns false, why is the REVIEW reason.
func (t *triager) markerSkip(r Row) (skip bool, why string) {
	if t.opts.Full {
		return false, ReasonFullSweep
	}
	m, ok := Newest(r.Markers().Valid)
	if !ok {
		return false, ReasonNoMarker
	}
	// Referenced beads: blockers (any status), parent, children. Dependents
	// are deliberately ignored.
	refs := t.g.Blockers(r.ID)
	if p, ok := t.g.Parent(r.ID); ok {
		refs = append(refs, p)
	}
	refs = append(refs, t.g.Children(r.ID)...)
	var refRows []Row
	for _, id := range refs {
		rr, ok := t.g.Row(id)
		if !ok {
			return false, ReasonUnknownRef
		}
		refRows = append(refRows, rr)
	}
	if m.Recheck.Kind == RecheckCloses && !t.g.Has(m.Recheck.BeadID) {
		return false, ReasonUnknownRef
	}

	// Overrides.
	for _, rr := range refRows {
		if rr.ClosedAt == "" {
			continue
		}
		if c, err := ParseTime(rr.ClosedAt); err != nil || c.After(m.Time) {
			return false, ReasonNeighbourClosed
		}
	}
	if r.DeferUntil != "" {
		d, err := parseDefer(r.DeferUntil)
		if err != nil {
			return false, ReasonDeferUnparsable
		}
		if !d.After(t.now) {
			return false, ReasonDeferElapsed
		}
	} else if r.Status == StatusDeferred {
		return false, ReasonDeferredNoDate
	}

	// Skip conditions.
	if u, err := ParseTime(r.UpdatedAt); err != nil || u.After(m.Time.Add(MarkerQuietWindow)) {
		return false, ReasonUpdatedAfter
	}
	for _, c := range r.Comments {
		if ownMarkerComment(c, m) {
			continue
		}
		if ct, err := ParseTime(c.CreatedAt); err != nil || ct.After(m.Time) {
			return false, ReasonNewerComment
		}
	}
	for _, rr := range refRows {
		for _, ts := range []string{rr.UpdatedAt, rr.ClosedAt} {
			if ts == "" {
				continue
			}
			if tm, err := ParseTime(ts); err != nil || tm.After(m.Time) {
				return false, ReasonNeighbourChanged
			}
		}
	}
	switch m.Recheck.Kind {
	case RecheckDate:
		if !t.now.Before(m.Recheck.Date) {
			return false, ReasonRecheckDue
		}
	case RecheckCloses:
		if br, _ := t.g.Row(m.Recheck.BeadID); br.Status == StatusClosed {
			return false, ReasonRecheckDue
		}
	}
	return true, ""
}

// ownMarkerComment reports whether comment c is the one carrying the newest
// marker m (the marker's own comment is stamped just after the marker time and
// must not count as a newer comment).
func ownMarkerComment(c Comment, m Marker) bool {
	for _, v := range ScanMarkers(c.Text, "comment").Valid {
		if v.Time.Equal(m.Time) {
			return true
		}
	}
	return false
}
