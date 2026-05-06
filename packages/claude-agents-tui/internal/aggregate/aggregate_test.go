package aggregate

import (
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestBuildGroupsByCwdAndTotalsTokens(t *testing.T) {
	now := time.Now()
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p1", Status: session.Working, TranscriptMTime: now.Add(-5 * time.Second)},
		{SessionID: "b", Cwd: "/p1", Status: session.Idle, TranscriptMTime: now.Add(-1 * time.Minute)},
		{SessionID: "c", Cwd: "/p2", Status: session.Working, TranscriptMTime: now.Add(-2 * time.Second)},
	}
	enriched := map[string]SessionEnrichment{
		"a": {ContextTokens: 1000, SessionTokens: 10_000},
		"b": {ContextTokens: 500, SessionTokens: 5_000},
		"c": {ContextTokens: 2000, SessionTokens: 20_000},
	}
	block := &ccusage.Block{CostUSD: 10.0, BurnRate: ccusage.BurnRate{TokensPerMinute: 100_000}, Projection: ccusage.Projection{RemainingMinutes: 100}}
	tree := Build(sessions, enriched, nil, block, "max_5x")
	if len(tree.Dirs) != 2 {
		t.Fatalf("want 2 dirs, got %d", len(tree.Dirs))
	}
	byPath := map[string]*Directory{}
	for _, d := range tree.Dirs {
		byPath[d.Path] = d
	}
	if byPath["/p1"].TotalTokens != 15_000 {
		t.Errorf("/p1 tokens = %d, want 15000", byPath["/p1"].TotalTokens)
	}
	if byPath["/p1"].WorkingN != 1 || byPath["/p1"].IdleN != 1 {
		t.Errorf("/p1 counts wrong: %+v", byPath["/p1"])
	}
}

func TestTopupOnlyDisplayedWhenCapReached(t *testing.T) {
	tree := &Tree{
		PlanCapUSD:  90,
		ActiveBlock: &ccusage.Block{CostUSD: 50},
	}
	if tree.TopupShouldDisplay() {
		t.Error("topup should not display when block under cap")
	}
	tree.ActiveBlock.CostUSD = 95
	if !tree.TopupShouldDisplay() {
		t.Error("topup should display when block at/over cap")
	}
}

func TestBuildSessionsSortedByStartedAtDesc(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessions := []*session.Session{
		{SessionID: "old", Cwd: "/p", StartedAt: base.Add(1 * time.Minute)},
		{SessionID: "mid", Cwd: "/p", StartedAt: base.Add(3 * time.Minute)},
		{SessionID: "new", Cwd: "/p", StartedAt: base.Add(5 * time.Minute)},
	}
	enriched := map[string]SessionEnrichment{
		"old": {SessionTokens: 10},
		"mid": {SessionTokens: 20},
		"new": {SessionTokens: 30},
	}
	tree := Build(sessions, enriched, nil, nil, "max_5x")
	if len(tree.Dirs) != 1 {
		t.Fatalf("want 1 dir, got %d", len(tree.Dirs))
	}
	got := tree.Dirs[0].Sessions
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(got))
	}
	want := []string{"new", "mid", "old"}
	for i, s := range got {
		if s.SessionID != want[i] {
			t.Errorf("sessions[%d].SessionID = %q, want %q", i, s.SessionID, want[i])
		}
	}
}

func TestBuildWindowResetsAtTakesLatest(t *testing.T) {
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Minute)
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p"},
		{SessionID: "b", Cwd: "/p"},
		{SessionID: "c", Cwd: "/p"},
	}
	enriched := map[string]SessionEnrichment{
		"a": {RateLimitResetsAt: t1},
		"b": {RateLimitResetsAt: t2},
		"c": {},
	}
	tree := Build(sessions, enriched, nil, nil, "max_5x")
	if !tree.WindowResetsAt.Equal(t2) {
		t.Errorf("WindowResetsAt = %v, want %v", tree.WindowResetsAt, t2)
	}
}

func TestBuildWindowResetsAtZeroWhenNoSessions(t *testing.T) {
	tree := Build(nil, nil, nil, nil, "max_5x")
	if !tree.WindowResetsAt.IsZero() {
		t.Errorf("WindowResetsAt = %v, want zero", tree.WindowResetsAt)
	}
}

func TestBuildSetsPRInfo(t *testing.T) {
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p1"},
		{SessionID: "b", Cwd: "/p2"},
	}
	enriched := map[string]SessionEnrichment{}
	prByDir := map[string]*session.PRInfo{
		"/p1": {Number: 42, Title: "My PR", URL: "https://gh/42"},
	}
	tree := Build(sessions, enriched, prByDir, nil, "max_5x")
	byPath := map[string]*Directory{}
	for _, d := range tree.Dirs {
		byPath[d.Path] = d
	}
	if byPath["/p1"].PRInfo == nil {
		t.Fatal("/p1 should have PRInfo set")
	}
	if byPath["/p1"].PRInfo.Number != 42 {
		t.Errorf("/p1 PRInfo.Number = %d, want 42", byPath["/p1"].PRInfo.Number)
	}
	if byPath["/p2"].PRInfo != nil {
		t.Error("/p2 should have nil PRInfo (not in prByDir)")
	}
}
