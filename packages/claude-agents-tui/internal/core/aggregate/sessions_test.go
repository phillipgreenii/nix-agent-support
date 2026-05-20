package aggregate

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/session"
)

func TestTree_SessionsFlattensAcrossDirs(t *testing.T) {
	tree := &Tree{
		Dirs: []*Directory{
			{
				Sessions: []*SessionView{
					{Session: &session.Session{SessionID: "a"}},
					{Session: &session.Session{SessionID: "b"}},
				},
			},
			{
				Sessions: []*SessionView{
					{Session: &session.Session{SessionID: "c"}},
				},
			},
		},
	}
	got := tree.Sessions()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"a", "b", "c"}
	for i, v := range got {
		if v.SessionID != want[i] {
			t.Errorf("Sessions()[%d].SessionID = %q, want %q", i, v.SessionID, want[i])
		}
	}
}

func TestTree_SessionsNilTreeIsSafe(t *testing.T) {
	var tree *Tree
	if got := tree.Sessions(); got != nil {
		t.Errorf("nil tree should return nil, got %+v", got)
	}
}

func TestTree_SessionsEmptyDirsReturnsEmpty(t *testing.T) {
	tree := &Tree{}
	got := tree.Sessions()
	if len(got) != 0 {
		t.Errorf("empty tree should return empty, got %d", len(got))
	}
}
