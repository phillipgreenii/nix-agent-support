package parser_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

func tline(uuid, extra string) string {
	return `{"type":"user","uuid":"` + uuid + `","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z",` + extra + `"message":{"role":"user","content":"hello"}}` + "\n"
}

func str(t *testing.T, k *testkit.Kit, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := k.Store.Read().QueryRowContext(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

// lookupsMatchRecords asserts every record's lookup ids resolve to the same
// strings that its fields rows hold (the values are still written there too).
func lookupsMatchRecords(t *testing.T, k *testkit.Kit) {
	t.Helper()
	for _, c := range []struct{ col, table, field string }{
		{"cwd_id", "lk_cwd", "cwd"},
		{"branch_id", "lk_branch", "git_branch"},
		{"version_id", "lk_version", "version"},
		{"entrypoint_id", "lk_entrypoint", "entrypoint"},
		{"user_type_id", "lk_user_type", "user_type"},
	} {
		q := `SELECT COUNT(*) FROM records r
LEFT JOIN ` + c.table + ` l ON l.id=r.` + c.col + `
LEFT JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='` + c.field + `')
WHERE l.v IS NOT f.s`
		if n := k.Count(q); n != 0 {
			t.Fatalf("%s: %d records whose lookup value differs from their fields row", c.col, n)
		}
	}
}

func TestRecordsCarryInternedLookupIdsAndNullWhenAbsent(t *testing.T) {
	k := testkit.New(t)
	full := `"cwd":"/example/work","gitBranch":"example-branch","version":"0.0.1","entrypoint":"cli","userType":"external",`
	k.Add("example-proj", "example-session-1.jsonl", tline("u1", full)+tline("u2", full)+tline("u3", `"cwd":"/example/other","version":"0.0.2",`)+tline("u4", ""))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM lk_cwd`); n != 2 {
		t.Fatalf("lk_cwd rows %d, want 2 (a value is interned once)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM lk_branch`); n != 1 {
		t.Fatalf("lk_branch rows %d, want 1", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND cwd_id IS NOT NULL`); n != 3 {
		t.Fatalf("line records with a cwd id %d, want 3", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND branch_id IS NOT NULL AND user_type_id IS NOT NULL`); n != 2 {
		t.Fatalf("line records with branch and user type ids %d, want 2", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND cwd_id IS NULL AND branch_id IS NULL AND version_id IS NULL AND entrypoint_id IS NULL AND user_type_id IS NULL`); n != 1 {
		t.Fatalf("line records with no ids %d, want 1 (u4 carries none)", n)
	}
	if got := str(t, k, `SELECT v FROM lk_version WHERE id=(SELECT version_id FROM records WHERE item=-1 ORDER BY id DESC LIMIT 1 OFFSET 1)`); got != "0.0.2" {
		t.Fatalf("u3 version %q", got)
	}
	lookupsMatchRecords(t, k)
}

func TestSessionFirstLookupIdsComeFromTheFirstRecordThatCarriesThem(t *testing.T) {
	k := testkit.New(t)
	// u1 has no version; u2 is the first with one; u3 changes it.
	f := k.Add("example-proj", "example-session-1.jsonl", tline("u1", `"entrypoint":"cli",`)+tline("u2", `"version":"0.0.1",`)+tline("u3", `"version":"0.0.2","userType":"external",`))
	k.Parse()
	if got := str(t, k, `SELECT v FROM lk_version WHERE id=(SELECT first_version_id FROM sessions WHERE session_id='example-session-1')`); got != "0.0.1" {
		t.Fatalf("first version %q, want 0.0.1", got)
	}
	if got := str(t, k, `SELECT v FROM lk_entrypoint WHERE id=(SELECT entrypoint_id FROM sessions WHERE session_id='example-session-1')`); got != "cli" {
		t.Fatalf("entrypoint %q", got)
	}
	if got := str(t, k, `SELECT v FROM lk_user_type WHERE id=(SELECT user_type_id FROM sessions WHERE session_id='example-session-1')`); got != "external" {
		t.Fatalf("user type %q", got)
	}
	// A later batch never changes what the first one filled.
	k.Append(f, tline("u4", `"version":"0.0.3",`))
	k.Parse()
	if got := str(t, k, `SELECT v FROM lk_version WHERE id=(SELECT first_version_id FROM sessions WHERE session_id='example-session-1')`); got != "0.0.1" {
		t.Fatalf("first version after an append %q, want 0.0.1", got)
	}
}

func TestLookupRowsSurviveAReparseAndKeepTheirIds(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", "example-session-1.jsonl", tline("u1", `"cwd":"/example/work","gitBranch":"example-branch",`))
	k.Parse()
	id := k.Count(`SELECT id FROM lk_cwd WHERE v='/example/work'`)
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM lk_cwd`); n != 1 || k.Count(`SELECT cwd_id FROM records WHERE item=-1`) != id {
		t.Fatalf("a reparse changed the lookup rows or ids")
	}
	lookupsMatchRecords(t, k)
}

// A rolled-back write bumps Store.RollbackEpoch because the lookup rows it
// wrote may be gone and ids reused; the cache must be dropped with it.
func TestLookupCacheIsDroppedAfterARolledBackWrite(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", tline("u1", `"cwd":"/example/work",`))
	k.Parse()
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM session_files`, `DELETE FROM session_stats`, `DELETE FROM records`, `DELETE FROM sessions`, `DELETE FROM lk_cwd`} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
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
	k.Add("example-proj", "example-session-3.jsonl", tline("u9", `"cwd":"/example/work",`))
	k.Parse()
	lookupsMatchRecords(t, k)
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE cwd_id IS NOT NULL AND cwd_id NOT IN (SELECT id FROM lk_cwd)`); n != 0 {
		t.Fatalf("%d records reference a missing lookup row", n)
	}
}
