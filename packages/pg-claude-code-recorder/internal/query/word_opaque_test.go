package query_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// wordKit parses the fixture into an index.db built with the new layout: no
// trigram table, opaque fields in the word index.
func wordKit(t *testing.T) *testkit.Kit {
	t.Helper()
	k := testkit.NewWithStoreOptions(t, store.Options{NoTrigramIndex: true})
	k.Add("example-proj", "example-session-1.jsonl", fixture)
	k.Parse()
	return k
}

func TestNewLayoutHasNoTrigramTable(t *testing.T) {
	k := wordKit(t)
	if k.Store.HasTrigram() {
		t.Fatal("HasTrigram on the new layout")
	}
	if n := k.Count(`SELECT count(*) FROM sqlite_master WHERE name='fts_tri'`); n != 0 {
		t.Fatalf("fts_tri exists: %d", n)
	}
	if n := k.Count(`SELECT count(*) FROM sqlite_master WHERE name='fts_word'`); n != 1 {
		t.Fatalf("fts_word missing: %d", n)
	}
	// Reopening keeps the layout (no fts_tri is created behind the option).
	k.Reopen()
	if n := k.Count(`SELECT count(*) FROM sqlite_master WHERE name='fts_tri'`); n != 0 {
		t.Fatalf("fts_tri after reopen: %d", n)
	}
}

func TestRunningLayoutKeepsTrigramTable(t *testing.T) {
	k := kit(t)
	if !k.Store.HasTrigram() {
		t.Fatal("v2 layout lost its trigram table")
	}
	// Even with the option, a file that has fts_tri keeps it and its routing.
	k.StoreOptions = store.Options{NoTrigramIndex: true}
	k.Reopen()
	if !k.Store.HasTrigram() || k.Count(`SELECT count(*) FROM sqlite_master WHERE name='fts_tri'`) != 1 {
		t.Fatal("an existing fts_tri was dropped by the option")
	}
	// Word search does not reach tool bodies in the running layout.
	if a := s(t, k, `{"mode":"word","q":"no verify","fields":["tool_use.input"]}`); len(a.Hits) != 0 {
		t.Fatalf("v2 word hits: %s", js(a))
	}
}

func TestWordSearchFindsToolBodies(t *testing.T) {
	k := wordKit(t)
	// A phrase in tool_use.input: punctuation splits tokens, so --no-verify is
	// the phrase "no verify".
	a := s(t, k, `{"mode":"word","q":"\"no verify\""}`)
	if len(a.Hits) != 1 || a.Hits[0].Field != "tool_use.input" {
		t.Fatalf("tool input phrase: %s", js(a))
	}
	a = s(t, k, `{"mode":"word","q":"--no-verify","fields":["tool_use.input"]}`)
	if len(a.Hits) != 1 || a.Hits[0].Field != "tool_use.input" || !strings.Contains(a.Hits[0].Snippet, "[no]-[verify]") {
		t.Fatalf("opaque field name: %s", js(a))
	}
	// A phrase and a bare word in tool_result.body, case-insensitive.
	a = s(t, k, `{"mode":"word","q":"\"OK done\"","fields":["tool_result.body"]}`)
	if len(a.Hits) != 1 || a.Hits[0].Field != "tool_result.body" || a.Hits[0].SessionID != "example-session-1" {
		t.Fatalf("tool result phrase: %s", js(a))
	}
	// Words are whole tokens: a partial token is a substring-mode lookup.
	if a := s(t, k, `{"mode":"word","q":"verif","fields":["tool_use.input"]}`); len(a.Hits) != 0 {
		t.Fatalf("partial token matched: %s", js(a))
	}
	// A word that is not in the field's text does not match it.
	if a := s(t, k, `{"mode":"word","q":"commit","fields":["tool_result.body"]}`); len(a.Hits) != 0 {
		t.Fatalf("wrong field: %s", js(a))
	}
}

func TestSubstringStillWorksOnNewLayout(t *testing.T) {
	k := wordKit(t)
	if a := s(t, k, `{"mode":"substring","q":"--no-verify"}`); len(a.Hits) != 1 {
		t.Fatalf("substring: %s", js(a))
	}
}
