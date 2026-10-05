// conformance_test.go: this packet's required conformance-suite proof —
// pkg/scriptout/conformance's existing Backend/ExecBackend/driver.Run suite
// (unchanged; this is a NEW consumer of it, not a modifier) run against the
// REAL COMPILED pg-connector-mail-osx-bridge binary, with a FAKE
// pg-osx-bridge-api socket listener standing in for the real daemon,
// resolved via t.Setenv("PG_OSX_BRIDGE_API_SOCKET", <fake listener's path>) —
// the SAME env var internal/client.go's own socketEnvVar/ResolveSocketPath
// resolution reads. Because the generic suite only exercises
// unknown-op/malformed-stdin/capabilities (driver.go's own doc comment), the
// mail-specific ops and the closed six-value error taxonomy are additionally
// driven end to end through scriptout.Invoke against the same real binary.
//
// Deliberately NOT gated behind a build tag: this package's tests run with
// plain `go test`, so the binary build + subprocess exec below run as part
// of every ordinary `go test ./...` pass over this module — the suite is
// fully hermetic (a fake, hand-rolled Unix-socket listener, no network, no
// Mail.app).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// osxBridgeAPISocketEnvVar mirrors internal/client.go's own (unexported)
// socketEnvVar constant by value — this backend resolves this exact env var,
// so the fake listener below must be wired in under the identical name.
const osxBridgeAPISocketEnvVar = "PG_OSX_BRIDGE_API_SOCKET"

// Deadlines are hang guards, not performance assertions: a cold `go build`
// under heavy machine load takes far longer than an idle build, so they are
// deliberately generous (still below go test's default 10m package timeout).
const (
	buildDeadline = 8 * time.Minute
	runDeadline   = 2 * time.Minute
)

// buildMailOsxBridgeBinary compiles this package's own real binary via
// `go build -o <dir>/pg-connector-mail-osx-bridge .`, run with THIS
// package's own directory as the build's working directory.
//
// The build runs ONCE per test process (the cold build is the slow part under
// load) and the binary is removed by TestMain.
func buildMailOsxBridgeBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pgmobbin")
		if err != nil {
			builtErr = err
			return
		}
		builtDir = dir
		builtBin = filepath.Join(dir, "pg-connector-mail-osx-bridge")
		ctx, cancel := context.WithTimeout(context.Background(), buildDeadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", builtBin, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			builtErr = fmt.Errorf("go build ./cmd/pg-connector-mail-osx-bridge: %w\n%s", err, out)
		}
	})
	if builtErr != nil {
		t.Fatal(builtErr)
	}
	return builtBin
}

var (
	builtOnce sync.Once
	builtDir  string
	builtBin  string
	builtErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		_ = os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

// fakeBridge is a minimal, hand-rolled Unix-socket listener standing in for
// the real pg-osx-bridge-api daemon — never importing that module's own
// socketserver/wire/mailapi packages (this backend talks to the daemon over
// the wire only, never as a Go import). It records every request's op and
// answers with whatever handle returns.
type fakeBridge struct {
	sock string
	mu   sync.Mutex
	ops  []string
}

func (f *fakeBridge) seenOps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func startFakeBridge(t *testing.T, handle func(op string, args map[string]any) map[string]any) *fakeBridge {
	t.Helper()

	// A short, dedicated temp dir rather than t.TempDir(): sockaddr_un's own
	// path-length cap is well under what a t.TempDir() path can produce.
	dir, err := os.MkdirTemp("", "pgmobfake")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	f := &fakeBridge{sock: filepath.Join(dir, "s.sock")}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatalf("listen on %s: %v", f.sock, err)
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
				args, _ := req["args"].(map[string]any)
				f.mu.Lock()
				f.ops = append(f.ops, op)
				f.mu.Unlock()
				_ = json.NewEncoder(conn).Encode(handle(op, args))
			}()
		}
	}()

	return f
}

func bridgeOK(result any) map[string]any {
	return map[string]any{"protocolVersion": 1, "schemaVersion": 1, "result": result}
}

func bridgeErr(code string) map[string]any {
	return map[string]any{"protocolVersion": 1, "error": map[string]any{"code": code, "message": "fake pg-osx-bridge-api: " + code}}
}

// TestConformance_RealBinary_FakeOsxBridgeSocketOnEnvOverride is the
// required proof: the full conformance suite passes when run against the
// real compiled pg-connector-mail-osx-bridge binary with a fake
// pg-osx-bridge-api socket listener.
func TestConformance_RealBinary_FakeOsxBridgeSocketOnEnvOverride(t *testing.T) {
	bin := buildMailOsxBridgeBinary(t)
	fb := startFakeBridge(t, func(string, map[string]any) map[string]any { return bridgeErr("unknown_op") })
	// ExecBackend spawns bin with no explicit Env, so it inherits this test
	// process's own env — t.Setenv here reaches the child.
	t.Setenv(osxBridgeAPISocketEnvVar, fb.sock)

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
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

// TestConformance_RealBinary_Capabilities pins the advertised capability
// surface: mail, search and attention schema versions, every mail op, the
// optional capabilities' ops, and no auth_status entry.
func TestConformance_RealBinary_Capabilities(t *testing.T) {
	bin := buildMailOsxBridgeBinary(t)
	fb := startFakeBridge(t, func(string, map[string]any) map[string]any { return bridgeErr("unknown_op") })
	t.Setenv(osxBridgeAPISocketEnvVar, fb.sock)

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	caps, err := scriptout.InvokeCapabilities(ctx, bin)
	if err != nil {
		t.Fatalf("InvokeCapabilities: %v", err)
	}
	wantVersions := map[string]int{"mail": schema.MailSchemaVersion, "search": schema.SearchSchemaVersion, "attention": schema.AttentionSchemaVersion}
	if !reflect.DeepEqual(caps.SchemaVersions, wantVersions) {
		t.Fatalf("SchemaVersions = %v, want %v", caps.SchemaVersions, wantVersions)
	}
	have := map[string]bool{}
	for _, op := range caps.Ops {
		have[op] = true
	}
	for _, op := range []string{"list", "show", "search_messages", "mark_read", "mark_unread", "archive", "unarchive", "fetch_attachment", "search", "list_attention"} {
		if !have[op] {
			t.Errorf("capabilities.ops = %v, missing %q", caps.Ops, op)
		}
	}
	if have[scriptout.OpAuthStatus] {
		t.Errorf("capabilities.ops = %v, must carry no auth_status (the bridge needs no client credential)", caps.Ops)
	}
}

// TestConformance_RealBinary_MailOpsAndErrorTaxonomy drives the mail ops end
// to end through the real binary and the fake bridge, and asserts every
// bridge error code lands on the matching closed-taxonomy sentinel, and
// that a delete-shaped op is unknown to the binary.
func TestConformance_RealBinary_MailOpsAndErrorTaxonomy(t *testing.T) {
	bin := buildMailOsxBridgeBinary(t)

	var (
		failMu   sync.Mutex
		failCode string
	)
	setFail := func(code string) {
		failMu.Lock()
		defer failMu.Unlock()
		failCode = code
	}
	fb := startFakeBridge(t, func(op string, args map[string]any) map[string]any {
		failMu.Lock()
		failWith := failCode
		failMu.Unlock()
		if failWith != "" {
			return bridgeErr(failWith)
		}
		switch op {
		case "list":
			return bridgeOK(map[string]any{"messages": []map[string]any{{
				"id": "<m1@example.com>", "subject": "Hello", "sender": "Ada <ada@example.com>",
				"dateReceived": "2026-10-05T09:30:00Z", "read": false, "flagged": false, "mailbox": "INBOX",
				"attachments": []map[string]any{{"id": "a1", "name": "x.pdf", "mimeType": "application/pdf", "size": 12}},
			}}})
		case "fetch-attachment":
			return bridgeOK(map[string]any{"path": "/data/attachments/x.pdf", "name": "x.pdf", "size": 12})
		default:
			return bridgeErr("unknown_op")
		}
	})
	t.Setenv(osxBridgeAPISocketEnvVar, fb.sock)

	cfg := json.RawMessage(`{"mailboxes":[{"name":"INBOX"}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()

	// list: translated through to the mail wire shape.
	resp, err := scriptout.Invoke(ctx, bin, "list", map[string]any{"mailbox": ""}, cfg)
	if err != nil {
		t.Fatalf("Invoke list: %v", err)
	}
	if resp.SchemaVersion != schema.MailSchemaVersion {
		t.Fatalf("list schemaVersion = %d, want %d", resp.SchemaVersion, schema.MailSchemaVersion)
	}
	var list schema.MailListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	if len(list.Entities) != 1 || list.Entities[0].ID != "<m1@example.com>" || len(list.Entities[0].Attachments) != 1 ||
		list.Entities[0].Attachments[0].Filename != "x.pdf" {
		t.Fatalf("list = %+v", list)
	}

	// fetch_attachment: the bridge's full saved path comes back.
	resp, err = scriptout.Invoke(ctx, bin, "fetch_attachment", map[string]any{"id": "<m1@example.com>", "attachment_id": "a1"}, cfg)
	if err != nil {
		t.Fatalf("Invoke fetch_attachment: %v", err)
	}
	var fetched schema.MailAttachmentFile
	if err := json.Unmarshal(resp.Result, &fetched); err != nil {
		t.Fatalf("decode fetch_attachment result: %v", err)
	}
	if fetched.Path != "/data/attachments/x.pdf" {
		t.Fatalf("fetched = %+v", fetched)
	}

	// attention is answered (empty), not an unknown op.
	resp, err = scriptout.Invoke(ctx, bin, "list_attention", nil, cfg)
	if err != nil {
		t.Fatalf("Invoke list_attention: %v", err)
	}
	var items []schema.AttentionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil || len(items) != 0 {
		t.Fatalf("list_attention result = %s (err %v), want an empty list", resp.Result, err)
	}

	// Each bridge error code lands on its same-named sentinel.
	taxonomy := map[string]error{
		"not_found":        scriptout.ErrNotFound,
		"unauthenticated":  scriptout.ErrUnauthenticated,
		"unavailable":      scriptout.ErrUnavailable,
		"unknown_op":       scriptout.ErrUnknownOp,
		"version_mismatch": scriptout.ErrVersionMismatch,
		"invalid_argument": scriptout.ErrInvalidArgument,
	}
	codes := make([]string, 0, len(taxonomy))
	for code := range taxonomy {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		setFail(code)
		_, err := scriptout.Invoke(ctx, bin, "show", map[string]any{"id": "<m1@example.com>"}, cfg)
		if !errors.Is(err, taxonomy[code]) {
			t.Errorf("bridge %q: err = %v, want errors.Is %v", code, err, taxonomy[code])
		}
	}
	setFail("")

	// A dead bridge (socket gone) is unavailable.
	t.Setenv(osxBridgeAPISocketEnvVar, filepath.Join(t.TempDir(), "gone.sock"))
	if _, err := scriptout.Invoke(ctx, bin, "show", map[string]any{"id": "<m1@example.com>"}, cfg); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("dead bridge: err = %v, want unavailable", err)
	}

	// Argument-shape failures never reach the bridge.
	before := len(fb.seenOps())
	if _, err := scriptout.Invoke(ctx, bin, "show", map[string]any{"id": ""}, cfg); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("empty id: err = %v, want invalid_argument", err)
	}
	if after := len(fb.seenOps()); after != before {
		t.Errorf("bridge saw %d extra requests for an invalid call", after-before)
	}

	// There is no delete-shaped op on the binary (INV-MAIL-1).
	for _, op := range []string{"delete", "delete_message", "trash", "expunge", "purge", "remove"} {
		if _, err := scriptout.Invoke(ctx, bin, op, map[string]any{"id": "<m1@example.com>"}, cfg); !errors.Is(err, scriptout.ErrUnknownOp) {
			t.Errorf("op %q: err = %v, want unknown_op", op, err)
		}
	}
}
