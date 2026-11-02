package client_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := tempDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(d) })
	return d
}

// fake is a scripted daemon: hello plus a feed handler that pops answers.
type fake struct {
	sock   string
	mu     sync.Mutex
	feeds  []func(body []byte) (any, *wire.Error)
	afters []string
	proto  int
}

func newFake(t *testing.T, proto int) *fake {
	t.Helper()
	f := &fake{sock: filepath.Join(shortDir(t), "r.sock"), proto: proto}
	s := server.New(obs.New(nil), nil, func() any {
		return wire.Hello{Service: "x", ProtocolVersion: f.proto, SchemaVersion: 1, Epoch: 1, Capabilities: []string{"feed", "hello"}, CaughtUp: true}
	})
	s.Register("feed", server.KindFeed, func(_ context.Context, body []byte) (any, *wire.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.afters = append(f.afters, string(body))
		if len(f.feeds) == 0 {
			return map[string]any{"entries": []any{}, "next_cursor": "1:0", "timed_out": true}, nil
		}
		h := f.feeds[0]
		f.feeds = f.feeds[1:]
		return h(body)
	})
	for _, op := range []string{"query", "search", "snapshot", "raw"} {
		op := op
		s.Register(op, server.KindQuery, func(_ context.Context, body []byte) (any, *wire.Error) {
			return nil, wire.Errf(op+"_fails", "scripted")
		})
	}
	if err := s.Listen(f.sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return f
}

func (f *fake) script(h ...func(body []byte) (any, *wire.Error)) {
	f.mu.Lock()
	f.feeds = append(f.feeds, h...)
	f.mu.Unlock()
}

func page(next string, more bool, entries ...map[string]any) func([]byte) (any, *wire.Error) {
	return func([]byte) (any, *wire.Error) {
		es := make([]any, len(entries))
		for i, e := range entries {
			es[i] = e
		}
		return map[string]any{"entries": es, "next_cursor": next, "more": more}, nil
	}
}

func rec(seq, file string, gen int, off int64) map[string]any {
	return map[string]any{"seq": seq, "kind": "record", "record": map[string]any{"file": file, "gen": gen, "offset": off, "item": -1}}
}

func reset(seq, file string, gen int) map[string]any {
	return map[string]any{"seq": seq, "kind": "reset", "file": file, "gen": gen, "parser": "claude-transcript@1"}
}

func dial(t *testing.T, sock string, sleeps *[]time.Duration) *client.Client {
	t.Helper()
	c, err := client.Dial(context.Background(), client.Options{
		Socket: sock, Name: "cli",
		Sleep: func(_ context.Context, d time.Duration) error { *sleeps = append(*sleeps, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The Home Manager module's socketPath default is
// "${xdg.stateHome}/pg-claude-code-recorder/recorder.sock"; this pins the
// client default to the same path (tests/pg-claude-code-recorder-hm-launchd.nix
// asserts the rendered side, pg2-wb689g). With only XDG_STATE_HOME set, no HOME.
func TestDefaultSocketWithOnlyXDGStateHomeMatchesHMRenderedPath(t *testing.T) {
	stateHome := "/example/home/.local/state"
	get := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	if got, want := client.DefaultSocket(get), stateHome+"/pg-claude-code-recorder/recorder.sock"; got != want {
		t.Fatalf("DefaultSocket = %q, want %q", got, want)
	}
}

func TestSocketDiscoveryEnvironmentThenDefaultPath(t *testing.T) {
	env := map[string]string{"HOME": "/example/home", "XDG_STATE_HOME": "/example/state"}
	get := func(k string) string { return env[k] }
	if got := client.DefaultSocket(get); got != "/example/state/pg-claude-code-recorder/recorder.sock" {
		t.Fatalf("default path %q", got)
	}
	env["PG_CLAUDE_CODE_RECORDER_SOCKET"] = "/example/other.sock"
	if got := client.DefaultSocket(get); got != "/example/other.sock" {
		t.Fatalf("environment %q", got)
	}
	// Dial uses the environment variable when no socket is given.
	f := newFake(t, 1)
	env["PG_CLAUDE_CODE_RECORDER_SOCKET"] = f.sock
	c, err := client.Dial(context.Background(), client.Options{Getenv: get})
	if err != nil {
		t.Fatalf("dial through the environment: %v", err)
	}
	if h, err := c.Hello(context.Background()); err != nil || h.ProtocolVersion != 1 || len(c.Capabilities()) != 2 {
		t.Fatalf("hello %+v %v", h, err)
	}
}

func TestDialReportsDaemonOfflineAndRefusesANewerProtocol(t *testing.T) {
	_, err := client.Dial(context.Background(), client.Options{Socket: filepath.Join(shortDir(t), "none.sock")})
	var ce *client.Error
	if !errors.Is(err, client.ErrDown) || !errors.As(err, &ce) || ce.Name != "daemon_offline" {
		t.Fatalf("offline: %v", err)
	}
	f := newFake(t, client.ProtocolVersion+1)
	_, err = client.Dial(context.Background(), client.Options{Socket: f.sock})
	if !errors.Is(err, client.ErrProtocol) {
		t.Fatalf("a higher protocol_version must be refused: %v", err)
	}
	f2 := newFake(t, client.ProtocolVersion)
	if _, err := client.Dial(context.Background(), client.Options{Socket: f2.sock}); err != nil {
		t.Fatalf("the same protocol_version is accepted: %v", err)
	}
}

func TestEveryWireErrorMapsToItsTypedError(t *testing.T) {
	f := newFake(t, 1)
	var sl []time.Duration
	c := dial(t, f.sock, &sl)
	for name, want := range map[string]error{
		wire.ErrBadRequest: client.ErrBadRequest, wire.ErrNotFound: client.ErrNotFound, wire.ErrBusy: client.ErrBusy,
		wire.ErrBehind: client.ErrBehind, wire.ErrUnavailable: client.ErrUnavailable, wire.ErrCursorExpired: client.ErrCursorExpired,
	} {
		name := name
		f.script(func([]byte) (any, *wire.Error) {
			e := wire.Errf(name, "scripted")
			e.Cursor = "1:42"
			return nil, e
		})
		_, err := c.FeedPage(context.Background(), nil, "", nil, 0, 0)
		var ce *client.Error
		if !errors.Is(err, want) || !errors.As(err, &ce) || ce.Name != name {
			t.Errorf("%s: %v", name, err)
		}
		if name == wire.ErrCursorExpired && ce.Cursor != "1:42" {
			t.Errorf("the resync cursor is lost: %+v", ce)
		}
	}
	if _, err := c.Query(context.Background(), client.QueryRequest{}); err == nil {
		t.Fatal("query against a failing daemon succeeded")
	}
	if _, err := c.Search(context.Background(), client.SearchRequest{Q: "x"}); err == nil {
		t.Fatal("search")
	}
	if _, err := c.Snapshot(context.Background(), client.SnapshotRequest{}); err == nil {
		t.Fatal("snapshot")
	}
	if _, err := c.Raw(context.Background(), client.RawRequest{File: "f_1", Length: 1}); err == nil {
		t.Fatal("raw")
	}
}

func TestBackoffStartsAtOneSecondDoublesAndStopsAtThirty(t *testing.T) {
	var b client.Backoff
	var got []time.Duration
	for i := 0; i < 8; i++ {
		got = append(got, b.Next())
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("schedule %v", got)
		}
	}
	b.Reset()
	if b.Next() != time.Second {
		t.Fatal("reset")
	}
	for _, name := range []string{wire.ErrBusy, wire.ErrUnavailable, wire.ErrDaemonOffline} {
		if !client.Retryable(&client.Error{Name: name}) {
			t.Errorf("%s must be retryable", name)
		}
	}
	for _, name := range []string{wire.ErrBadRequest, wire.ErrNotFound, wire.ErrCursorExpired} {
		if client.Retryable(&client.Error{Name: name}) {
			t.Errorf("%s must not be retryable", name)
		}
	}
}

func TestFeedAppliesResetsBeforeDeduplicatingAndBacksOff(t *testing.T) {
	f := newFake(t, 1)
	var sl []time.Duration
	c := dial(t, f.sock, &sl)
	f.script(
		page("1:4", false, rec("1:2", "f_1", 1, 0), rec("1:4", "f_1", 1, 10)),
		// busy twice, then unavailable: it backs off 1 s, 2 s, 4 s and retries the same cursor.
		func([]byte) (any, *wire.Error) { return nil, wire.Errf(wire.ErrBusy, "x") },
		func([]byte) (any, *wire.Error) { return nil, wire.Errf(wire.ErrBusy, "x") },
		func([]byte) (any, *wire.Error) { return nil, wire.Errf(wire.ErrUnavailable, "x") },
		// A redelivery of a record the iterator already returned is dropped.
		page("1:5", false, rec("1:4", "f_1", 1, 10), rec("1:5", "f_1", 1, 20)),
		// A reset is applied first, so the re-parsed records (same ids) come through again.
		page("1:9", false, reset("1:7", "f_1", 1), rec("1:8", "f_1", 1, 0), rec("1:9", "f_1", 1, 10)),
	)
	it := c.Feed(context.Background(), nil, "")
	var got []string
	for i := 0; i < 6; i++ {
		en, err := it.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if en.Kind == "reset" {
			got = append(got, "reset")
		} else {
			got = append(got, fmt.Sprintf("%s@%d", en.Record.File, en.Record.Offset))
		}
	}
	want := "f_1@0 f_1@10 f_1@20 reset f_1@0 f_1@10"
	if fmt.Sprint(got) != "["+want+"]" {
		t.Fatalf("entries %v, want %s", got, want)
	}
	if fmt.Sprint(sl) != fmt.Sprint([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("back off %v", sl)
	}
	// Every retry carried the same cursor: after the first page's next_cursor.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.afters) < 6 || f.afters[1] != f.afters[2] || f.afters[2] != f.afters[3] || f.afters[3] != f.afters[4] {
		t.Fatalf("requests %q", f.afters)
	}
}

func TestFeedCursorNeverSkipsAnEntryTheCallerHasNotConsumed(t *testing.T) {
	f := newFake(t, 1)
	var sl []time.Duration
	c := dial(t, f.sock, &sl)
	f.script(page("1:6", false, rec("1:2", "f_1", 1, 0), rec("1:4", "f_1", 1, 10), rec("1:6", "f_1", 1, 20)))
	it := c.Feed(context.Background(), nil, "1:0")
	if it.Cursor() != "1:0" {
		t.Fatalf("start cursor %q", it.Cursor())
	}
	for _, want := range []string{"1:2", "1:4", "1:6"} {
		if _, err := it.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if it.Cursor() != want {
			t.Fatalf("cursor %q, want %q", it.Cursor(), want)
		}
	}
	// A cursor_expired answer stops the iterator with the resync cursor.
	f.script(func([]byte) (any, *wire.Error) {
		e := wire.Errf(wire.ErrCursorExpired, "old")
		e.Cursor = "2:10"
		return nil, e
	})
	_, err := it.Next(context.Background())
	var ce *client.Error
	if !errors.Is(err, client.ErrCursorExpired) || !errors.As(err, &ce) || ce.Cursor != "2:10" {
		t.Fatalf("expired: %v", err)
	}
}
