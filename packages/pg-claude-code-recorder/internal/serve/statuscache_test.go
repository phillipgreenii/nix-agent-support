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
