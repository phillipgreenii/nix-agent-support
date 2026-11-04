package serve

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// seedFiles inserts n append files, each with one current path and one
// generation, straight into the tables (no transcripts on disk).
func seedFiles(t *testing.T, d *Daemon, dir string, n int) {
	t.Helper()
	err := d.st.WithWrite("", func(tx *sql.Tx) error {
		for i := 1; i <= n; i++ {
			if _, err := tx.Exec(`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(?,?,?,?,1)`, 100000+i, "transcript", "append", 1); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO paths(file_id,path,first_seen_ns) VALUES(?,?,1)`, 100000+i, filepath.Join(dir, fmt.Sprintf("f%d.jsonl", i))); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO generations(file_id,gen,start_ns) VALUES(?,1,1)`, 100000+i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Status answers from the cached snapshot: with the read pool closed (so any
// statement would fail) a call over 20,000 files still answers, quickly, and
// the answer does not depend on the file count.
func TestStatusIssuesNoStatementsAndIsFastOver20000Files(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 20000)
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	snap := d.loadStatusSnap()
	if snap == nil || snap.cov.Files != 20000 {
		t.Fatalf("snapshot %+v", snap)
	}
	if err := d.st.Read().Close(); err != nil { // from here every read statement errors
		t.Fatal(err)
	}
	if _, err := d.st.Stages(); err == nil {
		t.Fatal("read pool still answers; the test would prove nothing")
	}
	start := time.Now()
	body, werr := d.opStatus(context.Background(), nil)
	if werr != nil {
		t.Fatal(werr)
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("status took %v over 20000 files", el)
	}
	if st := body.(wire.Status); st.StatusAgeS < 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestStatusServesWarmingBeforeTheFirstSnapshotWhenTheLoopRuns(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.statusLoopOn.Store(true) // the loop is "running" but has produced nothing
	start := time.Now()
	body, werr := d.opStatus(context.Background(), nil)
	if werr != nil {
		t.Fatal(werr)
	}
	st := body.(wire.Status)
	if st.Healthy || st.Short != "behind" || time.Since(start) > 2*time.Second {
		t.Fatalf("status %+v", st)
	}
}
