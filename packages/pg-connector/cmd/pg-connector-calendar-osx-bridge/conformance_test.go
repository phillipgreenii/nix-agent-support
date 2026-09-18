// conformance_test.go: this packet's own required conformance-suite proof
// — pkg/scriptout/conformance's existing Backend/ExecBackend/driver.Run
// suite (unchanged; this packet is a NEW consumer of it, not a modifier)
// run against the REAL COMPILED pg-connector-calendar-osx-bridge binary,
// with a FAKE osx-bridge-api socket listener standing in for the real
// daemon, resolved via t.Setenv("OSX_BRIDGE_API_SOCKET", <fake listener's
// path>) — the SAME env var internal/client.go's own socketEnvVar/
// ResolveSocketPath resolution reads, mirroring
// cmd/pg-connector-thread-slack/conformance_test.go's own
// t.Setenv(internal.EnvBinary, fakeClaude) precedent exactly. Deliberately
// NOT gated behind a build tag: this packet's own Validation section runs
// this package's tests with plain `go test`, no `-tags` flag, so the
// binary build + subprocess exec below run as part of every ordinary
// `go test ./...` pass over this module — this suite is fully hermetic (a
// fake, hand-rolled Unix-socket listener, no network, no real EventKit/TCC)
// so there is no reason to keep it out of the default run.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// osxBridgeAPISocketEnvVar mirrors internal/client.go's own (unexported)
// socketEnvVar constant by value — this backend is a client of
// osx-bridge-api's real daemon socket and resolves this exact env var, so
// the fake listener below must be wired in under the identical name.
const osxBridgeAPISocketEnvVar = "OSX_BRIDGE_API_SOCKET"

// buildCalendarOsxBridgeBinary compiles this package's own real binary via
// `go build -o <dir>/pg-connector-calendar-osx-bridge .`, run with THIS
// package's own directory as the build's working directory — mirrors
// cmd/pg-connector-thread-slack/conformance_test.go's own
// buildThreadSlackBinary precedent, minus its build-tag gating (see this
// file's own package doc comment for why).
func buildCalendarOsxBridgeBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pg-connector-calendar-osx-bridge")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/pg-connector-calendar-osx-bridge: %v\n%s", err, out)
	}
	return bin
}

// startFakeOsxBridgeSocket starts a minimal, hand-rolled Unix-socket
// listener standing in for the real osx-bridge-api daemon — never
// importing packages/osx-bridge-api's own socketserver/wire/calendarapi
// packages (this backend's own Contract: this package talks to that
// daemon over the wire only, never as a Go import; see internal/client.go's
// package doc comment). It answers well-formed "calendars"/"events"
// replies (a well-formed empty result for "events") and a well-formed
// unknown_op error for anything else — enough for a live round trip
// should this backend ever dial it, though (mirroring
// cmd/pg-connector-thread-slack/conformance_test.go's own
// writeFakeClaude's identical observation) none of
// conformance.Run's own generic invoking/unknown-op, invoking/
// malformed-stdin, or invoking/capabilities cases actually reach this
// binary's calendar-specific ops at all — driver.go's own doc comment:
// "driver.go's own Run only exercises unknown-op/malformed-stdin/
// capabilities against a live backend."
func startFakeOsxBridgeSocket(t *testing.T) string {
	t.Helper()

	// A short, dedicated temp dir rather than t.TempDir(): sockaddr_un's
	// own path-length cap is well under what a t.TempDir() path (which
	// embeds the test name) can produce — mirrors
	// packages/osx-bridge-api/internal/socketserver/server_test.go's own
	// startServer precedent.
	dir, err := os.MkdirTemp("", "pgcobfake")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

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
				op, _ := req["op"].(string)
				var resp map[string]any
				switch op {
				case "calendars":
					resp = map[string]any{
						"protocolVersion": 1,
						"schemaVersion":   1,
						"result": map[string]any{
							"calendars": []map[string]any{{"id": "cal-1", "title": "Test Calendar", "readOnly": false}},
						},
					}
				case "events":
					resp = map[string]any{
						"protocolVersion": 1,
						"schemaVersion":   1,
						"result":          map[string]any{"events": []map[string]any{}},
					}
				default:
					resp = map[string]any{
						"protocolVersion": 1,
						"error":           map[string]any{"code": "unknown_op", "message": "fake osx-bridge-api: unknown op"},
					}
				}
				_ = json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()

	return sock
}

// TestConformance_RealBinary_FakeOsxBridgeSocketOnEnvOverride is this
// packet's own required proof: "the full conformance suite passes when
// run against the real compiled pg-connector-calendar-osx-bridge binary
// with a fake osx-bridge-api socket listener" [design: acceptance
// criterion 3].
func TestConformance_RealBinary_FakeOsxBridgeSocketOnEnvOverride(t *testing.T) {
	bin := buildCalendarOsxBridgeBinary(t)
	sock := startFakeOsxBridgeSocket(t)
	// ExecBackend spawns bin with no explicit Env, so it inherits this
	// test process's own env — t.Setenv here reaches the child exactly the
	// way internal.SocketClient's own env resolution is exercised against
	// a real subprocess.
	t.Setenv(osxBridgeAPISocketEnvVar, sock)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := conformance.Run(ctx, conformance.ExecBackend{Binary: bin})
	if len(results) == 0 {
		t.Fatal("conformance.Run produced no results at all")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}
