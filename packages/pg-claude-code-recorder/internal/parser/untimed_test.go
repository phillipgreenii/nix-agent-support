package parser_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// untimedFixture opens with a line that has no timestamp (a session's first
// line), then two timed lines, then an untimed line after the last timed one.
const untimedFixture = `{"type":"user","uuid":"u0","sessionId":"example-session-1","message":{"role":"user","content":"no time"}}
{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"first timed"}}
{"type":"user","uuid":"u2","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:05Z","message":{"role":"user","content":"last timed"}}
{"type":"user","uuid":"u3","sessionId":"example-session-1","message":{"role":"user","content":"untimed after"}}
`

// A record whose line has no timestamp stores time_ns 0 and own_time 0, never
// the time the recorder copied it; a timed record is unchanged; the session's
// first and last time ignore the untimed lines.
func TestUntimedRecordStoresNoTime(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", untimedFixture)
	k.Parse()

	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1`); n != 4 {
		t.Fatalf("%d line records, want 4", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND time_ns=0 AND own_time=0`); n != 2 {
		t.Fatalf("%d untimed line records with time_ns 0 and own_time 0, want 2", n)
	}
	want := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC).UnixNano()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE own_time=1 AND time_ns=?`, want); n != 1 {
		t.Fatalf("the first timed record does not keep its own time")
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item=-1 AND own_time=1`); n != 2 {
		t.Fatalf("%d timed line records, want 2", n)
	}
	last := time.Date(2026, 10, 29, 20, 0, 5, 0, time.UTC).UnixNano()
	if n := k.Count(`SELECT COUNT(*) FROM sessions WHERE session_id='example-session-1' AND first_time_ns=?`, want); n != 1 {
		t.Fatalf("session first time is not the first timed record")
	}
	if n := k.Count(`SELECT COUNT(*) FROM session_stats WHERE last_time_ns=?`, last); n != 1 {
		t.Fatalf("session last time is not the last timed record")
	}
	sessionTablesMatchRecords(t, k)
}

// A session whose first record is untimed gets its first time from the first
// timed record, whether that arrives in the same chunk or a later append.
func TestUntimedFirstLineDoesNotSetSessionFirstTime(t *testing.T) {
	k := testkit.New(t)
	id := k.Add("example-proj", "example-session-1.jsonl", `{"type":"user","uuid":"u0","sessionId":"example-session-1","message":{"role":"user","content":"no time"}}`+"\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM sessions WHERE session_id='example-session-1' AND first_time_ns=0`); n != 1 {
		t.Fatalf("an untimed first line set a first time")
	}
	k.Append(id, `{"type":"user","uuid":"u1","sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"first timed"}}`+"\n")
	k.Parse()
	want := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC).UnixNano()
	if n := k.Count(`SELECT COUNT(*) FROM sessions WHERE session_id='example-session-1' AND first_time_ns=?`, want); n != 1 {
		t.Fatalf("first time not filled from the first timed record")
	}
}
