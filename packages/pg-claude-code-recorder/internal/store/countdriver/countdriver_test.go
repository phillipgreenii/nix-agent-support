package countdriver

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open(name, "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSelectCountsOneQueryAndNRows(t *testing.T) {
	c := Register("countdriver-test-select")
	db := openDB(t, "countdriver-test-select")
	if _, err := db.Exec(`CREATE TABLE t(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO t(x) VALUES(?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	c.Reset()
	rs, err := db.Query(`SELECT x FROM t WHERE x >= ?`, 2)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rs.Next() {
		n++
	}
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	if n != 3 || c.Queries() != 1 || c.RowsRead() != 3 {
		t.Fatalf("rows %d queries %d rowsRead %d", n, c.Queries(), c.RowsRead())
	}
}

func TestResetZeroesBoth(t *testing.T) {
	c := Register("countdriver-test-reset")
	db := openDB(t, "countdriver-test-reset")
	if _, err := db.Exec(`CREATE TABLE t(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	if c.Queries() == 0 || c.RowsRead() == 0 {
		t.Fatalf("nothing counted: %d %d", c.Queries(), c.RowsRead())
	}
	c.Reset()
	if c.Queries() != 0 || c.RowsRead() != 0 {
		t.Fatalf("after reset: %d %d", c.Queries(), c.RowsRead())
	}
}

func TestRegisterTwiceReturnsSameCounter(t *testing.T) {
	a := Register("countdriver-test-twice")
	b := Register("countdriver-test-twice")
	if a != b {
		t.Fatal("different counters for one name")
	}
}

func TestPreparedStatementCounts(t *testing.T) {
	c := Register("countdriver-test-prepare")
	db := openDB(t, "countdriver-test-prepare")
	st, err := db.Prepare(`SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if c.Queries() < 1 {
		t.Fatalf("prepare not counted: %d", c.Queries())
	}
}
