package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

func TestPruneCommandDryRunThenYes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(t.Context(), dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400).UTC().Format(store.TSLayout)
	for _, s := range []string{
		`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('a/g1', '', 0, 0, 1, 0, 1)`,
		`INSERT INTO events (path, seq, ts, type) VALUES ('a/g1', 1, '` + old + `', 'user')`,
	} {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	read := func() string {
		b, _ := os.ReadFile(out.Name())
		return string(b)
	}

	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90"}, out, out); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "would remove 1 files") || !strings.Contains(got, "dry run") {
		t.Fatalf("dry run output: %s", got)
	}
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90", "--yes"}, out, out); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "removed 1 files") || !strings.Contains(got, "vacuumed") {
		t.Fatalf("yes output: %s", got)
	}
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "0"}, out, out); err == nil {
		t.Fatal("--days 0 must be refused")
	}
}

// seedOldFile builds a WAL-mode database with one expired file and returns its path.
func seedOldFile(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(t.Context(), dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400).UTC().Format(store.TSLayout)
	for _, s := range []string{
		`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('a/g1', '', 0, 0, 1, 0, 1)`,
		`INSERT INTO events (path, seq, ts, type) VALUES ('a/g1', 1, '` + old + `', 'user')`,
	} {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	return dbPath
}

func fileCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := store.Open(t.Context(), dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func withFreeBytes(t *testing.T, free int64) {
	t.Helper()
	orig := freeBytes
	freeBytes = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { freeBytes = orig })
}

func TestPruneRefusesWithoutFreeSpaceAndChangesNothing(t *testing.T) {
	dbPath := seedOldFile(t)
	withFreeBytes(t, 12345)
	sizeBefore := mustSize(t, dbPath)

	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	err = cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90", "--yes"}, out, out)
	if err == nil || errors.Is(err, errUsage) {
		t.Fatalf("want a refusal error, got %v", err)
	}
	if !strings.Contains(err.Error(), "refusing to prune") || !strings.Contains(err.Error(), "--force-low-space") {
		t.Fatalf("refusal message: %v", err)
	}
	if got := fileCount(t, dbPath); got != 1 {
		t.Fatalf("files after refusal = %d, want 1", got)
	}
	if got := mustSize(t, dbPath); got != sizeBefore {
		t.Fatalf("db size changed %d -> %d", sizeBefore, got)
	}
}

func mustSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestPruneForceLowSpaceRunsVacuum(t *testing.T) {
	dbPath := seedOldFile(t)
	withFreeBytes(t, 12345)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90", "--yes", "--force-low-space"}, out, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out.Name())
	if !strings.Contains(string(b), "vacuumed") || !strings.Contains(string(b), "WOULD-REFUSE") {
		t.Fatalf("output: %s", b)
	}
	if got := fileCount(t, dbPath); got != 0 {
		t.Fatalf("files after forced prune = %d, want 0", got)
	}
}

func TestPruneEnoughSpaceTruncatesWALAndVacuums(t *testing.T) {
	dbPath := seedOldFile(t)
	withFreeBytes(t, 1<<40)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90", "--yes"}, out, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out.Name())
	if !strings.Contains(string(b), "vacuumed") || !strings.Contains(string(b), ": OK") {
		t.Fatalf("output: %s", b)
	}
	// The VACUUM that follows the TRUNCATE checkpoint rewrites through the WAL, so
	// the closed database leaves no WAL behind or an empty one.
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 4096*4 {
		t.Fatalf("WAL not truncated: %d bytes", fi.Size())
	}
}

func TestPruneDryRunAndJSONReportSpace(t *testing.T) {
	dbPath := seedOldFile(t)
	withFreeBytes(t, 12345)
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90"}, out, out); err != nil {
		t.Fatalf("dry run must not fail: %v", err)
	}
	b, _ := os.ReadFile(out.Name())
	if !strings.Contains(string(b), "needed") || !strings.Contains(string(b), "available 12345 bytes") || !strings.Contains(string(b), "WOULD-REFUSE") {
		t.Fatalf("dry run output: %s", b)
	}

	jout, err := os.CreateTemp(t.TempDir(), "jout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jout.Close() }()
	if err := cmdPrune(context.Background(), []string{"--db", dbPath, "--days", "90", "--json"}, jout, jout); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(jout.Name())
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json: %v: %s", err, raw)
	}
	if got["refused"] != true || got["available_bytes"].(float64) != 12345 || got["needed_bytes"].(float64) <= 12345 {
		t.Fatalf("json fields: %v", got)
	}
}

func TestDefaultRetentionDays(t *testing.T) {
	if defaultRetentionDays != 45 {
		t.Fatalf("defaultRetentionDays = %d, want 45", defaultRetentionDays)
	}
}
