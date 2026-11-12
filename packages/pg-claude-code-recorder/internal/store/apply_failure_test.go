package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A commit whose index apply fails after the journal write (the live case: the
// database was locked) must leave no trace in the journal. Otherwise a replay
// holds records the index never applied and the doctor reports
// journal_differs_from_index for as long as the journal lives.
func TestFailedIndexApplyLeavesNoJournalRecord(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	defer func() { _ = s.Close() }()
	id := seed(t, s)
	jp := filepath.Join(dir, JournalName)
	before, err := os.ReadFile(jp)
	if err != nil {
		t.Fatal(err)
	}
	seqBefore := s.nextSeq

	if err := s.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `CREATE TRIGGER fail_commit BEFORE UPDATE ON files BEGIN SELECT RAISE(ABORT, 'database is locked'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(&Rec{T: TCommit, At: 30, File: id, Gen: 1, From: 10, To: 40, Size: 40, Tail: 1, TailLen: 5}); err == nil {
		t.Fatal("the commit was expected to fail")
	}
	after, _ := os.ReadFile(jp)
	if string(after) != string(before) {
		t.Fatalf("the failed commit left %d journal bytes behind", len(after)-len(before))
	}
	if s.nextSeq != seqBefore {
		t.Fatalf("nextSeq %d, want %d", s.nextSeq, seqBefore)
	}

	if err := s.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `DROP TRIGGER fail_commit`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(&Rec{T: TCommit, At: 31, File: id, Gen: 1, From: 10, To: 25, Size: 25, Tail: 2, TailLen: 5}); err != nil {
		t.Fatal(err)
	}
	fz, err := s.FreezeJournal()
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fz.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if d := DumpDiff(replay, fz.Live); d != "" {
		t.Fatalf("replay differs from the index: %s", d)
	}
}
