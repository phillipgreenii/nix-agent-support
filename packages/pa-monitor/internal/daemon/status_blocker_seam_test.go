package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// statusBucketCounts is the {working, blocked, idle} rollup plus the
// blocked-by-blocker breakdown — the counts every bucketer must agree on.
type statusBucketCounts struct {
	working, blocked, idle                     int
	humanInput, humanAuthn, usageLimit, errBlk int
}

// TestStatusBlockerSurfacesAgree is the ADR 0024 R12 test seam: derive
// status+blocker ONCE per session, then assert the live-tree bucketer
// (aggregate.Build → aggregate.Directory) and the DB-materialised bucketer
// (sqlite persist → service.BuildDirectories) produce identical buckets. Both
// derive from the same persisted/emitted status+blocker value, so if they ever
// disagree the "surfaces agree" invariant is broken.
func TestStatusBlockerSurfacesAgree(t *testing.T) {
	now := time.Now().UTC()

	// One session per (status, blocker) the model can produce.
	type spec struct {
		id      string
		status  session.Status
		blocker session.Blocker
	}
	specs := []spec{
		{"w1", session.Working, session.NoBlocker},
		{"w2", session.Working, session.NoBlocker},
		{"i1", session.Idle, session.NoBlocker},
		{"bhi", session.Blocked, session.HumanInput},
		{"bha", session.Blocked, session.HumanAuthn},
		{"bul", session.Blocked, session.UsageLimit},
		{"berr", session.Blocked, session.ErrorBlocker},
	}

	// --- Live path: aggregate.Build over the derived session values. ---
	var sessions []*session.Session
	enriched := map[string]aggregate.SessionEnrichment{}
	for _, s := range specs {
		sessions = append(sessions, &session.Session{
			SessionID: s.id, Cwd: "/repo", Status: s.status, Blocker: s.blocker,
		})
		enriched[s.id] = aggregate.SessionEnrichment{}
	}
	liveTree := aggregate.Build(sessions, enriched, nil, nil, 0)
	live := sumAggregate(liveTree)

	// --- DB path: persist the SAME derived values (as strings) and read back
	// through BuildDirectories. ---
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
	for _, s := range specs {
		if err := ss.Upsert(context.Background(), store.Session{
			SessionID:       s.id,
			PID:             &pid, // non-nil so FilterAll returns the row
			Cwd:             "/repo",
			Status:          s.status.String(),
			Blocker:         s.blocker.String(),
			LastProcessedAt: now,
			UpdatedAt:       now,
			CreatedAt:       now,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", s.id, err)
		}
	}
	rs := service.NewReadService(service.ReadDeps{
		Sessions: ss,
		Blocks:   sqlite.NewBlockStore(db),
		Weeks:    sqlite.NewWeekStore(db),
		Toggles:  sqlite.NewToggleStore(db),
		Nudges:   sqlite.NewNudgeStore(db),
	})
	st, err := rs.GetState(context.Background(), store.FilterAll)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	dbCounts := sumService(st.Dirs)

	if live != dbCounts {
		t.Fatalf("bucketers disagree:\n live = %+v\n db   = %+v", live, dbCounts)
	}
	// Sanity: the derivation actually populated every bucket we care about.
	want := statusBucketCounts{working: 2, blocked: 4, idle: 1, humanInput: 1, humanAuthn: 1, usageLimit: 1, errBlk: 1}
	if live != want {
		t.Errorf("buckets = %+v, want %+v", live, want)
	}
}

func sumAggregate(tree *aggregate.Tree) statusBucketCounts {
	var c statusBucketCounts
	for _, d := range tree.Dirs {
		c.working += d.WorkingN
		c.blocked += d.BlockedN
		c.idle += d.IdleN
		c.humanInput += d.BlockedHumanInputN
		c.humanAuthn += d.BlockedHumanAuthnN
		c.usageLimit += d.BlockedUsageLimitN
		c.errBlk += d.BlockedErrorN
	}
	return c
}

func sumService(dirs []*service.Directory) statusBucketCounts {
	var c statusBucketCounts
	for _, d := range dirs {
		c.working += d.WorkingN
		c.blocked += d.BlockedN
		c.idle += d.IdleN
		c.humanInput += d.BlockedHumanInputN
		c.humanAuthn += d.BlockedHumanAuthnN
		c.usageLimit += d.BlockedUsageLimitN
		c.errBlk += d.BlockedErrorN
	}
	return c
}
