package pipeline

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// failStoreWrites installs a SQLite trigger on the store's own file that
// aborts the named statement kind ("INSERT", "UPDATE") on table with a fixed
// message, through a second connection. It is a fault injection into the REAL
// store (no mock): the store's own code takes its own error path. The trigger
// lives as long as the temp database.
func failStoreWrites(t *testing.T, st *store.Store, op, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ddl := fmt.Sprintf(`CREATE TRIGGER inject_fail_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
		op, table, op, table)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}
