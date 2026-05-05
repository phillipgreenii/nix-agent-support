package render

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestFlattenRowsEmptyTree(t *testing.T) {
	rows := FlattenRows(&aggregate.Tree{}, TreeOpts{})
	if len(rows) != 0 {
		t.Errorf("expected empty rows, got %d", len(rows))
	}
}

func TestFlattenRowsSkipsEmptyDirs(t *testing.T) {
	empty := &aggregate.Directory{Path: "/empty"}
	active := &aggregate.Directory{
		Path: "/active",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{empty, active}}
	rows := FlattenRows(tree, TreeOpts{})
	for _, r := range rows {
		if r.DirIdx == 0 {
			t.Error("empty dir should not appear in rows")
		}
	}
}

func TestFlattenRowsStructure(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{})
	// Expected: DirHeaderKind, SessionKind, SessionKind, BlankKind
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d: %v", len(rows), rows)
	}
	if rows[0].Kind != DirHeaderKind {
		t.Errorf("rows[0] should be DirHeaderKind, got %v", rows[0].Kind)
	}
	if rows[1].Kind != SessionKind || rows[1].SessIdx != 0 || rows[1].FlatIdx != 0 {
		t.Errorf("rows[1]: want SessionKind/SessIdx=0/FlatIdx=0, got %+v", rows[1])
	}
	if rows[2].Kind != SessionKind || rows[2].SessIdx != 1 || rows[2].FlatIdx != 1 {
		t.Errorf("rows[2]: want SessionKind/SessIdx=1/FlatIdx=1, got %+v", rows[2])
	}
	if rows[3].Kind != BlankKind {
		t.Errorf("rows[3] should be BlankKind, got %v", rows[3].Kind)
	}
}

func TestFlattenRowsLineCountWithPrompt(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{
				Session:           &session.Session{SessionID: "a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{FirstPrompt: "do the thing"},
			},
			{
				Session: &session.Session{SessionID: "b", Status: session.Working},
			},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{})
	if rows[1].LineCount != 2 {
		t.Errorf("session with FirstPrompt: want LineCount=2, got %d", rows[1].LineCount)
	}
	if rows[2].LineCount != 1 {
		t.Errorf("session without FirstPrompt: want LineCount=1, got %d", rows[2].LineCount)
	}
}

func TestFlattenRowsDormantFiltered(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Dormant}},
		},
		WorkingN: 1,
		DormantN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{ShowAll: false})
	n := 0
	for _, r := range rows {
		if r.Kind == SessionKind {
			n++
		}
	}
	if n != 1 {
		t.Errorf("dormant filtered: want 1 session row, got %d", n)
	}
}

func TestFlattenRowsFlatIdxSpansMultipleDirs(t *testing.T) {
	d1 := &aggregate.Directory{
		Path: "/p1",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	d2 := &aggregate.Directory{
		Path: "/p2",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "c", Status: session.Working}},
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d1, d2}}
	rows := FlattenRows(tree, TreeOpts{})
	var got []int
	for _, r := range rows {
		if r.Kind == SessionKind {
			got = append(got, r.FlatIdx)
		}
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("FlatIdx should be 0,1,2 across dirs; got %v", got)
	}
}

func TestFlattenRowsDirIdxCorrect(t *testing.T) {
	d0 := &aggregate.Directory{
		Path:     "/p0",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "x", Status: session.Working}}},
	}
	d1 := &aggregate.Directory{
		Path:     "/p1",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "y", Status: session.Working}}},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d0, d1}}
	rows := FlattenRows(tree, TreeOpts{})
	found := false
	for _, r := range rows {
		if r.Kind == SessionKind && r.DirIdx == 1 {
			found = true
		}
	}
	if !found {
		t.Error("expected a SessionKind row with DirIdx=1")
	}
}
