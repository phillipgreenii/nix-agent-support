package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// fakePRByDir stands in for the poller's published cwd->*PRInfo map (an atomic
// read of the producer-owned DerivedState in production).
type fakePRByDir map[string]*session.PRInfo

func (f fakePRByDir) MonitorPRByDir() map[string]*session.PRInfo { return f }

// TestServedStateCarriesPRInfo is the F1 regression guard. The served
// DaemonState is materialised FROM THE DB (convertStateToAggregateTree), which
// deliberately drops PRInfo — there are no PR columns. snapshot() must
// re-annotate dir.PRInfo by cwd from the live producer map (the same pattern
// pending-nudge uses), so a remote TUI actually sees the PR. This is the test
// that would have caught the F1 showstopper: threading PRInfo through translate
// alone shows nothing, because the served path re-materialises from the DB.
func TestServedStateCarriesPRInfo(t *testing.T) {
	now := time.Now().UTC()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ss := sqlite.NewSessionStore(db)
	pid := 4242
	if err := ss.Upsert(context.Background(), store.Session{
		SessionID:       "s1",
		PID:             &pid, // non-nil so FilterAll returns the row
		Cwd:             "/repo",
		Status:          "working",
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rs := service.NewReadService(service.ReadDeps{
		Sessions: ss,
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})

	st := newSharedState()
	st.setReadService(rs)
	st.setPRByDirSource(fakePRByDir{
		"/repo": &session.PRInfo{Number: 7, Title: "t", State: "OPEN", URL: "https://example.com/pull/7"},
	})

	tree := st.snapshot()
	if tree == nil {
		t.Fatal("snapshot() = nil")
	}
	var found *session.PRInfo
	for _, d := range tree.Dirs {
		if d.Path == "/repo" {
			found = d.PRInfo
		}
	}
	if found == nil {
		t.Fatal("served tree dropped PRInfo for /repo — F1 regression (translate alone shows nothing)")
	}
	if found.Number != 7 || found.State != "OPEN" || found.URL != "https://example.com/pull/7" {
		t.Errorf("PRInfo = %+v, want {Number:7 State:OPEN URL:https://example.com/pull/7}", found)
	}

	// End-to-end: the wire DaemonState the TUI decodes actually carries pr_info.
	ds := pb.FromTree(tree)
	var wirePR *pb.PRInfo
	for _, d := range ds.GetDirs() {
		if d.GetPath() == "/repo" {
			wirePR = d.GetPrInfo()
		}
	}
	if wirePR == nil {
		t.Fatal("wire DaemonState has no pr_info for /repo")
	}
	if wirePR.GetNumber() != 7 || wirePR.GetState() != "OPEN" {
		t.Errorf("wire pr_info = {Number:%d State:%q}, want {7 OPEN}", wirePR.GetNumber(), wirePR.GetState())
	}
}
