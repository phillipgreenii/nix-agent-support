package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of the verified copy Open takes before it migrates a populated store.

var backupFileName = regexp.MustCompile(`\.pre-schema-3-\d{8}T\d{6}Z$`)

func v3Fixture(t *testing.T) string {
	t.Helper()
	fx, ok := fixtures(t)[3]
	if !ok {
		t.Fatal("no version 3 fixture")
	}
	return fx
}

func v3Ladder() []Migration { return BuiltinMigrations()[:3] }

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func userVersionOf(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func falsePtr() *bool { f := false; return &f }

func TestBackupOpeningAPopulatedV3StoreWritesABackupThatAV3BinaryOpens(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	copyFile(t, v3Fixture(t), path)
	now := time.Date(2026, 10, 11, 5, 18, 0, 0, time.UTC)
	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	s, res := mustOpen(t, path, opts)

	want := path + ".pre-schema-3-20261011T051800Z"
	if res.Outcome != OutcomeMigrated || res.FromVersion != 3 || res.BackupPath != want {
		t.Fatalf("result = %+v, want migrated from 3 with backup %s", res, want)
	}
	if !backupFileName.MatchString(res.BackupPath) {
		t.Errorf("backup name %q does not match the layout", res.BackupPath)
	}
	if got := userVersionOf(t, s); got != SchemaVersion {
		t.Errorf("migrated store user_version = %d, want %d", got, SchemaVersion)
	}

	// A binary with the v3 ladder opens the backup as a plain store at version 3
	// with its data, and it has no identity (the rung never ran on the copy).
	b, bres := mustOpen(t, res.BackupPath, Options{Synchronous: "OFF", Migrations: v3Ladder(), BackupBeforeMigrate: falsePtr()})
	if bres.Outcome != OutcomeOpened || bres.FromVersion != 3 || userVersionOf(t, b) != 3 {
		t.Fatalf("backup open result = %+v user_version=%d, want opened at 3", bres, userVersionOf(t, b))
	}
	assertFixtureData(t, ctx, b)
	assertFixtureChanges(t, ctx, b, 3)
	if _, err := b.Identity(ctx); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("the backup has an identity: %v", err)
	}
}

func TestBackupIsNotTakenForANewStoreOrAStoreAlreadyAtTheTop(t *testing.T) {
	path := dbPath(t)
	s, res := mustOpen(t, path, fastOpts())
	if res.BackupPath != "" {
		t.Errorf("a new store took a backup: %+v", res)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, res = mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeOpened || res.BackupPath != "" {
		t.Errorf("a store at the top took a backup: %+v", res)
	}
	if got, _ := filepath.Glob(path + backupInfix + "*"); len(got) != 0 {
		t.Errorf("backup files %v exist", got)
	}
}

func TestBackupBeforeMigrateFalseSkipsTheCopyButNotThePruning(t *testing.T) {
	path := dbPath(t)
	copyFile(t, v3Fixture(t), path)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	old := path + ".pre-schema-2-20260101T000000Z"
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	opts.BackupBeforeMigrate = falsePtr()
	_, res := mustOpen(t, path, opts)
	if res.Outcome != OutcomeMigrated || res.BackupPath != "" {
		t.Fatalf("result = %+v, want migrated without a backup", res)
	}
	if !slices.Contains(res.Pruned, old) {
		t.Errorf("Pruned = %v, want it to list %s", res.Pruned, old)
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the expired backup was not deleted: %v", err)
	}
	if got, _ := filepath.Glob(path + backupInfix + "*"); len(got) != 0 {
		t.Errorf("a backup was written although BackupBeforeMigrate is false: %v", got)
	}
}

func TestBackupNameThatExistsGetsANumericSuffix(t *testing.T) {
	path := dbPath(t)
	now := time.Date(2026, 10, 11, 5, 18, 0, 0, time.UTC)
	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	var got []string
	for range 3 {
		_ = os.Remove(path)
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		copyFile(t, v3Fixture(t), path)
		s, res, err := Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		got = append(got, res.BackupPath)
	}
	base := path + ".pre-schema-3-20261011T051800Z"
	want := []string{base, base + "-2", base + "-3"}
	if !slices.Equal(got, want) {
		t.Errorf("backup paths = %v, want %v", got, want)
	}
}

func TestBackupFailingRungMovesTheStoreAsideAndTheBackupIsIntact(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	copyFile(t, v3Fixture(t), path)
	// The fresh store created after the move-aside climbs the same ladder, so
	// the rung fails on its first call only.
	calls := 0
	ladder := append(v3Ladder(), Migration{
		Version: 4, Name: "fails once",
		Apply: func(*sql.Tx, func(string)) error {
			calls++
			if calls == 1 {
				return errors.New("boom")
			}
			return nil
		},
	})
	opts := fastOpts()
	opts.Migrations = ladder
	_, res := mustOpen(t, path, opts)

	if res.Outcome != OutcomeMovedAside || res.MovedAsideTo == "" || !errors.Is(res.Cause, errMigrationFailed) {
		t.Fatalf("result = %+v, want moved aside by a failed rung", res)
	}
	if res.BackupPath == "" || !backupFileName.MatchString(res.BackupPath) {
		t.Fatalf("BackupPath = %q, want the copy taken before the failed rung", res.BackupPath)
	}
	b, bres := mustOpen(t, res.BackupPath, Options{Synchronous: "OFF", Migrations: v3Ladder(), BackupBeforeMigrate: falsePtr()})
	if bres.Outcome != OutcomeOpened || bres.FromVersion != 3 {
		t.Fatalf("backup open result = %+v, want opened at 3", bres)
	}
	assertFixtureData(t, ctx, b)
	assertFixtureChanges(t, ctx, b, 3)
}

func TestBackupThatCannotBeMadeFailsOpenAndLeavesTheStoreUntouched(t *testing.T) {
	path := dbPath(t)
	copyFile(t, v3Fixture(t), path)
	before := fileHash(t, path)

	old := backupStore
	t.Cleanup(func() { backupStore = old })
	boom := errors.New("disk full")
	backupStore = func(*sql.DB, string) error { return boom }

	s, res, err := Open(path, fastOpts())
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded without a backup")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error = %v, want it to say why", err)
	}
	if errors.Is(err, errMigrationFailed) || isCorruption(err) {
		t.Errorf("a backup failure took the move-aside path: %v", err)
	}
	if res.Outcome == OutcomeMovedAside || res.MovedAsideTo != "" || res.BackupPath != "" {
		t.Errorf("result = %+v, want no move-aside and no backup", res)
	}
	if got, _ := filepath.Glob(path + ".moved-aside-*"); len(got) != 0 {
		t.Errorf("the store was moved aside: %v", got)
	}
	if fileHash(t, path) != before {
		t.Error("the store file changed")
	}
}

func TestBackupThatReportsAnotherVersionIsRejectedAndDeleted(t *testing.T) {
	path := dbPath(t)
	copyFile(t, v3Fixture(t), path)
	before := fileHash(t, path)

	old := backupStore
	t.Cleanup(func() { backupStore = old })
	// A "copy" that is a valid database at version 0.
	backupStore = func(_ *sql.DB, target string) error {
		db, err := sql.Open("sqlite", "file:"+target)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		_, err = db.Exec(`CREATE TABLE t (x INTEGER)`)
		return err
	}

	s, _, err := Open(path, fastOpts())
	if err == nil {
		_ = s.Close()
		t.Fatal("Open accepted an unverified backup")
	}
	if !strings.Contains(err.Error(), "schema version 0") {
		t.Errorf("error = %v, want the version mismatch", err)
	}
	if got, _ := filepath.Glob(path + backupInfix + "*"); len(got) != 0 {
		t.Errorf("the rejected copy was kept: %v", got)
	}
	if fileHash(t, path) != before {
		t.Error("the store file changed")
	}
}

func TestBackupInAnUnwritableDirectoryFailsOpenAndLeavesTheStoreUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a 0500 directory")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	lockedDir := filepath.Join(root, "locked")
	for _, d := range []string{realDir, lockedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(realDir, "store.db")
	copyFile(t, v3Fixture(t), real)
	// The store is opened through a symlink that sits in the unwritable
	// directory: SQLite keeps its journal files beside the real file, while the
	// backup name is built beside the path Open was given.
	link := filepath.Join(lockedDir, "store.db")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })
	before := fileHash(t, real)

	s, res, err := Open(link, fastOpts())
	if err == nil {
		_ = s.Close()
		t.Fatalf("Open succeeded although the backup directory is unwritable: %+v", res)
	}
	if !strings.Contains(err.Error(), "back up") {
		t.Errorf("error = %v, want the failed backup step", err)
	}
	if errors.Is(err, errMigrationFailed) || isCorruption(err) || res.MovedAsideTo != "" {
		t.Errorf("an unwritable backup directory took the move-aside path: %v %+v", err, res)
	}
	if got, _ := filepath.Glob(real + ".moved-aside-*"); len(got) != 0 {
		t.Errorf("the store was moved aside: %v", got)
	}
	if fileHash(t, real) != before {
		t.Error("the store file changed")
	}
}

func TestPruneBackupsDeletesOnlyExpiredBackupsOfThisStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	expired := write("store.db.pre-schema-3-20261001T000000Z")
	expiredSuffix := write("store.db.pre-schema-3-20261001T000000Z-2")
	expiredSidecar := write("store.db.pre-schema-3-20261001T000000Z-wal")
	fresh := write("store.db.pre-schema-3-20261011T110000Z")
	garbage := write("store.db.pre-schema-notastamp")
	other := write("other.db.pre-schema-3-20261001T000000Z")
	movedAside := write("store.db.moved-aside-20261001T000000.000000000Z")

	got := pruneBackups(path, now, 24*time.Hour)
	slices.Sort(got)
	if want := []string{expired, expiredSuffix}; !slices.Equal(got, want) {
		t.Errorf("pruned = %v, want %v", got, want)
	}
	for _, gone := range []string{expired, expiredSuffix, expiredSidecar} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was not deleted: %v", gone, err)
		}
	}
	for _, kept := range []string{fresh, garbage, other, movedAside} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was deleted: %v", kept, err)
		}
	}
}

func TestOpenPrunesExpiredBackupsAtTheSamePointAsMovedAsideStores(t *testing.T) {
	path := dbPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	oldBackup := path + ".pre-schema-3-20261009T120000Z"
	oldMoved := path + ".moved-aside-20261009T120000.000000000Z"
	for _, p := range []string{oldBackup, oldMoved} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts := fastOpts()
	opts.Now = func() time.Time { return now }
	opts.Expiry = 24 * time.Hour
	_, res := mustOpen(t, path, opts)
	want := []string{oldBackup, oldMoved}
	slices.Sort(res.Pruned)
	slices.Sort(want)
	if !slices.Equal(res.Pruned, want) {
		t.Errorf("Pruned = %v, want %v", res.Pruned, want)
	}
}
