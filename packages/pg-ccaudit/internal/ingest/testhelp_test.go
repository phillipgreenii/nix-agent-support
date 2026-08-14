package ingest

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// Hand-computed expectations for the committed fixture corpus
// (testdata/corpus). Every number below was derived by reading the three JSONL
// files line by line; nothing here is a value copied back out of the code.
//
//	projA/sess-main.jsonl  session S-MAIN, is_sidechain 0, 10 lines, all valid
//	  seq 0 assistant tool_use  toolu_A1 Bash  "sudo rtk find foo"   -> lead_cmd rtk
//	  seq 1 user      result    toolu_A1 ERROR "<tool_use_error>rtk: command not found</tool_use_error>"
//	  seq 2 assistant text      "rtk is not on PATH here; ..."       -> narration at seq 1+1
//	  seq 3 assistant tool_use  toolu_A2 Bash  "rtk find foo"        -> retry of A1, gap 3
//	  seq 4 user      result    toolu_A2 ERROR same body             -> same signature
//	  seq 5 assistant tool_use  toolu_A3 Read
//	  seq 6 user      result    toolu_A3 is_error FALSE, body "file body here" (14 runes)
//	  seq 7 system              durationMs 1234, hookCount 3, hookErrors ["ceta: denied"]
//	  seq 8 assistant tool_use  toolu_A4 Bash  "VAR=1 nice sleep 5"  -> lead_cmd sleep
//	  seq 9 user      result    toolu_A4 ERROR "Blocked: sleep 5"    -> signature "Blocked: sleep N"
//
//	projA/sess-sub.jsonl   session S-SUB, is_sidechain 1, 4 lines, all valid
//	  seq 0/2 assistant tool_use toolu_B1/B2 Bash "sleep 30"  -> identical inputs
//	  seq 1/3 user      result   both ERROR "Blocked: sleep 30" -> "Blocked: sleep N"
//
//	projB/sess-bad.jsonl   session S-BAD, is_sidechain 0, 9 lines: 7 valid, 2 bad
//	  seq 0 assistant tool_use  toolu_C1 Read {"file_path":"/home/nope/x"}
//	  seq 1 user      result    toolu_C1 ERROR "File does not exist: /home/nope/x"
//	                                            -> signature "File does not exist: PATH"
//	  seq 2 assistant text      "That root does not exist ..."  -> narration at seq 1+1
//	  seq 3 TRUNCATED JSON                                      -> lines_bad
//	  seq 4 BLANK LINE                                          -> lines_bad
//	  seq 5 assistant tool_use  toolu_C2 Grep
//	  seq 6 user      result    toolu_C2 is_error FALSE, body ["ok"] -> 2 runes
//	  seq 7 system              hookCount 5, hookErrors []      -> stored, filtered by query
//	  seq 8 system              hookCount 2, hookErrors ["ceta: denied"]
const (
	wantFiles       = 3
	wantLinesOK     = 21 // 10 + 4 + 7
	wantLinesBad    = 2  // the truncated line and the blank line, both in sess-bad
	wantEvents      = 21 // one per VALID line
	wantToolCalls   = 8  // A1 A2 A3 A4 + B1 B2 + C1 C2
	wantToolResults = 8
	wantErrors      = 6 // A1 A2 A4 + B1 B2 + C1
	wantNarration   = 2 // sess-main seq 2, sess-bad seq 2
)

const (
	sigRTK     = "<tool_use_error>rtk: command not found</tool_use_error>"
	sigSleep   = "Blocked: sleep N"
	sigMissing = "File does not exist: PATH"
)

// fixtureCorpus copies the committed corpus into a fresh temp dir so a test may
// append to or truncate a transcript without ever mutating the checked-in
// fixture — and so no test can accidentally reach the real corpus.
func fixtureCorpus(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	src := filepath.Join("testdata", "corpus")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture corpus: %v", err)
	}
	return root
}

func openTestDB(t *testing.T, withThinking bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcripts.db")
	db, err := store.Open(path, withThinking)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

// sweep runs one ingest with the quiescence window disabled, so `complete`
// depends only on whether every visible byte was consumed.
func sweep(t *testing.T, db *sql.DB, root string) Stats {
	t.Helper()
	return sweepWith(t, db, Options{Root: root, FinalAfter: FinalAfterImmediate})
}

func sweepWith(t *testing.T, db *sql.DB, opt Options) Stats {
	t.Helper()
	if opt.Progress == nil {
		opt.Progress = io.Discard
	}
	stats, err := Run(context.Background(), db, opt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return stats
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func mainTranscript(root string) string {
	return filepath.Join(root, "projA", "sess-main.jsonl")
}

// bumpMtime moves a file's mtime forward so a same-size rewrite is still visible
// as a change, and backward to simulate a rewrite.
func setMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer func() { _ = f.Close() }()
	for _, l := range lines {
		if _, err := f.WriteString(strings.TrimRight(l, "\n") + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}
