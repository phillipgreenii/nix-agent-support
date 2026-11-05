package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
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

func TestNewerSchemaSaysHowToProceedAndRebuildIsThatWay(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	epoch := s.Epoch()
	if _, err := s.wdb.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err := Open(dir, Options{})
	var ne *SchemaNewerError
	if !errors.As(err, &ne) || ne.Found != 99 || ne.Known != SchemaVersion || !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("newer schema: %v", err)
	}
	o := obs.New(nil)
	s2, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, Obs: o})
	if err != nil {
		t.Fatalf("--rebuild is the way past it: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if s2.SchemaVersion() != SchemaVersion || s2.Epoch() != epoch+1 || !s2.Rebuilt {
		t.Fatalf("schema %d epoch %d rebuilt %v", s2.SchemaVersion(), s2.Epoch(), s2.Rebuilt)
	}
	if _, err := s2.File(id); err != nil {
		t.Fatalf("the journal's observations came back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DBName+".pre-rebuild")); err != nil {
		t.Fatalf("the old index is kept aside: %v", err)
	}
	if v, _ := o.Reg.Counter(obs.MIndexRebuilds, nil); v != 1 {
		t.Fatalf("rebuild counter %v", v)
	}
	if ev := o.EventsNamed(obs.EvIndexRebuilt); len(ev) != 1 || ev[0].Attrs["forced"] != "true" {
		t.Fatalf("index_rebuilt events %+v", ev)
	}
}

func TestAForcedRebuildBumpsTheEpochAndKeepsEveryObservation(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	seed(t, s)
	want, _ := s.Dump()
	epoch := s.Epoch()
	_ = s.Close()
	s2, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	got, _ := s2.Dump()
	if s2.Epoch() != epoch+1 || strip(got) != strip(want) {
		t.Fatalf("epoch %d (was %d); observations differ:\n%s\n---\n%s", s2.Epoch(), epoch, got, want)
	}
	// The observations replayed from the journal equal those in the database.
	replay, err := ReplayDump(filepath.Join(dir, JournalName))
	if err != nil || replay != got {
		t.Fatalf("journal replay differs from the database: %v", err)
	}
	// A normal start does not rebuild.
	_ = s2.Close()
	s3 := open(t, dir)
	defer func() { _ = s3.Close() }()
	if s3.Rebuilt || s3.Epoch() != epoch+1 {
		t.Fatalf("rebuilt=%v epoch %d", s3.Rebuilt, s3.Epoch())
	}
}

func TestAnInterruptedRebuildFromTheJournalRecovers(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := seed(t, s)
	want, _ := s.Dump()
	_ = s.Close()
	for _, n := range []string{DBName, DBName + "-wal", DBName + "-shm"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	disarm := faults.ArmPanic("store.replay")
	crashed := faults.Recover(func() { _, _ = Open(dir, Options{}) })
	disarm()
	if !crashed {
		t.Fatal("the rebuild did not reach its replay crash point")
	}
	s2 := open(t, dir)
	defer func() { _ = s2.Close() }()
	got, _ := s2.Dump()
	if strip(got) != strip(want) || !s2.Rebuilt {
		t.Fatalf("after the interrupted rebuild: rebuilt=%v\n%s\n---\n%s", s2.Rebuilt, got, want)
	}
	if g, err := s2.Generation(id, 1); err != nil || g.CopyOffset != 10 {
		t.Fatalf("generation %+v %v", g, err)
	}
}

func TestOnWriteHooksRunAfterAParseCommit(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	n := 0
	s.OnWrite(func() { n++ })
	if err := s.WithWrite("", func(tx *sql.Tx) error { return nil }); err != nil || n != 1 {
		t.Fatalf("hook ran %d times, err %v", n, err)
	}
	if err := s.WithWrite("", func(tx *sql.Tx) error { return errors.New("no") }); err == nil || n != 1 {
		t.Fatalf("a rolled-back write must not fire the hook: %d %v", n, err)
	}
}

func TestOpenPointerReadsACopyASnapshotVersionAndAnArchive(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	app, snap := s.NewFileID(), s.NewFileID()
	if err := s.Commit(
		&Rec{T: TFile, At: 1, File: app, Source: "claude-transcript", Mode: "append"},
		&Rec{T: TFile, At: 1, File: snap, Source: "session-mode", Mode: "snapshot"},
	); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string]string{s.CopyPath(app, 1): "copy-bytes", s.SnapshotPath(snap, 2): "snapshot-bytes"} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		file int64
		gen  int
		want string
	}{{app, 1, "copy-bytes"}, {snap, 2, "snapshot-bytes"}} {
		b, err := s.ReadPointer(tc.file, tc.gen, 0, 64)
		if err != nil || string(b) != tc.want {
			t.Errorf("file %d gen %d: %q %v", tc.file, tc.gen, b, err)
		}
	}
	if b, err := s.ReadPointer(app, 1, 5, 5); err != nil || string(b) != "bytes" {
		t.Errorf("a range: %q %v", b, err)
	}
	if err := os.Rename(s.CopyPath(app, 1), s.ArchivePath(app, 1)); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReadPointer(app, 1, 0, 64); err != nil || string(b) != "copy-bytes" {
		t.Errorf("archived: %q %v", b, err)
	}
	if _, err := s.ReadPointer(app, 7, 0, 4); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing bytes: %v", err)
	}
}

func TestCountingDriverCountsStoreReads(t *testing.T) {
	c := countdriver.Register("store-test-counting")
	s, err := Open(t.TempDir(), Options{DriverName: "store-test-counting"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seed(t, s)
	c.Reset()
	rs, err := s.rdb.Query(`SELECT key FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rs.Next() {
		n++
	}
	_ = rs.Close()
	if n == 0 || c.Queries() != 1 || c.RowsRead() != int64(n) {
		t.Fatalf("rows %d queries %d rowsRead %d", n, c.Queries(), c.RowsRead())
	}
	// A second open under the same registered name does not panic.
	s2, err := Open(t.TempDir(), Options{DriverName: "store-test-counting"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
}

func TestDefaultDriverNameStillWorks(t *testing.T) {
	s, err := Open(t.TempDir(), Options{DriverName: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seed(t, s)
}

func TestCloseLogsWALTruncateStartThenDone(t *testing.T) {
	o := obs.New(nil)
	s, err := Open(t.TempDir(), Options{Obs: o})
	if err != nil {
		t.Fatal(err)
	}
	seed(t, s) // writes through the WAL
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var names []string
	var done map[string]string
	for _, e := range o.Events() {
		if e.Name == obs.EvWALTruncateStart || e.Name == obs.EvWALTruncateDone {
			names = append(names, e.Name)
			if e.Name == obs.EvWALTruncateDone {
				done = e.Attrs
			}
		}
	}
	if len(names) != 2 || names[0] != obs.EvWALTruncateStart || names[1] != obs.EvWALTruncateDone {
		t.Fatalf("events %v", names)
	}
	if _, ok := done["duration_ms"]; !ok {
		t.Fatalf("wal_truncate_done has no duration_ms: %v", done)
	}
}
