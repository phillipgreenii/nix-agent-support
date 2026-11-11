package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

// TestStatusFileToStoredBlockLimits pins the whole path that feeds the block row's
// rate_limits columns: a <session>.status.jsonl line written to a real recorder is
// read by the corpus Monitor (inventory, then the line fetch), folded into the
// account-global reading, applied to the tree and persisted on the block through
// the real SQLite store. A later status line must advance the stored columns.
func TestStatusFileToStoredBlockLimits(t *testing.T) {
	const cwd = "/example/proj"
	t0 := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(3 * time.Hour).Unix()
	line := func(at time.Time, pct int) string {
		return fmt.Sprintf(`{"ts":%d,"session_id":"s1","five_hour_pct":%d,"five_hour_resets_at":%d}`, at.Unix(), pct, reset)
	}

	e := corpustest.Start(t)
	e.Register(111, "s1", cwd, "", true)
	e.Put(corpustest.TranscriptRel(cwd, "s1"), fmt.Sprintf(`{"type":"user","timestamp":%q,"sessionId":"s1","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`, t0.Format(time.RFC3339)))
	e.Put(corpustest.StatusRel(cwd, "s1"), line(t0, 21))

	m := corpus.New(e.Facts, e.Discovery())
	lo := corpus.NewLimitsObserver()
	m.Register(lo)

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	blocks := sqlite.NewBlockStore(db)

	persist := func(now time.Time) (pct float64, captured string) {
		t.Helper()
		if _, err := m.Scan(ctx, now); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		tree := &aggregate.Tree{}
		applyLimits(tree, m.Limits())
		b := &usage.Block{ID: "2026-07-20T10Z", StartTime: t0, EndTime: t0.Add(5 * time.Hour)}
		if _, err := blocks.Upsert(ctx, blockToStoreBlockWithLimits(b, 100, now, tree)); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT five_hour_pct, limits_captured_at FROM blocks WHERE block_id = ?`, b.ID).Scan(&pct, &captured); err != nil {
			t.Fatalf("stored block has no limits: %v", err)
		}
		return pct, captured
	}

	pct, captured := persist(t0.Add(time.Minute))
	if pct != 21 || captured != t0.UTC().Format(time.RFC3339) {
		t.Fatalf("stored = %v at %q, want 21 at %s", pct, captured, t0.UTC().Format(time.RFC3339))
	}

	t1 := t0.Add(10 * time.Minute)
	e.Add(corpustest.StatusRel(cwd, "s1"), line(t1, 22))
	pct, captured = persist(t1.Add(time.Minute))
	if pct != 22 || captured != t1.UTC().Format(time.RFC3339) {
		t.Fatalf("after a new status line stored = %v at %q, want 22 at %s", pct, captured, t1.UTC().Format(time.RFC3339))
	}
}
