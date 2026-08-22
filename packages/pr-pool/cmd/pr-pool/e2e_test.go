package main

// This file is pr-pool's Cluster-5 end-to-end test (bead pg2-f3mcb.5): it builds
// the REAL ./cmd/pr-pool binary and execs it TWICE as genuinely separate OS
// processes talking over a real unix socket — "run-until-idle" boots the core,
// and a second, independent "push-inject" process discovers that core and
// injects an event into it — then asserts the injected event was actually
// DISPATCHED by the real command executor (a marker file the dispatched
// process itself wrote), not merely accepted into the queue.
//
// This is deliberately NOT the shape every other push-inject/ingest-event test
// in this package uses (e.g. TestPushInject_DeliversToTheRunningCore in
// push_inject_test.go, or startCore in ingest_event_test.go): those call
// pushInject(...)/runPushInject(...) as Go functions in-process against a
// core.Listen service running in the SAME test binary. That in-process shape
// is exactly the "libs wired only to each other, never to the compiled binary"
// gap this bead exists to close, so this file must not fall back to it.
//
// Hermetic: everything lives under t.TempDir()/a short os.MkdirTemp dir, no
// network, and no dependency on the shared/canonical beads Dolt server — only
// an isolated embedded-Dolt `bd init`, exactly like
// internal/beads/integration_test.go's bdRepo helper (unexported there, so its
// skip-gracefully shape is replicated here rather than imported). It skips
// (never fails) under -short or when bd / embedded-dolt is unavailable, e.g.
// the nix build sandbox where bd is only wired onto pr-pool's own runtime PATH
// via wrapProgram, not onto the sandbox running `go test`.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/core"
)

// e2eBeadsPrefix matches config.Default()'s BeadsPrefix ("zr") so the real
// run-until-idle subprocess's precheck (bd config get issue_prefix == expected)
// passes with no PR_POOL_BEADS_PREFIX override needed.
const e2eBeadsPrefix = "zr"

// e2eEventJSON is the operator-supplied event push-inject delivers to the real
// core. It carries an explicit far-future expiresAt (mirroring
// push_inject_test.go's testPushEvent) so the event is genuinely RETAINED for
// dispatch rather than born-expired-and-dropped-after-one-offer (INV-EVT-4).
const e2eEventJSON = `{"schemaVersion":"1","id":"e2e-op-1","type":"e2e-dispatch","expiresAt":"2099-01-01T00:00:00Z","payload":{}}`

// e2eMarkerContent is what the dispatched "command" role writes; its presence
// on disk, written by the SEPARATE run-until-idle process, is the proof that a
// real executor ran — not merely that the core's queue accepted the event.
const e2eMarkerContent = "dispatched"

// e2eBeadsRepo spins up an isolated embedded-Dolt beads store in a temp dir —
// the same skip-gracefully shape as internal/beads/integration_test.go's
// bdRepo (that helper is unexported in a different package, so it cannot be
// called directly): skip under -short, skip when bd is not on PATH, and skip
// (never fail) when embedded-dolt init itself does not work in this sandbox.
func e2eBeadsRepo(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping e2e process test in -short mode")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH; skipping e2e process test")
	}
	t.Setenv("BD_NON_INTERACTIVE", "1")
	dir := t.TempDir()
	r := beads.NewCLIRunnerForRepo(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if out, err := r.Run(ctx, "init", "--prefix", e2eBeadsPrefix); err != nil {
		t.Skipf("bd init failed (embedded dolt unavailable in this env): %v\n%s", err, out)
	}
	return dir
}

// buildPrPoolBinary compiles the REAL cmd/pr-pool main package this test file
// belongs to (`go build -o <tmp> .`, run with this package's own directory as
// the build's working directory) — the actual artifact operators run, not a
// package-level fake.
func buildPrPoolBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pr-pool")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pr-pool: %v\n%s", err, out)
	}
	return bin
}

// writeE2EConfig writes a real <repoRoot>/.pr-pool/config.toml declaring ONE
// command-type query (bound to nothing real — it just blocks for a few
// seconds so the core stays up long enough for push-inject to reach it; see
// the test's comment on timing) and ONE command-type role bound to the SAME
// event type push-inject will deliver. The role's command writes markerPath —
// the observable side effect proving real dispatch. Mirrors the exact
// `[[query]]`+`[query.command]` / `[[role]]`+`[role.command]` TOML shape
// internal/config/registry_test.go and example.go's emitQuery/emitRole already
// exercise, so the shape itself is not something this test invents.
func writeE2EConfig(t *testing.T, repoRoot, markerPath string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".pr-pool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// Single-quoted shell literal: t.TempDir() paths on this machine never
	// contain a single quote, so no escaping is needed.
	shellCmd := fmt.Sprintf("printf %s > '%s'", e2eMarkerContent, markerPath)
	body := fmt.Sprintf(`[pool]
self_login = "e2e-test-bot"

[[query]]
name = "e2e-source"
emits = ["e2e-dispatch"]
type = "command"
[query.command]
argv = ["sleep", "3"]
format = "json"

[[role]]
name = "e2e-sink"
type = "command"
binds = ["e2e-dispatch"]
[role.command]
argv = ["/bin/sh", "-c", %q]
`, shellCmd)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// TestE2E_RunUntilIdlePushInject_RealBinary is the bead pg2-f3mcb.5 acceptance
// test.
//
// Timing (why the query blocks for a few seconds): run-until-idle's queue
// starts empty, and with no query ever producing a real event, RunUntilIdle's
// very first pass would find the queue idle and exit almost instantly — far
// too fast a window for a second process to discover and inject into it. The
// "e2e-source" query's `sleep 3` backing command runs during ProduceTick,
// which happens AFTER core.Listen has already published the discovery record
// and started accepting connections but BEFORE RunUntilIdle's drain loop
// begins — so it manufactures a deterministic multi-second window in which the
// core is definitely up and definitely still pre-drain, without any polling
// race. push-inject runs synchronously well inside that window.
func TestE2E_RunUntilIdlePushInject_RealBinary(t *testing.T) {
	repoRoot := e2eBeadsRepo(t)
	bin := buildPrPoolBinary(t)
	logDir := shortDir(t) // short: a unix socket path is platform length-limited
	markerPath := filepath.Join(t.TempDir(), "dispatched.marker")

	writeE2EConfig(t, repoRoot, markerPath)

	t.Setenv("PR_POOL_REPO_ROOT", repoRoot)
	t.Setenv("PR_POOL_LOG_DIR", logDir)
	// Hermetic: never let a real operator's XDG-global budget config leak in.
	t.Setenv("PR_POOL_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "no-such-global-config.toml"))
	t.Setenv("PR_POOL_QUOTA_PAUSED", "")
	t.Setenv("PR_POOL_CICD_DOWN", "")
	// Cleared so push-inject is forced through REAL cross-process discovery
	// (core.Discover reading the record run-until-idle publishes under
	// PR_POOL_LOG_DIR) instead of an injected --socket/--token pair.
	t.Setenv(envSocket, "")
	t.Setenv(envToken, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runCmd := exec.CommandContext(ctx, bin, "run-until-idle")
	var runStdout, runStderr bytes.Buffer
	runCmd.Stdout = &runStdout
	runCmd.Stderr = &runStderr
	if err := runCmd.Start(); err != nil {
		t.Fatalf("start run-until-idle: %v", err)
	}

	// Poll for the discovery record the real, SEPARATE run-until-idle process
	// publishes — proof a genuinely different OS process's core is live and
	// reachable (core.Discover proves liveness by dialing the socket, not just
	// reading the record file).
	deadline := time.Now().Add(15 * time.Second)
	var discoverErr error
	for time.Now().Before(deadline) {
		if _, discoverErr = core.Discover(logDir); discoverErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if discoverErr != nil {
		_ = runCmd.Process.Kill()
		t.Fatalf("run-until-idle's core never became discoverable under %s: %v\nstdout=%s\nstderr=%s",
			logDir, discoverErr, runStdout.String(), runStderr.String())
	}

	// The real, second, independent process: push-inject discovers the core (no
	// --socket/--token) and injects the event over the real unix socket.
	pushCmd := exec.CommandContext(ctx, bin, "push-inject", "--json", e2eEventJSON)
	pushOut, err := pushCmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("push-inject: %v\nstderr=%s", err, stderr)
	}
	var report pushInjectJSON
	if err := json.Unmarshal(pushOut, &report); err != nil {
		t.Fatalf("push-inject stdout %q is not the expected JSON: %v", pushOut, err)
	}
	if !report.Accepted {
		t.Fatalf("push-inject report = %+v, want accepted", report)
	}
	if !report.Core.Discovered {
		t.Fatalf("push-inject report = %+v, want the core located via discovery, not an injected socket", report)
	}
	if report.Event.ID != "e2e-op-1" || report.Event.Type != "e2e-dispatch" {
		t.Fatalf("push-inject report event = %+v, want the injected event echoed", report.Event)
	}

	if err := runCmd.Wait(); err != nil {
		t.Fatalf("run-until-idle exit = %v, want 0\nstdout=%s\nstderr=%s", err, runStdout.String(), runStderr.String())
	}
	if !strings.Contains(runStdout.String()+runStderr.String(), "queue drained") {
		t.Errorf("run-until-idle output = %s%s, want it to report the queue drained", runStdout.String(), runStderr.String())
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("dispatched marker was never written by the real command executor: %v\nrun-until-idle stdout=%s\nstderr=%s",
			err, runStdout.String(), runStderr.String())
	}
	if got := strings.TrimSpace(string(data)); got != e2eMarkerContent {
		t.Fatalf("marker content = %q, want %q", got, e2eMarkerContent)
	}
}
