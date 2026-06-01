package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestWriteService_SerializesWrites(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	ws := NewWriteService(WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)

	now := time.Now().UTC()

	// Fire 20 concurrent UpsertSession calls; expect all to land.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ws.UpsertSession(ctx, store.Session{
				SessionID:       string(rune('a' + i)),
				LastProcessedAt: now,
				UpdatedAt:       now,
				CreatedAt:       now,
			})
		}(i)
	}
	wg.Wait()
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	ids, err := sqlite.NewSessionStore(db).AllSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 20 {
		t.Errorf("got %d session rows, want 20", len(ids))
	}
}
