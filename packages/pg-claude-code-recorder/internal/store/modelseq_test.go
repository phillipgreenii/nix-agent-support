package store

import (
	"database/sql"
	"testing"
)

// ModelInputSeq moves for everything LiveInputSeq covers and for the other
// inputs of the lifecycle model (a marked write, a stage or settle record), and
// not for an unrelated commit.
func TestModelInputSeqMovesForEveryModelInput(t *testing.T) {
	st, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seq := st.ModelInputSeq()
	expect := func(step string, moved bool) {
		t.Helper()
		got := st.ModelInputSeq()
		if moved && got != seq+1 || !moved && got != seq {
			t.Fatalf("%s: seq %d, want %d", step, got, map[bool]uint64{true: seq + 1, false: seq}[moved])
		}
		seq = got
	}

	if err := st.Commit(&Rec{T: TFile, At: 1, File: st.NewFileID(), Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 1}); err != nil {
		t.Fatal(err)
	}
	expect("unrelated journal commit", false)
	if err := st.WithWrite("", func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	expect("unmarked write", false)

	if err := st.WithWrite("", func(*sql.Tx) error { st.NoteModelInput(); return nil }); err != nil {
		t.Fatal(err)
	}
	expect("model-marked write", true)
	live := st.LiveInputSeq()
	if err := st.WithWrite("", func(*sql.Tx) error { st.NoteLiveInput(); return nil }); err != nil {
		t.Fatal(err)
	}
	expect("live-marked write", true)
	if st.LiveInputSeq() != live+1 {
		t.Fatal("a live-marked write did not move LiveInputSeq")
	}
	if err := st.WithWrite("", func(*sql.Tx) error { st.NoteModelInput(); return nil }); err != nil {
		t.Fatal(err)
	}
	if st.LiveInputSeq() != live+1 {
		t.Fatal("a model-marked write moved LiveInputSeq")
	}
	seq = st.ModelInputSeq()

	if err := st.Commit(&Rec{T: TProc, Kind: "seen", Pid: 1, ProcStart: "x", At: 2}); err != nil {
		t.Fatal(err)
	}
	expect("process record", true)
	if err := st.Commit(&Rec{T: TStage, At: 3, Session: "s", State: StageHeld}); err != nil {
		t.Fatal(err)
	}
	expect("stage record", true)
	if st.LiveInputSeq() != live+2 {
		t.Fatal("a stage record moved LiveInputSeq")
	}
}
