package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

func TestPruneCommandDryRunThenYes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400).UTC().Format(store.TSLayout)
	for _, s := range []string{
		`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at) VALUES ('a/g1', '', 0, 0, 1, 0, 1)`,
		`INSERT INTO events (path, seq, ts, type) VALUES ('a/g1', 1, '` + old + `', 'user')`,
	} {
		if _, err := db.Exec(s); err != nil {
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
