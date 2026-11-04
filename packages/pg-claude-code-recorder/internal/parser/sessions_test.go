package parser_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// sessionTablesMatchRecords asserts the invariants of the session tables
// against records, the table they are derived from: every record references a
// sessions row for its own session id, session_stats holds each session's
// record count and newest time, and session_files holds exactly the files that
// have records of the session.
func sessionTablesMatchRecords(t *testing.T, k *testkit.Kit) {
	t.Helper()
	if n := k.Count(`SELECT COUNT(*) FROM records r LEFT JOIN sessions s ON s.session_ref=r.session_ref WHERE s.session_id IS NOT r.session`); n != 0 {
		t.Fatalf("%d records reference a missing or different session", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE session_ref=0`); n != 0 {
		t.Fatalf("%d records have session_ref 0", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM (
SELECT s.session_ref, COUNT(r.id) c, COALESCE(MAX(r.time_ns),0) m FROM sessions s LEFT JOIN records r ON r.session=s.session_id GROUP BY s.session_ref
EXCEPT SELECT session_ref, record_count, last_time_ns FROM session_stats)`); n != 0 {
		t.Fatalf("%d sessions whose stats differ from records", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM (SELECT session_ref, record_count, last_time_ns FROM session_stats
EXCEPT SELECT s.session_ref, COUNT(r.id), COALESCE(MAX(r.time_ns),0) FROM sessions s LEFT JOIN records r ON r.session=s.session_id GROUP BY s.session_ref)`); n != 0 {
		t.Fatalf("%d stats rows with no matching records", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM (
SELECT session_ref, file_id FROM records GROUP BY session_ref, file_id
EXCEPT SELECT session_ref, file_id FROM session_files)`); n != 0 {
		t.Fatalf("%d (session, file) pairs missing from session_files", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM (
SELECT session_ref, file_id FROM session_files
EXCEPT SELECT session_ref, file_id FROM records)`); n != 0 {
		t.Fatalf("%d session_files rows without records", n)
	}
}

func TestSessionTablesAreWrittenWithRecords(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.AddAgent("example-proj", "example-session-1", "a1", `{"type":"assistant","uuid":"x1","isSidechain":true,"sessionId":"example-session-1","message":{"id":"m9","role":"assistant","content":[{"type":"text","text":"sub"}]}}`+"\n")
	k.Add("example-proj", "example-session-2.jsonl", strings.ReplaceAll(transcriptFixture, "example-session-1", "example-session-2"))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM sessions`); n != 2 {
		t.Fatalf("sessions %d, want 2", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM session_files WHERE session_ref=(SELECT session_ref FROM sessions WHERE session_id='example-session-1')`); n != 2 {
		t.Fatalf("session 1 files %d, want 2 (transcript and subagent)", n)
	}
	if n := k.Count(`SELECT record_count FROM session_stats WHERE session_ref=(SELECT session_ref FROM sessions WHERE session_id='example-session-2')`); n != 11 {
		t.Fatalf("session 2 record_count %d, want 11 (the fixture's hand-computed count)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM sessions WHERE project='example-proj'`); n != 2 {
		t.Fatalf("sessions with their project %d, want 2", n)
	}
	sessionTablesMatchRecords(t, k)
}

func TestSessionRefsAreAssignedInFirstSeenOrderAndEmptySessionHasASentinel(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "history.jsonl", `{"display":"a","timestamp":1761768000000,"project":"/example/work","sessionId":"example-session-b"}`+"\n"+`{"display":"b","timestamp":1761768001000,"project":"/example/work"}`+"\n"+`{"display":"c","timestamp":1761768002000,"project":"/example/work","sessionId":"example-session-a"}`+"\n")
	k.Parse()
	if n := k.Count(`SELECT session_ref FROM sessions WHERE session_id='example-session-b'`); n != 1 {
		t.Fatalf("first session ref %d, want 1", n)
	}
	if n := k.Count(`SELECT session_ref FROM sessions WHERE session_id=''`); n != 2 {
		t.Fatalf("sentinel ref %d, want 2", n)
	}
	if n := k.Count(`SELECT session_ref FROM sessions WHERE session_id='example-session-a'`); n != 3 {
		t.Fatalf("third session ref %d, want 3", n)
	}
	sessionTablesMatchRecords(t, k)
}

func TestSessionTablesFollowAnAppendAndAReparse(t *testing.T) {
	k := testkit.New(t)
	l1 := `{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"first"}}` + "\n"
	l2 := `{"type":"user","uuid":"u2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:09Z","message":{"role":"user","content":"second"}}` + "\n"
	f := k.Add("example-proj", "example-session-1.jsonl", l1)
	k.Parse()
	if n := k.Count(`SELECT record_count FROM session_stats`); n != 2 {
		t.Fatalf("record_count %d, want 2 (a line and its text child)", n)
	}
	k.Append(f, l2)
	k.Parse()
	if n := k.Count(`SELECT record_count FROM session_stats`); n != 4 {
		t.Fatalf("record_count after append %d, want 4", n)
	}
	sessionTablesMatchRecords(t, k)
	ref := k.Count(`SELECT session_ref FROM sessions`)
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT record_count FROM session_stats`); n != 4 {
		t.Fatalf("record_count after reparse %d, want 4 (rows are replaced, not added)", n)
	}
	if n := k.Count(`SELECT session_ref FROM sessions`); n != ref || k.Count(`SELECT COUNT(*) FROM sessions`) != 1 {
		t.Fatalf("a reparse changed the session reference: %d, want %d", n, ref)
	}
	sessionTablesMatchRecords(t, k)
}

// A failed write bumps Store.RollbackEpoch because the rows it wrote may be
// gone; the session cache must be dropped with it, or a later record would
// reference a sessions row that no longer exists.
func TestSessionCacheIsDroppedAfterARolledBackWrite(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", transcriptFixture)
	k.Parse()
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM session_files`, `DELETE FROM session_stats`, `DELETE FROM records`, `DELETE FROM sessions`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Store.WithWrite("", func(*sql.Tx) error { return errors.New("undo") }); err == nil {
		t.Fatal("the failing write succeeded")
	}
	k.Add("example-proj", "example-session-3.jsonl", strings.ReplaceAll(transcriptFixture, "example-session-1", "example-session-3"))
	k.Parse()
	sessionTablesMatchRecords(t, k)
}
