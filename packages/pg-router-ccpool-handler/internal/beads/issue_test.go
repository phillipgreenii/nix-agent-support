package beads

import (
	"context"
	"testing"
	"time"
)

func TestShowObj_object(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"open","parent":"zr-pr","metadata":{"author":"phillipg"}}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-1" || iss.Status != "open" || iss.Parent != "zr-pr" {
		t.Errorf("got %+v", iss)
	}
	if iss.Metadata["author"] != "phillipg" {
		t.Errorf("author = %v", iss.Metadata["author"])
	}
}

func TestShowObj_array(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-2","status":"closed"}]`}
	iss, err := ShowObj(context.Background(), fr, "zr-2")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-2" || iss.Status != "closed" {
		t.Errorf("got %+v", iss)
	}
}

func TestStatus(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"in_progress"}`}
	s, err := Status(context.Background(), fr, "zr-1")
	if err != nil || s != "in_progress" {
		t.Fatalf("status=%q err=%v", s, err)
	}
}

func TestReady_emptyAndArray(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-1","issue_type":"task","title":"process-feedback: x"}]`}
	got, err := Ready(context.Background(), fr, "--label", "worker-ready")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "zr-1" {
		t.Fatalf("got %+v", got)
	}
	// argv assertion: Ready appends --json --limit 0
	last := fr.args[len(fr.args)-1]
	wantTail := []string{"ready", "--label", "worker-ready", "--json", "--limit", "0"}
	if joinArgs(last) != joinArgs(wantTail) {
		t.Errorf("argv = %v, want %v", last, wantTail)
	}
}

func TestReady_handlesNonArray(t *testing.T) {
	fr := &fakeRunner{out: `null`}
	got, err := Ready(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("null should yield empty slice, got %v", got)
	}
}

// TestReady_handlesDataEnvelope locks in the bd-output contract: bd >=1.0.x wraps
// every `--json` payload in a `{"data":[...],"schema_version":N}` envelope rather
// than a bare top-level array. Ready must peel that envelope, otherwise the
// worker/feedback discovery queries silently see zero ready beads and every drain
// dispatches nothing (pg2-ygbt). The fixture is the real shape emitted by bd.
func TestReady_handlesDataEnvelope(t *testing.T) {
	fr := &fakeRunner{out: `{"data":[{"id":"zr-1","issue_type":"task","labels":["worker-ready"]},{"id":"zr-2","issue_type":"bug"}],"schema_version":1}`}
	got, err := Ready(context.Background(), fr, "--label", "worker-ready")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "zr-1" || got[1].ID != "zr-2" {
		t.Fatalf("data-envelope should yield both issues, got %+v", got)
	}
	if !got[0].HasLabel("worker-ready") {
		t.Errorf("labels lost through envelope decode; got %v", got[0].Labels)
	}
}

// TestReady_handlesEmptyDataEnvelope: an envelope with an empty data array is the
// no-ready-work case and must yield zero issues without error.
func TestReady_handlesEmptyDataEnvelope(t *testing.T) {
	fr := &fakeRunner{out: `{"data":[],"schema_version":1}`}
	got, err := Ready(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty data envelope should yield empty slice, got %v", got)
	}
}

// TestShowObj_dataEnvelope: `bd show <id> --json` is also wrapped in the
// `{"data":[<issue>]}` envelope. ShowObj must recover a populated Issue, not the
// empty Issue that an envelope-blind decoder produces (pg2-ygbt).
func TestShowObj_dataEnvelope(t *testing.T) {
	fr := &fakeRunner{out: `{"data":[{"id":"zr-9","status":"open","labels":["worker-ready","human"]}],"schema_version":1}`}
	iss, err := ShowObj(context.Background(), fr, "zr-9")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-9" || iss.Status != "open" {
		t.Fatalf("envelope show yielded empty/wrong issue: %+v", iss)
	}
	if !iss.HasLabel("worker-ready") || !iss.HasLabel("human") {
		t.Errorf("labels lost through envelope decode; got %v", iss.Labels)
	}
}

func TestUnclaim_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Unclaim(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--status=open", "--assignee="}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestAddHuman_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := AddHuman(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--add-label", "human"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestShowObj_parsesLabels(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"open","labels":["worker-ready","pool-launch-fail"]}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if !iss.HasLabel("pool-launch-fail") {
		t.Errorf("HasLabel(pool-launch-fail) = false; labels=%v", iss.Labels)
	}
	if iss.HasLabel("human") {
		t.Errorf("HasLabel(human) should be false; labels=%v", iss.Labels)
	}
}

func TestAddLabel_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := AddLabel(context.Background(), fr, "zr-1", "pool-launch-fail"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--add-label", "pool-launch-fail"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestRemoveLabel_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := RemoveLabel(context.Background(), fr, "zr-1", "pool-launch-fail"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--remove-label", "pool-launch-fail"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestHasLabel_fromShow(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","labels":["pool-launch-fail"]}`}
	got, err := HasLabel(context.Background(), fr, "zr-1", "pool-launch-fail")
	if err != nil || !got {
		t.Fatalf("HasLabel = %v, err=%v; want true,nil", got, err)
	}
	last := fr.args[len(fr.args)-1]
	want := []string{"show", "zr-1", "--json"}
	if joinArgs(last) != joinArgs(want) {
		t.Errorf("HasLabel must read via show --json; argv=%v", last)
	}
}

func TestList_parsesCreatedByAndArgv(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-1","created_by":"pgii-pool__worker"},{"id":"zr-2","created_by":"pg-pr daemon"}]`}
	got, err := List(context.Background(), fr, "--all")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "zr-1" || got[0].CreatedBy != "pgii-pool__worker" {
		t.Fatalf("got %+v", got)
	}
	// argv assertion: List appends --json --limit 0 after caller args
	last := fr.args[len(fr.args)-1]
	want := []string{"list", "--all", "--json", "--limit", "0"}
	if joinArgs(last) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", last, want)
	}
}

func joinArgs(a []string) string {
	s := ""
	for _, x := range a {
		s += "\x00" + x
	}
	return s
}

func TestComment_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Comment(context.Background(), fr, "zr-1", "interrupted — budget"); err != nil {
		t.Fatal(err)
	}
	want := []string{"comment", "zr-1", "interrupted — budget"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestIsID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"pg2-abc12", true},
		{"zr-r", true},
		{"pg2-abc12.3", true},
		{"ZR-Private/ziprecruiter#120058", false},
		{"2026-10-03T06:55:00Z", false},
		{"", false},
		{"-flag", false},
		{"has space", false},
	}
	for _, tc := range tests {
		if got := IsID(tc.id); got != tc.want {
			t.Errorf("IsID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// bd's `show --json` carries the claiming actor as `assignee`; the orphan
// reconcile compares it to the role's own actor before unclaiming a bead
// (INV-CCH-18).
func TestShowObj_parsesAssignee(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"in_progress","assignee":"pgii-pool__worker"}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.Assignee != "pgii-pool__worker" {
		t.Errorf("Assignee = %q", iss.Assignee)
	}
	fr = &fakeRunner{out: `{"id":"zr-1","status":"open"}`}
	if iss, _ = ShowObj(context.Background(), fr, "zr-1"); iss.Assignee != "" {
		t.Errorf("absent assignee must decode empty, got %q", iss.Assignee)
	}
}

// showDeps is `bd show --json`'s dependencies shape: full issues, each with its
// dependency_type (the parent edge is not a blocker).
const showDeps = `{"id":"zr-1","status":"open","defer_until":"2026-10-09T12:00:00Z","dependencies":[` +
	`{"id":"zr-p","title":"epic","status":"open","issue_type":"epic","dependency_type":"parent-child","labels":["x"]},` +
	`{"id":"zr-b","title":"blocker","status":"open","issue_type":"task","dependency_type":"blocks","metadata":{"k":"v"}}]}`

// readyDeps is `bd ready`/`bd list --json`'s dependencies shape: edge records
// whose metadata is a STRING. Declaring a typed or map metadata on Dependency
// would make decodeMany return nil for every list read.
const readyDeps = `[{"id":"zr-1","status":"open","defer_until":null,"dependencies":[` +
	`{"issue_id":"zr-1","depends_on_id":"zr-b","type":"blocks","created_at":"2026-10-08T00:00:00Z","created_by":"x","metadata":"{}"}]}]`

func TestShowObj_parsesDependenciesAndDeferUntil(t *testing.T) {
	iss, err := ShowObj(context.Background(), &fakeRunner{out: showDeps}, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(iss.Dependencies) != 2 || iss.Dependencies[1] != (Dependency{ID: "zr-b", Status: "open", DependencyType: "blocks"}) {
		t.Fatalf("Dependencies = %+v", iss.Dependencies)
	}
	if !iss.HasOpenBlocker() {
		t.Error("an open blocks dependency must count as a blocker")
	}
	if iss.DeferUntil != "2026-10-09T12:00:00Z" {
		t.Errorf("DeferUntil = %q", iss.DeferUntil)
	}
}

// TestReady_decodesEdgeRecordDependencies is the regression for the shared
// Issue type: the edge-record shape must still decode, so a source query that
// reads bd ready sees its beads (decodeMany returns nil on any unmarshal error).
func TestReady_decodesEdgeRecordDependencies(t *testing.T) {
	got, err := Ready(context.Background(), &fakeRunner{out: readyDeps})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "zr-1" {
		t.Fatalf("an edge-record dependencies array made the whole read decode empty: %+v", got)
	}
	if got[0].HasOpenBlocker() {
		t.Error("an edge record carries no show-shaped fields and must never read as a blocker")
	}
	if got[0].DeferUntil != "" {
		t.Errorf("a null defer_until must decode empty, got %q", got[0].DeferUntil)
	}
	list, err := List(context.Background(), &fakeRunner{out: readyDeps})
	if err != nil || len(list) != 1 {
		t.Fatalf("List must decode the edge-record shape too: %+v, %v", list, err)
	}
}

func TestHasOpenBlocker(t *testing.T) {
	cases := []struct {
		name string
		deps []Dependency
		want bool
	}{
		{"none", nil, false},
		{"parent only", []Dependency{{ID: "p", Status: "open", DependencyType: "parent-child"}}, false},
		{"closed blocker", []Dependency{{ID: "b", Status: "closed", DependencyType: "blocks"}}, false},
		{"open blocker", []Dependency{{ID: "b", Status: "open", DependencyType: "blocks"}}, true},
		{"in_progress blocker", []Dependency{{ID: "b", Status: "in_progress", DependencyType: "blocks"}}, true},
		{"edge record shape (no id)", []Dependency{{}}, false},
	}
	for _, tc := range cases {
		if got := (Issue{Dependencies: tc.deps}).HasOpenBlocker(); got != tc.want {
			t.Errorf("%s: HasOpenBlocker = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeferredUntilAfter(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"absent", "", false},
		{"future", "2026-10-08T12:00:01Z", true},
		{"future with offset", "2026-10-08T09:00:00-07:00", true},
		{"equal to now", "2026-10-08T12:00:00Z", false},
		{"past", "2026-10-07T00:00:00Z", false},
		{"unparseable fails open", "next tuesday", false},
		{"date only fails open", "2026-10-09", false},
	}
	for _, tc := range cases {
		if got := (Issue{DeferUntil: tc.value}).DeferredUntilAfter(now); got != tc.want {
			t.Errorf("%s: DeferredUntilAfter(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}
}
