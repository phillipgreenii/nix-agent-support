package sync

import (
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// TestMain disables SQLite durability for every store this package's tests
// open. Several files here (enrich_test.go, enrich_jira_wiring_test.go,
// prevents_test.go, ...) call store.Open directly rather than
// store.OpenForTest, so they need this seam rather than relying on
// OpenForTest's own SetSynchronousForTests call.
//
// Each store creation costs ~17 fsyncs (WAL conversion + one commit per
// schema migration + the close checkpoint) against a throwaway t.TempDir()
// database, and fsync latency on a loaded/slow-fsync builder is enough to
// blow `go test`'s 10-minute default timeout even though CPU time is
// trivial. Durability is meaningless for a database deleted at test exit.
// See store.synchronousPragma for the full write-up; mirrors ceta commit
// `1138b8a1`.
func TestMain(m *testing.M) {
	store.SetSynchronousForTests("OFF")
	os.Exit(m.Run())
}
