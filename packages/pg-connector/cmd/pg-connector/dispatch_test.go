package main

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestDispatch_ResolvesSingleRegisteredBackend(t *testing.T) {
	writeFakeBackend(t, "pg-connector-pr-github", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pg-connector-pr-github\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	resp, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var status scriptout.AuthStatus
	if err := scriptout.Decode(resp.Result, &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != scriptout.AuthOK {
		t.Fatalf("state = %q", status.State)
	}
}

func TestDispatch_NoBackendRegistered(t *testing.T) {
	reg, err := parseRegistry([]byte("connector: {}\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil, ""); err == nil {
		t.Fatal("expected error for no registered backend")
	}
}

func TestDispatch_AmbiguousMultipleBackends(t *testing.T) {
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - a\n    - b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := Dispatch(context.Background(), reg, "pr", scriptout.OpAuthStatus, nil, ""); err == nil {
		t.Fatal("expected error for ambiguous multi-backend targeted op")
	}
}

// dispatchTargetedTestResult is the minimal decode target the
// DispatchTargeted tests below use to identify WHICH fake backend a
// response came from — id is the only field these fakes' canned JSON
// carries.
type dispatchTargetedTestResult struct {
	ID string `json:"id"`
}

func TestDispatchTargeted_NoBackendRegistered(t *testing.T) {
	// The multi-instance resolution policy leaves Dispatch's existing
	// len(backends) == 0 case unaffected in either category —
	// DispatchTargeted must fail the same way Dispatch does when nothing
	// is registered.
	reg, err := parseRegistry([]byte("connector: {}\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := DispatchTargeted(context.Background(), reg, "issue", "show", nil, ""); err == nil {
		t.Fatal("expected error for no registered backend")
	}
}

func TestDispatchTargeted_SingleBackend_ResolvesLikeDispatch(t *testing.T) {
	// With exactly one registered backend, DispatchTargeted must resolve
	// identically to Dispatch's own single-backend path — introducing
	// DispatchTargeted must not change single-backend targeted-op
	// behavior.
	writeFakeBackend(t, "issue-single", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"only-one"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - issue-single\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "issue", "show", nil, "")
	if err != nil {
		t.Fatalf("DispatchTargeted: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "only-one" {
		t.Fatalf("id = %q, want %q", got.ID, "only-one")
	}
}

// The seven tests below cover the multi-instance resolution policy
// directly against DispatchTargeted (mirroring TestDispatch_
// AmbiguousMultipleBackends's own fixture style: parseRegistry +
// writeFakeBackend, no CLI/cobra wiring), one id-keyed op each for the
// issue and pr capability groups — ci's own three-case coverage lands in
// ci_test.go instead, alongside its updated pre-existing ambiguous-
// backends test. "show" is used as the representative id-keyed op for
// both groups; the policy itself is entirely op-agnostic
// (DispatchTargeted never inspects op), so any of the other named
// id-keyed ops would exercise the exact same code path.

func TestDispatchTargeted_Issue_MultipleBackends_FirstTriedSucceeds(t *testing.T) {
	writeFakeBackend(t, "issue-multi-a", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-a"}}`)
	writeFakeBackend(t, "issue-multi-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - issue-multi-a\n    - issue-multi-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "issue", "show", nil, "")
	if err != nil {
		t.Fatalf("DispatchTargeted: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-a" {
		t.Fatalf("id = %q, want the FIRST-registered backend's answer %q", got.ID, "from-a")
	}
}

func TestDispatchTargeted_Issue_MultipleBackends_FirstNotFound_SecondSucceeds(t *testing.T) {
	writeFakeBackend(t, "issue-multi-notfound", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in a"}}`)
	writeFakeBackend(t, "issue-multi-second", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-second"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - issue-multi-notfound\n    - issue-multi-second\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "issue", "show", nil, "")
	if err != nil {
		t.Fatalf("DispatchTargeted: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-second" {
		t.Fatalf("id = %q, want the SECOND backend's answer after the first's not_found", got.ID)
	}
}

func TestDispatchTargeted_Issue_MultipleBackends_AllNotFound(t *testing.T) {
	writeFakeBackend(t, "issue-multi-nf-a", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in a"}}`)
	writeFakeBackend(t, "issue-multi-nf-b", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - issue-multi-nf-a\n    - issue-multi-nf-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	_, err = DispatchTargeted(context.Background(), reg, "issue", "show", nil, "")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, scriptout.ErrNotFound) — the all-not_found aggregate", err)
	}
}

func TestDispatchTargeted_Pr_MultipleBackends_FirstTriedSucceeds(t *testing.T) {
	writeFakeBackend(t, "pr-multi-a", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-a"}}`)
	writeFakeBackend(t, "pr-multi-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pr-multi-a\n    - pr-multi-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "pr", "show", nil, "")
	if err != nil {
		t.Fatalf("DispatchTargeted: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-a" {
		t.Fatalf("id = %q, want the FIRST-registered backend's answer %q", got.ID, "from-a")
	}
}

func TestDispatchTargeted_Pr_MultipleBackends_FirstNotFound_SecondSucceeds(t *testing.T) {
	writeFakeBackend(t, "pr-multi-notfound", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in a"}}`)
	writeFakeBackend(t, "pr-multi-second", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-second"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pr-multi-notfound\n    - pr-multi-second\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "pr", "show", nil, "")
	if err != nil {
		t.Fatalf("DispatchTargeted: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-second" {
		t.Fatalf("id = %q, want the SECOND backend's answer after the first's not_found", got.ID)
	}
}

func TestDispatchTargeted_Pr_MultipleBackends_AllNotFound(t *testing.T) {
	writeFakeBackend(t, "pr-multi-nf-a", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in a"}}`)
	writeFakeBackend(t, "pr-multi-nf-b", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pr-multi-nf-a\n    - pr-multi-nf-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	_, err = DispatchTargeted(context.Background(), reg, "pr", "show", nil, "")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, scriptout.ErrNotFound) — the all-not_found aggregate", err)
	}
}

// TestDispatchTargeted_Pr_MultipleBackends_FirstError_ShortCircuits covers
// the resolution policy's short-circuit rule at the Dispatch level
// (ci_test.go's own TestRun_CiLogs_MultipleBackends_FirstError_
// ShortCircuits covers it at the CLI level): a non-not_found error from
// the first-tried backend is returned immediately, never swallowed to
// fall through to the second.
// --- --backend pinning (bead pg2-2j5ac.28.1, design's "id-less op
// rule") ---------------------------------------------------------------

func TestDispatch_Pinned_SkipsAmbiguityHardFail(t *testing.T) {
	writeFakeBackend(t, "pinned-a", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-a"}}`)
	writeFakeBackend(t, "pinned-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - pinned-a\n    - pinned-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := Dispatch(context.Background(), reg, "issue", "create", nil, "pinned-b")
	if err != nil {
		t.Fatalf("Dispatch with --backend pin: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-b" {
		t.Fatalf("id = %q, want the PINNED backend's answer %q", got.ID, "from-b")
	}
}

func TestDispatch_Pinned_UnregisteredNameIsError(t *testing.T) {
	writeFakeBackend(t, "pinned-only", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"x"}}`)
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - pinned-only\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := Dispatch(context.Background(), reg, "issue", "create", nil, "not-registered"); err == nil {
		t.Fatal("expected an error pinning to an unregistered backend")
	}
}

func TestDispatchTargeted_Pinned_SkipsTryEachPolicy(t *testing.T) {
	// Both backends would answer differently; pinning to the SECOND one
	// must go straight there rather than trying the first (registration
	// order) first — the try-each policy is specifically bypassed by a
	// pin.
	writeFakeBackend(t, "targeted-pin-a", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not found in a"}}`)
	writeFakeBackend(t, "targeted-pin-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"from-b"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - targeted-pin-a\n    - targeted-pin-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := DispatchTargeted(context.Background(), reg, "pr", "show", nil, "targeted-pin-b")
	if err != nil {
		t.Fatalf("DispatchTargeted with --backend pin: %v", err)
	}
	var got dispatchTargetedTestResult
	if err := scriptout.Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "from-b" {
		t.Fatalf("id = %q, want the PINNED backend's answer %q", got.ID, "from-b")
	}
}

func TestDispatchTargeted_Pinned_UnregisteredNameIsError(t *testing.T) {
	writeFakeBackend(t, "targeted-pin-only", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"x"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - targeted-pin-only\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if _, err := DispatchTargeted(context.Background(), reg, "pr", "show", nil, "not-registered"); err == nil {
		t.Fatal("expected an error pinning to an unregistered backend")
	}
}

// --- per-backend config attachment (bead pg2-2j5ac.28.1)
// ---------------------------------------------------------------------

func TestDispatch_AttachesBackendOwnConfigVerbatim(t *testing.T) {
	writeEchoFakeBackend(t, "echo-config-a")
	reg, err := parseRegistry([]byte(`
connector:
  issue:
    - echo-config-a
backends:
  echo-config-a:
    queries:
      ready: "ready"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := Dispatch(context.Background(), reg, "issue", "list", map[string]string{"query": "ready"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var echoed struct {
		Config struct {
			Queries map[string]string `json:"queries"`
		} `json:"config"`
	}
	if err := scriptout.Decode(resp.Result, &echoed); err != nil {
		t.Fatalf("decode echoed request: %v", err)
	}
	if echoed.Config.Queries["ready"] != "ready" {
		t.Fatalf("echoed config = %+v, want the registered backends.echo-config-a block", echoed.Config)
	}
}

func TestDispatch_NoRegisteredConfig_OmitsConfigMember(t *testing.T) {
	writeEchoFakeBackend(t, "echo-config-b")
	reg, err := parseRegistry([]byte("connector:\n  issue:\n    - echo-config-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	resp, err := Dispatch(context.Background(), reg, "issue", "show", map[string]string{"id": "1"}, "")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var echoed map[string]any
	if err := scriptout.Decode(resp.Result, &echoed); err != nil {
		t.Fatalf("decode echoed request: %v", err)
	}
	if _, ok := echoed["config"]; ok {
		t.Fatalf("echoed request = %v, must not carry a config member when no backends.<name> block is registered", echoed)
	}
}

func TestDispatchTargeted_Pr_MultipleBackends_FirstError_ShortCircuits(t *testing.T) {
	writeFakeBackend(t, "pr-multi-err-a", `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)
	writeFakeBackend(t, "pr-multi-err-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"should-never-be-returned"}}`)
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - pr-multi-err-a\n    - pr-multi-err-b\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	_, err = DispatchTargeted(context.Background(), reg, "pr", "show", nil, "")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, scriptout.ErrUnauthenticated), short-circuited on the first backend's error", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not classify as not_found", err)
	}
}
