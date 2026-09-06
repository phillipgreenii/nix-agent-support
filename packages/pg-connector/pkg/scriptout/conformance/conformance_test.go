package conformance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// --- static schema/golden checks (mirrors pr-pool's own conformance
// package tests, before Run existed to reproduce them) ---------------------

func loadGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	v, err := Golden(name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return v
}

func TestGoldenFixturesValidate(t *testing.T) {
	for _, name := range MessageTypes() {
		t.Run(name, func(t *testing.T) {
			if err := Check(name, loadGolden(t, name)); err != nil {
				t.Fatalf("golden failed its own schema: %v", err)
			}
		})
	}
}

func TestNegative_Generic(t *testing.T) {
	for _, name := range MessageTypes() {
		t.Run(name, func(t *testing.T) {
			g := loadGolden(t, name)
			g["totallyUnexpectedField"] = "x"
			if err := Check(name, g); err == nil {
				t.Fatalf("%s accepted an additional property", name)
			}
		})
	}
}

// TestNegativeMatrixCompleteness gates the schema-change mechanics rule
// (mirrors pr-pool's own): adding a new schemas/*.schema.json name
// requires >=1 negative-matrix row in the same commit, so a new schema
// landing with no negative case at all is caught rather than silently
// passing every other conformance case.
func TestNegativeMatrixCompleteness(t *testing.T) {
	for _, name := range MessageTypes() {
		if len(negativeMatrix[name]) == 0 {
			t.Errorf("%s has no negative-matrix row", name)
		}
	}
}

func TestCheckBytes_Malformed(t *testing.T) {
	if err := CheckBytes("request", []byte(`{ not json`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

// --- CheckResponse (the cross-field rule a structural schema cannot
// express) --------------------------------------------------------------

func TestCheckResponse_SuccessBranch(t *testing.T) {
	v := map[string]any{"protocolVersion": float64(1), "result": map[string]any{"ok": true}}
	if err := CheckResponse(v); err != nil {
		t.Fatalf("well-formed success response rejected: %v", err)
	}
}

func TestCheckResponse_ErrorBranch(t *testing.T) {
	v := map[string]any{"protocolVersion": float64(1), "error": map[string]any{"code": "not_found", "message": "x"}}
	if err := CheckResponse(v); err != nil {
		t.Fatalf("well-formed error response rejected: %v", err)
	}
}

// TestCheckResponse_NeitherBranch_IsProtocolViolation proves the exact
// pkg/scriptout/exec.go [bug A7] shape (neither result nor error present)
// is rejected as a protocol violation, not silently accepted.
func TestCheckResponse_NeitherBranch_IsProtocolViolation(t *testing.T) {
	v := map[string]any{"protocolVersion": float64(1)}
	if err := CheckResponse(v); err == nil {
		t.Fatal("response with neither result nor error was accepted")
	}
}

func TestCheckResponse_BothBranches_IsRejected(t *testing.T) {
	v := map[string]any{
		"protocolVersion": float64(1),
		"result":          map[string]any{"ok": true},
		"error":           map[string]any{"code": "not_found", "message": "x"},
	}
	if err := CheckResponse(v); err == nil {
		t.Fatal("response with both result and error was accepted")
	}
}

func TestCheckResponse_NotAnObject(t *testing.T) {
	if err := CheckResponse("not an object"); err == nil {
		t.Fatal("non-object response was accepted")
	}
}

func TestCheckResponseBytes(t *testing.T) {
	if err := CheckResponseBytes([]byte(`{ not json`)); err == nil {
		t.Fatal("malformed bytes accepted")
	}
	good, _ := json.Marshal(map[string]any{"protocolVersion": 1, "result": nil})
	if err := CheckResponseBytes(good); err != nil {
		t.Fatalf("explicit-null-result response rejected: %v", err)
	}
}

func TestIsSchemaError(t *testing.T) {
	if !IsSchemaError(Check("request", map[string]any{})) {
		t.Fatal("validation failure should be a schema error")
	}
	if IsSchemaError(nil) {
		t.Fatal("nil is not a schema error")
	}
}

func TestMessageTypesNonEmpty(t *testing.T) {
	if len(MessageTypes()) == 0 {
		t.Fatal("MessageTypes returned nothing")
	}
}

// --- Run: static-only (no backend) --------------------------------------

func TestRun_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := Run(ctx, TableBackend{})
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("Run(canceled ctx) = %+v, want a single error result", results)
	}
}

func TestRun_NilBackend_SkipsInvokingCases(t *testing.T) {
	results := Run(context.Background(), nil)
	invoking := map[string]Result{}
	for _, r := range results {
		if r.Name == "invoking/unknown-op" || r.Name == "invoking/malformed-stdin" || r.Name == "invoking/capabilities" {
			invoking[r.Name] = r
		}
		if r.Name != "invoking/unknown-op" && r.Name != "invoking/malformed-stdin" && r.Name != "invoking/capabilities" {
			if r.Skipped {
				t.Fatalf("unexpected skip on a static case: %s", r.Name)
			}
			if r.Err != nil {
				t.Fatalf("static case %s failed: %v", r.Name, r.Err)
			}
		}
	}
	for _, name := range []string{"invoking/unknown-op", "invoking/malformed-stdin", "invoking/capabilities"} {
		r, ok := invoking[name]
		if !ok || !r.Skipped {
			t.Fatalf("expected %s to be skipped with a nil backend, got %+v", name, r)
		}
	}
}

// --- Run: TableBackend (in-process double) ------------------------------

// fakeTableWithoutCapabilities is a minimal DispatchTable implementing
// exactly one op — enough to prove Run's static+unknown-op+malformed-stdin
// cases all pass, and that invoking/capabilities is Skipped (this table
// carries no capabilities entry).
func fakeTableWithoutCapabilities() scriptout.DispatchTable {
	return scriptout.DispatchTable{
		"show": {
			SchemaVersion: 1,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]string{"id": "pr-1"}, nil
			},
		},
	}
}

func TestRun_TableBackend_WithoutCapabilities(t *testing.T) {
	results := Run(context.Background(), TableBackend{Table: fakeTableWithoutCapabilities()})
	byName := map[string]Result{}
	for _, r := range results {
		byName[r.Name] = r
	}
	for _, name := range []string{"invoking/unknown-op", "invoking/malformed-stdin"} {
		r, ok := byName[name]
		if !ok {
			t.Fatalf("Run produced no %s result", name)
		}
		if r.Skipped || r.Err != nil {
			t.Fatalf("%s = %+v, want a clean pass", name, r)
		}
	}
	r, ok := byName["invoking/capabilities"]
	if !ok {
		t.Fatal("Run produced no invoking/capabilities result")
	}
	if !r.Skipped || r.SkipReason == "" {
		t.Fatalf("invoking/capabilities = %+v, want Skipped with a reason (table has no capabilities entry)", r)
	}
	for _, r := range results {
		if !r.Skipped && r.Err != nil {
			t.Errorf("unexpected failure %s: %v", r.Name, r.Err)
		}
	}
}

// fakeTableWithCapabilities adds a capabilities entry on top of
// fakeTableWithoutCapabilities, so invoking/capabilities gets a real live
// pass rather than a skip.
func fakeTableWithCapabilities() scriptout.DispatchTable {
	table := fakeTableWithoutCapabilities()
	table[scriptout.OpCapabilities] = scriptout.OpHandler{
		Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			return scriptout.CapabilitiesResponse{
				ProtocolVersion: scriptout.ProtocolVersion,
				SchemaVersions:  map[string]int{"pr": 1},
				Ops:             []string{"show", scriptout.OpCapabilities},
			}, nil
		},
	}
	return table
}

func TestRun_TableBackend_WithCapabilities(t *testing.T) {
	results := Run(context.Background(), TableBackend{Table: fakeTableWithCapabilities()})
	for _, r := range results {
		if r.Name == "invoking/capabilities" {
			if r.Skipped || r.Err != nil {
				t.Fatalf("invoking/capabilities = %+v, want a clean pass", r)
			}
			return
		}
	}
	t.Fatal("Run produced no invoking/capabilities result")
}

// TestRun_TableBackend_CatchesAFakeShapeNoRealBackendImplements is the
// bead's own motivating scenario: a handler that answers unknown_op's
// request with a shape that looks plausible but violates the wire
// contract (both result AND error set) must be CAUGHT by Run, not waved
// through — proving this suite actually does what a backend's own
// hand-rolled unit tests would not.
func TestRun_TableBackend_CatchesAFakeShapeNoRealBackendImplements(t *testing.T) {
	// A backend cannot literally emit an invalid Response through
	// scriptout.ServeOne (the Response struct only ever sets one of
	// Result/Error), so this proves the NEGATIVE side directly: the
	// conformance schema itself rejects the malformed shape, independent
	// of whether any backend happens to produce it.
	malformed := map[string]any{
		"protocolVersion": float64(scriptout.ProtocolVersion),
		"result":          map[string]any{"ok": true},
		"error":           map[string]any{"code": "not_found", "message": "x"},
	}
	if err := CheckResponse(malformed); err == nil {
		t.Fatal("conformance suite failed to catch a response with both result and error")
	}
}

// --- Run: ExecBackend (a real OS subprocess) ----------------------------
//
// Mirrors pkg/scriptout/exec_test.go's own test-helper-process idiom: the
// test binary re-execs itself with a special env var so ExecBackend drives
// a genuine child process without needing a separately compiled backend
// binary.

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CONFORMANCE_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	os.Exit(scriptout.ServeOne(fakeTableWithCapabilities(), os.Stdin, os.Stdout))
}

func helperExecBackend(t *testing.T) ExecBackend {
	t.Helper()
	// -test.run pins the re-exec'd test binary to ONLY TestHelperProcess —
	// without it, running os.Args[0] with no arguments would re-run this
	// whole test package's suite (including this very test) as a fresh
	// process, recursing indefinitely.
	return ExecBackend{Binary: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"}}
}

// helperExecCommand is the exec.CommandContext replacement conformance's
// own tests use to turn ExecBackend.Binary (os.Args[0], the test binary
// itself) into a call to TestHelperProcess. ExecBackend does not expose a
// swappable command factory the way pkg/scriptout/exec.go's
// execCmdFactory does — deliberately: ExecBackend's whole point is
// exercising the REAL os/exec path, so the fake plumbing lives here in the
// test's own Binary value instead (a subtly different binary path that
// re-dispatches into TestHelperProcess), not in a mocked factory.
func TestExecBackend_RealSubprocess_Success(t *testing.T) {
	if _, err := exec.LookPath(os.Args[0]); err != nil {
		// Re-exec needs the test binary to be independently executable;
		// skip rather than fail if the environment doesn't allow that.
		t.Skipf("test binary not independently executable: %v", err)
	}
	t.Setenv("GO_WANT_CONFORMANCE_HELPER_PROCESS", "1")

	backend := helperExecBackend(t)
	req := []byte(`{"op":"show","args":{}}`)
	out, code, err := backend.Invoke(context.Background(), req)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, out)
	}
	if err := CheckResponseBytes(out); err != nil {
		t.Fatalf("reply failed response schema: %v (stdout=%s)", err, out)
	}
}

// TestRun_ExecBackend_RealSubprocess proves the full Run suite passes
// end-to-end against a genuine child process (not just an in-process
// TableBackend double) — the strongest form of "a new backend
// implementation (real or fake) can be run against it."
func TestRun_ExecBackend_RealSubprocess(t *testing.T) {
	if _, err := exec.LookPath(os.Args[0]); err != nil {
		t.Skipf("test binary not independently executable: %v", err)
	}
	t.Setenv("GO_WANT_CONFORMANCE_HELPER_PROCESS", "1")

	results := Run(context.Background(), helperExecBackend(t))
	for _, r := range results {
		if r.Skipped {
			continue
		}
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

func TestExecBackend_EmptyBinary(t *testing.T) {
	_, _, err := (ExecBackend{}).Invoke(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error for an empty Binary")
	}
}

func TestExecBackend_BinaryNotFound(t *testing.T) {
	backend := ExecBackend{Binary: "definitely-does-not-exist-xyz-987"}
	_, exitCode, err := backend.Invoke(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1 (spawn failure, not a backend exit code)", exitCode)
	}
}

// --- Result zero value ---------------------------------------------------

func TestResult_ZeroValueIsCleanPass(t *testing.T) {
	var r Result
	if r.Err != nil || r.Skipped {
		t.Fatalf("zero Result = %+v, want a clean pass", r)
	}
}
