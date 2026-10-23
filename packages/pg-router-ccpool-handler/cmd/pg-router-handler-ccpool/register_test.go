package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// fakeCore listens on a unix socket in a short path (macOS limits socket paths
// to ~100 bytes, which t.TempDir often exceeds), answers one request with the
// given exit code and reply, and sends the request it saw on the returned
// channel.
func fakeCore(t *testing.T, exit int, reply string) (sock string, seen <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan map[string]any, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var req map[string]any
		_ = json.NewDecoder(c).Decode(&req)
		ch <- req
		_ = json.NewEncoder(c).Encode(map[string]any{"exitCode": exit, "reply": json.RawMessage(reply)})
	}()
	return sock, ch
}

// register keeps the old binary's flags, output and exit codes: it sends
// {schemaVersion, id, kind: "handler", self} to the core and relays the reply
// and the core's own exit code.
func TestRunRegister_relaysTheCoresReplyAndExitCode(t *testing.T) {
	sock, seen := fakeCore(t, 0, `{"registered":true}`)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"register", "--socket", sock, "--token", "tok", "--id", "ccpool-1", "--self", "degraded"})
	})
	if code != conformance.ExitOK || out != `{"registered":true}` {
		t.Fatalf("exit = %d, stdout = %q", code, out)
	}
	req := <-seen
	if req["token"] != "tok" || req["subcommand"] != "register" {
		t.Errorf("wire request = %+v", req)
	}
	payload, _ := req["payload"].(map[string]any)
	if payload["id"] != "ccpool-1" || payload["kind"] != "handler" || payload["self"] != "degraded" || payload["schemaVersion"] == "" {
		t.Errorf("register payload = %+v", payload)
	}
}

func TestRunRegister_usageAndNoCoreErrors(t *testing.T) {
	if got := run([]string{"register"}); got != conformance.ExitUsage {
		t.Errorf("register without --id: exit %d, want %d", got, conformance.ExitUsage)
	}
	if got := run([]string{"register", "--id", "x", "stray"}); got != conformance.ExitUsage {
		t.Errorf("register with an argument: exit %d, want %d", got, conformance.ExitUsage)
	}
	if got := run([]string{"register", "--bogus"}); got != conformance.ExitUsage {
		t.Errorf("register with an unknown flag: exit %d, want %d", got, conformance.ExitUsage)
	}
	t.Setenv("PG_ROUTER_SOCKET", "")
	t.Setenv("PG_ROUTER_TOKEN", "")
	if got := run([]string{"register", "--id", "x"}); got != conformance.ExitError {
		t.Errorf("register with no core socket: exit %d, want %d", got, conformance.ExitError)
	}
}

// self-status reads the core's request from stdin and relays it, with the core's
// reply and exit code.
func TestRunSelfStatus_relaysStdinToTheCore(t *testing.T) {
	sock, seen := fakeCore(t, 0, `{"ok":true}`)
	restore := redirectStdin(t, `{"schemaVersion":"1","id":"s-1"}`)
	defer restore()
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"self-status", "--socket", sock, "--token", "tok"})
	})
	if code != conformance.ExitOK || out != `{"ok":true}` {
		t.Fatalf("exit = %d, stdout = %q", code, out)
	}
	req := <-seen
	payload, _ := req["payload"].(map[string]any)
	if req["subcommand"] != "self-status" || payload["id"] != "s-1" {
		t.Errorf("wire request = %+v", req)
	}
}

func TestRunSelfStatus_usageAndNoCoreErrors(t *testing.T) {
	if got := run([]string{"self-status", "stray"}); got != conformance.ExitUsage {
		t.Errorf("self-status with an argument: exit %d, want %d", got, conformance.ExitUsage)
	}
	t.Setenv("PG_ROUTER_SOCKET", "")
	t.Setenv("PG_ROUTER_TOKEN", "")
	if got := run([]string{"self-status"}); got != conformance.ExitError {
		t.Errorf("self-status with no core socket: exit %d, want %d", got, conformance.ExitError)
	}
	if !strings.Contains(helpText, "register") || !strings.Contains(helpText, "self-status") {
		t.Errorf("help must list register and self-status")
	}
}
