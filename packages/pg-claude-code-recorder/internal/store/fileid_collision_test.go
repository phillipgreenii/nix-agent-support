package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func fileRec(id int64, source, mode string) *Rec {
	return &Rec{T: TFile, At: 5, File: id, Source: source, Mode: mode}
}

func TestNewFileRecordCollidingWithExistingIDOfOtherModeOrSourceFails(t *testing.T) {
	o := obs.New(nil)
	s, err := Open(t.TempDir(), Options{Obs: o})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := s.NewFileID()
	if err := s.Commit(fileRec(id, "claude-transcript", "append")); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Rec{fileRec(id, "claude-transcript", "snapshot"), fileRec(id, "other", "append")} {
		var ce *FileIDCollisionError
		if err := s.Commit(r); !errors.As(err, &ce) || ce.ID != id {
			t.Fatalf("Commit %s/%s: %v, want FileIDCollisionError", r.Source, r.Mode, err)
		}
	}
	// A collision inside one batch is refused too, and nothing of it is journaled.
	var ce *FileIDCollisionError
	if err := s.Commit(fileRec(900, "a", "append"), fileRec(900, "a", "snapshot")); !errors.As(err, &ce) {
		t.Fatalf("in-batch collision: %v", err)
	}
	if _, err := s.File(900); err == nil {
		t.Fatal("a rejected batch reached the index")
	}
	if v, _ := o.Reg.Counter(obs.MFileIDCollisions, map[string]string{"where": "write"}); v != 3 {
		t.Fatalf("write collisions %v, want 3", v)
	}
	if n := len(o.EventsNamed(obs.EvFileIDCollision)); n != 3 {
		t.Fatalf("events %d, want 3", n)
	}
	// The row is untouched.
	if f, err := s.File(id); err != nil || f.Mode != "append" {
		t.Fatalf("file %+v %v", f, err)
	}
}

func TestIdenticalFileRecordReapplyStaysANoOp(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	id := s.NewFileID()
	for i := 0; i < 2; i++ {
		if err := s.Commit(fileRec(id, "claude-transcript", "append")); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
}

func TestReplayOfJournalHoldingAFileIDCollisionOpensAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.NewFileID()
	if err := s.Commit(fileRec(id, "claude-transcript", "snapshot")); err != nil {
		t.Fatal(err)
	}
	last := s.nextSeq
	_ = s.Close()
	// The reissue the recorder wrote before the check existed: same id, other mode.
	b, err := json.Marshal(&Rec{Seq: last, T: TFile, At: 6, File: id, Source: "claude-transcript", Mode: "append"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, JournalName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Both a plain open (replays the new record) and a forced rebuild must open.
	for _, rebuild := range []bool{false, true} {
		o := obs.New(nil)
		s2, err := Open(dir, Options{Obs: o, Rebuild: rebuild})
		if err != nil {
			t.Fatalf("Open(rebuild=%v): %v", rebuild, err)
		}
		if len(s2.ReplayCollisions) != 1 || s2.ReplayCollisions[0] != id {
			t.Fatalf("ReplayCollisions %v", s2.ReplayCollisions)
		}
		if v, _ := o.Reg.Counter(obs.MFileIDCollisions, map[string]string{"where": "replay"}); v != 1 {
			t.Fatalf("replay collisions %v", v)
		}
		if got, err := s2.File(id); err != nil || got.Mode != "snapshot" {
			t.Fatalf("first record must win: %+v %v", got, err)
		}
		if s2.NewFileID() <= id {
			t.Fatal("new ids must stay above reissued ones")
		}
		_ = s2.Close()
	}
}
