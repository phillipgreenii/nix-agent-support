//go:build integration

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

const bulkKillEnv = "PG_CCREC_BULK_KILL_DIR"

// TestKilledBulkRebuildLeavesAFileTheNextStartRefuses really kills a process
// (SIGKILL: no deferred cleanup, no Close) in the middle of a bulk rebuild
// that has an uncommitted batch open.
func TestKilledBulkRebuildLeavesAFileTheNextStartRefuses(t *testing.T) {
	if dir := os.Getenv(bulkKillEnv); dir != "" {
		s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
		if err != nil {
			os.Exit(2)
		}
		s.BulkBegin()
		_ = s.WithWrite("", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('half','1')`)
			return err
		})
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	dir := seededDir(t)
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestKilledBulkRebuildLeavesAFileTheNextStartRefuses$")
	cmd.Env = append(os.Environ(), bulkKillEnv+"="+dir)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != -1 {
		t.Fatalf("the child was not killed by a signal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DBName)); err != nil {
		t.Fatal(err)
	}
	var unfinished *UnfinishedRebuildError
	if _, err := Open(dir, Options{}); !errors.As(err, &unfinished) {
		t.Fatalf("next start after a kill mid-rebuild: %v, want UnfinishedRebuildError", err)
	}
	if _, err := Open(dir, Options{ReplayOnly: true}); !errors.As(err, &unfinished) {
		t.Fatalf("a replay-only open must refuse too: %v", err)
	}
	s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true})
	if err != nil {
		t.Fatalf("rerunning the rebuild: %v", err)
	}
	_ = s.Close()
}
