package service

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
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
	defer func() { _ = db.Close() }()
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
			_ = ws.UpsertSession(ctx, store.Session{
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

// goroutineRecordingSessionStore is a test-only SessionStore that records
// the goroutine ID of every Upsert call so we can verify serialisation.
type goroutineRecordingSessionStore struct {
	mu  sync.Mutex
	ids []int64
}

func (g *goroutineRecordingSessionStore) Upsert(_ context.Context, _ store.Session) error {
	g.mu.Lock()
	g.ids = append(g.ids, currentGoroutineID())
	g.mu.Unlock()
	return nil
}

func (g *goroutineRecordingSessionStore) List(_ context.Context, _ store.Filter, _ int64, _ store.FreshnessWindow) ([]store.SessionWithContribution, error) {
	return nil, nil
}

func (g *goroutineRecordingSessionStore) GetByID(_ context.Context, _ string, _ store.FreshnessWindow) (*store.Session, error) {
	return nil, nil
}

func (g *goroutineRecordingSessionStore) MarkDeleted(_ context.Context, _ []string, _ time.Time) error {
	return nil
}

func (g *goroutineRecordingSessionStore) MarkRevived(_ context.Context, _ []string) error {
	return nil
}

func (g *goroutineRecordingSessionStore) HardDelete(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func (g *goroutineRecordingSessionStore) AllSessionIDs(_ context.Context) ([]string, error) {
	return nil, nil
}

func (g *goroutineRecordingSessionStore) MarkEscalated(_ context.Context, _ string) error {
	return nil
}

// currentGoroutineID parses the current goroutine ID from the stack trace.
func currentGoroutineID() int64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	id, _ := strconv.ParseInt(string(b[:i]), 10, 64)
	return id
}

// blockingSessionStore is a test-only SessionStore whose Upsert blocks until
// unblockCh is closed. This lets tests saturate the write queue before
// allowing any op to complete.
type blockingSessionStore struct {
	unblockCh chan struct{}
}

func (b *blockingSessionStore) Upsert(_ context.Context, _ store.Session) error {
	<-b.unblockCh
	return nil
}

func (b *blockingSessionStore) List(_ context.Context, _ store.Filter, _ int64, _ store.FreshnessWindow) ([]store.SessionWithContribution, error) {
	return nil, nil
}

func (b *blockingSessionStore) GetByID(_ context.Context, _ string, _ store.FreshnessWindow) (*store.Session, error) {
	return nil, nil
}

func (b *blockingSessionStore) MarkDeleted(_ context.Context, _ []string, _ time.Time) error {
	return nil
}

func (b *blockingSessionStore) MarkRevived(_ context.Context, _ []string) error {
	return nil
}

func (b *blockingSessionStore) HardDelete(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func (b *blockingSessionStore) AllSessionIDs(_ context.Context) ([]string, error) {
	return nil, nil
}

func (b *blockingSessionStore) MarkEscalated(_ context.Context, _ string) error {
	return nil
}

// TestWriteService_StopUnblocksFullQueueSenders verifies that senders blocked
// on a full queue (or waiting for a result) are unblocked and return when
// Stop() is called — no goroutine hangs.
func TestWriteService_StopUnblocksFullQueueSenders(t *testing.T) {
	unblockCh := make(chan struct{})
	blocker := &blockingSessionStore{unblockCh: unblockCh}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	ws := NewWriteService(WriteDeps{
		Sessions:      blocker,
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ctx := context.Background()
	ws.Start(ctx)

	const n = 100
	now := time.Now().UTC()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = ws.UpsertSession(ctx, store.Session{
				SessionID:       strconv.Itoa(i),
				LastProcessedAt: now,
				UpdatedAt:       now,
				CreatedAt:       now,
			})
		}(i)
	}

	// Give goroutines time to pile up in the queue.
	time.Sleep(20 * time.Millisecond)

	// Stop the service. Unblock the store so the drain can finish any in-flight
	// ops, then stop fires and remaining queued/blocked senders must return.
	close(unblockCh)
	ws.Stop()

	// All 100 callers must return — if any deadlock, this Wait hangs and the
	// test times out.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// pass
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: some UpsertSession callers are still blocked after Stop()")
	}
}

func TestWriteService_OpsRunOnSingleGoroutine(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	recorder := &goroutineRecordingSessionStore{}

	ws := NewWriteService(WriteDeps{
		Sessions:      recorder,
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

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = ws.UpsertSession(ctx, store.Session{
				SessionID:       strconv.Itoa(i),
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

	recorder.mu.Lock()
	ids := recorder.ids
	recorder.mu.Unlock()

	if len(ids) != n {
		t.Fatalf("got %d recorded calls, want %d", len(ids), n)
	}
	first := ids[0]
	for i, id := range ids[1:] {
		if id != first {
			t.Errorf("op %d ran on goroutine %d, want %d (all ops must use the single writer goroutine)", i+1, id, first)
		}
	}
}
