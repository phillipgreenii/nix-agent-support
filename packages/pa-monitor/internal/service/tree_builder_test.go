package service

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestBuildDirectories_GroupsByCwd(t *testing.T) {
	sessions := []store.SessionWithContribution{
		{Session: store.Session{SessionID: "a", Cwd: "/work/x", Status: "working", SessionTokens: 10, CostUSD: 0.5}, BlockCostUSD: 0.5, BlockTokens: 10},
		{Session: store.Session{SessionID: "b", Cwd: "/work/x", Status: "idle", SessionTokens: 5}, BlockTokens: 5},
		{Session: store.Session{SessionID: "c", Cwd: "/work/y", Status: "working", SessionTokens: 20}, BlockTokens: 20},
	}
	dirs := BuildDirectories(sessions)
	if len(dirs) != 2 {
		t.Fatalf("got %d dirs, want 2", len(dirs))
	}
	// Sort or look up by Path.
	byPath := map[string]*Directory{}
	for _, d := range dirs {
		byPath[d.Path] = d
	}
	x := byPath["/work/x"]
	if x.WorkingN != 1 || x.IdleN != 1 {
		t.Errorf("/work/x counts: working=%d idle=%d", x.WorkingN, x.IdleN)
	}
	if x.TotalTokens != 15 {
		t.Errorf("/work/x TotalTokens=%d, want 15", x.TotalTokens)
	}
	y := byPath["/work/y"]
	if y.WorkingN != 1 || y.TotalTokens != 20 {
		t.Errorf("/work/y: working=%d tokens=%d", y.WorkingN, y.TotalTokens)
	}
}
