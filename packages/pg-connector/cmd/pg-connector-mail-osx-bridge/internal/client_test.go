package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// startFakeSocket starts a minimal, hand-rolled Unix-socket listener that
// decodes exactly one JSON request per connection (a bare map, not
// pg-osx-bridge-api's own wire.Request) and replies with whatever handle
// returns — deliberately NOT importing pg-osx-bridge-api's own
// socketserver/wire/mailapi packages: this client is exercised against
// nothing but its own locally-defined mirror types (see client.go's package
// doc comment).
func startFakeSocket(t *testing.T, handle func(req map[string]any) any) (sockPath string, requests func() []map[string]any) {
	t.Helper()

	// A short, dedicated temp dir rather than t.TempDir(): sockaddr_un's
	// path-length cap is well under what a t.TempDir() path can produce.
	dir, err := os.MkdirTemp("", "pgmob")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var seen []map[string]any

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var req map[string]any
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, req)
				mu.Unlock()
				_ = json.NewEncoder(conn).Encode(handle(req))
			}()
		}
	}()

	return sockPath, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), seen...)
	}
}

func okResponse(result any) map[string]any {
	return map[string]any{"protocolVersion": 1, "schemaVersion": 1, "result": result}
}

func argsOf(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	args, ok := req["args"].(map[string]any)
	if !ok {
		t.Fatalf("request args = %#v, want an object", req["args"])
	}
	return args
}

func TestSocketClient_List_CarriesArgsAndDecodes(t *testing.T) {
	received := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	sock, requests := startFakeSocket(t, func(map[string]any) any {
		return okResponse(map[string]any{"messages": []map[string]any{{
			"id": "<m1@example.com>", "subject": "Hello", "sender": "Ada <ada@example.com>",
			"dateReceived": received.Format(time.RFC3339), "read": true, "flagged": true, "mailbox": "INBOX",
			"attachments": []map[string]any{{"id": "a1", "name": "x.pdf", "mimeType": "application/pdf", "size": 12}},
		}}})
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.List(context.Background(), apiListQuery{Mailbox: "INBOX", UnreadOnly: true, Limit: 7})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "<m1@example.com>" || !got[0].DateReceived.Equal(received) ||
		len(got[0].Attachments) != 1 || got[0].Attachments[0].MimeType != "application/pdf" || got[0].Attachments[0].Size != 12 {
		t.Fatalf("got = %+v", got)
	}

	reqs := requests()
	if len(reqs) != 1 || reqs[0]["service"] != serviceName || reqs[0]["op"] != opList {
		t.Fatalf("request = %+v, want service=%q op=%q", reqs, serviceName, opList)
	}
	if pv, _ := reqs[0]["protocolVersion"].(float64); int(pv) != wireProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %d", reqs[0]["protocolVersion"], wireProtocolVersion)
	}
	args := argsOf(t, reqs[0])
	if args["mailbox"] != "INBOX" || args["unreadOnly"] != true || args["limit"] != float64(7) {
		t.Fatalf("args = %+v", args)
	}
	if _, ok := args["since"]; !ok {
		t.Fatalf("args = %+v, want a since key (the bridge reads the zero time as no lower bound)", args)
	}
}

func TestSocketClient_Show_FlattensDetail(t *testing.T) {
	sock, requests := startFakeSocket(t, func(map[string]any) any {
		return okResponse(map[string]any{"message": map[string]any{
			"id": "<m1@example.com>", "subject": "Hello", "sender": "ada@example.com",
			"dateReceived": "2026-10-05T09:30:00Z", "mailbox": "INBOX", "attachments": []any{},
			"to": []string{"me@example.com"}, "cc": []string{}, "body": "the body",
		}})
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.Show(context.Background(), "<m1@example.com>")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "<m1@example.com>" || got.Subject != "Hello" || got.Body != "the body" || len(got.To) != 1 {
		t.Fatalf("got = %+v", got)
	}
	reqs := requests()
	if reqs[0]["op"] != opShow || argsOf(t, reqs[0])["id"] != "<m1@example.com>" {
		t.Fatalf("request = %+v", reqs)
	}
}

func TestSocketClient_Search_CarriesArgs(t *testing.T) {
	sock, requests := startFakeSocket(t, func(map[string]any) any {
		return okResponse(map[string]any{"messages": []any{}})
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.Search(context.Background(), apiSearchQuery{Query: "invoice", Mailbox: "INBOX", Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %+v, want none", got)
	}
	reqs := requests()
	args := argsOf(t, reqs[0])
	if reqs[0]["op"] != opSearch || args["query"] != "invoice" || args["mailbox"] != "INBOX" || args["limit"] != float64(3) {
		t.Fatalf("request = %+v", reqs)
	}
}

func TestSocketClient_StateOps_UseBridgeOpNames(t *testing.T) {
	sock, requests := startFakeSocket(t, func(req map[string]any) any {
		return okResponse(map[string]any{"id": argsOfRaw(req)["id"]})
	})
	c := &SocketClient{SocketPath: sock}
	ctx := context.Background()

	for name, fn := range map[string]func() error{
		opMarkRead:   func() error { return c.SetRead(ctx, "<m1@example.com>", true) },
		opMarkUnread: func() error { return c.SetRead(ctx, "<m1@example.com>", false) },
		opArchive:    func() error { return c.Archive(ctx, "<m1@example.com>") },
		opUnarchive:  func() error { return c.Unarchive(ctx, "<m1@example.com>") },
	} {
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	var ops []string
	for _, r := range requests() {
		ops = append(ops, r["op"].(string))
		if argsOfRaw(r)["id"] != "<m1@example.com>" {
			t.Fatalf("request = %+v, want args.id", r)
		}
	}
	want := map[string]bool{opMarkRead: false, opMarkUnread: false, opArchive: false, opUnarchive: false}
	for _, op := range ops {
		if _, ok := want[op]; !ok {
			t.Fatalf("unexpected op %q sent (ops = %v)", op, ops)
		}
		want[op] = true
	}
	for op, seen := range want {
		if !seen {
			t.Fatalf("op %q never sent (ops = %v)", op, ops)
		}
	}
}

func argsOfRaw(req map[string]any) map[string]any {
	args, _ := req["args"].(map[string]any)
	return args
}

func TestSocketClient_FetchAttachment_NoDestinationField(t *testing.T) {
	sock, requests := startFakeSocket(t, func(map[string]any) any {
		return okResponse(map[string]any{"path": "/var/mail-attachments/x.pdf", "name": "x.pdf", "size": 12})
	})
	c := &SocketClient{SocketPath: sock}

	got, err := c.FetchAttachment(context.Background(), "<m1@example.com>", "a1")
	if err != nil {
		t.Fatalf("FetchAttachment: %v", err)
	}
	if got.Path != "/var/mail-attachments/x.pdf" || got.Name != "x.pdf" || got.Size != 12 {
		t.Fatalf("got = %+v", got)
	}
	reqs := requests()
	args := argsOf(t, reqs[0])
	if reqs[0]["op"] != opFetchAttachment || args["messageId"] != "<m1@example.com>" || args["attachmentId"] != "a1" {
		t.Fatalf("request = %+v", reqs)
	}
	if len(args) != 2 {
		t.Fatalf("args = %+v, want exactly messageId and attachmentId (the destination directory is never a request field)", args)
	}
}

func TestSocketClient_ErrorCodes_ClassifyToScriptoutSentinels(t *testing.T) {
	cases := map[string]error{
		"not_found":        scriptout.ErrNotFound,
		"unauthenticated":  scriptout.ErrUnauthenticated,
		"unavailable":      scriptout.ErrUnavailable,
		"unknown_op":       scriptout.ErrUnknownOp,
		"version_mismatch": scriptout.ErrVersionMismatch,
		"invalid_argument": scriptout.ErrInvalidArgument,
		"something_else":   scriptout.ErrUnavailable, // outside the closed six-value set
	}
	for code, want := range cases {
		t.Run(code, func(t *testing.T) {
			sock, _ := startFakeSocket(t, func(map[string]any) any {
				return map[string]any{"protocolVersion": 1, "error": map[string]any{"code": code, "message": "boom"}}
			})
			c := &SocketClient{SocketPath: sock}
			_, err := c.Show(context.Background(), "<m1@example.com>")
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want errors.Is %v", err, want)
			}
		})
	}
}

func TestSocketClient_DialFailure_Unavailable(t *testing.T) {
	c := &SocketClient{SocketPath: filepath.Join(t.TempDir(), "no-such.sock")}
	_, err := c.List(context.Background(), apiListQuery{})
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestSocketClient_MalformedResponse_Unavailable(t *testing.T) {
	sock, _ := startFakeSocket(t, func(map[string]any) any { return "not an envelope" })
	c := &SocketClient{SocketPath: sock}
	_, err := c.List(context.Background(), apiListQuery{})
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestSocketClient_MalformedResult_Unavailable(t *testing.T) {
	sock, _ := startFakeSocket(t, func(map[string]any) any { return okResponse("not an object") })
	c := &SocketClient{SocketPath: sock}
	_, err := c.List(context.Background(), apiListQuery{})
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestResolveSocketPath_EnvVarWins(t *testing.T) {
	got, err := ResolveSocketPath(func(k string) string {
		if k == socketEnvVar {
			return "/tmp/override.sock"
		}
		return "/ignored"
	})
	if err != nil || got != "/tmp/override.sock" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestResolveSocketPath_XDGStateHomeDefault(t *testing.T) {
	got, err := ResolveSocketPath(func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/state"
		}
		return ""
	})
	if err != nil || got != "/state/pg-osx-bridge-api/pg-osx-bridge-api.sock" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestResolveSocketPath_HomeFallbackDefault(t *testing.T) {
	got, err := ResolveSocketPath(func(k string) string {
		if k == "HOME" {
			return "/home-dir"
		}
		return ""
	})
	if err != nil || got != "/home-dir/.local/state/pg-osx-bridge-api/pg-osx-bridge-api.sock" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestResolveSocketPath_NeitherSet_Error(t *testing.T) {
	if _, err := ResolveSocketPath(func(string) string { return "" }); err == nil {
		t.Fatal("want an error when neither env var nor HOME is set")
	}
}
