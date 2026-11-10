package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestLiveInputSeqMovesOnlyForMarkedCommitsAndProcessRecords(t *testing.T) {
	st, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := st.LiveInputSeq()

	// An unmarked write commit and a journal apply of another type: no move.
	if err := st.WithWrite("", func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(&Rec{T: TFile, At: 1, File: st.NewFileID(), Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 1}); err != nil {
		t.Fatal(err)
	}
	if got := st.LiveInputSeq(); got != base {
		t.Fatalf("seq %d after unmarked writes, want %d", got, base)
	}

	// A marked write moves it only once it commits.
	err = st.WithWrite("", func(*sql.Tx) error {
		st.NoteLiveInput()
		if got := st.LiveInputSeq(); got != base {
			t.Errorf("seq %d before the commit, want %d", got, base)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.LiveInputSeq(); got != base+1 {
		t.Fatalf("seq %d after a marked commit, want %d", got, base+1)
	}

	// A marked write that fails does not move it; the mark waits for the next commit.
	_ = st.WithWrite("", func(*sql.Tx) error { st.NoteLiveInput(); return errors.New("fail") })
	if got := st.LiveInputSeq(); got != base+1 {
		t.Fatalf("seq %d after a failed write, want %d", got, base+1)
	}

	// A Process observation moves it.
	if err := st.Commit(&Rec{T: TProc, Kind: "seen", Pid: 1, ProcStart: "x", At: 2}); err != nil {
		t.Fatal(err)
	}
	if got := st.LiveInputSeq(); got != base+2 {
		t.Fatalf("seq %d after a Process record, want %d", got, base+2)
	}
}
