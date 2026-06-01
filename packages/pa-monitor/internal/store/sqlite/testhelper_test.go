package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// openTestDB returns a freshly-migrated in-memory DB. Closed at test end.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}
