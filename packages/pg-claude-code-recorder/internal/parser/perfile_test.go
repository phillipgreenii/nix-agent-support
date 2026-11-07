package parser_test

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// ParseAll resolves a file's path once per file, not
// once per generation, and does not fetch the file again after AllFiles.
func TestParseAllQueriesOncePerFile(t *testing.T) {
	const files = 6
	c := countdriver.Register("parseall-once-per-file")
	k := testkit.New(t)
	k.StoreOptions = store.Options{DriverName: "parseall-once-per-file"}
	k.Reopen()
	for i := 0; i < files; i++ {
		body := strings.ReplaceAll(transcriptFixture, "example-session-1", fmt.Sprintf("example-session-%d", i))
		id := k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", i), body)
		k.NewGeneration(id, body) // a second generation with bytes to parse
	}
	c.Reset()
	k.Parse()
	if n := c.Matching("FROM paths WHERE file_id=? AND closed_ns IS NULL"); n != files {
		t.Fatalf("CurrentPaths ran %d times for %d files with 2 generations each", n, files)
	}
	if n := c.Matching("FROM commits"); n != 0 {
		t.Fatalf("the parser ran %d statements against the removed commits table", n)
	}
	// OpenPointer's own mode look-up reads only `mode`; the File query reads
	// the whole row (id, source, mode, ...).
	if n := c.Matching("deleted_ns FROM files WHERE id=?"); n != 0 {
		t.Fatalf("ParseAll re-fetched %d files that AllFiles returned", n)
	}
	if k.Count(`SELECT COUNT(*) FROM generations WHERE parse_offset<copy_offset`) != 0 {
		t.Fatal("a generation was left unparsed")
	}
}

// A generation whose parse_state version is old is re-parsed by reparseStale,
// which advances parse_offset; the parse after it MUST read the new offset, or
// it parses the same bytes again and inserts duplicates.
func TestParseAllAfterReparseStaleParsesNothingTwice(t *testing.T) {
	k := testkit.New(t)
	first := strings.Split(strings.TrimRight(transcriptFixture, "\n"), "\n")
	half := strings.Join(first[:len(first)/2], "\n") + "\n"
	rest := strings.Join(first[len(first)/2:], "\n") + "\n"
	id := k.Add("example-proj", "example-session-1.jsonl", half)
	k.Parse()
	// The generation is now behind: an older parser version and unparsed bytes.
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE parse_state SET version=0`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	k.Append(id, rest)
	k.Parse() // must neither fail nor duplicate

	want := testkit.New(t)
	want.Add("example-proj", "example-session-1.jsonl", half+rest)
	want.Parse()
	for _, q := range []string{
		`SELECT COUNT(*) FROM records`,
		`SELECT COUNT(*) FROM (SELECT DISTINCT file_id,gen,off,item FROM records)`,
		`SELECT COUNT(*) FROM generations WHERE parse_offset<copy_offset`,
	} {
		if g, w := k.Count(q), want.Count(q); g != w {
			t.Fatalf("%s: %d, a clean parse gives %d", q, g, w)
		}
	}
	if k.Count(`SELECT COUNT(*) FROM records`) == 0 {
		t.Fatal("no records parsed")
	}
}

// A version-bump re-parse on a store that already holds the generation's rows
// completes without a UNIQUE failure and leaves each row once.
func TestVersionBumpReparseOverExistingRowsCompletes(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	before := k.Count(`SELECT COUNT(*) FROM records`)
	if before == 0 {
		t.Fatal("no records parsed")
	}
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE parse_state SET version=0`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Parser.ParseAll(); err != nil {
		t.Fatalf("re-parse over existing rows: %v", err)
	}
	if after := k.Count(`SELECT COUNT(*) FROM records`); after != before {
		t.Fatalf("records %d after re-parse, %d before", after, before)
	}
	if k.Count(`SELECT COUNT(*) FROM parse_state WHERE version=0`) != 0 {
		t.Fatal("generation left at the old version")
	}
}

// ParseAllUntil gives up between files once stop closes, leaving the stale
// generation at its old version for the next start.
func TestParseAllUntilStopsAndLeavesStaleGenerations(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE parse_state SET version=0`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	close(stop)
	if err := k.Parser.ParseAllUntil(stop); !errors.Is(err, parser.ErrParseStopped) {
		t.Fatalf("err = %v, want ErrParseStopped", err)
	}
	if k.Count(`SELECT COUNT(*) FROM parse_state WHERE version=0`) == 0 {
		t.Fatal("a stopped parse re-parsed anyway")
	}
}
