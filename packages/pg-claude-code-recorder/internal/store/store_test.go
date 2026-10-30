package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seed(t *testing.T, s *Store) int64 {
	t.Helper()
	id := s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&Rec{T: TPath, At: 10, File: id, Path: "/c/projects/p/s.jsonl"},
		&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&Rec{T: TCommit, At: 12, File: id, Gen: 1, From: 0, To: 10, Size: 10, Tail: 77, TailLen: 10},
	); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpenCreatesLayoutWALAndSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	defer func() { _ = s.Close() }()
	for _, n := range []string{DBName, JournalName, CopiesDir, ArchivesDir} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	var mode string
	if err := s.rdb.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	if s.SchemaVersion() != SchemaVersion || s.Epoch() != 1 {
		t.Fatalf("schema %d epoch %d", s.SchemaVersion(), s.Epoch())
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.wdb.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(dir, Options{}); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err %v", err)
	}
}

func TestJournalIsWrittenFirstSoACrashBeforeTheDBCommitIsReplayed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.NewFileID()
	disarm := faults.ArmPanic("store.after-journal")
	crashed := faults.Recover(func() {
		_ = s.Commit(&Rec{T: TFile, At: 5, File: id, Source: "x", Mode: "append"})
	})
	disarm()
	if !crashed {
		t.Fatal("not reached")
	}
	if _, err := s.File(id); err == nil {
		t.Fatal("the database saw the record before the crash point")
	}
	_ = s.Close()
	s2 := open(t, dir)
	defer func() { _ = s2.Close() }()
	if _, err := s2.File(id); err != nil {
		t.Fatalf("journal record not replayed: %v", err)
	}
	if s2.NewFileID() <= id {
		t.Fatal("file id reused after replay")
	}
}

func TestReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	seed(t, s)
	before, _ := s.Dump()
	recs, _, _, _ := readJournal(filepath.Join(dir, JournalName))
	ptrs := make([]*Rec, len(recs))
	for i := range recs {
		ptrs[i] = &recs[i]
	}
	if err := s.applyAll(ptrs); err != nil { // apply every record a second time
		t.Fatal(err)
	}
	after, _ := s.Dump()
	if before != after {
		t.Fatalf("replay changed observations:\n%s\n---\n%s", before, after)
	}
	_ = s.Close()
}

func TestLostIndexAndTornJournalTailRebuildObservations(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	if err := s.Commit(&Rec{T: TCommit, At: 20, File: id, Gen: 1, From: 10, To: 25, Size: 25, Tail: 78, TailLen: 15}); err != nil {
		t.Fatal(err)
	}
	want, _ := s.Dump()
	epoch := s.Epoch()
	_ = s.Close()

	for _, n := range []string{DBName, DBName + "-wal", DBName + "-shm"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	jp := filepath.Join(dir, JournalName)
	jf, _ := os.OpenFile(jp, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = jf.WriteString(`{"seq":99,"t":"commit","file":1,"gen":1,"to":9999`) // torn: no closing brace, no newline
	_ = jf.Close()
	delta, _ := JournalDelta(jp)
	if delta.TornBytes == 0 || delta.LastGoodSeq == 0 {
		t.Fatalf("delta %+v", delta)
	}

	s2 := open(t, dir)
	defer func() { _ = s2.Close() }()
	if !s2.Rebuilt || s2.TornBytes != delta.TornBytes {
		t.Fatalf("rebuilt=%v torn=%d", s2.Rebuilt, s2.TornBytes)
	}
	if s2.Epoch() != epoch+1 {
		t.Fatalf("epoch %d, want %d", s2.Epoch(), epoch+1)
	}
	got, _ := s2.Dump()
	if strip(got) != strip(want) {
		t.Fatalf("observations differ:\n%s\n---\n%s", got, want)
	}
	g, err := s2.Generation(id, 1)
	if err != nil || g.CopyOffset != 25 {
		t.Fatalf("gen %+v %v", g, err)
	}
	// And the rebuilt state agrees with its own journal.
	replay, err := ReplayDump(jp)
	if err != nil || replay != got {
		t.Fatalf("replay differs: %v", err)
	}
}

func strip(dump string) string {
	i := strings.Index(dump, "[files]")
	return dump[i:]
}

func TestCorruptIndexIsSetAsideAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	_ = s.Close()
	for _, n := range []string{DBName + "-wal", DBName + "-shm"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	if err := os.WriteFile(filepath.Join(dir, DBName), []byte(strings.Repeat("not a database ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir)
	defer func() { _ = s2.Close() }()
	if !s2.Rebuilt {
		t.Fatal("not rebuilt")
	}
	if _, err := s2.File(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DBName+".corrupt")); err != nil {
		t.Fatal("corrupt index not kept aside")
	}
}

func TestStartGenerationKeepsOldGenerationAndPinsResetUntilReleased(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	defer func() { _ = s.Close() }()
	id := seed(t, s)
	gen, err := s.StartGeneration(id, "full_hash_mismatch", "bytes", 30)
	if err != nil || gen != 2 {
		t.Fatalf("gen %d %v", gen, err)
	}
	gens, _ := s.Generations(id)
	if len(gens) != 2 || gens[0].CopyOffset != 10 || gens[1].CopyOffset != 0 {
		t.Fatalf("gens %+v", gens)
	}
	f, _ := s.File(id)
	if f.CurGen != 2 {
		t.Fatalf("cur gen %d", f.CurGen)
	}
	rs, _ := s.Resets(true)
	if len(rs) != 1 || rs[0].FromGen != 1 || rs[0].ToGen != 2 || rs[0].Reason != "full_hash_mismatch" {
		t.Fatalf("resets %+v", rs)
	}
	// The pin survives a reopen (it is journaled) until clients advance.
	_ = s.Close()
	s = open(t, dir)
	if rs, _ := s.Resets(true); len(rs) != 1 {
		t.Fatalf("pin lost on reopen: %+v", rs)
	}
	if err := s.ReleaseResets(rs[0].ID, 40); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.Resets(true); len(rs) != 0 {
		t.Fatalf("still pinned: %+v", rs)
	}
	if all, _ := s.Resets(false); len(all) != 1 {
		t.Fatalf("reset entry dropped: %+v", all)
	}
}

func TestFirstGenerationIsNotAReset(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	id := s.NewFileID()
	_ = s.Commit(&Rec{T: TFile, At: 1, File: id, Source: "x", Mode: "append"})
	if _, err := s.StartGeneration(id, "first", "bytes", 2); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.Resets(false); len(rs) != 0 {
		t.Fatalf("resets %+v", rs)
	}
}

func TestCommitHookRunsAfterEachDatabaseCommit(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	n := 0
	s.OnCommit(func() { n++ })
	seed(t, s)
	if n != 1 {
		t.Fatalf("hook ran %d times", n)
	}
}

func TestSingleWriteConnection(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	if got := s.wdb.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("write connection pool %d", got)
	}
}
