package serve

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

// The status loop re-reads from the store only the tables a commit can have
// changed: nothing when nothing committed, the file rows for a commit that left
// the paths and stages alone (the steady stream of copy commits), the paths
// table only after a path record, and the stage rows only after a stage record.
// A refresh that re-reads every table each time is what made the refresh a top
// path of the daemon's CPU profile. Runs a real daemon, store and SQLite driver
// with a statement counter in the driver.
func TestStatusRefreshReadsOnlyTheTablesACommitChanged(t *testing.T) {
	c := countdriver.Register("status-rows-counting")
	e := newEnv(t, false)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: "status-rows-counting"})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 20)
	d.rowsOK, d.pathsOK, d.stagesOK = false, false, false // the direct inserts bypass the journal

	refresh := func(label string) int64 {
		t.Helper()
		c.Reset()
		if err := d.refreshStatus(); err != nil {
			t.Fatal(err)
		}
		q := c.Queries()
		t.Logf("%s: %d statements", label, q)
		return q
	}
	commit := func(r *store.Rec) {
		t.Helper()
		if err := d.st.Commit(r); err != nil {
			t.Fatal(err)
		}
	}

	refresh("first refresh") // fills every cache
	if q := refresh("nothing committed"); q != 0 {
		t.Fatalf("a refresh with no commit issued %d statements, want 0", q)
	}
	commit(&store.Rec{T: store.TState, At: 5, File: 100001, State: "ok"})
	if q := refresh("file-only commit"); q != 1 {
		t.Fatalf("a commit that touched no path or stage: %d statements, want 1 (the file rows)", q)
	}
	commit(&store.Rec{T: store.TPath, At: 6, File: 100002, Path: filepath.Join(e.claude, "projects", "example-proj", "moved.jsonl")})
	if q := refresh("path commit"); q != 2 {
		t.Fatalf("a path commit: %d statements, want 2 (file rows and paths)", q)
	}
	commit(&store.Rec{T: store.TStage, At: 7, Session: "s", State: store.StageHeld})
	if q := refresh("stage commit"); q != 2 {
		t.Fatalf("a stage commit: %d statements, want 2 (file rows and stages)", q)
	}
	if got := d.loadStatusSnap().cov.Held; got != 1 {
		t.Fatalf("held conversations %d after the stage commit, want 1", got)
	}
}
