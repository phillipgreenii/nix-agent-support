package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestServePostStartup_success proves the basic wire contract: a
// schema-legal handler.postStartup request gets a handler.postStartup-reply
// back, exit 0.
func TestServePostStartup_success(t *testing.T) {
	var stdout bytes.Buffer
	code := servePostStartup(strings.NewReader(`{"schemaVersion":"1","id":"hs-1"}`), &stdout)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitOK)
	}
	var reply map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not JSON: %v; got %s", err, stdout.String())
	}
	if reply["id"] != "hs-1" || reply["outcome"] != "ok" {
		t.Errorf("reply = %+v, want id=hs-1 outcome=ok", reply)
	}
}

// TestServePostStartup_rejectsMalformedRequest proves the schema check runs.
func TestServePostStartup_rejectsMalformedRequest(t *testing.T) {
	var stdout bytes.Buffer
	code := servePostStartup(strings.NewReader(`{"schemaVersion":"1"}`), &stdout) // missing id
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d on a schema-invalid request", code, conformance.ExitError)
	}
	if !strings.Contains(stdout.String(), "error") {
		t.Errorf("stdout = %q, want an error reply", stdout.String())
	}
}

// TestServePostStartup_touchesNoCcpoolRunner locks in the no-op contract
// explicitly (decision #2: postStartup does nothing today, built only for
// symmetry with preShutdown — "it does nothing" is exactly the kind of
// thing that gets left untested or accidentally given work later).
// servePostStartup's own signature — (stdin io.Reader, stdout io.Writer),
// with no runner parameter at all — already makes reaching a ccpool/beads
// runner structurally impossible; this test additionally proves the CLI
// entrypoint one level up (runPostStartup) never opens the
// --role-config/--config files it accepts purely for flag-surface symmetry
// with dispatch/preShutdown: pointing both at nonexistent paths must NOT
// fail the call, which it could not avoid doing if either was ever actually
// read.
func TestServePostStartup_touchesNoCcpoolRunner(t *testing.T) {
	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"hs-2"}`)
	defer restoreIn()

	var code int
	got := captureStdout(t, func() {
		code = runPostStartup([]string{"--role-config", "/does/not/exist/role.json", "--config", "/does/not/exist/config.json"})
	})
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d (postStartup must never open --role-config/--config)", code, conformance.ExitOK)
	}
	if !strings.Contains(got, `"outcome":"ok"`) {
		t.Errorf("stdout = %q, want a successful reply", got)
	}
}

// redirectStdin temporarily replaces os.Stdin with a pipe pre-loaded with
// body and already closed for writing (so a reader never blocks), returning
// a restore func. t.Cleanup is not used deliberately: some callers need the
// restore to run before a later assertion in the same test.
func redirectStdin(t *testing.T, body string) (restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(body); err != nil {
		t.Fatalf("write stdin pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin pipe writer: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = old
		_ = r.Close()
	}
}

// captureStdout temporarily replaces os.Stdout with a pipe, runs fn, restores
// os.Stdout, and returns everything fn wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	return <-done
}
