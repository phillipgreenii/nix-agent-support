package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notifyclient"
)

// serve answers one connection on a short socket path with reply and returns
// the decoded request.
func serve(t *testing.T, reply string) (path string, req chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nfy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	req = make(chan map[string]any, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var m map[string]any
		if json.NewDecoder(c).Decode(&m) == nil {
			req <- m
			_, _ = c.Write([]byte(reply + "\n"))
		}
	}()
	return path, req
}

// TF-4: hostile text reaches the daemon as the plain data it is, over the socket.
func TestSystemPostsTheTextAsDataOverTheSocket(t *testing.T) {
	path, req := serve(t, `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"x"}}`)
	s := notify.System{Client: notifyclient.Client{SocketPath: path}}
	title, body := `-e "x" \ end tell`, "say \"hi\" & do shell script \"id\"\nünï"
	if err := s.Notify(context.Background(), title, body, "Glass"); err != nil {
		t.Fatal(err)
	}
	args, _ := (<-req)["args"].(map[string]any)
	want := map[string]any{"title": title, "body": body, "sound": "Glass", "source": "pg-task-focus"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

// TF-7: a daemon refusal is returned, not swallowed.
func TestSystemReturnsADaemonRefusal(t *testing.T) {
	path, _ := serve(t, `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unauthenticated","message":"denied"}}`)
	s := notify.System{Client: notifyclient.Client{SocketPath: path}}
	if err := s.Notify(context.Background(), "t", "b", ""); err == nil {
		t.Fatal("a refusal was swallowed")
	}
}

// TF-6: ValidSoundName stays, for config validation.
func TestValidSoundNameRefusesANameThatIsNotAPlainName(t *testing.T) {
	for _, bad := range []string{"../..", "a/b", "", "Glass.aiff", string(make([]byte, 65)), "a;b"} {
		if notify.ValidSoundName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"Glass", "My Sound_1-b"} {
		if !notify.ValidSoundName(good) {
			t.Errorf("%q refused", good)
		}
	}
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	if !notify.ValidSoundName(string(long)) {
		t.Error("64 characters refused")
	}
}

func TestFakeRecordsAndFails(t *testing.T) {
	f := &notify.Fake{NotifyErr: errors.New("x")}
	if err := f.Notify(context.Background(), "t", "b", "Glass"); err == nil {
		t.Fatal("the failure was swallowed")
	}
	if !reflect.DeepEqual(f.Sounds(), []string{"Glass"}) || f.Notified()[0].Source != "pg-task-focus" {
		t.Errorf("recorded %v, %v", f.Sounds(), f.Notified())
	}
}
