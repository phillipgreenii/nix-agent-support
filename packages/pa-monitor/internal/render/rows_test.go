package render

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/treestate"
)

func TestFlattenPathTreeEmpty(t *testing.T) {
	state := treestate.NewState()
	rows := FlattenPathTree(nil, state, TreeOpts{})
	if len(rows) != 0 {
		t.Errorf("want empty rows, got %d", len(rows))
	}
}

func TestFlattenPathTreeSingleNodeExpanded(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	// Expected: PathNodeKind, SessionKind, BlankKind
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != PathNodeKind {
		t.Errorf("rows[0] should be PathNodeKind, got %v", rows[0].Kind)
	}
	if rows[0].NodePath != "/p" {
		t.Errorf("rows[0].NodePath: want /p, got %s", rows[0].NodePath)
	}
	if rows[1].Kind != SessionKind {
		t.Errorf("rows[1] should be SessionKind, got %v", rows[1].Kind)
	}
	if rows[1].Session == nil {
		t.Error("rows[1].Session should not be nil")
	}
	if rows[2].Kind != BlankKind {
		t.Errorf("rows[2] should be BlankKind, got %v", rows[2].Kind)
	}
}

func TestFlattenPathTreeCollapsedSkipsContents(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	state.Toggle("/p")
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	// Expected: PathNodeKind, BlankKind (sessions skipped)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (collapsed), got %d: %+v", len(rows), rows)
	}
	if !rows[0].Collapsed {
		t.Error("collapsed node row should have Collapsed=true")
	}
	if rows[1].Kind != BlankKind {
		t.Errorf("rows[1] should be BlankKind, got %v", rows[1].Kind)
	}
}

func TestFlattenPathTreeIsLastInGroup(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	var sessRows []Row
	for _, r := range rows {
		if r.Kind == SessionKind {
			sessRows = append(sessRows, r)
		}
	}
	if len(sessRows) != 2 {
		t.Fatalf("want 2 session rows, got %d", len(sessRows))
	}
	if sessRows[0].IsLastInGroup {
		t.Error("first session should not be last in group")
	}
	if !sessRows[1].IsLastInGroup {
		t.Error("second session should be last in group")
	}
}

func TestFlattenPathTreeFiltersDormant(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			// Idle + a stale transcript ⇒ long-idle (dormant age refinement).
			{Session: &session.Session{SessionID: "b", Status: session.Idle, TranscriptMTime: time.Now().Add(-time.Hour)}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{ShowAll: false})
	sess := 0
	for _, r := range rows {
		if r.Kind == SessionKind {
			sess++
		}
	}
	if sess != 1 {
		t.Errorf("dormant filtered: want 1 session row, got %d", sess)
	}
}

// A directory whose only sessions are dormant must NOT render a parent node in
// active view (ShowAll=false) — otherwise it shows an empty dir with a toggle
// that does nothing (pg2-...). In show-all it renders normally.
func TestFlattenPathTreeSkipsEmptyParentInActiveView(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Idle, TranscriptMTime: time.Now().Add(-time.Hour)}},
		},
	}
	countNodes := func(showAll bool) int {
		rows := FlattenPathTree([]*aggregate.PathNode{n}, treestate.NewState(), TreeOpts{ShowAll: showAll})
		c := 0
		for _, r := range rows {
			if r.Kind == PathNodeKind {
				c++
			}
		}
		return c
	}
	if got := countNodes(false); got != 0 {
		t.Errorf("active view: empty (all-dormant) parent should be skipped, got %d node rows", got)
	}
	if got := countNodes(true); got != 1 {
		t.Errorf("show-all: parent should render, got %d node rows", got)
	}
}

func TestFlattenPathTreeFlatIdxSpansNodes(t *testing.T) {
	n1 := &aggregate.PathNode{
		FullPath:    "/p1",
		DisplayPath: "/p1",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	n2 := &aggregate.PathNode{
		FullPath:    "/p2",
		DisplayPath: "/p2",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n1, n2}, state, TreeOpts{})
	var flatIdxs []int
	for _, r := range rows {
		if r.Kind == SessionKind {
			flatIdxs = append(flatIdxs, r.FlatIdx)
		}
	}
	if len(flatIdxs) != 2 || flatIdxs[0] != 0 || flatIdxs[1] != 1 {
		t.Errorf("FlatIdx should be 0,1 across nodes; got %v", flatIdxs)
	}
}
