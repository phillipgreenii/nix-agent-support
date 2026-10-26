package corecall

import (
	"bytes"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

func TestResolveRef_flagsThenEnv(t *testing.T) {
	t.Setenv(EnvSocket, "/env.sock")
	t.Setenv(EnvToken, "env-tok")
	if r, err := ResolveRef("/flag.sock", "flag-tok"); err != nil || r != (Ref{"/flag.sock", "flag-tok"}) {
		t.Errorf("flags: %+v, %v", r, err)
	}
	// A flag socket with no token does NOT borrow the env token.
	if r, _ := ResolveRef("/flag.sock", ""); r.Token != "" {
		t.Errorf("token = %q, want empty", r.Token)
	}
	if r, err := ResolveRef("", ""); err != nil || r != (Ref{"/env.sock", "env-tok"}) {
		t.Errorf("env: %+v, %v", r, err)
	}
	t.Setenv(EnvSocket, "")
	if _, err := ResolveRef("", ""); err == nil {
		t.Error("no socket anywhere must be an error")
	}
}

func TestRelay_roundTripsThroughAUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer ln.Close()
	got := make(chan wireRequest, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var req wireRequest
		_ = json.NewDecoder(c).Decode(&req)
		got <- req
		_ = json.NewEncoder(c).Encode(wireResponse{ExitCode: 7, Reply: json.RawMessage(`{"ok":true}`)})
	}()
	var out, errb bytes.Buffer
	code := Relay(&out, &errb, Ref{Socket: sock, Token: "tok"}, "register", []byte(`{"id":"x"}`))
	if code != 7 || out.String() != `{"ok":true}` {
		t.Errorf("code %d out %q err %q", code, out.String(), errb.String())
	}
	if req := <-got; req.Token != "tok" || req.Subcommand != "register" || string(req.Payload) != `{"id":"x"}` {
		t.Errorf("request = %+v", req)
	}
}

func TestRelay_noCoreIsAnError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Relay(&out, &errb, Ref{Socket: filepath.Join(t.TempDir(), "none.sock")}, "register", nil); code == 0 {
		t.Errorf("relay to an absent core exited 0")
	}
}
