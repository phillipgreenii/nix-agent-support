package query_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// noTrigramKit parses the fixture and then drops the trigram table, so any
// query that still reads it fails.
func noTrigramKit(t *testing.T) *testkit.Kit {
	t.Helper()
	k := kit(t)
	dropTrigram(t, k)
	return k
}

// dropTrigram removes fts_tri; call it after the last Parse (the parser still
// writes to it).
func dropTrigram(t *testing.T, k *testkit.Kit) {
	t.Helper()
	if err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TABLE fts_tri`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSubstringWorksWithoutTheTrigramTable(t *testing.T) {
	k := noTrigramKit(t)
	if n := k.Count(`SELECT count(*) FROM sqlite_master WHERE name='fts_tri'`); n != 0 {
		t.Fatalf("fts_tri still exists: %d", n)
	}
	// Hit in a tool input and in a tool result body, each with a match offset.
	a := s(t, k, `{"mode":"substring","q":"--no-verify"}`)
	if len(a.Hits) != 1 || a.Hits[0].Field != "tool_use.input" || a.Hits[0].MatchAt == nil ||
		*a.Hits[0].MatchAt != strings.Index(`{"command":"git commit --no-verify -m wip"}`, "--no-verify") ||
		!strings.Contains(a.Hits[0].Snippet, "--no-verify") || a.Partial {
		t.Fatalf("tool input: %s", js(a))
	}
	a = s(t, k, `{"mode":"substring","q":"ok don"}`)
	if len(a.Hits) != 1 || a.Hits[0].Field != "tool_result.body" || a.Hits[0].SessionID != "example-session-1" {
		t.Fatalf("tool result body: %s", js(a))
	}
	// A miss.
	if a := s(t, k, `{"mode":"substring","q":"never-written-text"}`); len(a.Hits) != 0 || a.Partial {
		t.Fatalf("miss: %s", js(a))
	}
	// Case sensitive.
	if a := s(t, k, `{"mode":"substring","q":"--NO-VERIFY"}`); len(a.Hits) != 0 {
		t.Fatalf("case-folded hit: %s", js(a))
	}
	// Prose is not an opaque field: a word of an assistant message is not found.
	if a := s(t, k, `{"mode":"substring","q":"registry file"}`); len(a.Hits) != 0 {
		t.Fatalf("prose matched: %s", js(a))
	}
	// The word path still works with the table gone.
	if a := s(t, k, `{"mode":"word","q":"registry"}`); len(a.Hits) == 0 {
		t.Fatal("word search broke")
	}
}

func TestSubstringScopedByFilter(t *testing.T) {
	k := kit(t)
	k.Add("example-proj", "example-session-2.jsonl", `{"type":"assistant","uuid":"v1","sessionId":"example-session-2","timestamp":"2026-10-30T10:00:00Z","message":{"id":"m9","model":"example-model-a","role":"assistant","content":[{"type":"tool_use","id":"tu_9","name":"Bash","input":{"command":"git push --no-verify"}}]}}
`)
	k.Parse()
	dropTrigram(t, k)
	if n := len(s(t, k, `{"mode":"substring","q":"--no-verify"}`).Hits); n != 2 {
		t.Fatalf("unscoped: %d", n)
	}
	a := s(t, k, `{"mode":"substring","q":"--no-verify","where":{"field":"session_id","eq":"example-session-2"}}`)
	if len(a.Hits) != 1 || a.Hits[0].SessionID != "example-session-2" {
		t.Fatalf("session scope: %s", js(a))
	}
	a = s(t, k, `{"mode":"substring","q":"--no-verify","where":{"field":"time","range":{"to":"2026-10-30T00:00:00Z"}}}`)
	if len(a.Hits) != 1 || a.Hits[0].SessionID != "example-session-1" {
		t.Fatalf("time scope: %s", js(a))
	}
	// Newest first.
	a = s(t, k, `{"mode":"substring","q":"--no-verify","limit":1}`)
	if len(a.Hits) != 1 || a.Hits[0].SessionID != "example-session-2" || !a.More {
		t.Fatalf("order: %s", js(a))
	}
}

func TestSubstringByteBudgetAnswersPartial(t *testing.T) {
	k := kit(t)
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","uuid":"w%d","sessionId":"example-session-3","timestamp":"2026-10-31T10:00:0%dZ","message":{"id":"mw%d","model":"example-model-a","role":"assistant","content":[{"type":"tool_use","id":"tw%d","name":"Bash","input":{"command":"echo filler-%d"}}]}}`+"\n", i, i, i, i, i)
	}
	k.Add("example-proj", "example-session-3.jsonl", b.String())
	k.Parse()
	dropTrigram(t, k)
	// Unbounded by default: the oldest hit (session 1) is found.
	if a := s(t, k, `{"mode":"substring","q":"--no-verify"}`); len(a.Hits) != 1 || a.Partial {
		t.Fatalf("default budget: %s", js(a))
	}
	// A budget that covers about two of the five newest bodies stops the scan,
	// and says how far it got; the older hit is not reported as a miss.
	k.Engine.SubstringBudget = 40
	a := s(t, k, `{"mode":"substring","q":"--no-verify"}`)
	if !a.Partial || a.ScannedBytes < 40 || a.ScannedTo == "" || len(a.Hits) != 0 || a.Note == "" {
		t.Fatalf("budget: %s", js(a))
	}
	// The scoped retry the note asks for completes within the same budget.
	a = s(t, k, `{"mode":"substring","q":"--no-verify","where":{"field":"session_id","eq":"example-session-1"}}`)
	if a.Partial || len(a.Hits) != 1 {
		t.Fatalf("scoped within budget: %s", js(a))
	}
}
