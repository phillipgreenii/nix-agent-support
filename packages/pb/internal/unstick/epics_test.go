package unstick

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	epClosedTS = "2026-09-20T00:00:00Z" // children closed well before markerTS
)

func epEpic(id string, opts ...trOpt) Row {
	return trMk(id, append([]trOpt{trTyp("epic")}, opts...)...)
}

func epKid(id string, parents ...string) Row {
	r := trMk(id, trClosedAt(epClosedTS))
	for _, p := range parents {
		trChildOf(p)(&r)
	}
	return r
}

// epReady builds ready rows from the named rows, carrying type, labels and
// assignee as `bd ready` does; templates names ids whose ready row is_template.
func epReady(rows []Row, templates []string, ids ...string) []ReadyRow {
	var out []ReadyRow
	for _, id := range ids {
		for _, r := range rows {
			if r.ID == id {
				out = append(out, ReadyRow{
					ID: id, Status: r.Status, IssueType: r.IssueType, Labels: r.Labels, Assignee: r.Assignee,
					IsTemplate: contains(templates, id),
				})
			}
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestEpicCandidatesTable(t *testing.T) {
	cases := []struct {
		name      string
		rows      []Row
		templates []string
		notReady  []string // ids excluded from the ready list
		opts      TriageOptions
		want      []string
	}{
		{
			name: "all children closed",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1"), epKid("e1.2", "e1")},
			want: []string{"e1"},
		},
		{
			name: "single closed child",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1")},
			want: []string{"e1"},
		},
		{
			name: "an open child disqualifies",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1"), trMk("e1.2", trChildOf("e1"))},
		},
		{
			name: "a deferred child disqualifies",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1"), trMk("e1.2", trChildOf("e1"), trStatus(StatusDeferred), trDeferUntil("2099-01-01T00:00:00Z"))},
		},
		{
			name: "a blocked child disqualifies",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1"), trMk("e1.2", trChildOf("e1"), trStatus(StatusBlocked), trBlockedBy("e1.1"))},
		},
		{
			name: "an in_progress child disqualifies",
			rows: []Row{epEpic("e1"), epKid("e1.1", "e1"), trMk("e1.2", trChildOf("e1"), trStatus(StatusInProgress))},
		},
		{
			name: "zero children (undecomposed phase container)",
			rows: []Row{epEpic("e1")},
		},
		{
			name: "a blocks edge is not a child",
			rows: []Row{epEpic("e1"), trMk("b", trStatus(StatusClosed), trClosedAt(epClosedTS), trBlockedBy("e1"))},
		},
		{
			name: "claimed (open with an assignee)",
			rows: []Row{epEpic("e1", trAssignee("worker-1")), epKid("e1.1", "e1")},
		},
		{
			name: "in_progress epic",
			rows: []Row{epEpic("e1", trStatus(StatusInProgress)), epKid("e1.1", "e1")},
		},
		{
			name: "focus-item label",
			rows: []Row{epEpic("e1", trLabel("focus-item")), epKid("e1.1", "e1")},
		},
		{
			name:      "template",
			rows:      []Row{epEpic("e1"), epKid("e1.1", "e1")},
			templates: []string{"e1"},
		},
		{
			name: "human-labelled epic is still a candidate",
			rows: []Row{epEpic("e1", trLabel("human")), epKid("e1.1", "e1")},
			want: []string{"e1"},
		},
		{
			name:     "not in bd ready",
			rows:     []Row{epEpic("e1"), epKid("e1.1", "e1")},
			notReady: []string{"e1"},
		},
		{
			name: "not an epic",
			rows: []Row{trMk("e1"), epKid("e1.1", "e1")},
		},
		{
			name: "child absent from the export is not closed",
			rows: []Row{epEpic("e1", func(r *Row) {
				r.Dependencies = append(r.Dependencies, Dep{IssueID: "ghost", DependsOnID: "e1", Type: DepParentChild})
			}), epKid("e1.1", "e1")},
		},
		{
			name: "nested epic with an open child epic: only the inner one",
			rows: []Row{
				epEpic("p"), epEpic("p.1", trChildOf("p")), epKid("p.1.1", "p.1"), epKid("p.1.2", "p.1"),
			},
			want: []string{"p.1"},
		},
		{
			name: "nested epic whose child epic is closed: outer becomes a candidate",
			rows: []Row{
				epEpic("p"), epEpic("p.1", trChildOf("p"), trClosedAt(epClosedTS)), epKid("p.1.1", "p.1"),
			},
			want: []string{"p"},
		},
		{
			name: "multi-parent closed child counts for both parents",
			rows: []Row{epEpic("e1"), epEpic("e2"), epKid("shared", "e1", "e2")},
			want: []string{"e1", "e2"},
		},
		{
			name: "multi-parent open child blocks both parents",
			rows: []Row{epEpic("e1"), epEpic("e2"), trMk("shared", trChildOf("e1"), trChildOf("e2"))},
		},
		{
			name: "--label narrows candidates",
			rows: []Row{epEpic("e1", trLabel("alpha")), epKid("e1.1", "e1"), epEpic("e2"), epKid("e2.1", "e2")},
			opts: TriageOptions{Label: "alpha"},
			want: []string{"e1"},
		},
		{
			name: "--id-prefix narrows candidates",
			rows: []Row{epEpic("aa-1"), epKid("aa-1.1", "aa-1"), epEpic("bb-1"), epKid("bb-1.1", "bb-1")},
			opts: TriageOptions{IDPrefix: "bb-"},
			want: []string{"bb-1"},
		},
		{
			name: "sorted by id",
			rows: []Row{epEpic("z"), epKid("z.1", "z"), epEpic("a"), epKid("a.1", "a"), epEpic("m"), epKid("m.1", "m")},
			want: []string{"a", "m", "z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every open or in_progress row is listed as ready (except notReady), so
			// the candidate rule has to reject claimed and in_progress epics itself.
			var readyIDs []string
			for _, r := range tc.rows {
				if (r.Status == StatusOpen || r.Status == StatusInProgress) && !contains(tc.notReady, r.ID) {
					readyIDs = append(readyIDs, r.ID)
				}
			}
			res := trRun(t, tc.rows, epReady(tc.rows, tc.templates, readyIDs...), tc.opts)
			trEq(t, "EpicCandidates", res.EpicCandidates, tc.want)
			if res.Counts.EpicCandidates != len(tc.want) {
				t.Errorf("Counts.EpicCandidates = %d, want %d", res.Counts.EpicCandidates, len(tc.want))
			}
			if got := len(res.EpicMarkerSkip) + len(res.EpicReview); got != len(tc.want) {
				t.Errorf("marker-skip + review = %d, want %d", got, len(tc.want))
			}
			for _, id := range res.EpicCandidates {
				if contains(res.Targets, id) {
					t.Errorf("epic candidate %s is also a target", id)
				}
			}
		})
	}
}

func TestEpicCandidatesLeaveTargetPartitionUnchanged(t *testing.T) {
	base := []Row{
		trMk("r"),
		trMk("t1", trStatus(StatusBlocked), trBlockedBy("r")),
		trMk("d", trStatus(StatusDeferred), trDeferUntil("2099-01-01T00:00:00Z")),
		trMk("h", trLabel("human")),
	}
	withEpics := append(append([]Row(nil), base...),
		epEpic("e1"), epKid("e1.1", "e1"), epEpic("e2"), epKid("e2.1", "e2"))
	plain := trRun(t, base, trRdy(base, "r", "h"), TriageOptions{})
	got := trRun(t, withEpics, epReady(withEpics, nil, "r", "h", "e1", "e2"), TriageOptions{})
	trEq(t, "epic candidates", got.EpicCandidates, []string{"e1", "e2"})
	for name, pair := range map[string][2][]string{
		"Targets":    {plain.Targets, got.Targets},
		"Live":       {plain.Live, got.Live},
		"MarkerSkip": {plain.MarkerSkip, got.MarkerSkip},
		"Review":     {plain.Review, got.Review},
		"Drainable":  {plain.Drainable, got.Drainable},
	} {
		trEq(t, name, pair[1], pair[0])
	}
	if plain.Arithmetic() != got.Arithmetic() {
		t.Errorf("partition line changed: %q -> %q", plain.Arithmetic(), got.Arithmetic())
	}
	if err := got.CheckPartition(); err != nil {
		t.Fatal(err)
	}
}

func TestEpicMarkerSkip(t *testing.T) {
	marked := func(opts ...trOpt) []Row {
		e := epEpic("e1", append([]trOpt{trNotes(trMarker(markerTS, "on-change")), trUpdated(markerTS)}, opts...)...)
		return []Row{e, epKid("e1.1", "e1")}
	}
	t.Run("valid quiet marker skips", func(t *testing.T) {
		rows := marked()
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "skip", res.EpicMarkerSkip, []string{"e1"})
		trEq(t, "review", res.EpicReview, nil)
		trEq(t, "candidates", res.EpicCandidates, []string{"e1"})
		if res.Counts.EpicCandidates != 1 || res.Counts.EpicMarkerSkip != 1 {
			t.Errorf("counts = %+v", res.Counts)
		}
	})
	t.Run("--full ignores markers", func(t *testing.T) {
		rows := marked()
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{Full: true})
		trEq(t, "skip", res.EpicMarkerSkip, nil)
		trEq(t, "review", res.EpicReview, []string{"e1"})
	})
	t.Run("a child closed after the marker invalidates it", func(t *testing.T) {
		rows := marked()
		rows[1] = trMk("e1.1", trChildOf("e1"), trClosedAt("2026-10-09T11:00:00Z"))
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "review", res.EpicReview, []string{"e1"})
	})
	t.Run("a new child changes the neighbourhood", func(t *testing.T) {
		rows := append(marked(), epKid("e1.2", "e1"))
		rows[2] = trMk("e1.2", trChildOf("e1"), trClosedAt("2026-10-09T12:00:00Z"))
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "review", res.EpicReview, []string{"e1"})
	})
	t.Run("epic edited after the marker", func(t *testing.T) {
		rows := marked(trUpdated("2026-10-09T12:00:00Z"))
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "review", res.EpicReview, []string{"e1"})
	})
	t.Run("recheck date due", func(t *testing.T) {
		rows := marked(trNotes(trMarker(markerTS, "2026-10-10")))
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "review", res.EpicReview, []string{"e1"})
	})
	t.Run("a needs-children marker also skips while valid", func(t *testing.T) {
		rows := marked(trNotes("[unstick " + markerTS + "] needs-children: scope lists more work; recheck-when: on-change"))
		res := trRun(t, rows, epReady(rows, nil, "e1"), TriageOptions{})
		trEq(t, "skip", res.EpicMarkerSkip, []string{"e1"})
	})
}

func TestPackEpics(t *testing.T) {
	ids := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, "e"+strings.Repeat("x", i/26)+string(rune('a'+i%26)))
		}
		return out
	}
	for n, want := range map[int][]int{
		0: nil, 1: {1}, 2: {2}, 3: {3}, 5: {5}, 6: {3, 3}, 7: {4, 3}, 10: {5, 5}, 11: {4, 4, 3}, 16: {4, 4, 4, 4}, 21: {5, 4, 4, 4, 4},
	} {
		got := PackEpics(ids(n))
		var sizes []int
		seen := map[string]bool{}
		for _, b := range got {
			sizes = append(sizes, len(b))
			if len(b) > EpicMaxBatch || (n >= EpicMinBatch && len(b) < EpicMinBatch) {
				t.Errorf("n=%d: batch of %d outside %d..%d", n, len(b), EpicMinBatch, EpicMaxBatch)
			}
			for _, id := range b {
				seen[id] = true
			}
		}
		if !reflect.DeepEqual(sizes, want) {
			t.Errorf("n=%d sizes = %v, want %v", n, sizes, want)
		}
		if len(seen) != n {
			t.Errorf("n=%d: %d distinct ids batched", n, len(seen))
		}
	}
	// Input order does not matter; the input is not modified; batches do not alias.
	in := []string{"c", "a", "b", "e", "d", "f"}
	cp := append([]string(nil), in...)
	got := PackEpics(in)
	if !reflect.DeepEqual(got, [][]string{{"a", "b", "c"}, {"d", "e", "f"}}) || !reflect.DeepEqual(in, cp) {
		t.Errorf("got %v, input now %v", got, in)
	}
	got[0] = append(got[0], "zz")
	if got[1][0] != "d" {
		t.Error("batches alias each other")
	}
}

func TestEpicBatchName(t *testing.T) {
	if EpicBatchName(0) != "E01" || EpicBatchName(11) != "E12" {
		t.Errorf("names %q %q", EpicBatchName(0), EpicBatchName(11))
	}
}

func TestWriteEpicBatchesWritesFilesAndFacts(t *testing.T) {
	rows := []Row{epEpic("e1"), epKid("e1.1", "e1"), epEpic("e2"), epKid("e2.1", "e2")}
	g := NewGraph(rows)
	w := Workdir{Path: t.TempDir()}
	for _, d := range []string{BatchesDir, FactsDir} {
		if err := os.Mkdir(w.Join(d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	names, err := WriteEpicBatches(w, [][]string{{"e1", "e2"}}, g)
	if err != nil || !reflect.DeepEqual(names, []string{"E01"}) {
		t.Fatalf("names %v err %v", names, err)
	}
	b, _ := os.ReadFile(filepath.Join(w.Path, BatchesDir, "E01"))
	if string(b) != "e1\ne2\n" {
		t.Errorf("batch file = %q", b)
	}
	facts, _ := os.ReadFile(filepath.Join(w.Path, FactsDir, "E01.json"))
	if !strings.Contains(string(facts), `"close_reason"`) || !strings.Contains(string(facts), `"e1.1"`) {
		t.Errorf("facts must carry the children with close_reason:\n%s", facts)
	}
	if _, err := WriteEpicBatches(w, [][]string{{"nope"}}, g); err == nil {
		t.Error("an epic absent from the export must be an error")
	}
}
