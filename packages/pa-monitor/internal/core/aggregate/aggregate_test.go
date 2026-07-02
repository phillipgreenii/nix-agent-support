package aggregate

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
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
	block := &usage.Block{CostUSD: 10.0, BurnRate: usage.BurnRate{TokensPerMinute: 100_000}, Projection: usage.Projection{RemainingMinutes: 100}}
	tree := Build(sessions, enriched, nil, block, 90.0)
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

func TestBuildSetsPlanCapFromArg(t *testing.T) {
	// Build sources the plan cap from its blockCapUSD argument (supplied by the
	// caller from the Account), not from an inline usage.PlanCapUSD lookup.
	tree := Build(nil, nil, nil, nil, 90.0)
	if tree.PlanCapUSD != 90.0 {
		t.Errorf("tree.PlanCapUSD = %v, want 90 (from blockCapUSD arg)", tree.PlanCapUSD)
	}
}

func TestTopupOnlyDisplayedWhenCapReached(t *testing.T) {
	tree := &Tree{
		PlanCapUSD:  90,
		ActiveBlock: &usage.Block{CostUSD: 50},
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
	tree := Build(sessions, enriched, nil, nil, 90.0)
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
	tree := Build(sessions, enriched, nil, nil, 90.0)
	if !tree.WindowResetsAt.Equal(t2) {
		t.Errorf("WindowResetsAt = %v, want %v", tree.WindowResetsAt, t2)
	}
}

func TestBuildWindowResetsAtZeroWhenNoSessions(t *testing.T) {
	tree := Build(nil, nil, nil, nil, 90.0)
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
	tree := Build(sessions, enriched, prByDir, nil, 90.0)
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

func TestAggregateCarriesLastError(t *testing.T) {
	now := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	rec := &transcript.ErrorRecord{
		Kind: transcript.ErrUnknown, Text: "API Error: The socket connection was closed unexpectedly",
		At: now, IsTerminal: true,
	}
	sessions := []*session.Session{
		{SessionID: "sid-1", Cwd: "/tmp/work"},
	}
	enriched := map[string]SessionEnrichment{
		"sid-1": {LastError: rec, LastErrorRetryable: true},
	}
	tree := Build(sessions, enriched, nil, nil, 0)
	if len(tree.Dirs) == 0 || len(tree.Dirs[0].Sessions) == 0 {
		t.Fatal("expected one session in tree")
	}
	got := tree.Dirs[0].Sessions[0].LastError
	if got == nil {
		t.Fatal("LastError = nil, want pointer to rec")
	}
	if got.Kind != transcript.ErrUnknown || !tree.Dirs[0].Sessions[0].LastErrorRetryable {
		t.Errorf("LastError = %+v retryable=%v, want unknown+retryable", got, tree.Dirs[0].Sessions[0].LastErrorRetryable)
	}
}

func TestBuildPopulatesDirectoryBurnRateSum(t *testing.T) {
	sessions := []*session.Session{
		{SessionID: "a", Cwd: "/p1"},
		{SessionID: "b", Cwd: "/p1"},
		{SessionID: "c", Cwd: "/p2"},
	}
	enriched := map[string]SessionEnrichment{
		"a": {BurnRateShort: 100},
		"b": {BurnRateShort: 50},
		"c": {BurnRateShort: 200},
	}
	tree := Build(sessions, enriched, nil, nil, 0)

	byPath := map[string]*Directory{}
	for _, d := range tree.Dirs {
		byPath[d.Path] = d
	}

	if got := byPath["/p1"].BurnRateSum; got != 150 {
		t.Errorf("/p1 BurnRateSum = %.0f, want 150", got)
	}
	if got := byPath["/p2"].BurnRateSum; got != 200 {
		t.Errorf("/p2 BurnRateSum = %.0f, want 200", got)
	}
}
