package unstick

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	rtStart = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	rtNow   = time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC)
)

func rtMarker(ts, outcome string) string {
	return "[unstick " + ts + "] " + outcome + ": synthetic reason; recheck-when: on-change"
}

// rtIn builds a minimal input: pre statuses from pre (id -> status), start
// 12:00, ready sets empty/unknown.
func rtIn(pre map[string]string, post []Row) ReportInput {
	st := PrepareState{Start: FormatTime(rtStart), Now: FormatTime(rtStart), Pre: pre}
	return ReportInput{Prepare: st, Post: post, Now: rtNow, Batches: map[string][]string{}, Results: map[string]string{}}
}

func rtFind(cs []Change, id string) (Change, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return Change{}, false
}

func TestParseResultsClosed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"plain", "closed sy-1: stale", map[string]string{"sy-1": "stale"}},
		{"bullet dash", "- closed sy-1: stale", map[string]string{"sy-1": "stale"}},
		{"bullet star indent", "   * closed sy-1: stale", map[string]string{"sy-1": "stale"}},
		{"no reason", "closed sy-1", map[string]string{"sy-1": ""}},
		{
			"multi hyphen and dotted id", "closed tc-mol-4prt: a\nclosed tc-o14i5.3.7: b",
			map[string]string{"tc-mol-4prt": "a", "tc-o14i5.3.7": "b"},
		},
		{"reason keeps colons", "closed sy-1: why: because", map[string]string{"sy-1": "why: because"}},
		{"prose not a claim", "closed sy-1 because it was old", map[string]string{}},
		{"mid-line not a claim", "I closed sy-1: nope", map[string]string{}},
		{"capital not a claim", "Closed sy-1: nope", map[string]string{}},
		{"first reason wins", "closed sy-1: one\nclosed sy-1: two", map[string]string{"sy-1": "one"}},
		{"trailing space", "closed sy-1: r  \t", map[string]string{"sy-1": "r"}},
		{"empty", "", map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseResultsClosed(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestParseFollowups(t *testing.T) {
	op, fu := ParseFollowups("OPERATOR: b thing\n- OPERATOR: a thing\nFOLLOWUP: x\nFOLLOWUP: x\nFOLLOWUP:\nnoise\n  * FOLLOWUP: y\n")
	if !reflect.DeepEqual(op, []string{"a thing", "b thing"}) {
		t.Errorf("operator = %v", op)
	}
	if !reflect.DeepEqual(fu, []string{"x", "y"}) {
		t.Errorf("followup = %v", fu)
	}
	op, fu = ParseFollowups("")
	if len(op) != 0 || len(fu) != 0 || op == nil || fu == nil {
		t.Errorf("empty = %#v %#v (want non-nil empty)", op, fu)
	}
}

func TestParseBatchIDs(t *testing.T) {
	got := ParseBatchIDs("# comment\nsy-2\n\n  sy-1  \nsy-2\n")
	if !reflect.DeepEqual(got, []string{"sy-1", "sy-2"}) {
		t.Errorf("got %v", got)
	}
}

func TestPrepareState_roundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "prepare.json")
	in := PrepareState{
		Start: "2026-10-10T12:00:00Z", Now: "2026-10-10T12:00:05Z",
		Counts: map[string]int{"open": 2, "ready": 1}, Review: []string{"b", "a", "a"},
		ReadyIDs: []string{"z"}, Pre: map[string]string{"a": "open"}, Closed: []string{"c2", "c1"},
	}
	if err := WritePrepare(p, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPrepare(p)
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.Review = []string{"a", "b"}
	want.Closed = []string{"c1", "c2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
	// Byte-stable.
	b1, _ := os.ReadFile(p)
	if err := WritePrepare(p, got); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p)
	if string(b1) != string(b2) {
		t.Error("second write differs")
	}
}

func TestReadPrepare_errors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadPrepare(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: want error")
	}
	bad := filepath.Join(dir, "bad.json")
	for name, content := range map[string]string{"not json": "{", "no start": `{"counts":{}}`, "bad start": `{"start":"2026-10-10"}`} {
		if err := os.WriteFile(bad, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPrepare(bad); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// Minimal valid file normalizes nil maps.
	if err := os.WriteFile(bad, []byte(`{"start":"2026-10-10T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := ReadPrepare(bad)
	if err != nil || st.Pre == nil || st.Counts == nil {
		t.Errorf("st=%+v err=%v", st, err)
	}
}

func TestBuildReport_attribution(t *testing.T) {
	const after = "2026-10-10T12:30:00Z"
	const before = "2026-10-10T11:30:00Z"
	closedRow := func(id string) Row { return Row{ID: id, Status: StatusClosed, CloseReason: "r"} }
	tests := []struct {
		name       string
		pre        map[string]string
		closedPre  []string
		post       Row
		batched    bool
		resultsTxt string
		want       string // "sweep", "peer" or "" (not a change)
	}{
		{
			"marker branch",
			map[string]string{"a": "open"},
			nil,
			Row{ID: "a", Status: StatusOpen, Labels: nil, Notes: rtMarker(after, "undeferred")},
			true, "", "",
		},
		{
			"closed via marker",
			map[string]string{"a": "open"},
			nil,
			Row{ID: "a", Status: StatusClosed, CloseReason: "r", Notes: rtMarker(after, "closed")},
			true, "", AttribSweep,
		},
		{
			"closed via results branch, no marker",
			map[string]string{"a": "blocked"},
			nil,
			closedRow("a"), true, "closed a: stale", AttribSweep,
		},
		{
			"closed, batched, results does not list it",
			map[string]string{"a": "blocked"},
			nil,
			closedRow("a"), true, "closed b: stale", AttribPeer,
		},
		{
			"closed, not batched, listed in results",
			map[string]string{"a": "blocked"},
			nil,
			closedRow("a"), false, "closed a: stale", AttribPeer,
		},
		{
			"closed, not batched, has marker",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusClosed, Notes: rtMarker(after, "closed")},
			false, "", AttribPeer,
		},
		{
			"closed, batched, only unchanged marker",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusClosed, Notes: rtMarker(after, "unchanged")},
			true, "", AttribPeer,
		},
		{
			"closed, batched, marker older than start",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusClosed, Notes: rtMarker(before, "closed")},
			true, "", AttribPeer,
		},
		{
			"marker exactly at start counts",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusClosed, Notes: rtMarker("2026-10-10T12:00:00Z", "closed")},
			true, "", AttribSweep,
		},
		{
			"results line but already closed pre is no change",
			map[string]string{},
			[]string{"a"},
			closedRow("a"), true, "closed a: x", "",
		},
		{
			"unchanged status unmarked",
			map[string]string{"a": "open"},
			nil,
			Row{ID: "a", Status: StatusOpen},
			true, "", "",
		},
		{
			"status moved by peer",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusOpen},
			false, "", AttribPeer,
		},
		{
			"status moved, batched, only unchanged marker",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusOpen, Notes: rtMarker(after, "unchanged")},
			true, "", AttribPeer,
		},
		{
			"status moved, batched, real marker",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusOpen, Notes: rtMarker(after, "fix-deps")},
			true, "", AttribSweep,
		},
		{
			"marker in comment counts",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusOpen, Comments: []Comment{{Text: rtMarker(after, "de-labelled")}}},
			true, "", AttribSweep,
		},
		{
			"malformed marker does not count",
			map[string]string{"a": "blocked"},
			nil,
			Row{ID: "a", Status: StatusOpen, Notes: "[unstick 2026-10-10] fix-deps: r; recheck-when: on-change"},
			true, "", AttribPeer,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := rtIn(tc.pre, []Row{tc.post})
			in.Prepare.Closed = tc.closedPre
			if tc.batched {
				in.Batches["B01"] = []string{"a"}
			}
			in.Results["B01"] = tc.resultsTxt
			rep := BuildReport(in)
			var got string
			switch {
			case rep.SweepN == 1:
				got = AttribSweep
			case rep.PeerN == 1:
				got = AttribPeer
			}
			if got != tc.want {
				t.Errorf("attribution = %q want %q (report %+v)", got, tc.want, rep)
			}
			if _, isPeer := rtFind(rep.Peers, "a"); isPeer != (tc.want == AttribPeer) {
				t.Errorf("in peers list = %v for want %q", isPeer, tc.want)
			}
			if rep.SweepN+rep.PeerN > 1 {
				t.Errorf("counted twice: %+v", rep)
			}
		})
	}
}

func TestBuildReport_diffs(t *testing.T) {
	mk := func(id, status string, mut func(*Row)) Row {
		r := Row{ID: id, Status: status}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	blocks := func(id string, on ...string) func(*Row) {
		return func(r *Row) {
			for _, o := range on {
				r.Dependencies = append(r.Dependencies, Dep{IssueID: id, DependsOnID: o, Type: DepBlocks})
			}
		}
	}
	type want struct {
		undeferred, delabelled, retargeted string // substring of detail, "" = must be absent
	}
	tests := []struct {
		name      string
		pre, post Row
		want      want
	}{
		{
			"defer cleared, still deferred status n/a",
			mk("a", StatusDeferred, func(r *Row) { r.DeferUntil = "2026-10-20T00:00:00Z" }), mk("a", StatusOpen, nil),
			want{undeferred: "defer_until 2026-10-20T00:00:00Z -> none"},
		},
		{
			"defer date moved is not undeferred",
			mk("a", StatusDeferred, func(r *Row) { r.DeferUntil = "2026-10-20T00:00:00Z" }),
			mk("a", StatusDeferred, func(r *Row) { r.DeferUntil = "2026-11-20T00:00:00Z" }),
			want{},
		},
		{
			"deferred status to open with date kept",
			mk("a", StatusDeferred, func(r *Row) { r.DeferUntil = "2026-10-20T00:00:00Z" }),
			mk("a", StatusOpen, func(r *Row) { r.DeferUntil = "2026-10-20T00:00:00Z" }),
			want{undeferred: "status deferred -> open"},
		},
		{
			"label removed", mk("a", StatusOpen, func(r *Row) { r.Labels = []string{"human", "pb"} }),
			mk("a", StatusOpen, func(r *Row) { r.Labels = []string{"pb"} }),
			want{delabelled: "removed labels: human"},
		},
		{
			"label added is not de-labelled", mk("a", StatusOpen, func(r *Row) { r.Labels = []string{"pb"} }),
			mk("a", StatusOpen, func(r *Row) { r.Labels = []string{"pb", "human"} }),
			want{},
		},
		{
			"blockers swapped", mk("a", StatusBlocked, blocks("a", "b")), mk("a", StatusBlocked, blocks("a", "c")),
			want{retargeted: "removed b; added c"},
		},
		{
			"blocker dropped", mk("a", StatusBlocked, blocks("a", "b", "c")), mk("a", StatusBlocked, blocks("a", "c")),
			want{retargeted: "removed b"},
		},
		{"parent-child edge ignored", mk("a", StatusOpen, func(r *Row) {
			r.Dependencies = []Dep{{IssueID: "a", DependsOnID: "p", Type: DepParentChild}}
		}), mk("a", StatusOpen, nil), want{}},
		{"dependent edge (other direction) ignored", mk("a", StatusOpen, func(r *Row) {
			r.Dependencies = []Dep{{IssueID: "z", DependsOnID: "a", Type: DepBlocks}}
		}), mk("a", StatusOpen, nil), want{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := rtIn(map[string]string{"a": tc.pre.Status}, []Row{tc.post})
			in.PreRows = []Row{tc.pre}
			in.Batches["B01"] = []string{"a"}
			in.Post[0].Notes = rtMarker("2026-10-10T12:30:00Z", "fix-deps")
			rep := BuildReport(in)
			for _, c := range []struct {
				kind string
				list []Change
				sub  string
			}{{"undeferred", rep.Undeferred, tc.want.undeferred}, {"delabelled", rep.Delabelled, tc.want.delabelled}, {"retargeted", rep.Retargeted, tc.want.retargeted}} {
				ch, ok := rtFind(c.list, "a")
				if c.sub == "" {
					if ok {
						t.Errorf("%s: unexpected %+v", c.kind, ch)
					}
					continue
				}
				if !ok || !strings.Contains(ch.Detail, c.sub) || ch.Attribution != AttribSweep {
					t.Errorf("%s: got %+v ok=%v want detail containing %q attributed sweep", c.kind, ch, ok, c.sub)
				}
			}
		})
	}
}

func TestBuildReport_diffSkippedWithoutPreRow(t *testing.T) {
	in := rtIn(map[string]string{"a": StatusOpen}, []Row{{ID: "a", Status: StatusOpen, Labels: []string{"x"}}})
	rep := BuildReport(in)
	if len(rep.Undeferred)+len(rep.Delabelled)+len(rep.Retargeted) != 0 {
		t.Errorf("diffs without pre rows: %+v", rep)
	}
}

func TestBuildReport_newBeadsAndPeers(t *testing.T) {
	in := rtIn(map[string]string{"a": StatusOpen}, []Row{
		{ID: "n2", Status: StatusOpen}, {ID: "a", Status: StatusOpen}, {ID: "n1", Status: StatusClosed}, {ID: "old", Status: StatusClosed},
	})
	in.Prepare.Closed = []string{"old"}
	in.Batches["B01"] = []string{"n1", "n2"} // batch membership cannot make a new bead sweep-attributed
	in.Results["B01"] = "closed n1: x"
	rep := BuildReport(in)
	if !reflect.DeepEqual(rep.NewBeads, []string{"n1", "n2"}) {
		t.Errorf("new = %v", rep.NewBeads)
	}
	if len(rep.Closed) != 0 || rep.SweepN != 0 || rep.PeerN != 0 {
		t.Errorf("new/old closed leaked into changes: %+v", rep)
	}
}

func TestBuildReport_closedAndMarkers(t *testing.T) {
	const ts = "2026-10-10T12:30:00Z"
	pre := map[string]string{"c1": "open", "c2": "blocked", "m1": "open", "m2": "open", "r1": "in_progress"}
	post := []Row{
		{ID: "c2", Status: StatusClosed, CloseReason: "peer reason"},
		{ID: "c1", Status: StatusClosed, CloseReason: "stale", Notes: rtMarker(ts, "closed")},
		{ID: "m1", Status: StatusOpen, Notes: rtMarker(ts, "unchanged") + "\n" + rtMarker("2026-10-10T12:40:00Z", "unchanged")},
		{ID: "m2", Status: StatusOpen, Notes: rtMarker(ts, "unchanged")},
		{ID: "r1", Status: StatusOpen, Notes: rtMarker(ts, "released")},
	}
	in := rtIn(pre, post)
	in.Batches["B01"] = []string{"c1", "m1", "m2", "r1"}
	rep := BuildReport(in)
	if got := []ClosedBead{{"c1", "stale", AttribSweep}, {"c2", "peer reason", AttribPeer}}; !reflect.DeepEqual(rep.Closed, got) {
		t.Errorf("closed = %+v", rep.Closed)
	}
	wantM := []OutcomeGroup{
		{"closed", 1, []string{"c1"}},
		{"released", 1, []string{"r1"}},
		{"unchanged", 3, []string{"m1", "m2"}},
	}
	if !reflect.DeepEqual(rep.MarkersByOutcome, wantM) {
		t.Errorf("markers = %+v", rep.MarkersByOutcome)
	}
	if !reflect.DeepEqual(rep.ClaimsReleased, []string{"r1"}) {
		t.Errorf("released = %v", rep.ClaimsReleased)
	}
}

func TestBuildReport_preExistingMarkersNotCounted(t *testing.T) {
	line := rtMarker("2026-10-10T12:10:00Z", "fix-deps") // inside the window but present pre-sweep
	pre := Row{ID: "a", Status: StatusOpen, Notes: line}
	post := Row{ID: "a", Status: StatusOpen, Notes: line + "\n" + rtMarker("2026-10-10T12:20:00Z", "unchanged")}
	in := rtIn(map[string]string{"a": "open"}, []Row{post})
	in.PreRows = []Row{pre}
	rep := BuildReport(in)
	if len(rep.MarkersByOutcome) != 1 || rep.MarkersByOutcome[0].Outcome != OutcomeUnchanged {
		t.Errorf("markers = %+v", rep.MarkersByOutcome)
	}
}

func TestBuildReport_countsArithmetic(t *testing.T) {
	pre := map[string]string{"a": "open", "b": "open", "c": "blocked", "d": "deferred"}
	post := []Row{{ID: "a", Status: StatusOpen}, {ID: "b", Status: StatusClosed}, {ID: "c", Status: StatusOpen}, {ID: "d", Status: StatusOpen}, {ID: "e", Status: StatusInProgress}}
	in := rtIn(pre, post)
	in.Prepare.Counts = map[string]int{"open": 2, "blocked": 1, "deferred": 1, "in_progress": 0, "ready": 1}
	in.PostReadyIDs, in.HavePostReady = []string{"a", "a", "c"}, true
	rep := BuildReport(in)
	var lines []string
	for _, c := range rep.Counts {
		lines = append(lines, c.Arithmetic)
	}
	want := []string{
		"open: 2 -> 3 (+1 = 3 - 2)",
		"blocked: 1 -> 0 (-1 = 0 - 1)",
		"deferred: 1 -> 0 (-1 = 0 - 1)",
		"in_progress: 0 -> 1 (+1 = 1 - 0)",
		"ready: 1 -> 2 (+1 = 2 - 1)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if rep.Counts[0].After == nil || *rep.Counts[0].After != 3 || *rep.Counts[0].Delta != 1 {
		t.Errorf("structured count wrong: %+v", rep.Counts[0])
	}
}

func TestBuildReport_countsFallBackToPreMap(t *testing.T) {
	in := rtIn(map[string]string{"a": "open", "b": "open", "c": "deferred"}, nil)
	rep := BuildReport(in)
	if rep.Counts[0].Before != 2 || rep.Counts[2].Before != 1 {
		t.Errorf("fallback counts = %+v", rep.Counts)
	}
}

func TestBuildReport_readyUnavailable(t *testing.T) {
	rep := BuildReport(rtIn(map[string]string{}, nil))
	last := rep.Counts[len(rep.Counts)-1]
	if last.Name != "ready" || last.After != nil || last.Arithmetic != "ready: 0 -> unavailable" {
		t.Errorf("ready line = %+v", last)
	}
	if rep.OpenNotReady != nil {
		t.Errorf("open-not-ready should be nil, got %+v", rep.OpenNotReady)
	}
	if !strings.Contains(rep.RenderHuman(), "open-not-ready: unavailable") {
		t.Error("human output should say open-not-ready unavailable")
	}
}

func TestBuildReport_openNotReadySplit(t *testing.T) {
	pre := map[string]string{
		"s1": "blocked", "s2": "deferred", "p1": "open", "stay": "open", "rdy": "open", "ip": "in_progress", "gone": "open",
	}
	post := []Row{
		{ID: "s1", Status: StatusClosed, Notes: rtMarker("2026-10-10T12:30:00Z", "closed")},
		{ID: "s2", Status: StatusOpen}, // undeferred by sweep; becomes ready
		{ID: "p1", Status: StatusClosed},
		{ID: "stay", Status: StatusOpen},
		{ID: "rdy", Status: StatusOpen},
		{ID: "ip", Status: StatusOpen}, // released: enters open-not-ready
		{ID: "gone", Status: StatusOpen},
	}
	in := rtIn(pre, post)
	in.Prepare.ReadyIDs = []string{"rdy"}
	in.PostReadyIDs, in.HavePostReady = []string{"rdy", "s2", "gone"}, true
	in.Batches["B01"] = []string{"s1", "s2"}
	in.PreRows = []Row{{ID: "s2", Status: StatusDeferred, DeferUntil: "2026-10-01T00:00:00Z"}}
	in.Post[1].Notes = rtMarker("2026-10-10T12:31:00Z", "undeferred")
	sp := BuildReport(in).OpenNotReady
	if sp == nil {
		t.Fatal("nil split")
	}
	// pre not-ready: s1 s2 p1 stay gone = 5 ("gone" is open and not in pre ready);
	// post not-ready: stay ip = 2.
	want := NotReadySplit{
		Before: 5, After: 2, Left: 4, LeftSweep: 2, LeftPeers: 2, Entered: 1,
		LeftIDs: []string{"gone", "p1", "s1", "s2"}, EnteredIDs: []string{"ip"},
		Arithmetic: "open-not-ready: 5 - 4 left (sweep 2 + peers 2) + 1 entered = 2",
	}
	if !reflect.DeepEqual(*sp, want) {
		t.Errorf("split =\n%+v\nwant\n%+v", *sp, want)
	}
	if sp.Before-sp.Left+sp.Entered != sp.After || sp.Left != sp.LeftSweep+sp.LeftPeers {
		t.Error("invariants violated")
	}
}

func TestBuildReport_emptySweep(t *testing.T) {
	in := rtIn(map[string]string{}, nil)
	in.HavePostReady = true
	rep := BuildReport(in)
	if rep.SweepN != 0 || rep.PeerN != 0 || rep.ChangedBy != "changed in window 0 = sweep 0 + peers 0" {
		t.Errorf("changed = %q", rep.ChangedBy)
	}
	if rep.OpenNotReady == nil || rep.OpenNotReady.Arithmetic != "open-not-ready: 0 - 0 left (sweep 0 + peers 0) + 0 entered = 0" {
		t.Errorf("split = %+v", rep.OpenNotReady)
	}
	// All list fields are non-nil so JSON shows [] not null.
	b, err := rep.RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("json contains null:\n%s", b)
	}
	h := rep.RenderHuman()
	for _, s := range []string{"Closed: none", "Peers (changed, not attributed to the sweep): none", "OPERATOR: none", "FOLLOWUP: none", "New beads: none"} {
		if !strings.Contains(h, s) {
			t.Errorf("human output missing %q:\n%s", s, h)
		}
	}
}

func TestBuildReport_followupsGrouped(t *testing.T) {
	in := rtIn(nil, nil)
	in.Followups = "FOLLOWUP: f2\nOPERATOR: o1\nFOLLOWUP: f1\n"
	rep := BuildReport(in)
	if !reflect.DeepEqual(rep.Operator, []string{"o1"}) || !reflect.DeepEqual(rep.Followup, []string{"f1", "f2"}) {
		t.Errorf("op=%v fu=%v", rep.Operator, rep.Followup)
	}
}

func TestBuildReport_deterministic(t *testing.T) {
	in := rtIn(map[string]string{"b": "open", "a": "blocked", "c": "open"}, []Row{
		{ID: "c", Status: StatusClosed}, {ID: "a", Status: StatusOpen}, {ID: "b", Status: StatusClosed}, {ID: "z", Status: StatusOpen}, {ID: "y", Status: StatusOpen},
	})
	first, _ := BuildReport(in).RenderJSON()
	for i := 0; i < 20; i++ {
		got, _ := BuildReport(in).RenderJSON()
		if string(got) != string(first) {
			t.Fatal("non-deterministic JSON output")
		}
	}
	var rep Report
	if err := json.Unmarshal(first, &rep); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.NewBeads, []string{"y", "z"}) {
		t.Errorf("new beads not sorted: %v", rep.NewBeads)
	}
}

func TestRenderHuman_containsHeuristicAndArithmetic(t *testing.T) {
	h := BuildReport(rtIn(map[string]string{"a": "open"}, []Row{{ID: "a", Status: StatusOpen}})).RenderHuman()
	for _, s := range []string{"HEURISTIC", "open: 1 -> 1 (+0 = 1 - 1)", "Sweep report (start 2026-10-10T12:00:00Z, report 2026-10-10T13:00:00Z)"} {
		if !strings.Contains(h, s) {
			t.Errorf("missing %q in:\n%s", s, h)
		}
	}
}

// ---- fixture-driven end-to-end test (testdata/report) ----

// rtLoadFixture copies testdata/report into a fresh work directory layout and
// loads it through LoadReportInput.
func rtLoadFixture(t *testing.T) ReportInput {
	t.Helper()
	src := filepath.Join("testdata", "report")
	w := Workdir{Path: t.TempDir()}
	copyFile := func(from, to string) {
		b, err := os.ReadFile(filepath.Join(src, from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(w.Join(to)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(w.Join(to), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copyFile("export.jsonl", ExportFile)
	copyFile("prepare.json", PrepareFile)
	copyFile("followups.txt", FollowupsFile)
	copyFile("batches/B01", filepath.Join(BatchesDir, "B01"))
	copyFile("results/B01.md", filepath.Join(ResultsDir, "B01.md"))
	post, err := ReadExportFile(filepath.Join(src, "export.post.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := os.ReadFile(filepath.Join(src, "ready.post.json"))
	if err != nil {
		t.Fatal(err)
	}
	rr, err := ParseReady(ready)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rr {
		ids = append(ids, r.ID)
	}
	in, err := LoadReportInput(w, post, ids, true, rtNow)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestReportFixture(t *testing.T) {
	rep := BuildReport(rtLoadFixture(t))

	var arith []string
	for _, c := range rep.Counts {
		arith = append(arith, c.Arithmetic)
	}
	wantCounts := []string{
		"open: 5 -> 7 (+2 = 7 - 5)",
		"blocked: 3 -> 1 (-2 = 1 - 3)",
		"deferred: 2 -> 0 (-2 = 0 - 2)",
		"in_progress: 1 -> 0 (-1 = 0 - 1)",
		"ready: 2 -> 4 (+2 = 4 - 2)",
	}
	if !reflect.DeepEqual(arith, wantCounts) {
		t.Errorf("counts:\n%s", strings.Join(arith, "\n"))
	}
	if got, want := rep.OpenNotReady.Arithmetic, "open-not-ready: 8 - 5 left (sweep 2 + peers 3) + 1 entered = 4"; got != want {
		t.Errorf("split = %q want %q", got, want)
	}
	if got, want := rep.ChangedBy, "changed in window 9 = sweep 5 + peers 4"; got != want {
		t.Errorf("changed = %q want %q", got, want)
	}

	wantClosed := []ClosedBead{
		{"sy-11", "by a peer", AttribPeer},
		{"sy-4", "stale: superseded", AttribSweep},
		{"sy-7", "done elsewhere", AttribPeer},
		{"sy-9", "done", AttribPeer},
	}
	if !reflect.DeepEqual(rep.Closed, wantClosed) {
		t.Errorf("closed = %+v", rep.Closed)
	}
	for _, id := range []string{"sy-1", "sy-10"} {
		if _, ok := rtFind(rep.Undeferred, id); !ok {
			t.Errorf("%s not in undeferred", id)
		}
	}
	if c, _ := rtFind(rep.Undeferred, "sy-1"); c.Attribution != AttribSweep {
		t.Errorf("sy-1 attribution = %s", c.Attribution)
	}
	if c, _ := rtFind(rep.Undeferred, "sy-10"); c.Attribution != AttribPeer {
		t.Errorf("sy-10 attribution = %s", c.Attribution)
	}
	if c, ok := rtFind(rep.Delabelled, "sy-2"); !ok || c.Detail != "removed labels: human" || c.Attribution != AttribSweep {
		t.Errorf("sy-2 delabel = %+v", c)
	}
	if c, ok := rtFind(rep.Retargeted, "sy-3"); !ok || c.Detail != "blockers: removed sy-9; added sy-8" {
		t.Errorf("sy-3 retarget = %+v", c)
	}
	if !reflect.DeepEqual(rep.ClaimsReleased, []string{"sy-6"}) || !reflect.DeepEqual(rep.NewBeads, []string{"sy-20"}) {
		t.Errorf("released=%v new=%v", rep.ClaimsReleased, rep.NewBeads)
	}
	wantM := map[string]int{"de-labelled": 1, "fix-deps": 1, "released": 1, "undeferred": 1, "unchanged": 2}
	gotM := map[string]int{}
	for _, g := range rep.MarkersByOutcome {
		gotM[g.Outcome] = g.Count
	}
	if !reflect.DeepEqual(gotM, wantM) {
		t.Errorf("markers = %v want %v", gotM, wantM)
	}
	if !reflect.DeepEqual(rep.Operator, []string{"decide on sy-5 policy", "review sy-2 label"}) ||
		!reflect.DeepEqual(rep.Followup, []string{"file a bead for sy-3 cleanup"}) {
		t.Errorf("followups op=%v fu=%v", rep.Operator, rep.Followup)
	}
}

func TestReportFixtureGolden(t *testing.T) {
	rep := BuildReport(rtLoadFixture(t))
	js, err := rep.RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"report.golden.txt": rep.RenderHuman(), "report.golden.json": string(js)} {
		p := filepath.Join("testdata", "report", name)
		if os.Getenv("UNSTICK_REPORT_UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(p, []byte(got), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%v (set UNSTICK_REPORT_UPDATE_GOLDEN=1 to create)", err)
		}
		if strings.HasSuffix(name, ".json") {
			// The repo's prettier hook reformats *.json, so compare semantically.
			var a, b any
			if err := json.Unmarshal(want, &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(got), &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s differs (set UNSTICK_REPORT_UPDATE_GOLDEN=1 to refresh):\n%s", name, got)
			}
			continue
		}
		if string(want) != got {
			t.Errorf("%s differs (set UNSTICK_REPORT_UPDATE_GOLDEN=1 to refresh):\n%s", name, got)
		}
	}
}

func TestLoadReportInput_toleratesMissingOptionalFiles(t *testing.T) {
	w := Workdir{Path: t.TempDir()}
	if err := WritePrepare(w.Join(PrepareFile), PrepareState{Start: "2026-10-10T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Join(ExportFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := LoadReportInput(w, nil, nil, false, rtNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Batches) != 0 || len(in.Results) != 0 || in.Followups != "" {
		t.Errorf("in = %+v", in)
	}
	if _, err := LoadReportInput(Workdir{Path: t.TempDir()}, nil, nil, false, rtNow); err == nil {
		t.Error("missing prepare.json: want error")
	}
}
