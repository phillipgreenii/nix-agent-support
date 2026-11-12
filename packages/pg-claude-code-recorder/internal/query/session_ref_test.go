package query_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// sessionRefKit parses three sessions (one with a subagent file, one line with
// no session id at all) on a store created empty, which marks it ready for
// session_ref.
func sessionRefKit(t *testing.T) *testkit.Kit {
	t.Helper()
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", fixture)
	k.AddAgent("example-proj", "example-session-1", "a1", `{"type":"assistant","uuid":"x1","isSidechain":true,"sessionId":"example-session-1","timestamp":"2026-10-29T20:01:00Z","message":{"id":"m9","role":"assistant","content":[{"type":"text","text":"registry subagent words"}]}}`+"\n")
	k.Add("example-proj", "example-session-2.jsonl", strings.ReplaceAll(fixture, "example-session-1", "example-session-2"))
	k.Add("example-proj", "example-session-3.jsonl", `{"type":"user","uuid":"n1","timestamp":"2026-10-29T21:00:00Z","message":{"role":"user","content":"a line with no session id registry"}}`+"\n")
	k.Parse()
	// One record of each linked type, so follow by session has targets.
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `UPDATE records SET type='status' WHERE id IN (SELECT MIN(id) FROM records WHERE item=-1 GROUP BY session)`); err != nil {
			return err
		}
		// A record of the empty session, which has the sentinel sessions row.
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO sessions(session_id,first_time_ns) VALUES('',0) ON CONFLICT(session_id) DO NOTHING`); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `UPDATE records SET session='', session_ref=(SELECT session_ref FROM sessions WHERE session_id='') WHERE id=(SELECT MAX(id) FROM records WHERE item=0)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// textEngine is k's engine reading the text session column, the way a store
// that is not marked ready does.
func textEngine(k *testkit.Kit) *query.Engine {
	e := *k.Engine
	e.SessionRef = nil
	return &e
}

var sessionRequests = []string{
	`{"where":{"field":"session_id","eq":"example-session-1"},"select":["type","session_id","time"],"order":[{"by":"time"},{"by":"type"}]}`,
	`{"where":{"field":"session_id","eq":"no-such-session"},"select":["type"]}`,
	`{"where":{"field":"session_id","in":["example-session-1","example-session-3","absent"]},"select":["type","session_id"],"order":[{"by":"session_id"},{"by":"time"}]}`,
	`{"where":{"field":"session_id","in":[]},"select":["type"]}`,
	`{"where":{"not":{"field":"session_id","eq":"example-session-2"}},"select":["session_id"],"group":["session_id"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"session_id"}]}`,
	`{"where":{"field":"session_id","contains":"session-2"},"select":["type","session_id"],"order":[{"by":"time"},{"by":"type"}]}`,
	`{"where":{"field":"session_id","range":{"from":"example-session-2"}},"select":["session_id"],"group":["session_id"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"session_id","dir":"desc"}]}`,
	`{"where":{"field":"session_id","exists":true},"group":["session_id","type"],"aggregate":[{"fn":"count","as":"n"}],"order":[{"by":"session_id"},{"by":"type"}]}`,
	`{"where":{"field":"session_id","eq":""},"select":["type","session_id"]}`,
	`{"where":{"all":[{"field":"session_id","eq":"example-session-1"},{"field":"item","eq":-1}]},"group":["file","project"],"aggregate":[{"fn":"count"}],"order":[{"by":"project"}]}`,
	`{"where":{"field":"type","eq":"assistant"},"select":["uuid","session_id"],"order":[{"by":"session_id","dir":"desc"},{"by":"uuid"}]}`,
	`{"where":{"field":"item","eq":-1},"select":["uuid","session"],"follow":"status"}`,
}

func runBoth(t *testing.T, k *testkit.Kit, req string) (string, string) {
	t.Helper()
	var r query.Request
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		t.Fatal(err)
	}
	run := func(e *query.Engine) string {
		a, err := e.Query(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: %v", req, err)
		}
		a.AsOf = ""
		return js(a)
	}
	return run(textEngine(k)), run(k.Engine)
}

// On a real store the query layer answers the same through session_ref and
// sessions as through records.session, for every way a request can name the
// session.
func TestQueriesThroughSessionRefEqualTheTextSessionPath(t *testing.T) {
	k := sessionRefKit(t)
	if !k.Store.SessionRefReady() {
		t.Fatal("the fixture store is not marked ready")
	}
	for _, req := range sessionRequests {
		text, ref := runBoth(t, k, req)
		if text != ref {
			t.Errorf("%s\n text: %s\n  ref: %s", req, text, ref)
		}
	}
	// A nonempty answer, so equality is not two empty results.
	if a := q(t, k, sessionRequests[0]); len(a.Rows) == 0 {
		t.Fatal("the session filter matched nothing")
	}
	if a := q(t, k, sessionRequests[8]); len(a.Rows) == 0 {
		t.Fatal("the empty session matched nothing: the sentinel session is not read")
	}
	if a := q(t, k, sessionRequests[len(sessionRequests)-1]); len(a.Rows) == 0 {
		t.Fatal("follow by session found nothing")
	}
}

func TestSearchThroughSessionRefEqualsTheTextSessionPath(t *testing.T) {
	k := sessionRefKit(t)
	for _, req := range []string{
		`{"q":"registry"}`,
		`{"q":"registry","where":{"field":"session_id","eq":"example-session-1"}}`,
		`{"mode":"substring","q":"git commit"}`,
		`{"mode":"substring","q":"git commit","where":{"field":"session_id","in":["example-session-1","example-session-2"]}}`,
	} {
		var r query.SearchRequest
		if err := json.Unmarshal([]byte(req), &r); err != nil {
			t.Fatal(err)
		}
		run := func(e *query.Engine) string {
			a, err := e.Search(context.Background(), r)
			if err != nil {
				t.Fatalf("%s: %v", req, err)
			}
			return js(a)
		}
		text, ref := run(textEngine(k)), run(k.Engine)
		if text != ref {
			t.Errorf("%s\n text: %s\n  ref: %s", req, text, ref)
		}
		var a query.SearchAnswer
		_ = json.Unmarshal([]byte(ref), &a)
		if len(a.Hits) == 0 {
			t.Errorf("%s: no hits, equality proves nothing", req)
		}
	}
}

// A store that predates session_ref has older rows with session_ref 0 and no
// session rows. Its engine is not marked ready, so it keeps answering from the
// text column; an engine wrongly told to read session_ref on it would lose
// every one of those rows, which is why the gate exists.
func TestAStoreWithUnreferencedRowsKeepsAnsweringFromTheTextSession(t *testing.T) {
	k := sessionRefKit(t)
	want := map[string]string{}
	for _, req := range sessionRequests {
		text, _ := runBoth(t, k, req)
		want[req] = text
	}
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{`UPDATE records SET session_ref=0`, `DELETE FROM session_files`, `DELETE FROM session_stats`, `DELETE FROM sessions`, `DELETE FROM meta WHERE key='session_ref_complete'`} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	k.Reopen()
	if k.Store.SessionRefReady() {
		t.Fatal("a store without the mark is ready")
	}
	for _, req := range sessionRequests {
		if got := q(t, k, req); func() string { got.AsOf = ""; return js(got) }() != want[req] {
			t.Errorf("%s: the answer changed on a store with unreferenced rows", req)
		}
	}
	// The gate is what protects it: the same engine told session_ref is ready
	// loses the rows.
	forced := *k.Engine
	forced.SessionRef = func() bool { return true }
	var r query.Request
	_ = json.Unmarshal([]byte(sessionRequests[0]), &r)
	if a, err := forced.Query(context.Background(), r); err != nil || len(a.Rows) != 0 {
		t.Fatalf("forced session_ref on unreferenced rows: %v rows, %v; the test no longer shows the gate matters", a, err)
	}
}
