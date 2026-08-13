package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

const testIngestRequest = `{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"t"}]}`

// shortDir returns a SHORT temp dir; a unix socket path is capped at ~104 bytes by
// the platform and t.TempDir() embeds the (long) test name.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "prp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// startCore brings up a real core in logDir and returns it.
func startCore(t *testing.T, logDir string) *core.Service {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	// The CONFIGURED binding set: the types these tests' fixtures emit. An event of
	// any other type is unknown to the configuration and the core rejects it
	// (INV-DISP-3), so a core under test must declare what its fixtures send.
	svc, err := core.Listen(core.Options{LogDir: logDir, Queue: q, Bindings: core.NewBindings("review-requested", "t")})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Accept(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Accept = %v, want nil", err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := core.Discover(logDir); err == nil {
			return svc
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("core never became discoverable")
	return nil
}

// callCore relays the core's reply and coarse exit code verbatim.
func TestCallCore_RelaysReplyAndExitCode(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)
	ref := svc.Ref()

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, ref, core.SubcommandIngestEvent, []byte(testIngestRequest))
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if err := conformance.Check(core.IngestReplySchema, reply); err != nil {
		t.Fatalf("relayed reply failed the reply schema: %v", err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1", reply["accepted"])
	}
	if depth := svc.Queue().DepthByType()["t"]; depth != 1 {
		t.Fatalf("queue depth = %d, want the event enqueued in the core", depth)
	}
}

// A rejected event must reach the caller as exit 1 with the rejection body — the
// callback contract's coarse code plus the rich outcome in the JSON.
func TestCallCore_RelaysRejection(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, svc.Ref(), core.SubcommandIngestEvent,
		// An unparseable `expiresAt`: schema-valid (it is just a string there) but not
		// convertible, so the core rejects the event rather than the envelope.
		[]byte(`{"schemaVersion":"1","id":"trk-1","events":[{"id":"bad","type":"t","expiresAt":"soon"}]}`))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 for a rejected event", code)
	}
	if !strings.Contains(stdout.String(), `"rejected"`) {
		t.Fatalf("stdout = %q, want the rejection body", stdout.String())
	}
}

// With no core running, the CLI FAILS with a "no running core" diagnostic and a
// remedy — it never starts one (ADR 0036).
func TestCallCore_NoRunningCoreIsAnError(t *testing.T) {
	var stdout, stderr strings.Builder
	ref := core.Ref{Socket: filepath.Join(shortDir(t), "gone.sock")}
	code := callCore(&stdout, &stderr, ref, core.SubcommandIngestEvent, []byte(testIngestRequest))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running core") {
		t.Fatalf("stderr = %q, want a no-running-core diagnostic", stderr.String())
	}
	if !strings.Contains(stderr.String(), "start the core's socket service") {
		t.Fatalf("stderr = %q, want the remedy, since the CLI will not start one itself", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing on stdout for a locate failure", stdout.String())
	}
}

// Locating the core: an injected socket (flag, then env) wins; otherwise discover
// under the log dir; otherwise ErrNoRunningCore.
func TestLocateCore(t *testing.T) {
	t.Run("flags win", func(t *testing.T) {
		t.Setenv(envSocket, "/env/sock")
		t.Setenv(envToken, "env-token")
		got, err := locateCore("/flag/sock", "flag-token")
		if err != nil {
			t.Fatalf("locateCore: %v", err)
		}
		if got != (core.Ref{Socket: "/flag/sock", Token: "flag-token"}) {
			t.Fatalf("ref = %+v, want the flag values", got)
		}
	})
	t.Run("env is the fallback injection", func(t *testing.T) {
		t.Setenv(envSocket, "/env/sock")
		t.Setenv(envToken, "env-token")
		got, err := locateCore("", "")
		if err != nil {
			t.Fatalf("locateCore: %v", err)
		}
		if got != (core.Ref{Socket: "/env/sock", Token: "env-token"}) {
			t.Fatalf("ref = %+v, want the env values", got)
		}
	})
	t.Run("discovery under the log dir", func(t *testing.T) {
		dir := shortDir(t)
		t.Setenv(envSocket, "")
		t.Setenv(envToken, "")
		t.Setenv("PR_POOL_LOG_DIR", dir)
		svc := startCore(t, dir)
		got, err := locateCore("", "")
		if err != nil {
			t.Fatalf("locateCore: %v", err)
		}
		if got != svc.Ref() {
			t.Fatalf("ref = %+v, want the discovered core %+v", got, svc.Ref())
		}
	})
	t.Run("nothing to locate", func(t *testing.T) {
		t.Setenv(envSocket, "")
		t.Setenv(envToken, "")
		t.Setenv("PR_POOL_LOG_DIR", shortDir(t))
		if _, err := locateCore("", ""); err == nil {
			t.Fatal("locateCore succeeded with no core, want an error")
		}
	})
}

// A usage error on the callback subcommand exits 1, NOT 2: 2 is the common
// contract's pre-accept busy signal, and a source that read a bad flag as "busy"
// would silently drop events.
func TestRunIngestEvent_UsageErrorIsExit1NotBusy(t *testing.T) {
	if code := runIngestEvent([]string{"--nope"}); code != conformance.ExitError {
		t.Fatalf("unknown flag exit = %d, want %d (never %d/busy)", code, conformance.ExitError, conformance.ExitBusy)
	}
	if code := runIngestEvent([]string{"extra-positional"}); code != conformance.ExitError {
		t.Fatalf("positional exit = %d, want %d", code, conformance.ExitError)
	}
}

// Routing: `ingest-event` is its own route and forwards its args, and the help
// text advertises it.
func TestRoute_ingestEvent(t *testing.T) {
	r := route([]string{"pr-pool", "ingest-event", "--socket", "/s", "--token", "t"})
	if r.kind != routeIngestEvent {
		t.Fatalf("kind = %v, want routeIngestEvent", r.kind)
	}
	if strings.Join(r.rest, " ") != "--socket /s --token t" {
		t.Fatalf("rest = %v, want the flags forwarded", r.rest)
	}
	if !strings.Contains(helpText, "ingest-event") {
		t.Fatal("helpText does not mention ingest-event")
	}
}
