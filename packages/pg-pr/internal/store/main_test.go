package store

import (
	"os"
	"testing"
)

// TestMain makes every store this package opens non-durable.
//
// This package's own tests (store_test.go, migrate_test.go,
// pull_request_test.go, ...) call Open directly rather than OpenForTest, so
// they need their own seam into synchronousPragma. Each test builds a fresh
// DB under t.TempDir(), and each creation costs ~17 fsyncs (WAL conversion +
// one commit per schema migration + the close checkpoint). fsync latency is a
// host-filesystem property that spans orders of magnitude, and on a loaded or
// slow-fsync builder that has been enough to blow `go test`'s 10-minute
// default timeout even though CPU time stays trivial. See synchronousPragma
// and SetSynchronousForTests in store.go for the full write-up, and ceta
// commit `1138b8a1` for the precedent this mirrors.
//
// A test that needs real durability semantics must restore synchronousPragma
// to "" for its own duration.
func TestMain(m *testing.M) {
	SetSynchronousForTests("OFF")
	os.Exit(m.Run())
}
