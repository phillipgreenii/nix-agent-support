package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// feedDaemon answers feed, snapshot and raw with scripted shapes and records
// the request bodies.
func feedDaemon(t *testing.T, feedAns func(n int) (any, *wire.Error)) (string, *[]string) {
	t.Helper()
	sock := filepath.Join(shortDir(t), "r.sock")
	var seen []string
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	calls := 0
	s.Register("feed", server.KindFeed, func(_ context.Context, b []byte) (any, *wire.Error) {
		seen = append(seen, "feed "+string(b))
		calls++
		return feedAns(calls)
	})
	s.Register("snapshot", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		seen = append(seen, "snapshot "+string(b))
		if strings.Contains(string(b), `"page":"r2"`) {
			return map[string]any{"cursor": "1:8", "entries": []any{map[string]any{"seq": "1:6", "kind": "record"}}, "more": false}, nil
		}
		return map[string]any{"cursor": "1:8", "entries": []any{map[string]any{"seq": "1:2", "kind": "record"}}, "more": true, "next_page": "r2"}, nil
	})
	s.Register("raw", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		seen = append(seen, "raw "+string(b))
		return map[string]any{"file": "f_1", "gen": 1, "offset": 0, "length": 3, "encoding": "utf8", "data": "abc"}, nil
	})
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return sock, &seen
}

func TestFeedSendsTheCursorWaitAndFilterAndExitsSixWhenTheWaitEndsEmpty(t *testing.T) {
	sock, seen := feedDaemon(t, func(n int) (any, *wire.Error) {
		if n == 1 {
			return map[string]any{"entries": []any{map[string]any{"seq": "1:2", "kind": "record"}}, "next_cursor": "1:2", "more": false, "timed_out": false}, nil
		}
		return map[string]any{"entries": []any{}, "next_cursor": "1:9", "more": false, "timed_out": true}, nil
	})
	a, out, _ := testApp(nil)
	if code := a.run([]string{"feed", "--socket", sock, "--after", "1:1", "--wait", "30s", "--limit", "5", "--project", "type,session", "--where", `{"field":"type","eq":"assistant"}`}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{`"after":"1:1"`, `"wait_ms":30000`, `"limit":5`, `"project":["type","session"]`, `"where":{"eq":"assistant","field":"type"}`} {
		if !strings.Contains((*seen)[0], want) {
			t.Errorf("request %s lacks %s", (*seen)[0], want)
		}
	}
	if !strings.Contains(out.String(), `"next_cursor":"1:2"`) {
		t.Fatalf("answer %q", out.String())
	}
	a2, out2, _ := testApp(nil)
	if code := a2.run([]string{"feed", "--socket", sock, "--after", "1:2", "--wait", "1s"}); code != wire.ExitTimeout {
		t.Fatalf("a wait that ended empty exits %d, want %d", code, wire.ExitTimeout)
	}
	if !strings.Contains(out2.String(), `"timed_out":true`) {
		t.Fatalf("the answer still prints: %q", out2.String())
	}
}

func TestFeedExitsSevenOnAnExpiredCursorAndPrintsJSONLEntries(t *testing.T) {
	sock, _ := feedDaemon(t, func(n int) (any, *wire.Error) {
		if n == 1 {
			e := wire.Errf(wire.ErrCursorExpired, "old")
			e.Cursor = "2:7"
			return nil, e
		}
		return map[string]any{"entries": []any{map[string]any{"seq": "1:2", "kind": "reset", "file": "f_1"}, map[string]any{"seq": "1:4", "kind": "record"}}, "next_cursor": "1:4"}, nil
	})
	a, _, errw := testApp(nil)
	if code := a.run([]string{"feed", "--socket", sock, "--after", "9:9", "--json"}); code != wire.ExitCursor {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errw.String(), `"error":"cursor_expired"`) {
		t.Fatalf("stderr %q", errw.String())
	}
	a2, out, _ := testApp(nil)
	if code := a2.run([]string{"feed", "--socket", sock, "--jsonl"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var first map[string]any
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first["kind"] != "reset" {
		t.Fatalf("jsonl %q", out.String())
	}
}

func TestSnapshotAllFollowsEveryPageAndRawPrintsTheBytes(t *testing.T) {
	sock, seen := feedDaemon(t, func(int) (any, *wire.Error) { return map[string]any{}, nil })
	a, out, _ := testApp(nil)
	if code := a.run([]string{"snapshot", "--socket", sock, "--all"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var ans map[string]any
	if err := json.Unmarshal(out.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	if es, _ := ans["entries"].([]any); len(es) != 2 || ans["more"] != false || ans["cursor"] != "1:8" {
		t.Fatalf("merged %v", ans)
	}
	if !strings.Contains((*seen)[1], `"at":"1:8"`) || !strings.Contains((*seen)[1], `"page":"r2"`) {
		t.Fatalf("the second page repeats the cursor: %v", *seen)
	}
	a2, out2, _ := testApp(nil)
	if code := a2.run([]string{"raw", "--socket", sock, "--record", "f_1:1:0:-1"}); code != 0 || out2.String() != "abc" {
		t.Fatalf("raw %d %q", code, out2.String())
	}
	if !strings.Contains((*seen)[2], `"record":{"file":"f_1","gen":1,"item":-1,"offset":0}`) {
		t.Fatalf("raw request %v", *seen)
	}
	a3, _, errw := testApp(nil)
	if code := a3.run([]string{"raw", "--socket", sock}); code != wire.ExitUsage || !strings.Contains(errw.String(), "--record") {
		t.Fatalf("usage %d %q", code, errw.String())
	}
	a4, _, _ := testApp(nil)
	if code := a4.run([]string{"raw", "--socket", sock, "--record", "f_1:x"}); code != wire.ExitUsage {
		t.Fatalf("a bad record is a usage error: %d", code)
	}
}

func TestRebuildRefusesWhileADaemonAnswers(t *testing.T) {
	sock, _ := feedDaemon(t, func(int) (any, *wire.Error) { return map[string]any{}, nil })
	a, _, errw := testApp(nil)
	if code := a.run([]string{"rebuild", "--socket", sock}); code != wire.ExitDegraded || !strings.Contains(errw.String(), "stop it first") {
		t.Fatalf("exit %d %q", code, errw.String())
	}
}
