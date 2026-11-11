package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// queryDaemon answers query and search with canned shapes and records the
// request bodies it saw.
func queryDaemon(t *testing.T, hasSession bool, hasText bool) (string, *[]string) {
	t.Helper()
	sock := filepath.Join(shortDir(t), "r.sock")
	var seen []string
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("query", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		seen = append(seen, "query "+string(b))
		rows := [][]any{}
		if hasSession {
			rows = [][]any{{"example-session-1", 3}}
		}
		return map[string]any{"as_of": "1:2", "columns": []string{"session", "records"}, "rows": rows, "more": false}, nil
	})
	s.Register("search", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		seen = append(seen, "search "+string(b))
		hits := []any{}
		if hasText {
			hits = []any{map[string]any{"field": "assistant_text", "snippet": "a [hit]"}}
		}
		return map[string]any{"as_of": "1:2", "hits": hits, "more": false}, nil
	})
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return sock, &seen
}

func TestQueryReadsStdinAppliesLimitAndPrintsJSONL(t *testing.T) {
	sock, seen := queryDaemon(t, true, false)
	a, out, _ := testApp(nil)
	a.stdin = strings.NewReader(`{"from":"records","where":{"field":"type","eq":"user"}}`)
	if code := a.run([]string{"query", "--socket", sock, "--limit", "7", "--jsonl"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains((*seen)[0], `"limit":7`) || !strings.Contains((*seen)[0], `"from":"records"`) {
		t.Fatalf("request %v", *seen)
	}
	var row map[string]any
	if err := json.Unmarshal(out.Bytes(), &row); err != nil || row["session"] != "example-session-1" {
		t.Fatalf("jsonl %q %v", out.String(), err)
	}
}

func TestSearchRequestFileAndPlainAnswer(t *testing.T) {
	sock, seen := queryDaemon(t, false, true)
	req := filepath.Join(shortDir(t), "req.json")
	if err := writeFile(req, `{"mode":"word","q":"hit"}`); err != nil {
		t.Fatal(err)
	}
	a, out, _ := testApp(nil)
	if code := a.run([]string{"search", "--socket", sock, "--request", req}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains((*seen)[0], `"q":"hit"`) || !strings.Contains(out.String(), `"hits"`) {
		t.Fatalf("seen %v out %q", *seen, out.String())
	}
}

func TestFindUsesSessionQueryThenTextSearch(t *testing.T) {
	sock, seen := queryDaemon(t, true, true)
	a, out, _ := testApp(nil)
	if code := a.run([]string{"find", "--socket", sock, "example-session-1"}); code != 0 || !strings.Contains(out.String(), `"kind":"session"`) {
		t.Fatalf("exit %d out %q", code, out.String())
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0], `"field":"session"`) {
		t.Fatalf("a session id is a query on the session key: %v", *seen)
	}
	sock2, seen2 := queryDaemon(t, false, true)
	a, out, _ = testApp(nil)
	if code := a.run([]string{"find", "--socket", sock2, "pg2-abc"}); code != 0 || !strings.Contains(out.String(), `"kind":"text"`) {
		t.Fatalf("text: %q", out.String())
	}
	if len(*seen2) != 2 || !strings.HasPrefix((*seen2)[1], "search ") {
		t.Fatalf("text goes to search: %v", *seen2)
	}
	sock3, _ := queryDaemon(t, false, false)
	a, _, errw := testApp(nil)
	if code := a.run([]string{"find", "--socket", sock3, "nothing"}); code != wire.ExitNotFound || !strings.Contains(errw.String(), "not_found") {
		t.Fatalf("not found: %d %q", code, errw.String())
	}
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o600) }
