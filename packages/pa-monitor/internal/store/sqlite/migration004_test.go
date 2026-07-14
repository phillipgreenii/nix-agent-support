package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// TestMigrate004_AppliesCleanlyOn003DB proves migration 004 (five_hour_resets_at)
// applies on a DB already at 003, the new column is nullable, and an old row reads
// back NULL — never 0 and never 1970 (ADR 0021 §6).
func TestMigrate004_AppliesCleanlyOn003DB(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	applyMigrationsUpTo(t, db, 3)

	oldRow := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO blocks (block_id, started_at, ended_at, last_processed_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		"old-block", oldRow, oldRow, oldRow, oldRow); err != nil {
		t.Fatalf("insert old block: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate to head: %v", err)
	}

	var maxV int
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&maxV); err != nil {
		t.Fatalf("max version: %v", err)
	}
	if maxV != 5 {
		t.Errorf("schema_migrations max version = %d, want 5", maxV)
	}

	var n, notnull int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info('blocks') WHERE name='five_hour_resets_at'").Scan(&n); err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("blocks.five_hour_resets_at present = %d, want 1", n)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT \"notnull\" FROM pragma_table_info('blocks') WHERE name='five_hour_resets_at'").Scan(&notnull); err != nil {
		t.Fatalf("pragma notnull: %v", err)
	}
	if notnull != 0 {
		t.Errorf("blocks.five_hour_resets_at notnull = %d, want 0 (nullable)", notnull)
	}

	var fiveRst sql.NullString
	if err := db.QueryRowContext(ctx,
		"SELECT five_hour_resets_at FROM blocks WHERE block_id = ?", "old-block").Scan(&fiveRst); err != nil {
		t.Fatalf("select old five_hour_resets_at: %v", err)
	}
	if fiveRst.Valid {
		t.Errorf("old five_hour_resets_at read back valid=%q, want NULL (never 1970)", fiveRst.String)
	}
}

// TestBlockStore_FiveHourResetsAtRoundTrip proves the store persists and reads back
// FiveHourResetsAt: nil stays nil (unknown), a set value round-trips exactly.
func TestBlockStore_FiveHourResetsAtRoundTrip(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Unset -> nil.
	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:         "no-5h-reset",
		StartedAt:       now.Add(-time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("Upsert no-5h-reset: %v", err)
	}
	row := db.QueryRowContext(ctx, blockSelectColumns+" FROM blocks WHERE block_id = ?", "no-5h-reset")
	got, err := scanBlock(row)
	if err != nil {
		t.Fatalf("scanBlock: %v", err)
	}
	if got.FiveHourResetsAt != nil {
		t.Errorf("FiveHourResetsAt = %v, want nil (unknown, never 1970)", *got.FiveHourResetsAt)
	}

	// Set -> round-trips.
	fiveRst := now.Add(3 * time.Hour)
	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:          "with-5h-reset",
		StartedAt:        now.Add(-time.Hour),
		EndedAt:          now.Add(4 * time.Hour),
		LastProcessedAt:  now,
		UpdatedAt:        now,
		FiveHourResetsAt: &fiveRst,
	}); err != nil {
		t.Fatalf("Upsert with-5h-reset: %v", err)
	}
	row2 := db.QueryRowContext(ctx, blockSelectColumns+" FROM blocks WHERE block_id = ?", "with-5h-reset")
	got2, err := scanBlock(row2)
	if err != nil {
		t.Fatalf("scanBlock: %v", err)
	}
	if got2.FiveHourResetsAt == nil || !got2.FiveHourResetsAt.Equal(fiveRst) {
		t.Errorf("FiveHourResetsAt = %v, want %v", got2.FiveHourResetsAt, fiveRst)
	}
}
