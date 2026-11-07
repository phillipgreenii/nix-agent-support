package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// v2Store builds a store in the version 2 layout (the default options: session
// text, trigram index) and marks it as written by a version 2 binary.
func v2Store(t *testing.T, dir string) {
	t.Helper()
	s := open(t, dir)
	seed(t, s)
	if _, err := s.wdb.Exec(`UPDATE meta SET value='2' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionTwoStoreIsRefusedAndRebuildsIntoTheVersionThreeLayout(t *testing.T) {
	dir := t.TempDir()
	v2Store(t, dir)
	path := filepath.Join(dir, DBName)
	before := sum(t, path)

	_, err := Open(dir, Options{V3Layout: true})
	var older *SchemaOlderError
	if !errors.As(err, &older) || older.Found != 2 || older.Known != SchemaVersion {
		t.Fatalf("Open of a version 2 index: %v, want a SchemaOlderError 2 -> %d", err, SchemaVersion)
	}
	if sum(t, path) != before {
		t.Fatal("refusing the older schema changed index.db")
	}

	s, err := Open(dir, Options{V3Layout: true, Rebuild: true, FreeBytes: func(string) (int64, error) { return 1 << 40, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if sum(t, path+".pre-rebuild") != before {
		t.Fatal("the old file was not kept intact as index.db.pre-rebuild")
	}
	var sessionCol, tri, ext int
	_ = s.Read().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name='session'`).Scan(&sessionCol)
	_ = s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='fts_tri'`).Scan(&tri)
	_ = s.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='ext_assistant_usage'`).Scan(&ext)
	if sessionCol != 0 || tri != 0 || ext != 1 || s.HasSessionText() || !s.SessionRefReady() || s.SchemaVersion() != SchemaVersion {
		t.Fatalf("not the v3 layout: session col %d, fts_tri %d, ext %d, ready %v, version %d", sessionCol, tri, ext, s.SessionRefReady(), s.SchemaVersion())
	}
	for _, ix := range []string{"records_session", "records_session_source_time"} {
		if hasIndex(t, s, ix) {
			t.Errorf("v3 still has %s", ix)
		}
	}
	for _, ix := range []string{"records_session_ref_source_time", "records_cwd", "records_branch"} {
		if !hasIndex(t, s, ix) {
			t.Errorf("v3 lacks %s", ix)
		}
	}
	// Reopening without the option keeps the layout the file has.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir)
	defer func() { _ = s2.Close() }()
	if s2.HasSessionText() || s2.HasTrigram() || !s2.HasExt() {
		t.Fatal("a reopen changed the layout")
	}
}

func TestRebuildRefusedBelowTheFreeSpaceFloorLeavesTheIndexInPlace(t *testing.T) {
	dir := t.TempDir()
	v2Store(t, dir)
	path := filepath.Join(dir, DBName)
	before := sum(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	need, est := RebuildSpaceNeeded(fi.Size())
	if est != fi.Size()*85/100 || need != est+JournalSizeLimit {
		t.Fatalf("need %d estimate %d for a %d-byte file", need, est, fi.Size())
	}

	o := obs.New(nil)
	free := need - 1
	_, err = Open(dir, Options{V3Layout: true, Rebuild: true, Obs: o, FreeBytes: func(string) (int64, error) { return free, nil }})
	var low *InsufficientSpaceError
	if !errors.As(err, &low) || low.FreeBytes != free || low.NeedBytes != need {
		t.Fatalf("Open: %v, want an InsufficientSpaceError", err)
	}
	if sum(t, path) != before {
		t.Fatal("a refused rebuild changed index.db")
	}
	if _, err := os.Stat(path + ".pre-rebuild"); err == nil {
		t.Fatal("a refused rebuild moved the old file aside")
	}
	if v, _ := o.Reg.Counter(obs.MRebuildRefused, nil); v != 1 {
		t.Fatalf("%s = %v, want 1", obs.MRebuildRefused, v)
	}
	if evs := o.EventsNamed(obs.EvRebuildRefused); len(evs) != 1 || evs[0].Attrs["free_bytes"] == "" || evs[0].Attrs["need_bytes"] == "" {
		t.Fatalf("events %v", evs)
	}

	// The refusal is also the "could not rebuild for lack of space" signal:
	// counted by op and logged with the figures needed and had.
	if v, _ := o.Reg.Counter(obs.MBlockedNoSpace, map[string]string{"op": obs.OpRebuild}); v != 1 {
		t.Fatalf("%s{op=rebuild} = %v, want 1", obs.MBlockedNoSpace, v)
	}
	if evs := o.EventsNamed(obs.EvBlockedNoSpace); len(evs) != 1 || evs[0].Attrs["op"] != obs.OpRebuild ||
		evs[0].Attrs["needed_bytes"] != strconv.FormatInt(need, 10) || evs[0].Attrs["had_bytes"] != strconv.FormatInt(need-1, 10) {
		t.Fatalf("blocked events %v", evs)
	}

	// Exactly at the floor it proceeds.
	free = need
	s, err := Open(dir, Options{V3Layout: true, Rebuild: true, Obs: o, FreeBytes: func(string) (int64, error) { return free, nil }})
	if err != nil {
		t.Fatalf("at the floor: %v", err)
	}
	_ = s.Close()
}

func TestBulkRebuildRecordsWallTimeRateAndProfile(t *testing.T) {
	dir := t.TempDir()
	v2Store(t, dir)
	s, err := Open(dir, Options{V3Layout: true, Rebuild: true, BulkRebuild: true, FreeBytes: func(string) (int64, error) { return 1 << 40, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.BulkBegin()
	if err := s.BulkAdd(10, 100); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	p, err := s.RecordRebuildSummary(2*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if p.FinishSec <= 0 || p.CheckpointSec <= 0 || p.IndexBuildSec <= 0 {
		t.Fatalf("profile %+v", p)
	}
	var js string
	if err := s.Read().QueryRow(`SELECT value FROM meta WHERE key=?`, metaRebuildProfile).Scan(&js); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"wall_sec":2`, `"records":1000`, `"records_per_sec":500`, `"index_build_sec"`, `"fts_optimize_sec"`, `"checkpoint_sec"`, `"batch_commit_sec"`} {
		if !strings.Contains(js, want) {
			t.Errorf("summary %s lacks %s", js, want)
		}
	}
}
