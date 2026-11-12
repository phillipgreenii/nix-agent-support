package notifyclient_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notifyclient"
)

// fakeDaemon listens on a short socket path (darwin limits sun_path) and
// answers each connection with reply(requestJSON); a nil reply hangs.
func fakeDaemon(t *testing.T, reply func(raw []byte) string) (path string, got chan []byte) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ntf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, "s.sock")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got = make(chan []byte, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				var v json.RawMessage
				if err := json.NewDecoder(c).Decode(&v); err != nil {
					return
				}
				got <- v
				if reply == nil {
					time.Sleep(2 * time.Second)
					return
				}
				_, _ = c.Write([]byte(reply(v) + "\n"))
			}()
		}
	}()
	return path, got
}

const okReply = `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"abc"}}`

// TF-1, TF-4, TF-5, pin (d): the request carries title, body, sound and source
// only, and hostile text round-trips.
func TestPostSendsTheContractFieldsOnly(t *testing.T) {
	path, got := fakeDaemon(t, func([]byte) string { return okReply })
	title, body := `-x "q" \ do shell script "id" ünï`, "a\nb 'c'"
	if err := (notifyclient.Client{SocketPath: path}).Post(context.Background(), title, body, "Hero"); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ProtocolVersion int
		Service, Op     string
		Args            map[string]any
	}
	if err := json.Unmarshal(<-got, &req); err != nil {
		t.Fatal(err)
	}
	if req.ProtocolVersion != 1 || req.Service != "notify" || req.Op != "post" {
		t.Errorf("envelope = %+v", req)
	}
	want := map[string]any{"title": title, "body": body, "sound": "Hero", "source": "pg-task-focus"}
	if len(req.Args) != len(want) {
		t.Errorf("args = %v, want exactly %v", req.Args, want)
	}
	for k, v := range want {
		if req.Args[k] != v {
			t.Errorf("args[%q] = %v, want %v", k, req.Args[k], v)
		}
	}
}

// TF-5: an empty sound omits the field.
func TestPostOmitsAnEmptySound(t *testing.T) {
	path, got := fakeDaemon(t, func([]byte) string { return okReply })
	if err := (notifyclient.Client{SocketPath: path}).Post(context.Background(), "t", "b", ""); err != nil {
		t.Fatal(err)
	}
	if raw := string(<-got); strings.Contains(raw, "sound") {
		t.Errorf("an empty sound was sent: %s", raw)
	}
}

// TF-12: over-cap text is cut on a rune boundary before sending.
func TestPostCutsOverCapTextOnARuneBoundary(t *testing.T) {
	path, got := fakeDaemon(t, func([]byte) string { return okReply })
	title := strings.Repeat("é", 200) // 400 bytes
	if err := (notifyclient.Client{SocketPath: path}).Post(context.Background(), title, strings.Repeat("x", 3000), ""); err != nil {
		t.Fatal(err)
	}
	var req struct{ Args struct{ Title, Body string } }
	if err := json.Unmarshal(<-got, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Args.Title) != 256 || req.Args.Title != strings.Repeat("é", 128) || len(req.Args.Body) != 2048 {
		t.Errorf("title %d bytes, body %d bytes", len(req.Args.Title), len(req.Args.Body))
	}
	if got := notifyclient.CutBytes("aé", 2); got != "a" {
		t.Errorf("CutBytes split a rune: %q", got)
	}
}

// TF-7 (c): every failure is a non-nil error.
func TestPostReportsEachFailure(t *testing.T) {
	for _, code := range []string{"unauthenticated", "invalid_argument", "unavailable"} {
		path, _ := fakeDaemon(t, func([]byte) string {
			return `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"` + code + `","message":"m"}}`
		})
		err := (notifyclient.Client{SocketPath: path}).Post(context.Background(), "t", "b", "")
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Errorf("%s: err = %v", code, err)
		}
	}
	for name, reply := range map[string]string{"empty": "", "not json": "nope", "neither": `{"protocolVersion":1}`} {
		path, _ := fakeDaemon(t, func([]byte) string { return reply })
		if err := (notifyclient.Client{SocketPath: path}).Post(context.Background(), "t", "b", ""); err == nil {
			t.Errorf("%s reply: no error", name)
		}
	}
}

// TF-9: an unreachable or hung daemon is bounded and returns an error.
func TestPostIsBoundedWhenTheDaemonIsAbsentOrHung(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.sock")
	if err := (notifyclient.Client{SocketPath: missing}).Post(context.Background(), "t", "b", ""); err == nil {
		t.Error("an absent daemon was not an error")
	}
	path, _ := fakeDaemon(t, nil)
	start := time.Now()
	err := (notifyclient.Client{SocketPath: path, Timeout: 100 * time.Millisecond}).Post(context.Background(), "t", "b", "")
	if err == nil || time.Since(start) > time.Second {
		t.Errorf("a hung daemon: err %v after %v", err, time.Since(start))
	}
}

// The socket path follows the discovery rule.
func TestSocketDiscovery(t *testing.T) {
	path, got := fakeDaemon(t, func([]byte) string { return okReply })
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", path)
	if err := (notifyclient.Client{}).Post(context.Background(), "t", "b", ""); err != nil {
		t.Fatal(err)
	}
	<-got
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := (notifyclient.Client{}).Post(context.Background(), "t", "b", ""); err == nil {
		t.Error("the XDG path has no daemon, want an error")
	}
}
