package parser_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const askSess = "example-session-1"

func askLine(ts, id string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"q-%s","sessionId":"%s","timestamp":"%s","message":{"id":"m-%s","role":"assistant","content":[{"type":"tool_use","id":"%s","name":"AskUserQuestion","input":{}}]}}`+"\n", id, askSess, ts, id, id)
}

func otherToolLine(ts, id string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"o-%s","sessionId":"%s","timestamp":"%s","message":{"id":"m-%s","role":"assistant","content":[{"type":"tool_use","id":"%s","name":"Bash","input":{}}]}}`+"\n", id, askSess, ts, id, id)
}

func answerLine(ts, id string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"r-%s","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"%s","content":"ok"}]}}`+"\n", id, askSess, ts, id)
}

func askNow(t *testing.T, k *testkit.Kit) store.AskFacts {
	t.Helper()
	a, err := k.Store.AskFacts(context.Background(), askSess, k.Store.SessionRefReady())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var askTranscript = "" +
	askLine("2026-01-01T00:00:01Z", "t1") + answerLine("2026-01-01T00:00:02Z", "t1") +
	otherToolLine("2026-01-01T00:00:03Z", "b1") + answerLine("2026-01-01T00:00:04Z", "b1") +
	askLine("2026-01-01T00:00:05Z", "t2") + answerLine("2026-01-01T00:00:06Z", "t2") +
	askLine("2026-01-01T00:00:07Z", "t3")

func TestAskFactsFollowTheTranscript(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", askSess+".jsonl", askTranscript)
	k.Parse()
	if a := askNow(t, k); !a.Known || !a.Open || a.ToolUseID != "t3" {
		t.Fatalf("open question: %+v", a)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ask_questions`); n != 3 {
		t.Fatalf("%d question rows, want 3 (Bash is not a question)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ask_answers`); n != 2 {
		t.Fatalf("%d answer rows, want 2 (only answers to questions)", n)
	}
}

func TestAskFactsSubagentTranscriptIsNotTheSessions(t *testing.T) {
	k := testkit.New(t)
	k.AddAgent("example-proj", askSess, "agent1", askLine("2026-01-01T00:00:01Z", "sub1"))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM ask_questions`); n != 0 {
		t.Fatalf("%d question rows from a subagent file, want 0", n)
	}
}

func TestAskFactsAnswerClosesTheQuestionAndAReparseKeepsTheTables(t *testing.T) {
	k := testkit.New(t)
	f := k.Add("example-proj", askSess+".jsonl", askLine("2026-01-01T00:00:01Z", "t1"))
	k.Parse()
	if a := askNow(t, k); !a.Open {
		t.Fatalf("unanswered: %+v", a)
	}
	k.Append(f, answerLine("2026-01-01T00:00:02Z", "t1"))
	k.Parse()
	if a := askNow(t, k); a.Open {
		t.Fatalf("answered: %+v", a)
	}
	if err := k.Parser.ReparseGen(f, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ask_questions`); n != 1 {
		t.Fatalf("%d question rows after a reparse, want 1 (replaced, not added)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM ask_answers`); n != 1 {
		t.Fatalf("%d answer rows after a reparse, want 1", n)
	}
	if a := askNow(t, k); a.Open {
		t.Fatalf("after reparse: %+v", a)
	}
}

func TestAskFactsUnknownSession(t *testing.T) {
	k := testkit.New(t)
	a, err := k.Store.AskFacts(context.Background(), "nope", true)
	if err != nil || a.Known {
		t.Fatalf("%+v %v", a, err)
	}
}

// The backfill of a database that existed before the tables must reach the same
// answer as the live parse, over more than one range, and mark the tables
// complete only at its end.
func TestBackfillAskFactsMatchesTheLiveTables(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", askSess+".jsonl", askTranscript)
	k.Add("example-proj", "example-session-2.jsonl", strings.ReplaceAll(askTranscript, askSess, "example-session-2"))
	k.AddAgent("example-proj", askSess, "agent1", askLine("2026-01-01T00:00:09Z", "sub1"))
	k.Parse()
	want := askNow(t, k)
	dump := func() string {
		var b strings.Builder
		for _, q := range []string{`SELECT rec_id,session_ref,time_ns,tool_use_id FROM ask_questions ORDER BY rec_id`, `SELECT rec_id,tool_use_id FROM ask_answers ORDER BY rec_id`} {
			rs, err := k.Store.Read().QueryContext(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rs.Columns()
			for rs.Next() {
				v := make([]any, len(cols))
				p := make([]any, len(cols))
				for i := range v {
					p[i] = &v[i]
				}
				if err := rs.Scan(p...); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintln(&b, v...)
			}
			_ = rs.Close()
			b.WriteString("--\n")
		}
		return b.String()
	}
	live := dump()
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM ask_questions`, `DELETE FROM ask_answers`, `DELETE FROM meta WHERE key LIKE 'ask_facts%'`} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Store.AskFacts(context.Background(), askSess, true); err != store.ErrAskFactsUnavailable {
		t.Fatalf("before the backfill: %v, want unavailable", err)
	}
	done, steps := false, 0
	for !done {
		if steps++; steps > 10 {
			t.Fatal("backfill does not finish")
		}
		if done, err = k.Parser.BackfillAskFacts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := dump(); got != live {
		t.Fatalf("backfilled tables differ from the live ones:\n%s\nlive:\n%s", got, live)
	}
	if got := askNow(t, k); got != want {
		t.Fatalf("answer %+v, want %+v", got, want)
	}
}

// Plan guard: the read names only the small ask tables and searches them by
// index, so its cost does not depend on how many records the session has.
func TestAskFactsReadPlanUsesTheAskIndexes(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", askSess+".jsonl", askTranscript)
	k.Parse()
	rs, err := k.Store.Read().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+store.AskFactsSQL, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	var lines []string
	for rs.Next() {
		var id, parent, unused int
		var d string
		if err := rs.Scan(&id, &parent, &unused, &d); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, d)
	}
	p := strings.Join(lines, "\n")
	for _, want := range []string{"SEARCH q USING INDEX ask_questions_session (session_ref=?)", "SEARCH a USING COVERING INDEX ask_answers_tool (tool_use_id=?)"} {
		if !strings.Contains(p, want) {
			t.Errorf("plan lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "records") || strings.Contains(p, "SCAN") || strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("plan touches records or scans a table:\n%s", p)
	}
}
