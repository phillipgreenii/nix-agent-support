package store

import (
	"os"
	"testing"
)

// TestMain makes every store this package opens non-durable.
//
// This package's own tests (store_test.go, lock_test.go) call Open
// directly rather than OpenForTest, so they need their own seam into
// synchronousPragma. Each test builds a fresh DB under t.TempDir(), and
// each creation costs several fsyncs (WAL conversion + the schema
// migration commit + the close checkpoint). Durability is meaningless for
// a database deleted at test exit, so tests opt out of it — ported
// convention from packages/pg-pr/internal/store/main_test.go.
//
// A test that needs real durability semantics must restore
// synchronousPragma to "" for its own duration.
func TestMain(m *testing.M) {
	SetSynchronousForTests("OFF")
	os.Exit(m.Run())
}
