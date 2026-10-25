package serve_test

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// seedEntities leaves two issue entities: ALPHA-8 closed a month ago (past the
// seven-day expiry) and ALPHA-9 closed an hour ago.
func seedEntities(t *testing.T) func(r *rig) {
	return func(r *rig) {
		st, _, err := store.Open(r.storePath(), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		now := time.Now()
		for id, closed := range map[string]time.Time{"ALPHA-8": now.Add(-30 * 24 * time.Hour), "ALPHA-9": now.Add(-time.Hour)} {
			if err := st.TouchEntity(ctx, groups.EntityKind, id, now); err != nil {
				t.Fatal(err)
			}
			if err := st.SetClosedAt(ctx, groups.EntityKind, id, closed); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheSweepEvictsAnIssueClosedPastTheExpiryAndKeepsTheOthers(t *testing.T) {
	r := newRig(t, fakePjiraDown, "", seedEntities(t))
	d := r.start(serve.Hooks{SweepInterval: 50 * time.Millisecond})
	d.waitReady()
	until(t, 30*time.Second, "the sweep to evict the issue closed a month ago", func() bool {
		return r.count(`SELECT COUNT(*) FROM entity WHERE id = 'ALPHA-8'`) == 0
	})
	if n := r.count(`SELECT COUNT(*) FROM entity WHERE id = 'ALPHA-9'`); n != 1 {
		t.Errorf("the issue closed an hour ago must stay, entity rows = %d", n)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}
