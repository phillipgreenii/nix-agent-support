package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/core/poller"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// TestTickIntegration_SamplesAndPersistsLimits proves the daemon tick samples the
// wired LimitsSource each tick and persists the reading onto the active block, so
// the store->tree (GetState) path reflects the authoritative rate_limits (ADR 0021
// §1/§6). A nil-source daemon leaves the columns NULL; this test wires a fake
// source and asserts the block's five_hour_pct is written.
func TestTickIntegration_SamplesAndPersistsLimits(t *testing.T) {
	dir := shortTempDir(t)
	paths := Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ws := service.NewWriteService(service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ws.Start(context.Background())
	t.Cleanup(ws.Stop)

	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessJSON, _ := json.Marshal(map[string]any{
		"sessionId": "test-sess-1",
		"pid":       os.Getpid(),
		"cwd":       dir,
		"kind":      "project",
		"startedAt": time.Now().UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(sessDir, "test-sess-1.json"), sessJSON, 0o600); err != nil {
		t.Fatalf("write session fixture: %v", err)
	}

	activeBlockBody, _ := json.Marshal(map[string]any{
		"blocks": []map[string]any{
			{
				"id":        "2026-06-01T10Z",
				"startTime": "2026-06-01T10:00:00Z",
				"endTime":   "2026-06-01T15:00:00Z",
				"isActive":  true,
				"costUSD":   5.0,
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())

	cache := usage.NewCachedRunner(time.Hour, time.Second,
		func(context.Context) ([]byte, error) { return activeBlockBody, nil })
	cache.Start(ctx)
	p := &poller.Poller{
		SessionsDir:  sessDir,
		ClaudeHome:   dir,
		PidAlive:     func(pid int) bool { return pid == os.Getpid() },
		Now:          time.Now,
		Pricer:       usage.NewProvider(cache, &usage.Runner{}),
		WriteService: ws,
		DB:           db,
	}

	// Fake LimitsSource: fixed authoritative 5h reading (account-global).
	fivePct := 34.0
	fiveRst := time.Unix(1782958200, 0)
	captured := time.Now()
	limits := &fakeLimitsSource{limits: &Limits{
		FiveHourPct:      &fivePct,
		FiveHourResetsAt: fiveRst,
		CapturedAt:       captured,
	}}

	tickSynced := make(chan struct{}, 10)
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, RunOptions{
			Paths:        paths,
			Tick:         30 * time.Millisecond,
			Poller:       p,
			WriteService: ws,
			Limits:       limits,
			TreeObserver: func(_ *aggregate.Tree) {
				_ = ws.Sync(ctx)
				select {
				case tickSynced <- struct{}{}:
				default:
				}
			},
		})
	}()

	waitForFile(t, paths.Socket)
	for i := 0; i < 2; i++ {
		select {
		case <-tickSynced:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for tick %d", i+1)
		}
	}

	var fivePctCol sql.NullFloat64
	var fiveRstCol sql.NullString
	err = db.QueryRowContext(context.Background(),
		"SELECT five_hour_pct, five_hour_resets_at FROM blocks WHERE block_id = ?",
		"2026-06-01T10Z").Scan(&fivePctCol, &fiveRstCol)
	if err != nil {
		t.Fatalf("query block limits: %v", err)
	}
	if !fivePctCol.Valid || fivePctCol.Float64 != 34.0 {
		t.Errorf("persisted five_hour_pct valid=%v value=%v, want 34 (sampled from LimitsSource)",
			fivePctCol.Valid, fivePctCol.Float64)
	}
	if !fiveRstCol.Valid {
		t.Error("persisted five_hour_resets_at is NULL, want the sampled reset")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWith did not return")
	}
}
