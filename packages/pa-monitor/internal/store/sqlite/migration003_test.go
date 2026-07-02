package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// applyMigrationsUpTo opens a fresh in-memory DB and applies migrations
// 001..maxVersion (inclusive), stopping BEFORE any later migration. It
// mirrors Migrate but bounds the applied set so a test can simulate a DB
// that predates migration 003.
func applyMigrationsUpTo(t *testing.T, db *sql.DB, maxVersion int) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	type mig struct {
		version int
		name    string
	}
	var ms []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &v); err != nil {
			t.Fatalf("parse version %q: %v", e.Name(), err)
		}
		ms = append(ms, mig{version: v, name: e.Name()})
	}
	// sort ascending
	for i := 0; i < len(ms); i++ {
		for j := i + 1; j < len(ms); j++ {
			if ms[j].version < ms[i].version {
				ms[i], ms[j] = ms[j], ms[i]
			}
		}
	}
	for _, m := range ms {
		if m.version > maxVersion {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + m.name)
		if err != nil {
			t.Fatalf("read %s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("record %s: %v", m.name, err)
		}
	}
}

// TestMigrate003_AppliesCleanlyOn002DB proves migration 003 applies on a DB
// that is already at version 002 (the ADR §6 / acceptance-criteria "003
// applies on a 002 DB" requirement), and that the four new columns exist and
// are nullable.
func TestMigrate003_AppliesCleanlyOn002DB(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Bring the DB to version 002 only.
	applyMigrationsUpTo(t, db, 2)

	// Insert an "old" block row using only pre-003 columns, so the new
	// columns take their column-default (which MUST be NULL / unknown).
	oldRow := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO blocks (block_id, started_at, ended_at, last_processed_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		"old-block", oldRow, oldRow, oldRow, oldRow); err != nil {
		t.Fatalf("insert old block: %v", err)
	}

	// Now run the full migrator; 003 must apply cleanly on top of 002.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate to head: %v", err)
	}

	var maxV int
	if err := db.QueryRowContext(ctx,
		"SELECT MAX(version) FROM schema_migrations").Scan(&maxV); err != nil {
		t.Fatalf("max version: %v", err)
	}
	if maxV != 3 {
		t.Errorf("schema_migrations max version = %d, want 3", maxV)
	}

	for _, col := range []string{"five_hour_pct", "seven_day_pct", "seven_day_resets_at", "limits_captured_at"} {
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM pragma_table_info('blocks') WHERE name=?", col).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if n != 1 {
			t.Errorf("blocks.%s present = %d, want 1", col, n)
		}
		// The column must be nullable (notnull == 0), because NULL means
		// "unknown/stale" and is the common case for seven_day.
		var notnull int
		if err := db.QueryRowContext(ctx,
			"SELECT \"notnull\" FROM pragma_table_info('blocks') WHERE name=?", col).Scan(&notnull); err != nil {
			t.Fatalf("pragma notnull %s: %v", col, err)
		}
		if notnull != 0 {
			t.Errorf("blocks.%s notnull = %d, want 0 (nullable)", col, notnull)
		}
	}

	// The old row's new columns MUST read back NULL, not 0 and not 1970.
	var (
		fivePct sql.NullFloat64
		sevPct  sql.NullFloat64
		sevRst  sql.NullString
		capAt   sql.NullString
	)
	if err := db.QueryRowContext(ctx, `
		SELECT five_hour_pct, seven_day_pct, seven_day_resets_at, limits_captured_at
		FROM blocks WHERE block_id = ?`, "old-block").Scan(&fivePct, &sevPct, &sevRst, &capAt); err != nil {
		t.Fatalf("select old block new cols: %v", err)
	}
	if fivePct.Valid {
		t.Errorf("old five_hour_pct read back valid=%v value=%v, want NULL", fivePct.Valid, fivePct.Float64)
	}
	if sevPct.Valid {
		t.Errorf("old seven_day_pct read back valid, want NULL")
	}
	if sevRst.Valid {
		t.Errorf("old seven_day_resets_at read back valid=%q, want NULL (never 1970)", sevRst.String)
	}
	if capAt.Valid {
		t.Errorf("old limits_captured_at read back valid=%q, want NULL (never 1970)", capAt.String)
	}
}

// TestBlockStore_LimitsRoundTrip_NullNotZeroNot1970 is the integration
// round-trip for the new nullable limits columns: a Block persisted with the
// limits fields unset (nil pointers) reads back nil — never 0.0 and never a
// 1970 timestamp — while a Block persisted WITH values reads them back exactly.
func TestBlockStore_LimitsRoundTrip_NullNotZeroNot1970(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Case 1: limits unset -> must round-trip as nil (unknown/stale).
	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:         "no-limits",
		StartedAt:       now.Add(-time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
		// FiveHourPct / SevenDayPct / SevenDayResetsAt / LimitsCapturedAt all nil.
	}); err != nil {
		t.Fatalf("Upsert no-limits: %v", err)
	}
	got, err := bs.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got == nil {
		t.Fatal("GetActive returned nil for active block")
	}
	if got.FiveHourPct != nil {
		t.Errorf("FiveHourPct = %v, want nil (unknown, not 0)", *got.FiveHourPct)
	}
	if got.SevenDayPct != nil {
		t.Errorf("SevenDayPct = %v, want nil (unknown, not 0)", *got.SevenDayPct)
	}
	if got.SevenDayResetsAt != nil {
		t.Errorf("SevenDayResetsAt = %v, want nil (never 1970)", *got.SevenDayResetsAt)
	}
	if got.LimitsCapturedAt != nil {
		t.Errorf("LimitsCapturedAt = %v, want nil (never 1970)", *got.LimitsCapturedAt)
	}

	// Case 2: limits present -> must round-trip exactly.
	fivePct := 34.0
	sevPct := 0.0 // deliberately zero: a real "0% used" reading, distinct from NULL.
	sevRst := now.Add(6 * 24 * time.Hour)
	capAt := now
	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:          "with-limits",
		StartedAt:        now.Add(-time.Hour),
		EndedAt:          now.Add(4 * time.Hour),
		LastProcessedAt:  now,
		UpdatedAt:        now,
		FiveHourPct:      &fivePct,
		SevenDayPct:      &sevPct,
		SevenDayResetsAt: &sevRst,
		LimitsCapturedAt: &capAt,
	}); err != nil {
		t.Fatalf("Upsert with-limits: %v", err)
	}
	// Fetch the specific row by scanning it back directly (GetActive returns
	// the newest active block; both are active so pin by block_id).
	row := db.QueryRowContext(ctx, blockSelectColumns+" FROM blocks WHERE block_id = ?", "with-limits")
	got2, err := scanBlock(row)
	if err != nil {
		t.Fatalf("scanBlock with-limits: %v", err)
	}
	if got2 == nil {
		t.Fatal("with-limits row not found")
	}
	if got2.FiveHourPct == nil || *got2.FiveHourPct != fivePct {
		t.Errorf("FiveHourPct = %v, want %v", got2.FiveHourPct, fivePct)
	}
	if got2.SevenDayPct == nil || *got2.SevenDayPct != sevPct {
		t.Errorf("SevenDayPct = %v, want %v (real 0%%, not NULL)", got2.SevenDayPct, sevPct)
	}
	if got2.SevenDayResetsAt == nil || !got2.SevenDayResetsAt.Equal(sevRst) {
		t.Errorf("SevenDayResetsAt = %v, want %v", got2.SevenDayResetsAt, sevRst)
	}
	if got2.LimitsCapturedAt == nil || !got2.LimitsCapturedAt.Equal(capAt) {
		t.Errorf("LimitsCapturedAt = %v, want %v", got2.LimitsCapturedAt, capAt)
	}
}
