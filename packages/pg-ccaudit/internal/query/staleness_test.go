package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStalenessReportsCurrentIndex(t *testing.T) {
	_, root, db := buildIndex(t)
	s := Measure(context.Background(), db, root)
	if s.Stale() {
		t.Errorf("index reported stale immediately after ingest: %+v", s)
	}
	if s.FilesOnDisk != 3 || s.FilesIndexed != 3 {
		t.Errorf("files on disk/indexed = %d/%d, want 3/3", s.FilesOnDisk, s.FilesIndexed)
	}
	if s.LinesBad != 2 {
		t.Errorf("lines_bad = %d, want 2", s.LinesBad)
	}
	note := s.Note()
	if !strings.Contains(note, "index current") {
		t.Errorf("note does not say the index is current: %q", note)
	}
	if !strings.Contains(note, "malformed") {
		t.Errorf("note omits the malformed-line count, which is the coverage proof: %q", note)
	}
}

// T-13: a stale index still answers, and says how far behind it is. It does NOT
// quietly fix itself — a query that kicked off a multi-minute sweep is the
// surprise this machine's posture against self-starting background work exists to
// prevent.
func TestStalenessReportsBehindIndexWithoutIngesting(t *testing.T) {
	dbPath, root, db := buildIndex(t)

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}

	// A brand-new transcript nobody has indexed, plus an append to an indexed one.
	newFile := filepath.Join(root, "projB", "sess-fresh.jsonl")
	body := `{"type":"assistant","uuid":"f-0","sessionId":"S-FRESH","timestamp":"2026-07-28T00:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"new"}]}}` + "\n"
	if err := os.WriteFile(newFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	grown := filepath.Join(root, "projA", "sess-main.jsonl")
	f, err := os.OpenFile(grown, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s := Measure(context.Background(), db, root)
	if !s.Stale() {
		t.Fatal("index not reported stale after a new file and an append")
	}
	if s.FilesOnDisk != 4 {
		t.Errorf("files on disk = %d, want 4", s.FilesOnDisk)
	}
	if s.FilesIndexed != 3 {
		t.Errorf("files indexed = %d, want 3", s.FilesIndexed)
	}
	if s.FilesMissing != 1 {
		t.Errorf("files never indexed = %d, want 1", s.FilesMissing)
	}
	if s.FilesBehind != 1 {
		t.Errorf("files changed since indexing = %d, want 1", s.FilesBehind)
	}
	if s.BytesBehind != int64(2*len(body)) {
		t.Errorf("bytes behind = %d, want %d", s.BytesBehind, 2*len(body))
	}

	note := s.Note()
	for _, want := range []string{"BEHIND", "does NOT ingest", "pg-ccaudit ingest"} {
		if !strings.Contains(note, want) {
			t.Errorf("note omits %q: %s", want, note)
		}
	}

	// A query still answers, and measuring plus querying left the index alone.
	if _, rows := runNamed(t, db, root, "top-signatures", nil, "", ""); len(rows) != 3 {
		t.Errorf("%d rows from the stale index, want the 3 it already holds", len(rows))
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the index was modified by a query: size %d->%d, mtime %s->%s",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}

// An unreadable corpus root must not make the index unusable — only the
// comparison is unavailable.
func TestStalenessWithMissingRoot(t *testing.T) {
	_, _, db := buildIndex(t)
	s := Measure(context.Background(), db, filepath.Join(t.TempDir(), "does-not-exist"))
	if s.FilesOnDisk != 0 {
		t.Errorf("files on disk = %d, want 0", s.FilesOnDisk)
	}
	if got := s.Note(); !strings.Contains(got, "UNKNOWN") && !strings.Contains(got, "index current") {
		t.Errorf("unexpected note for a missing root: %q", got)
	}
}

func TestStalenessCountsOpenFiles(t *testing.T) {
	_, root, db := buildIndex(t)
	s := Measure(context.Background(), db, root)
	// buildIndex sweeps with the quiescence window disabled, so every fully
	// consumed file is final.
	if s.OpenFiles != 0 {
		t.Errorf("open files = %d, want 0", s.OpenFiles)
	}
}
