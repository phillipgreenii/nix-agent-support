package unstick

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var triageNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	oldTS    = "2026-09-01T00:00:00Z"
	markerTS = "2026-10-09T10:00:00Z"
)

type trOpt func(*Row)

func trMk(id string, opts ...trOpt) Row {
	r := Row{ID: id, Status: StatusOpen, IssueType: "task", UpdatedAt: oldTS, CreatedAt: oldTS}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func trStatus(s string) trOpt   { return func(r *Row) { r.Status = s } }
func trTyp(s string) trOpt      { return func(r *Row) { r.IssueType = s } }
func trLabel(l ...string) trOpt { return func(r *Row) { r.Labels = append(r.Labels, l...) } }
func trUpdated(ts string) trOpt { return func(r *Row) { r.UpdatedAt = ts } }
func trClosedAt(ts string) trOpt {
	return func(r *Row) { r.Status = StatusClosed; r.ClosedAt = ts; r.UpdatedAt = ts }
}
func trDeferUntil(ts string) trOpt { return func(r *Row) { r.DeferUntil = ts } }
func trAssignee(a string) trOpt    { return func(r *Row) { r.Assignee = a } }
func trNotes(n string) trOpt       { return func(r *Row) { r.Notes = n } }
func trComment(ts, text string) trOpt {
	return func(r *Row) { r.Comments = append(r.Comments, Comment{CreatedAt: ts, Text: text}) }
}

func trBlockedBy(ids ...string) trOpt {
	return func(r *Row) {
		for _, id := range ids {
			r.Dependencies = append(r.Dependencies, Dep{IssueID: r.ID, DependsOnID: id, Type: DepBlocks})
		}
	}
}

func trChildOf(parent string) trOpt {
	return func(r *Row) {
		r.Dependencies = append(r.Dependencies, Dep{IssueID: r.ID, DependsOnID: parent, Type: DepParentChild})
	}
}

func trMarker(ts, recheck string) string {
	return "[unstick " + ts + "] unchanged: waiting on something; recheck-when: " + recheck
}

// rdy builds ready rows from the named rows (labels and type copied).
func trRdy(rows []Row, ids ...string) []ReadyRow {
	var out []ReadyRow
	for _, id := range ids {
		for _, r := range rows {
			if r.ID == id {
				out = append(out, ReadyRow{ID: id, Status: r.Status, IssueType: r.IssueType, Labels: r.Labels})
			}
		}
	}
	return out
}

func trRun(t *testing.T, rows []Row, ready []ReadyRow, opts TriageOptions) TriageResult {
	t.Helper()
	res := Triage(rows, ready, opts, triageNow)
	if err := res.CheckPartition(); err != nil {
		t.Fatal(err)
	}
	return res
}

func trEq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestLiveFixpointTable(t *testing.T) {
	type tc struct {
		name     string
		rows     []Row
		ready    []string
		wantLive []string
		wantRev  []string
	}
	cases := []tc{
		{
			"chain of two clears via ready root",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r")), trMk("t2", trStatus(StatusBlocked), trBlockedBy("t1"))},
			[]string{"r"},
			[]string{"t1", "t2"},
			nil,
		},
		{
			"root carries human label: not drainable, chain is REVIEW",
			[]Row{trMk("r", trLabel("human")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r")), trMk("t2", trStatus(StatusBlocked), trBlockedBy("t1"))},
			[]string{"r"},
			nil,
			[]string{"t1", "t2"},
		},
		{
			"root carries human-focus-required",
			[]Row{trMk("r", trLabel("human-focus-required")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"root carries refactor-campaign",
			[]Row{trMk("r", trLabel("refactor-campaign")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"root is an epic: not drainable",
			[]Row{trMk("r", trTyp("epic")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"target human-labelled",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusBlocked), trLabel("human"), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"target has defer_until",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusBlocked), trDeferUntil("2027-01-01T00:00:00Z"), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"target status deferred",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusDeferred), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"closed blocker ignored when another open blocker is LIVE",
			[]Row{trMk("r"), trMk("c", trClosedAt("2026-10-01T00:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r", "c"))},
			[]string{"r"},
			[]string{"t1"},
			nil,
		},
		{
			"only closed blockers: anomalous, REVIEW (deviation from spec)",
			[]Row{trMk("c", trClosedAt("2026-10-01T00:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("c"))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"no blockers at all: anomalous, REVIEW",
			[]Row{trMk("t1", trStatus(StatusBlocked))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"one open blocker not LIVE",
			[]Row{trMk("r"), trMk("o", trStatus(StatusBlocked), trLabel("human")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r", "o"))},
			[]string{"r"},
			nil,
			[]string{"o", "t1"},
		},
		{
			"unknown blocker id forces REVIEW",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r", "ghost"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"cycle never LIVE",
			[]Row{trMk("a", trStatus(StatusBlocked), trBlockedBy("b")), trMk("b", trStatus(StatusBlocked), trBlockedBy("a"))},
			nil, nil,
			[]string{"a", "b"},
		},
		{
			"cycle hanging off a live root stays out",
			[]Row{trMk("r"), trMk("a", trStatus(StatusBlocked), trBlockedBy("b", "r")), trMk("b", trStatus(StatusBlocked), trBlockedBy("a"))},
			[]string{"r"},
			nil,
			[]string{"a", "b"},
		},
		{
			"gate blocker in ready is never LIVE",
			[]Row{trMk("g", trTyp("gate")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("g"))},
			[]string{"g"},
			nil,
			[]string{"t1"},
		},
		{
			"gate target itself is never LIVE",
			[]Row{trMk("r"), trMk("g", trTyp("gate"), trStatus(StatusBlocked), trBlockedBy("r"))},
			[]string{"r"},
			nil,
			[]string{"g"},
		},
		{
			"in_progress gate does not seed LIVE",
			[]Row{trMk("g", trTyp("gate"), trStatus(StatusInProgress), trUpdated("2026-10-10T11:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("g"))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"in_progress updated 23h ago seeds LIVE",
			[]Row{trMk("ip", trStatus(StatusInProgress), trUpdated("2026-10-09T13:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("ip"))},
			nil,
			[]string{"t1"},
			nil,
		},
		{
			"in_progress updated 25h ago does not",
			[]Row{trMk("ip", trStatus(StatusInProgress), trUpdated("2026-10-09T11:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("ip"))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"in_progress exactly 24h ago does not (strictly less than)",
			[]Row{trMk("ip", trStatus(StatusInProgress), trUpdated("2026-10-09T12:00:00Z")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("ip"))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"in_progress with unparsable updated_at does not seed",
			[]Row{trMk("ip", trStatus(StatusInProgress), trUpdated("yesterday")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("ip"))},
			nil, nil,
			[]string{"t1"},
		},
		{
			"healthy ancestor keeps LIVE",
			[]Row{trMk("r"), trMk("p"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("p"))},
			[]string{"r"},
			[]string{"t1"},
			nil,
		},
		{
			"deferred ancestor of target",
			[]Row{trMk("r"), trMk("p", trStatus(StatusDeferred)), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("p"))},
			[]string{"r"},
			nil,
			[]string{"p", "t1"},
		},
		{
			"blocked ancestor of target",
			[]Row{trMk("r"), trMk("p", trStatus(StatusBlocked), trBlockedBy("r2")), trMk("r2"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("p"))},
			[]string{"r", "r2"},
			[]string{"p"},
			[]string{"t1"},
		},
		{
			"human ancestor of target",
			[]Row{trMk("r"), trMk("p", trLabel("human")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("p"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"grand-ancestor with defer_until",
			[]Row{trMk("r"), trMk("gp", trDeferUntil("2027-01-01T00:00:00Z")), trMk("p", trChildOf("gp")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("p"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"unknown parent forces REVIEW",
			[]Row{trMk("r"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("r"), trChildOf("ghost"))},
			[]string{"r"},
			nil,
			[]string{"t1"},
		},
		{
			"blocker's ancestor is deferred",
			[]Row{trMk("p", trStatus(StatusDeferred)), trMk("b", trStatus(StatusBlocked), trChildOf("p"), trBlockedBy("r")), trMk("r"), trMk("t1", trStatus(StatusBlocked), trBlockedBy("b"))},
			[]string{"r"},
			nil,
			[]string{"b", "t1"},
		},
		{
			"blocker's ancestor is human-labelled (blocker is ready)",
			[]Row{trMk("p", trLabel("human")), trMk("b", trChildOf("p")), trMk("t1", trStatus(StatusBlocked), trBlockedBy("b"))},
			[]string{"b"},
			nil,
			[]string{"t1"},
		},
		{
			"blocked by its own descendant",
			[]Row{trMk("t1", trStatus(StatusBlocked), trBlockedBy("c")), trMk("c", trChildOf("t1"))},
			[]string{"c"},
			nil,
			[]string{"t1"},
		},
		{
			"blocked by its own grand-descendant",
			[]Row{trMk("t1", trStatus(StatusBlocked), trBlockedBy("gc")), trMk("c", trChildOf("t1")), trMk("gc", trChildOf("c"))},
			[]string{"c", "gc"},
			nil,
			[]string{"t1"},
		},
		{
			"blocked by a sibling is fine",
			[]Row{trMk("p", trStatus(StatusBlocked), trBlockedBy("r")), trMk("r"), trMk("s", trChildOf("p")), trMk("t1", trStatus(StatusBlocked), trChildOf("p"), trBlockedBy("s"))},
			[]string{"r", "s"},
			[]string{"p"},
			[]string{"t1"},
		},
		{
			"dotted ids work",
			[]Row{trMk("tc-o14i5.3.7"), trMk("tc-o14i5.3.8", trStatus(StatusBlocked), trBlockedBy("tc-o14i5.3.7"))},
			[]string{"tc-o14i5.3.7"},
			[]string{"tc-o14i5.3.8"},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := trRun(t, c.rows, trRdy(c.rows, c.ready...), TriageOptions{})
			// Targets that are not ready and not listed in want are asserted via Review below.
			trEq(t, "live", res.Live, c.wantLive)
			for _, id := range c.wantRev {
				found := false
				for _, r := range res.Review {
					found = found || r == id
				}
				if !found {
					t.Fatalf("%s not in review %v", id, res.Review)
				}
			}
		})
	}
}

func TestDrainableExclusions(t *testing.T) {
	rows := []Row{
		trMk("ok"),
		trMk("h", trLabel("human")),
		trMk("hf", trLabel("human-focus-required")),
		trMk("rc", trLabel("refactor-campaign")),
		trMk("ep", trTyp("epic")),
		trMk("tp"),
		trMk("other", trLabel("unrelated")),
	}
	ready := trRdy(rows, "ok", "h", "hf", "rc", "ep", "tp", "other")
	for i := range ready {
		if ready[i].ID == "tp" {
			ready[i].IsTemplate = true
		}
	}
	res := trRun(t, rows, ready, TriageOptions{})
	trEq(t, "drainable", res.Drainable, []string{"ok", "other"})
	if res.Counts.Drainable != 2 || res.Counts.Ready != 7 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if len(res.Targets) != 0 {
		t.Fatalf("ready beads are not targets: %v", res.Targets)
	}
}

func TestTargetsAndClaimCandidates(t *testing.T) {
	rows := []Row{
		trMk("ready1"),
		trMk("blocked1", trStatus(StatusBlocked), trBlockedBy("ready1")),
		trMk("open-notready", trLabel("human")),
		trMk("deferred1", trStatus(StatusDeferred), trDeferUntil("2027-01-01T00:00:00Z")),
		trMk("deferred-assigned", trStatus(StatusDeferred), trAssignee("w-1")),
		trMk("blocked-assigned", trStatus(StatusBlocked), trAssignee("w-2")),
		trMk("ready-assigned", trAssignee("w-3")),
		trMk("ip1", trStatus(StatusInProgress), trAssignee("w-4"), trUpdated("2026-10-10T10:00:00Z")),
		trMk("ip-unassigned", trStatus(StatusInProgress)),
		trMk("closed1", trClosedAt("2026-10-01T00:00:00Z")),
	}
	res := trRun(t, rows, trRdy(rows, "ready1", "ready-assigned"), TriageOptions{})
	trEq(t, "targets", res.Targets, []string{"blocked1", "deferred1", "open-notready"})
	trEq(t, "assigned-open", res.AssignedOpen, []string{"blocked-assigned", "deferred-assigned"})
	trEq(t, "in-progress", res.InProgress, []string{"ip-unassigned", "ip1"})
	var ids []string
	for _, c := range res.ClaimCandidates {
		ids = append(ids, c.ID)
	}
	trEq(t, "claim candidates", ids, []string{"blocked-assigned", "deferred-assigned", "ip-unassigned", "ip1"})
	if res.ClaimCandidates[3].Assignee != "w-4" || res.ClaimCandidates[3].Status != StatusInProgress {
		t.Fatalf("candidate detail = %+v", res.ClaimCandidates[3])
	}
	want := TriageCounts{Open: 3, Blocked: 2, Deferred: 2, InProgress: 2, Ready: 2, Targets: 3, LiveSkip: 1, Review: 2, Drainable: 2}
	if res.Counts != want {
		t.Fatalf("counts = %+v want %+v", res.Counts, want)
	}
	if got, want := res.Arithmetic(), "targets 3 = live 1 + marker 0 + review 2"; got != want {
		t.Fatalf("arithmetic = %q want %q", got, want)
	}
}

func TestNarrowingAppliesAfterLive(t *testing.T) {
	rows := []Row{
		trMk("a-root"),
		trMk("a-mid", trStatus(StatusBlocked), trBlockedBy("a-root"), trLabel("x")),
		trMk("b-leaf", trStatus(StatusBlocked), trBlockedBy("a-mid"), trLabel("y")),
		trMk("b-other", trStatus(StatusBlocked), trLabel("y")),
	}
	ready := trRdy(rows, "a-root")
	all := trRun(t, rows, ready, TriageOptions{})
	trEq(t, "live (unnarrowed)", all.Live, []string{"a-mid", "b-leaf"})

	// b-leaf is LIVE only because a-mid is LIVE; narrowing to label y must keep it LIVE
	// even though a-mid is outside the narrowed target set.
	byLabel := trRun(t, rows, ready, TriageOptions{Label: "y"})
	trEq(t, "targets", byLabel.Targets, []string{"b-leaf", "b-other"})
	trEq(t, "live", byLabel.Live, []string{"b-leaf"})
	trEq(t, "review", byLabel.Review, []string{"b-other"})

	byPrefix := trRun(t, rows, ready, TriageOptions{IDPrefix: "b-"})
	trEq(t, "targets", byPrefix.Targets, []string{"b-leaf", "b-other"})
	trEq(t, "live", byPrefix.Live, []string{"b-leaf"})
	if byPrefix.Counts.Drainable != all.Counts.Drainable {
		t.Fatal("drainable must not be narrowed")
	}
	both := trRun(t, rows, ready, TriageOptions{Label: "x", IDPrefix: "b-"})
	if len(both.Targets) != 0 {
		t.Fatalf("targets = %v", both.Targets)
	}
}

func TestMarkerSkipTable(t *testing.T) {
	const ok = "2026-10-09T10:05:00Z" // within 15 min of markerTS
	base := func(extra ...trOpt) Row {
		opts := append([]trOpt{trStatus(StatusBlocked), trNotes(trMarker(markerTS, "on-change")), trUpdated(ok)}, extra...)
		return trMk("t", opts...)
	}
	type tc struct {
		name string
		rows []Row
		opts TriageOptions
		skip bool
		why  string
	}
	cases := []tc{
		{"baseline skips", []Row{base()}, TriageOptions{}, true, ""},
		{"baseline with --full reviews", []Row{base()}, TriageOptions{Full: true}, false, ReasonFullSweep},
		{"no marker", []Row{trMk("t", trStatus(StatusBlocked))}, TriageOptions{}, false, ReasonNoMarker},
		{"updated exactly 15 min after marker skips", []Row{base(trUpdated("2026-10-09T10:15:00Z"))}, TriageOptions{}, true, ""},
		{"updated 16 min after marker", []Row{base(trUpdated("2026-10-09T10:16:00Z"))}, TriageOptions{}, false, ReasonUpdatedAfter},
		{"updated before marker is fine", []Row{base(trUpdated("2026-10-09T09:00:00Z"))}, TriageOptions{}, true, ""},
		{"unparsable updated_at", []Row{base(trUpdated("garbage"))}, TriageOptions{}, false, ReasonUpdatedAfter},
		{"newer comment", []Row{base(trComment("2026-10-09T10:01:00Z", "operator says hi"))}, TriageOptions{}, false, ReasonNewerComment},
		{"older comment is fine", []Row{base(trComment("2026-10-09T09:00:00Z", "earlier"))}, TriageOptions{}, true, ""},
		{"unparsable comment time", []Row{base(trComment("??", "x"))}, TriageOptions{}, false, ReasonNewerComment},
		{
			"marker's own comment does not count as newer",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trComment("2026-10-09T10:00:03Z", trMarker(markerTS, "on-change")))},
			TriageOptions{},
			true, "",
		},
		{
			"blocker updated after marker",
			[]Row{base(trBlockedBy("b")), trMk("b", trUpdated("2026-10-09T10:30:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourChanged,
		},
		{
			"blocker updated before marker",
			[]Row{base(trBlockedBy("b")), trMk("b", trUpdated("2026-10-09T09:30:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"parent updated after marker",
			[]Row{base(trChildOf("p")), trMk("p", trUpdated("2026-10-09T10:30:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourChanged,
		},
		{
			"child updated after marker",
			[]Row{base(), trMk("c", trChildOf("t"), trUpdated("2026-10-09T10:30:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourChanged,
		},
		{
			"dependent updated after marker is ignored",
			[]Row{base(), trMk("d", trBlockedBy("t"), trUpdated("2026-10-09T10:30:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"dependent closed after marker is ignored",
			[]Row{base(), trMk("d", trBlockedBy("t"), trClosedAt("2026-10-09T10:30:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"unknown blocker forces REVIEW in marker path",
			[]Row{base(trBlockedBy("ghost"))},
			TriageOptions{},
			false, ReasonUnknownRef,
		},
		{
			"unknown parent forces REVIEW in marker path",
			[]Row{base(trChildOf("ghost"))},
			TriageOptions{},
			false, ReasonUnknownRef,
		},
		{
			"recheck date in the future skips",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "2026-11-01")))},
			TriageOptions{},
			true, "",
		},
		{
			"recheck date in the past",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "2026-10-10")))},
			TriageOptions{},
			false, ReasonRecheckDue,
		},
		{
			"recheck date is yesterday",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "2026-10-09")))},
			TriageOptions{},
			false, ReasonRecheckDue,
		},
		{
			"recheck date is tomorrow",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "2026-10-11")))},
			TriageOptions{},
			true, "",
		},
		{
			"recheck: bead still open skips",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "w-1 closes"))), trMk("w-1")},
			TriageOptions{},
			true, "",
		},
		{
			"recheck: bead in_progress skips",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "w-1 closes"))), trMk("w-1", trStatus(StatusInProgress))},
			TriageOptions{},
			true, "",
		},
		{
			"recheck: bead closed before marker is due",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "w-1 closes"))), trMk("w-1", trClosedAt("2026-10-09T09:00:00Z"))},
			TriageOptions{},
			false, ReasonRecheckDue,
		},
		{
			"recheck: bead absent",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "w-1 closes")))},
			TriageOptions{},
			false, ReasonUnknownRef,
		},
		{
			"recheck: dotted id closed",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "tc-o14i5.3.7 closes"))), trMk("tc-o14i5.3.7", trClosedAt("2026-10-09T09:00:00Z"))},
			TriageOptions{},
			false, ReasonRecheckDue,
		},
		{
			"recheck: multi-hyphen id open",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "tc-mol-4prt closes"))), trMk("tc-mol-4prt")},
			TriageOptions{},
			true, "",
		},
		{
			"override: blocker closed after marker",
			[]Row{base(trBlockedBy("b")), trMk("b", trClosedAt("2026-10-09T11:00:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourClosed,
		},
		{
			"override: blocker closed before marker, quiet since",
			[]Row{base(trBlockedBy("b")), trMk("b", trClosedAt("2026-10-09T09:00:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"override: parent closed after marker",
			[]Row{base(trChildOf("p")), trMk("p", trClosedAt("2026-10-09T11:00:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourClosed,
		},
		{
			"override: child closed after marker",
			[]Row{base(), trMk("c", trChildOf("t"), trClosedAt("2026-10-09T11:00:00Z"))},
			TriageOptions{},
			false, ReasonNeighbourClosed,
		},
		{
			"override: closed neighbour with unparsable closed_at",
			[]Row{base(trBlockedBy("b")), func() Row { r := trMk("b"); r.Status = StatusClosed; r.ClosedAt = "bogus"; return r }()},
			TriageOptions{},
			false, ReasonNeighbourClosed,
		},
		{
			"override: defer_until elapsed",
			[]Row{base(trDeferUntil("2026-10-10T11:00:00Z"))},
			TriageOptions{},
			false, ReasonDeferElapsed,
		},
		{
			"defer_until in the future keeps skip",
			[]Row{base(trDeferUntil("2026-10-11T11:00:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"defer_until bare date elapsed",
			[]Row{base(trDeferUntil("2026-10-10"))},
			TriageOptions{},
			false, ReasonDeferElapsed,
		},
		{
			"defer_until unparsable",
			[]Row{base(trDeferUntil("soon"))},
			TriageOptions{},
			false, ReasonDeferUnparsable,
		},
		{
			"override: deferred with empty defer_until",
			[]Row{base(trStatus(StatusDeferred))},
			TriageOptions{},
			false, ReasonDeferredNoDate,
		},
		{
			"override: deferred with elapsed defer_until",
			[]Row{base(trStatus(StatusDeferred), trDeferUntil("2026-10-09T23:00:00Z"))},
			TriageOptions{},
			false, ReasonDeferElapsed,
		},
		{
			"deferred with future defer_until and valid marker skips",
			[]Row{base(trStatus(StatusDeferred), trDeferUntil("2027-01-01T00:00:00Z"))},
			TriageOptions{},
			true, "",
		},
		{
			"marker in a comment works",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trComment("2026-10-09T10:00:02Z", trMarker(markerTS, "on-change")))},
			TriageOptions{},
			true, "",
		},
		{
			"date-only marker counts as no marker",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes("[unstick 2026-10-09] unchanged: x; recheck-when: on-change"))},
			TriageOptions{},
			false, ReasonNoMarker,
		},
		{
			"non-Z marker counts as no marker",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes("[unstick 2026-10-09T10:00:00+02:00] unchanged: x; recheck-when: on-change"))},
			TriageOptions{},
			false, ReasonNoMarker,
		},
		{
			"newest marker wins: old due date superseded",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker("2026-09-01T10:00:00Z", "2026-09-05")+"\n"+trMarker(markerTS, "on-change")))},
			TriageOptions{},
			true, "",
		},
		{
			"newest marker wins regardless of order in notes",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker(markerTS, "on-change")+"\n"+trMarker("2026-09-01T10:00:00Z", "2026-09-05")))},
			TriageOptions{},
			true, "",
		},
		{
			"newest marker wins: newer one is due",
			[]Row{trMk("t", trStatus(StatusBlocked), trUpdated(ok), trNotes(trMarker("2026-09-01T10:00:00Z", "on-change")+"\n"+trMarker(markerTS, "2026-10-09")))},
			TriageOptions{},
			false, ReasonRecheckDue,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := trRun(t, c.rows, nil, c.opts)
			inSkip := len(res.MarkerSkip) == 1 && res.MarkerSkip[0] == "t"
			if inSkip != c.skip {
				t.Fatalf("skip = %v want %v (review=%v reasons=%v)", inSkip, c.skip, res.Review, res.ReviewReasons)
			}
			if !c.skip {
				if got := res.ReviewReasons["t"]; got != c.why {
					t.Fatalf("reason = %q want %q", got, c.why)
				}
			}
		})
	}
}

func TestLiveWinsOverMarker(t *testing.T) {
	rows := []Row{
		trMk("r"),
		trMk("t", trStatus(StatusBlocked), trBlockedBy("r"), trNotes(trMarker(markerTS, "on-change")), trUpdated("2026-10-09T10:05:00Z")),
	}
	res := trRun(t, rows, trRdy(rows, "r"), TriageOptions{})
	trEq(t, "live", res.Live, []string{"t"})
	trEq(t, "marker", res.MarkerSkip, nil)
}

func TestMalformedMarkersListed(t *testing.T) {
	rows := []Row{
		trMk("good", trStatus(StatusBlocked), trNotes(trMarker(markerTS, "on-change"))),
		trMk("dateonly", trStatus(StatusBlocked), trNotes("[unstick 2026-10-09] unchanged: x; recheck-when: on-change")),
		trMk("nonz", trStatus(StatusBlocked), trComment(oldTS, "[unstick 2026-10-09T10:00:00+02:00] unchanged: x; recheck-when: on-change")),
		trMk("norecheck", trStatus(StatusDeferred), trNotes("[unstick 2026-10-09T10:00:00Z] unchanged: x")),
		trMk("closedbad", trClosedAt("2026-10-01T00:00:00Z"), trNotes("[unstick junk")),
	}
	res := trRun(t, rows, nil, TriageOptions{})
	var ids []string
	for _, m := range res.MalformedMarkers {
		ids = append(ids, m.ID)
		if len(m.Lines) == 0 || len(m.Markers) == 0 {
			t.Fatalf("%s has no lines", m.ID)
		}
	}
	trEq(t, "malformed", ids, []string{"dateonly", "nonz", "norecheck"})
	for _, id := range []string{"dateonly", "nonz", "norecheck"} {
		if res.ReviewReasons[id] != ReasonNoMarker {
			t.Fatalf("%s reason = %q", id, res.ReviewReasons[id])
		}
	}
}

func TestTriageDeterministicAcrossInputOrder(t *testing.T) {
	rows := []Row{
		trMk("r"), trMk("d"),
		trMk("t1", trStatus(StatusBlocked), trBlockedBy("r")),
		trMk("t2", trStatus(StatusBlocked), trBlockedBy("t1")),
		trMk("t3", trStatus(StatusBlocked), trBlockedBy("d", "ghost")),
		trMk("t4", trStatus(StatusDeferred), trDeferUntil("2027-01-01T00:00:00Z")),
		trMk("ip", trStatus(StatusInProgress), trAssignee("w")),
		trMk("t5", trStatus(StatusBlocked), trUpdated("2026-10-09T10:05:00Z"), trNotes(trMarker(markerTS, "on-change"))),
	}
	ready := trRdy(rows, "r", "d")
	want := Triage(rows, ready, TriageOptions{}, triageNow)
	for _, perm := range [][]int{{7, 6, 5, 4, 3, 2, 1, 0}, {3, 1, 4, 0, 5, 2, 7, 6}} {
		shuffled := make([]Row, len(rows))
		for i, p := range perm {
			shuffled[i] = rows[p]
		}
		rev := append([]ReadyRow(nil), ready...)
		sort.Slice(rev, func(i, j int) bool { return rev[i].ID > rev[j].ID })
		got := Triage(shuffled, rev, TriageOptions{}, triageNow)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order changed result:\n got %+v\nwant %+v", got, want)
		}
	}
	for _, l := range [][]string{want.Targets, want.Live, want.MarkerSkip, want.Review, want.Drainable} {
		if !sort.StringsAreSorted(l) {
			t.Fatalf("not sorted: %v", l)
		}
	}
	// Same inputs and now: identical; different now: time-dependent outcomes change.
	again := Triage(rows, ready, TriageOptions{}, triageNow)
	if !reflect.DeepEqual(again, want) {
		t.Fatal("not repeatable")
	}
	if len(want.Live) != 2 || len(want.MarkerSkip) != 1 || len(want.Review) != 2 {
		t.Fatalf("unexpected partition: %s", want.Arithmetic())
	}
}

func TestTriageInjectedNowChangesDeferElapsed(t *testing.T) {
	rows := []Row{trMk("t", trStatus(StatusBlocked), trUpdated("2026-10-09T10:05:00Z"), trDeferUntil("2026-10-11T00:00:00Z"), trNotes(trMarker(markerTS, "on-change")))}
	before := Triage(rows, nil, TriageOptions{}, triageNow)
	after := Triage(rows, nil, TriageOptions{}, triageNow.Add(48*time.Hour))
	trEq(t, "before marker", before.MarkerSkip, []string{"t"})
	trEq(t, "after review", after.Review, []string{"t"})
}

func TestCheckPartitionDetectsViolation(t *testing.T) {
	bad := TriageResult{Targets: []string{"a", "b"}, Live: []string{"a"}, Review: []string{"a"}}
	if err := bad.CheckPartition(); err == nil {
		t.Fatal("expected duplicate membership error")
	}
	bad = TriageResult{Targets: []string{"a"}}
	if err := bad.CheckPartition(); err == nil {
		t.Fatal("expected sum error")
	}
}

func TestTriageDoesNotMutateInput(t *testing.T) {
	rows := []Row{trMk("r", trLabel("z", "a")), trMk("t", trStatus(StatusBlocked), trBlockedBy("r"))}
	before := append([]string(nil), rows[0].Labels...)
	trRun(t, rows, trRdy(rows, "r"), TriageOptions{})
	if !reflect.DeepEqual(rows[0].Labels, before) {
		t.Fatal("labels mutated")
	}
}

func TestTriageSyntheticFixture(t *testing.T) {
	rows, err := ReadExportFile("testdata/triage-export.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/triage-ready.json")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := ParseReady(raw)
	if err != nil {
		t.Fatal(err)
	}
	res := trRun(t, rows, ready, TriageOptions{})
	trEq(t, "targets", res.Targets, []string{"sx-def1", "sx-gate1", "sx-gdep", "sx-live1", "sx-live2", "sx-rev1", "sx-skip1"})
	trEq(t, "live", res.Live, []string{"sx-live1", "sx-live2"})
	trEq(t, "marker", res.MarkerSkip, []string{"sx-skip1"})
	trEq(t, "review", res.Review, []string{"sx-def1", "sx-gate1", "sx-gdep", "sx-rev1"})
	trEq(t, "drainable", res.Drainable, []string{"sx-ready1"})
	trEq(t, "assigned", res.AssignedOpen, []string{"sx-asg1"})
	trEq(t, "inprogress", res.InProgress, []string{"sx-ip1"})
	if got, want := res.Arithmetic(), "targets 7 = live 2 + marker 1 + review 4"; got != want {
		t.Fatalf("arithmetic = %q want %q", got, want)
	}
	full := trRun(t, rows, ready, TriageOptions{Full: true})
	trEq(t, "full review", full.Review, []string{"sx-def1", "sx-gate1", "sx-gdep", "sx-rev1", "sx-skip1"})
}

// TestExcludedLabelsMatchDrainBeads is the drift test: the claim query in
// drain-beads.md must exclude exactly the labels the sweep treats as
// non-drainable.
func TestExcludedLabelsMatchDrainBeads(t *testing.T) {
	b, err := os.ReadFile("../../../../claude-marketplace/pb/commands/drain-beads.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`bd ready --exclude-label ([A-Za-z0-9,_-]+)`)
	m := re.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("drain-beads.md has no `bd ready --exclude-label ...` claim query")
	}
	got := strings.Split(m[1], ",")
	want := ExcludedLabels()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drain-beads.md excludes %v but ExcludedLabelsCSV is %v", got, want)
	}
	if m[1] != ExcludedLabelsCSV {
		t.Fatalf("order drifted: %q vs %q", m[1], ExcludedLabelsCSV)
	}
}

// A focus-item bead is held by the decider: a sweep must never review it, in
// EVERY status, whatever its defer date, blockers or markers. A claimed one
// stays a claim candidate and is never a target.
func TestFocusItemExcludedFromTargets(t *testing.T) {
	rows := []Row{
		trMk("f-deferred", trStatus(StatusDeferred), trLabel("focus-item"), trDeferUntil("2026-09-02T00:00:00Z")),
		trMk("f-blocked", trStatus(StatusBlocked), trLabel("focus-item")),
		trMk("f-open", trLabel("focus-item")),
		trMk("f-claimed", trStatus(StatusInProgress), trLabel("focus-item"), trAssignee("someone")),
		trMk("plain", trStatus(StatusBlocked)),
	}
	res := trRun(t, rows, nil, TriageOptions{Full: true})
	trEq(t, "targets", res.Targets, []string{"plain"})
	trEq(t, "review", res.Review, []string{"plain"})
	trEq(t, "focus excluded", res.FocusExcluded, []string{"f-blocked", "f-deferred", "f-open"})
	if res.Counts.FocusExcluded != 3 || res.Counts.Targets != 1 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	var claimed []string
	for _, c := range res.ClaimCandidates {
		claimed = append(claimed, c.ID)
	}
	trEq(t, "claim candidates", claimed, []string{"f-claimed"})

	// Narrowing must not resurrect a focus bead, and the exclusion count is
	// independent of the narrowing.
	narrowed := trRun(t, rows, nil, TriageOptions{IDPrefix: "f-"})
	trEq(t, "narrowed targets", narrowed.Targets, nil)
	trEq(t, "narrowed focus excluded", narrowed.FocusExcluded, []string{"f-blocked", "f-deferred", "f-open"})
}
