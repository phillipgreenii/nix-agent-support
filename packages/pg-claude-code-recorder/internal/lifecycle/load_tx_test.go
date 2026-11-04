package lifecycle

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

func TestLoadReadsEverythingInOneReadTransaction(t *testing.T) {
	c := countdriver.Register("lifecycle-load-counting")
	st, err := store.Open(t.TempDir(), store.Options{DriverName: "lifecycle-load-counting"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := &Engine{St: st}
	c.Reset()
	if _, err := e.load(); err != nil {
		t.Fatal(err)
	}
	t.Logf("load: %d statements in %d transactions", c.Queries(), c.Transactions())
	if c.Queries() < 6 || c.Transactions() != 1 {
		t.Fatalf("load took %d transactions for %d statements, want 1 transaction", c.Transactions(), c.Queries())
	}
}
